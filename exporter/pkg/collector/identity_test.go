// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"slices"
	"testing"
	"time"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func TestSubmittedByMetricContract(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := fixtureStore()
		s.UpsertIdentity(store.IdentityRecord{UID: "w1", Result: identity.Result{Submitter: "alice", Source: "tenant", Type: "unknown", Status: identity.Complete}})
		baseline := gather(t, s, Options{})
		metrics := gather(t, s, Options{Identity: enabled})
		if len(metrics) != 3 {
			t.Fatalf("published %d families, want 3", len(metrics))
		}
		for name, rows := range metrics {
			if len(rows) != len(baseline[name]) {
				t.Fatalf("%s sample count changed", name)
			}
			for _, row := range rows {
				want := make([]string, 0, len(baseline[name][0].Label)+1)
				for _, pair := range baseline[name][0].Label {
					want = append(want, pair.GetName())
				}
				if enabled && name != MetricExporterObservationHealthy {
					want = append(want, LabelSubmittedBy)
					if label(row, LabelSubmittedBy) != "alice" {
						t.Fatalf("%s did not inherit root submitter", name)
					}
				}
				var got []string
				for _, pair := range row.Label {
					got = append(got, pair.GetName())
				}
				slices.Sort(want)
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Fatalf("%s labels = %v, want %v", name, got, want)
				}
			}
		}
	}
}

func TestSubmitterResolutionAndRecovery(t *testing.T) {
	a := identity.Result{Submitter: "alice", Source: "tenant-a", Type: "unknown", Status: identity.Complete}
	for _, tc := range []struct {
		name      string
		root, pod identity.Result
		want      string
	}{
		{name: "root only", root: a, want: "alice"},
		{name: "pod only", pod: a, want: "alice"},
		{name: "agreement", root: a, pod: a, want: "alice"},
		{name: "missing"},
		{name: "partial", pod: identity.Result{Submitter: "alice", Source: "tenant-a", Type: "unknown", Status: identity.Partial}, want: "alice"},
		{name: "conflicting principal", root: a, pod: identity.Result{Submitter: "bob", Source: "tenant-a", Status: identity.Complete}},
		{name: "conflicting authority", root: a, pod: identity.Result{Submitter: "alice", Source: "tenant-b", Status: identity.Complete}},
		{name: "evaluation error", root: a, pod: identity.Result{Status: identity.EvaluationError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixtureStore()
			s.UpsertIdentity(store.IdentityRecord{UID: "w1", Result: tc.root})
			s.UpsertIdentity(store.IdentityRecord{UID: "p1", Result: tc.pod})
			assertSubmittedBy(t, s, tc.want)
			s.DeleteIdentity("p1")
			assertSubmittedBy(t, s, tc.root.Submitter)
		})
	}
}

func TestStaleOrMismatchedAttributionCannotSupplySubmitter(t *testing.T) {
	s := fixtureStore()
	a := identity.Result{Submitter: "alice", Source: "tenant", Type: "unknown", Status: identity.Complete}
	s.UpsertIdentity(store.IdentityRecord{UID: "w1", Result: a})
	s.InvalidateWorkload("w1", store.ReasonPermissionDenied)
	assertSubmittedBy(t, s, "")
	root, _ := s.Workload("w1")
	s.SetHealth([]store.HealthRecord{{Scope: "workload", UID: "w1", Ref: root.Ref, Healthy: true, VerifiedAt: time.Now(), MaxAge: time.Minute}}, s.HealthEpoch())
	assertSubmittedBy(t, s, "alice")
	s.SetHealth([]store.HealthRecord{{Scope: "workload", UID: "w1", Ref: root.Ref, Healthy: true, VerifiedAt: time.Now().Add(-2 * time.Minute), MaxAge: time.Minute}}, s.HealthEpoch())
	assertSubmittedBy(t, s, "")
	s.SetHealth([]store.HealthRecord{{Scope: "workload", UID: "w1", Ref: root.Ref, Healthy: true, VerifiedAt: time.Now(), MaxAge: time.Minute}}, s.HealthEpoch())
	a.Submitter = "bob"
	s.UpsertIdentity(store.IdentityRecord{UID: "p1", Result: a})
	pod, _ := s.Pod("p1")
	pod.Workload.Name = "different-root"
	s.UpsertPod(pod)
	assertSubmittedBy(t, s, "alice")
	s.DeleteWorkload("w1")
	if metrics := gather(t, s, Options{Identity: true}); len(metrics) != 0 {
		t.Fatalf("deleted workload retained metrics: %v", metrics)
	}
}

func assertSubmittedBy(t *testing.T, s *store.Store, want string) {
	t.Helper()
	metrics := gather(t, s, Options{Identity: true})
	for _, name := range []string{MetricPodWorkloadInfo, MetricWorkloadStatus} {
		if len(metrics[name]) == 0 {
			t.Fatalf("missing %s", name)
		}
		for _, row := range metrics[name] {
			if got := label(row, LabelSubmittedBy); got != want {
				t.Fatalf("%s submitted_by=%q, want %q", name, got, want)
			}
		}
	}
}
