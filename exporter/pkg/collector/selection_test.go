// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	dto "github.com/prometheus/client_model/go"
)

func gather(t *testing.T, s *store.Store, options Options) map[string][]*dto.Metric {
	t.Helper()
	families, err := testGatherer(t, s, options).Gather()
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

func TestSelectedPhasesRemainDense(t *testing.T) {
	options, err := ParseOptions("Running,Failed")
	if err != nil {
		t.Fatal(err)
	}
	metrics := gather(t, fixtureStore(), options)
	if len(metrics[MetricWorkloadStatus]) != 2 {
		t.Fatal("unselected phases emitted")
	}
	for _, m := range metrics[MetricWorkloadStatus] {
		want := 0.0
		if label(m, LabelPhase) == "Running" {
			want = 1
		}
		if m.GetGauge().GetValue() != want {
			t.Fatal("selected phase lost its dense 0/1 value")
		}
	}
	options, err = ParseOptions("none")
	if err != nil {
		t.Fatal(err)
	}
	metrics = gather(t, fixtureStore(), options)
	if len(metrics[MetricWorkloadStatus]) != 0 || len(metrics[MetricPodWorkloadInfo]) != 2 || len(metrics[MetricExporterObservationHealthy]) != 1 {
		t.Fatal("disabling phases changed attribution or health")
	}
}

func TestMultiplePhasesAndDefinitionReplacement(t *testing.T) {
	s := fixtureStore()
	w, _ := s.Workload("w1")
	w.Phases = []v1alpha1.ResourceStatus{v1alpha1.RunningStatus, v1alpha1.DegradedStatus}
	w.Karta, w.KartaSource, w.OwnerResolution = "custom-jobset", "cluster", "ignored_owner"
	s.UpsertWorkload(w)
	metrics := gather(t, s, Options{})
	active := 0
	for _, m := range metrics[MetricWorkloadStatus] {
		if m.GetGauge().GetValue() == 1 {
			active++
		}
		if label(m, LabelKarta) != "custom-jobset" || label(m, LabelKartaSource) != "cluster" || label(m, LabelOwnerResolution) != "ignored_owner" {
			t.Fatal("old definition labels retained")
		}
	}
	if active != 2 {
		t.Fatalf("active phases = %d, want 2", active)
	}
}

func TestUIDRecreationAndMappingReplacement(t *testing.T) {
	s := fixtureStore()
	recreated, _ := s.Workload("w1")
	recreated.UID = "w2"
	s.UpsertWorkload(recreated)
	metrics := gather(t, s, Options{})
	seen := map[string]bool{}
	for _, m := range metrics[MetricWorkloadStatus] {
		seen[label(m, LabelWorkloadUID)] = true
	}
	if !seen["w1"] || !seen["w2"] {
		t.Fatal("same-name UIDs were merged")
	}
	pod, _ := s.Pod("p1")
	s.DeletePod("p1")
	pod.UID, pod.WorkloadUID, pod.Replica = "replacement", "w2", "group-2"
	s.UpsertPod(pod)
	s.DeleteWorkload("w1")
	metrics = gather(t, s, Options{})
	if len(metrics[MetricPodWorkloadInfo]) != 1 || len(metrics[MetricWorkloadStatus]) != 9 {
		t.Fatal("deleted wrong incarnation or retained old Pod")
	}
	m := metrics[MetricPodWorkloadInfo][0]
	if label(m, LabelPodUID) != "replacement" || label(m, LabelReplicaGroupID) != "group-2" || label(m, LabelWorkloadUID) != "w2" {
		t.Fatal("old attribution retained")
	}
}

func TestPodMappingRequiresConfirmedRootInSameSnapshot(t *testing.T) {
	for _, scenario := range []string{"deleted", "unresolved", "child", "version", "boundary"} {
		t.Run(scenario, func(t *testing.T) {
			s := fixtureStore()
			registry := testGatherer(t, s, Options{})
			assertPods := func(want int) {
				t.Helper()
				families, err := registry.Gather()
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, family := range families {
					if family.GetName() == MetricPodWorkloadInfo {
						count = len(family.Metric)
					}
				}
				if count != want {
					t.Fatalf("Pod mappings = %d, want %d", count, want)
				}
			}
			assertPods(2)
			root, _ := s.Workload("w1")
			latePod, _ := s.Pod("p1")
			switch scenario {
			case "deleted":
				s.DeleteWorkload(root.UID)
				// A concurrent attribution that already read the root can finish late.
				s.UpsertPod(latePod)
			case "unresolved":
				root.OwnerResolution = ""
				root.OwnerFailure = store.ReasonUnresolvedOwner
				s.UpsertWorkload(root)
			case "child":
				root.OwnerResolution = ""
				s.UpsertWorkload(root)
			case "version":
				root.Ref.Version = "v2"
				s.UpsertWorkload(root)
			case "boundary":
				root.OwnerResolution = "ignored_owner"
				s.UpsertWorkload(root)
			}
			assertPods(0)
			root.OwnerFailure = ""
			root.OwnerResolution = "resolved"
			root.HasStatus = false
			s.UpsertWorkload(root)
			latePod.Workload = root.Ref
			s.UpsertPod(latePod)
			s.DeletePod("p2")
			assertPods(1)
		})
	}
}
