// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

func gather(t *testing.T, s Capturer, options Options) map[string][]*dto.Metric {
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
	if len(metrics[MetricWorkloadStatus]) != 0 || len(metrics[MetricPodWorkloadInfo]) != 2 || len(metrics[MetricExporterObservationValid]) != 1 || len(metrics[MetricWorkloadCreatedTimestamp]) != 1 {
		t.Fatal("disabling phases changed other families")
	}
}

func TestMultiplePhasesAndDefinitionReplacement(t *testing.T) {
	s := fixtureStore()
	p := *s.Published("w1")
	p.Workload.Phases = []v1alpha1.ResourceStatus{v1alpha1.RunningStatus, v1alpha1.DegradedStatus}
	p.Workload.Karta, p.Workload.KartaSource, p.Workload.OwnerResolution = "custom-jobset", "cluster", "ignored_owner"
	s.Publish(&p)
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
	recreated := *s.Published("w1")
	recreated.Workload.UID = "w2"
	recreated.PodPages = nil
	s.Publish(&recreated)
	metrics := gather(t, s, Options{})
	seen := map[string]bool{}
	for _, m := range metrics[MetricWorkloadStatus] {
		seen[label(m, LabelWorkloadUID)] = true
	}
	if !seen["w1"] || !seen["w2"] {
		t.Fatal("same-name UIDs were merged")
	}
	pod, _ := s.Published("w1").Pod("p1")
	pod.Record.UID, pod.Record.WorkloadUID, pod.Record.Replica = "replacement", "w2", "group-2"
	s.Publish(recreated.WithPod("replacement", &pod))
	s.RemovePublication("w1")
	metrics = gather(t, s, Options{})
	if len(metrics[MetricPodWorkloadInfo]) != 1 || len(metrics[MetricWorkloadStatus]) != 9 {
		t.Fatal("deleted wrong incarnation or retained old Pod")
	}
	m := metrics[MetricPodWorkloadInfo][0]
	if label(m, LabelPodUID) != "replacement" || label(m, LabelReplicaGroupID) != "group-2" || label(m, LabelWorkloadUID) != "w2" {
		t.Fatal("old attribution retained")
	}
}

func TestPendingAndGenericPublicationOmitInapplicableFields(t *testing.T) {
	s := fixtureStore()
	p := *s.Published("w1")
	p.Workload.HasStatus = false
	p.Workload.Karta, p.Workload.KartaSource = "", ""
	s.Publish(&p)
	metrics := gather(t, s, Options{})
	if len(metrics[MetricWorkloadStatus]) != 0 || len(metrics[MetricPodWorkloadInfo]) != 2 || len(metrics[MetricWorkloadCreatedTimestamp]) != 1 {
		t.Fatal("generic root lost valid fields")
	}
	p.PodPages = nil
	s.Publish(&p)
	metrics = gather(t, s, Options{})
	if len(metrics[MetricPodWorkloadInfo]) != 0 || len(metrics[MetricWorkloadCreatedTimestamp]) != 1 || len(metrics[MetricExporterObservationValid]) != 1 {
		t.Fatal("pending mapping invented a row/failure or removed creation")
	}
}

func TestOptionsAreCapturedAtRegistration(t *testing.T) {
	options := Options{WorkloadPhases: []v1alpha1.ResourceStatus{v1alpha1.RunningStatus}}
	registry := testGatherer(t, fixtureStore(), options)
	options.WorkloadPhases[0] = v1alpha1.FailedStatus
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == MetricWorkloadStatus && (len(family.Metric) != 1 || label(family.Metric[0], LabelPhase) != "Running") {
			t.Fatal("caller modified registered options")
		}
	}
}
