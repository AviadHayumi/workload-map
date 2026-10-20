// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/test/scale"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

// These fixtures measure publication and exposition, not informer or cluster capacity.
func publicationScaleStore(scenario scale.Case) *store.Store {
	s := fixtureStore()
	prototype := *s.Published("w1")
	pod, _ := prototype.Pod("p1")
	s.RemovePublication("w1")
	for i := range scenario.Workloads {
		publication := prototype
		publication.Workload.UID = types.UID(fmt.Sprintf("workload-%d", i))
		publication.Workload.Ref.Name = string(publication.Workload.UID)
		publication.PodPages = nil
		for j := range scenario.Pods {
			row := pod
			row.Record.UID = types.UID(fmt.Sprintf("pod-%d-%d", i, j))
			row.Record.Name = string(row.Record.UID)
			row.Record.WorkloadUID, row.Record.Workload = publication.Workload.UID, publication.Workload.Ref
			publication = *publication.WithPod(row.Record.UID, &row)
		}
		s.Publish(&publication)
	}
	return s
}

func verifyPublicationHTTP(handler http.Handler, scenario scale.Case, changed bool) (int64, error) {
	expectedRoots := make(map[string]string, scenario.Workloads)
	type podIdentity struct{ workload, name string }
	expectedPods := make(map[string]podIdentity, scenario.Workloads*scenario.Pods)
	for i := range scenario.Workloads {
		if changed && i%10 == 0 {
			continue
		}
		name := fmt.Sprintf("workload-%d", i)
		uid := name
		if changed && i%10 == 1 {
			uid += "-replacement"
		}
		expectedRoots[uid] = name
		for j := range scenario.Pods {
			name := fmt.Sprintf("pod-%d-%d", i, j)
			podUID := name
			if changed && i%10 == 1 {
				podUID += "-replacement"
			}
			expectedPods[podUID] = podIdentity{workload: uid, name: name}
		}
	}
	roots, podsPerRoot := len(expectedRoots), scenario.Pods
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, scale.Request())
	if response.Code != http.StatusOK {
		return 0, fmt.Errorf("HTTP status %d", response.Code)
	}
	bytes := int64(response.Body.Len())
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(response.Body)
	if err != nil {
		return bytes, err
	}
	counts := map[string]int{"karta_workload_status": roots * 9, "karta_workload_created_timestamp_seconds": roots, "karta_pod_workload_info": roots * podsPerRoot, "karta_exporter_observation_valid": 1}
	for name, want := range counts {
		if got := len(families[name].GetMetric()); got != want {
			return bytes, fmt.Errorf("%s rows = %d, want %d", name, got, want)
		}
	}
	status := make(map[string]map[string]bool, roots)
	positive := make(map[string]int, roots)
	created := make(map[string]bool, roots)
	seenPods := make(map[string]bool, roots*podsPerRoot)
	for name, family := range families {
		if _, ok := counts[name]; !ok {
			return bytes, fmt.Errorf("unexpected family %s", name)
		}
		for _, row := range family.Metric {
			uid := label(row, "workload_uid")
			value := row.GetGauge().GetValue()
			if name != "karta_exporter_observation_valid" {
				if expectedRoots[uid] == "" || label(row, "workload_name") != expectedRoots[uid] || label(row, "namespace") != "team-a" {
					return bytes, fmt.Errorf("unexpected workload identity %v", row)
				}
			}
			switch name {
			case "karta_workload_status":
				phase := label(row, "phase")
				if status[uid] == nil {
					status[uid] = make(map[string]bool, 9)
				}
				phases := []string{"Initializing", "Running", "Completed", "Failed", "Degraded", "Suspended", "Suspending", "Resuming", "Undefined"}
				if !slices.Contains(phases, phase) || status[uid][phase] || label(row, "observation_valid") != "true" || (value != 0 && value != 1) {
					return bytes, fmt.Errorf("invalid status row %v", row)
				}
				status[uid][phase] = true
				if value == 1 {
					if phase != "Running" && phase != "Failed" {
						return bytes, fmt.Errorf("unexpected active phase %s", phase)
					}
					positive[uid]++
				}
			case "karta_workload_created_timestamp_seconds":
				if uid == "" || created[uid] || value != 1792324800 {
					return bytes, fmt.Errorf("invalid creation row %v", row)
				}
				created[uid] = true
			case "karta_pod_workload_info":
				podUID := label(row, "pod_uid")
				expected, ok := expectedPods[podUID]
				if !ok || expected.workload != uid || label(row, "pod_name") != expected.name || seenPods[podUID] || label(row, "observation_valid") != "true" || value != 1 {
					return bytes, fmt.Errorf("invalid mapping row %v", row)
				}
				seenPods[podUID] = true
			case "karta_exporter_observation_valid":
				if label(row, "scope") != "resource_type" || label(row, "reason") != "none" || uid != "" || value != 1 {
					return bytes, fmt.Errorf("invalid resource health %v", row)
				}
			}
		}
	}
	for uid, phases := range status {
		if !created[uid] || len(phases) != 9 || positive[uid] != 1 {
			return bytes, fmt.Errorf("workload %s lost creation or dense phases", uid)
		}
	}
	return bytes, nil
}

