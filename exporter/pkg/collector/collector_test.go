// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"slices"
	"testing"
	"time"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

func fixtureStore() *store.Store {
	s := store.New()
	ref := store.WorkloadRef{Namespace: "team-a", Name: "llm", Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}
	s.UpsertWorkload(store.WorkloadRecord{UID: "w1", Ref: ref, Karta: "jobset-x-k8s-io-jobset-v1alpha2", KartaSource: "catalog", OwnerResolution: "resolved", HasStatus: true, Phases: []v1alpha1.ResourceStatus{v1alpha1.RunningStatus}})
	s.UpsertPod(store.PodRecord{UID: "p1", Namespace: "team-a", Name: "prefill-0", WorkloadUID: "w1", Workload: ref, OwnerResolution: "resolved", Component: "replicatedjob", Instance: "prefill"})
	s.UpsertPod(store.PodRecord{UID: "p2", Namespace: "team-a", Name: "decode-0", WorkloadUID: "w1", Workload: ref, OwnerResolution: "resolved", Component: "replicatedjob", Instance: "decode"})
	s.SetHealth([]store.HealthRecord{{Scope: "workload", UID: "w1", Ref: ref, Healthy: true, VerifiedAt: time.Now(), MaxAge: time.Minute}}, s.HealthEpoch())
	return s
}

func TestExactKEPContract(t *testing.T) {
	metrics := gather(t, fixtureStore(), Options{})
	wantLabels := map[string][]string{
		"karta_pod_workload_info":            {"namespace", "workload", "workload_group", "workload_version", "workload_kind", "workload_uid", "owner_resolution", "pod", "pod_uid", "component", "component_instance", "replica_group_id"},
		"karta_workload_status":              {"namespace", "workload", "workload_group", "workload_version", "workload_kind", "workload_uid", "phase", "owner_resolution", "karta", "karta_source"},
		"karta_exporter_observation_healthy": {"scope", "namespace", "group", "version", "kind", "workload", "workload_uid", "reason"},
	}
	if len(metrics) != len(wantLabels) {
		t.Fatalf("published %d metric families, want exactly %d: %v", len(metrics), len(wantLabels), metrics)
	}
	for name, labels := range wantLabels {
		slices.Sort(labels)
		if len(metrics[name]) == 0 {
			t.Fatalf("missing metric family %s", name)
		}
		for _, m := range metrics[name] {
			got := make([]string, 0, len(m.Label))
			for _, pair := range m.Label {
				got = append(got, pair.GetName())
			}
			slices.Sort(got)
			if !slices.Equal(got, labels) || m.Gauge == nil {
				t.Fatalf("%s labels=%v, want %v; gauge=%v", name, got, labels, m.Gauge)
			}
		}
	}
	if len(metrics[MetricPodWorkloadInfo]) != 2 || len(metrics[MetricWorkloadStatus]) != 9 || len(metrics[MetricExporterObservationHealthy]) != 1 {
		t.Fatal("unexpected snapshot cardinality")
	}
	for _, m := range metrics[MetricPodWorkloadInfo] {
		if label(m, LabelWorkloadUID) != "w1" || label(m, LabelPodUID) == "" || label(m, LabelWorkloadVersion) != "v1alpha2" || label(m, LabelOwnerResolution) != "resolved" || m.GetGauge().GetValue() != 1 {
			t.Fatalf("incorrect Pod identity: %v", m)
		}
	}
	for _, m := range metrics[MetricWorkloadStatus] {
		want := 0.0
		if label(m, LabelPhase) == "Running" {
			want = 1
		}
		if m.GetGauge().GetValue() != want || label(m, LabelKartaSource) != "catalog" || label(m, LabelKarta) != "jobset-x-k8s-io-jobset-v1alpha2" {
			t.Fatalf("incorrect dense phase or provenance: %v", m)
		}
	}
}

func TestRootStatusAndUnknownOwner(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		resolution             string
		failure                string
		status                 bool
		wantStatus, wantHealth int
	}{
		{name: "confirmed", resolution: "resolved", status: true, wantStatus: 9, wantHealth: 1},
		{name: "ignored boundary", resolution: "ignored_owner", status: true, wantStatus: 9, wantHealth: 1},
		{name: "confirmed child", status: true},
		{name: "unresolved zero-Pod root", failure: "unresolved_owner", status: true, wantHealth: 1},
		{name: "no status definition", resolution: "resolved", wantHealth: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := store.New()
			s.UpsertWorkload(store.WorkloadRecord{UID: "root", Ref: store.WorkloadRef{Namespace: "ns", Name: "root"}, OwnerResolution: tc.resolution, OwnerFailure: tc.failure, HasStatus: tc.status})
			metrics := gather(t, s, Options{})
			if len(metrics[MetricWorkloadStatus]) != tc.wantStatus || len(metrics[MetricExporterObservationHealthy]) != tc.wantHealth {
				t.Fatalf("status/health count %d/%d; want %d/%d", len(metrics[MetricWorkloadStatus]), len(metrics[MetricExporterObservationHealthy]), tc.wantStatus, tc.wantHealth)
			}
			if tc.failure != "" && label(metrics[MetricExporterObservationHealthy][0], LabelReason) != tc.failure {
				t.Fatal("missing immediate owner failure")
			}
		})
	}
}

func TestRecoveryReplacesReasonAndDeletionRemovesSeries(t *testing.T) {
	s := fixtureStore()
	registry := testGatherer(t, s, Options{})
	s.InvalidateWorkload("w1", store.ReasonPermissionDenied)
	assertHealth := func(value float64, reason string) {
		t.Helper()
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, family := range families {
			if family.GetName() != MetricExporterObservationHealthy {
				continue
			}
			for _, m := range family.Metric {
				count++
				if m.GetGauge().GetValue() != value || label(m, LabelReason) != reason {
					t.Fatalf("health = %v", m)
				}
			}
		}
		if count != 1 {
			t.Fatalf("health rows = %d, want 1", count)
		}
	}
	assertHealth(0, "permission_denied")
	workload, _ := s.Workload("w1")
	s.UpsertWorkload(workload)
	assertHealth(0, "permission_denied")
	s.SetHealth([]store.HealthRecord{{Scope: "workload", UID: "w1", Ref: workload.Ref, Healthy: true, VerifiedAt: time.Now(), MaxAge: time.Minute}}, s.HealthEpoch())
	assertHealth(1, "none")
	s.DeleteWorkload("w1")
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("deleted workload retained current series: %v", families)
	}
}
