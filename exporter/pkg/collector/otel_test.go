// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/testing/protocmp"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/telemetry"
)

type testingT interface {
	Helper()
	Fatalf(string, ...any)
	Cleanup(func())
}

func testGatherer(t testingT, s *store.Store, options Options) *prometheus.Registry {
	t.Helper()
	registry := prometheus.NewRegistry()
	provider, err := telemetry.NewPrometheusProvider(registry)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shut down provider: %v", err)
		}
	})
	registration, err := New(provider.Meter("karta-exporter"), s, options)
	if err != nil {
		t.Fatalf("register observations: %v", err)
	}
	t.Cleanup(func() {
		if err := registration.Unregister(); err != nil {
			t.Fatalf("unregister observations: %v", err)
		}
	})
	return registry
}

func TestOTelMatchesLegacyAcrossCollections(t *testing.T) {
	for _, mode := range []string{PodMetricsCounts, PodMetricsInventory, PodMetricsBoth} {
		t.Run(mode, func(t *testing.T) {
			s := fixtureStore()
			options := Options{PodMetricsMode: mode, PendingPods: func() int { return 2 }, KartaCounts: func() (int, int, int) { return 3, 1, 1 }}
			current := testGatherer(t, s, options)
			legacy := prometheus.NewRegistry()
			legacy.MustRegister(newLegacy(s, options))
			for step := range 4 {
				switch step {
				case 1:
					pod, _ := s.Pod("p1")
					s.DeletePod("p1")
					pod.UID = "replacement"
					pod.Instance = "new-instance"
					s.UpsertPod(pod)
				case 2:
					s.DeleteWorkload("w1")
				}
				got, err := current.Gather()
				if err != nil {
					t.Fatal(err)
				}
				want, err := legacy.Gather()
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
					t.Fatalf("collection %d differs (-legacy +OTel):\n%s", step, diff)
				}
			}
		})
	}
}

func TestOTelPreservesMoreThanDefaultCardinality(t *testing.T) {
	s := fixtureStore()
	pod, _ := s.Pod("p1")
	for i := range 2500 {
		pod.UID = types.UID(fmt.Sprintf("pod-%d", i))
		pod.Name = string(pod.UID)
		s.UpsertPod(pod)
	}
	workload, _ := s.Workload("w1")
	for i := range 250 {
		workload.UID = types.UID(fmt.Sprintf("workload-%d", i))
		workload.Ref.Name = string(workload.UID)
		s.UpsertWorkload(workload)
	}
	registry := testGatherer(t, s, Options{})
	for pass := range 2 {
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]int{}
		for _, family := range families {
			found[family.GetName()] = len(family.Metric)
			if family.GetName() != MetricPodWorkloadInfo {
				continue
			}
			if len(family.Metric) != 2502 {
				t.Fatalf("pass %d: pod mappings = %d, want 2502", pass, len(family.Metric))
			}
			for _, m := range family.Metric {
				if label(m, LabelUID) == "" {
					t.Fatal("lost a pod identity to cardinality overflow")
				}
			}
		}
		if found[MetricPodWorkloadInfo] != 2502 || found[MetricWorkloadStatus] != 2259 {
			t.Fatalf("pass %d: pod/status counts = %d/%d, want 2502/2259", pass, found[MetricPodWorkloadInfo], found[MetricWorkloadStatus])
		}
	}
}

func TestConcurrentOTelScrapesAndUpdates(t *testing.T) {
	s := fixtureStore()
	registry := testGatherer(t, s, Options{PodMetricsMode: PodMetricsInventory})
	pod, _ := s.Pod("p1")
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 100 {
			pod.Instance = fmt.Sprintf("instance-%d", i)
			s.UpsertPod(pod)
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 20 {
				families, err := registry.Gather()
				if err != nil {
					t.Error(err)
					return
				}
				var mappings []*dto.Metric
				for _, family := range families {
					if family.GetName() == MetricPodWorkloadInfo {
						mappings = family.Metric
					}
				}
				if len(mappings) != 2 {
					t.Errorf("concurrent scrape returned %d mappings, want 2", len(mappings))
				}
			}
		})
	}
	wg.Wait()
}