func TestScalePrometheusExposition(t *testing.T) {
	if os.Getenv("KARTA_SCALE_WORKLOADS") == "" {
		t.Skip("set KARTA_SCALE_WORKLOADS=1000,10000,100000 for publication-only scale validation")
	}
	cases, err := scale.Cases(os.Getenv("KARTA_SCALE_WORKLOADS"), os.Getenv("KARTA_SCALE_PODS_PER_WORKLOAD"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range cases {
		t.Run(scenario.Name(), func(t *testing.T) {
			s := publicationScaleStore(scenario)
			handler := promhttp.HandlerFor(testGatherer(t, s, Options{}), promhttp.HandlerOpts{})
			started := time.Now()
			bytes, err := verifyPublicationHTTP(handler, scenario, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("publication-only exposition: roots=%d pods=%d bytes=%d verification_wall=%s", scenario.Workloads, scenario.Workloads*scenario.Pods, bytes, time.Since(started))
			for i := range scenario.Workloads {
				uid := types.UID(fmt.Sprintf("workload-%d", i))
				if i%10 == 0 {
					s.RemovePublication(uid)
				}
				if i%10 == 1 {
					replacement := *s.Published(uid)
					replacement.Workload.UID += "-replacement"
					pages := replacement.PodPages
					replacement.PodPages = nil
					for _, page := range pages {
						for _, pod := range page.Rows {
							pod.Record.UID += "-replacement"
							pod.Record.WorkloadUID = replacement.Workload.UID
							replacement = *replacement.WithPod(pod.Record.UID, &pod)
						}
					}
					s.RemovePublication(uid)
					s.Publish(&replacement)
				}
			}
			if _, err := verifyPublicationHTTP(handler, scenario, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func BenchmarkScalePrometheusHTTP(b *testing.B) {
	if os.Getenv("KARTA_SCALE_WORKLOADS") == "" {
		b.Skip("set KARTA_SCALE_WORKLOADS for publication-only scale scenarios")
	}
	cases, err := scale.Cases(os.Getenv("KARTA_SCALE_WORKLOADS"), os.Getenv("KARTA_SCALE_PODS_PER_WORKLOAD"))
	if err != nil {
		b.Fatal(err)
	}
	for _, scenario := range cases {
		b.Run(scenario.Name(), func(b *testing.B) {
			handler := promhttp.HandlerFor(testGatherer(b, publicationScaleStore(scenario), Options{}), promhttp.HandlerOpts{})
			bytes, err := verifyPublicationHTTP(handler, scenario, false)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(bytes)
			b.ReportAllocs()
			for b.Loop() {
				response := &scale.Response{}
				handler.ServeHTTP(response, scale.Request())
				if response.Code != 200 || response.Bytes != bytes {
					b.Fatalf("HTTP %d bytes=%d, want 200 bytes=%d", response.Code, response.Bytes, bytes)
				}
			}
		})
	}
}

func TestScaleConcurrentHTTPOracle(t *testing.T) {
	s := publicationScaleStore(scale.Case{Workloads: 32, Pods: 2})
	handler := promhttp.HandlerFor(testGatherer(t, s, Options{}), promhttp.HandlerOpts{})
	var workers sync.WaitGroup
	stop := make(chan struct{})
	workers.Go(func() {
		for iteration := 0; ; iteration++ {
			select {
			case <-stop:
				return
			default:
			}
			uid := types.UID(fmt.Sprintf("workload-%d", iteration%32))
			publication := *s.Published(uid)
			phase := v1alpha1.RunningStatus
			if iteration%2 == 0 {
				phase = v1alpha1.FailedStatus
			}
			publication.Workload.Phases = []v1alpha1.ResourceStatus{phase}
			s.Publish(&publication)
		}
	})
	var scrapers sync.WaitGroup
	for range 4 {
		scrapers.Go(func() {
			for range 4 {
				if _, err := verifyPublicationHTTP(handler, scale.Case{Workloads: 32, Pods: 2}, false); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	scrapers.Wait()
	close(stop)
	workers.Wait()
}
