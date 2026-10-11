// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func gather(t *testing.T, s *store.Store, options Options) map[string][]*dto.Metric {
	t.Helper()
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(New(s, options))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]*dto.Metric)
	for _, family := range families {
		result[family.GetName()] = family.Metric
	}
	return result
}

func label(metric *dto.Metric, name string) string {
	for _, pair := range metric.Label {
		if pair.GetName() == name {
			return pair.GetValue()
		}
	}
	return ""
}

func TestPodMetricModes(t *testing.T) {
	for _, mode := range []string{PodMetricsCounts, PodMetricsInventory, PodMetricsBoth} {
		t.Run(mode, func(t *testing.T) {
			s := fixtureStore()
			workload, _ := s.Workload("w1")
			workload.Components = append(workload.Components, store.ComponentState{Component: "service", Instance: "empty"})
			s.UpsertWorkload(workload)
			s.UpsertPod(store.PodRecord{
				UID: "p3", Namespace: "team-a", Name: "unresolved", WorkloadUID: "w1", Workload: workload.Ref,
				Component: SentinelUnknown, Instance: SentinelUnknown,
			})
			options, err := ParseOptions("all", "all", mode)
			if err != nil {
				t.Fatal(err)
			}
			metrics := gather(t, s, options)
			wantCounts, wantInfo := 20, 4
			if mode == PodMetricsInventory {
				wantCounts = 0
			}
			if mode == PodMetricsCounts {
				wantInfo = 0
			}
			if len(metrics[MetricComponentPods]) != wantCounts || len(metrics[MetricComponentInfo]) != wantInfo {
				t.Fatalf("counts=%d info=%d; want %d and %d", len(metrics[MetricComponentPods]), len(metrics[MetricComponentInfo]), wantCounts, wantInfo)
			}
			if len(metrics[MetricComponentReplicas]) != 2 {
				t.Fatal("nil desired scale must remain absent")
			}
			for _, m := range metrics[MetricComponentPods] {
				if label(m, LabelComponentInstance) == "empty" && m.GetGauge().GetValue() != 0 {
					t.Fatal("known empty component must have zero counts")
				}
				if label(m, LabelComponent) == SentinelUnknown && m.GetGauge().GetValue() != 0 {
					t.Fatal("unset Pod phase must not be counted as Unknown")
				}
			}
			if len(metrics[MetricPodWorkloadInfo]) != 3 {
				t.Fatal("Pod list was lost")
			}
			for _, m := range metrics[MetricPodWorkloadInfo] {
				if label(m, LabelWorkloadUID) != "w1" || label(m, LabelUID) == "" {
					t.Fatal("missing Kubernetes identity")
				}
				if mode != PodMetricsCounts && label(m, LabelUID) == "p1" && label(m, LabelPodPhase) != "Running" {
					t.Fatal("current Pod phase missing from attribution")
				}
			}
		})
	}
}

func TestSelectedPhasesRemainDense(t *testing.T) {
	options, err := ParseOptions("Running,Failed", "Running", "counts")
	if err != nil {
		t.Fatal(err)
	}
	metrics := gather(t, fixtureStore(), options)
	if len(metrics[MetricWorkloadStatus]) != 2 || len(metrics[MetricComponentPods]) != 2 {
		t.Fatal("unselected phases were emitted")
	}
	for _, m := range metrics[MetricWorkloadStatus] {
		want := 0.0
		if label(m, LabelPhase) == "Running" {
			want = 1
		}
		if m.GetGauge().GetValue() != want {
			t.Fatal("selected state lost its 0/1 observation")
		}
	}
	options, err = ParseOptions("none", "none", "counts")
	if err != nil {
		t.Fatal(err)
	}
	metrics = gather(t, fixtureStore(), options)
	if len(metrics[MetricWorkloadStatus]) != 0 || len(metrics[MetricComponentPods]) != 0 || len(metrics[MetricWorkloadInfo]) != 1 {
		t.Fatal("disabled phases must disappear without removing workload inventory")
	}
}

func TestRecreatedWorkloadIdentity(t *testing.T) {
	s := fixtureStore()
	recreated, _ := s.Workload("w1")
	recreated.UID = "w2"
	s.UpsertWorkload(recreated)
	metrics := gather(t, s, Options{})
	for _, name := range []string{MetricWorkloadInfo, MetricWorkloadGeneration, MetricWorkloadCreated} {
		seen := map[string]bool{}
		for _, m := range metrics[name] {
			seen[label(m, LabelWorkloadUID)] = true
		}
		if !seen["w1"] || !seen["w2"] {
			t.Fatalf("%s merged two same-name incarnations", name)
		}
	}
	s.DeleteWorkload("w1")
	metrics = gather(t, s, Options{})
	if len(metrics[MetricPodWorkloadInfo]) != 0 || len(metrics[MetricWorkloadInfo]) != 1 {
		t.Fatal("deleting the old incarnation removed the wrong workload or retained its Pods")
	}
}

func TestPodPhaseTransitionUsesCurrentValue(t *testing.T) {
	s := fixtureStore()
	options := Options{PodMetricsMode: PodMetricsInventory}
	before := gather(t, s, options)
	pod, _ := s.Pod("p1")
	pod.Phase = corev1.PodSucceeded
	s.UpsertPod(pod)
	after := gather(t, s, options)
	if len(after[MetricPodWorkloadInfo]) != len(before[MetricPodWorkloadInfo]) {
		t.Fatal("old phase retained")
	}
	for _, m := range after[MetricPodWorkloadInfo] {
		if label(m, LabelUID) == "p1" && label(m, LabelPodPhase) != "Succeeded" {
			t.Fatal("old phase emitted")
		}
	}
}
