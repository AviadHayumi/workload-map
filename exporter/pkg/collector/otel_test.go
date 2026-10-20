// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/telemetry"
)

type testingT interface {
	Helper()
	Fatalf(string, ...any)
	Cleanup(func())
}

func testGatherer(t testingT, s Capturer, options Options) *prometheus.Registry {
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

func TestOTelPreservesMoreThanDefaultCardinality(t *testing.T) {
	s := fixtureStore()
	pod, _ := s.Published("w1").Pod("p1")
	for i := range 2500 {
		pod.Record.UID = types.UID(fmt.Sprintf("pod-%d", i))
		pod.Record.Name = string(pod.Record.UID)
		s.Publish(s.Published("w1").WithPod(pod.Record.UID, &pod))
	}
	publication := *s.Published("w1")
	publication.PodPages = nil
	workload := publication.Workload
	for i := range 250 {
		workload.UID = types.UID(fmt.Sprintf("workload-%d", i))
		workload.Ref.Name = string(workload.UID)
		next := publication
		next.Workload = workload
		s.Publish(&next)
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
				if label(m, LabelPodUID) == "" {
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
	registry := testGatherer(t, s, Options{})
	pod, _ := s.Published("w1").Pod("p1")
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 100 {
			pod.Record.Instance = fmt.Sprintf("instance-%d", i)
			s.Publish(s.Published("w1").WithPod(pod.Record.UID, &pod))
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
