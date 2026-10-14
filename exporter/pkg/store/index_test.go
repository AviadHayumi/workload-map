// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"testing"
	"time"
)

func TestNamespaceFailureSurvivesConcurrentVerification(t *testing.T) {
	s := New()
	now := time.Now()
	rows := []HealthRecord{
		{Scope: "workload", UID: "a", Ref: WorkloadRef{Namespace: "a"}, Healthy: true, VerifiedAt: now, MaxAge: time.Minute},
		{Scope: "workload", UID: "b", Ref: WorkloadRef{Namespace: "b"}, Healthy: true, VerifiedAt: now, MaxAge: time.Minute},
	}
	s.SetHealth(rows, s.HealthEpoch())
	epoch := s.HealthEpoch()
	s.InvalidateNamespace("a", ReasonUnresolvedOwner)
	for range 2 {
		got := s.Snapshot().Health
		if value, reason := got[0].Status(now); value != 0 || reason != ReasonUnresolvedOwner {
			t.Fatalf("namespace failure lost: %v", got[0])
		}
		if got[1].Value(now) != 1 {
			t.Fatal("unrelated namespace invalidated")
		}
		s.SetHealth(rows, epoch)
	}
	if !rows[0].Healthy {
		t.Fatal("SetHealth mutated caller-owned verification results")
	}
	s.SetHealth(rows, s.HealthEpoch())
	if s.Snapshot().Health[0].Value(now) != 1 || len(s.namespaceFailureEpoch) != 0 {
		t.Fatal("fresh verification did not recover and retire namespace failure")
	}
}

func TestHealthReasonPreservesLatestFailure(t *testing.T) {
	s := New()
	now := time.Now()
	row := HealthRecord{Scope: "workload", UID: "a", Healthy: true, VerifiedAt: now, MaxAge: time.Minute, Dependencies: []WorkloadRef{{Group: "batch", Kind: "Job"}}}
	s.SetHealth([]HealthRecord{row}, 0)
	s.InvalidateHealth("batch", "Job", ReasonPermissionDenied)
	s.InvalidateWorkload("a", ReasonEvaluationFailed)
	s.SetHealth([]HealthRecord{row}, 0)
	got := s.Snapshot().Health[0]
	if value, reason := got.Status(now); value != 0 || reason != ReasonEvaluationFailed {
		t.Fatalf("concurrent reason overwritten: %+v", got)
	}
	s.SetHealth([]HealthRecord{row}, s.HealthEpoch())
	got = s.Snapshot().Health[0]
	if value, reason := got.Status(now); value != 1 || reason != ReasonNone {
		t.Fatalf("recovery kept failure reason: %+v", got)
	}
	if value, reason := got.Status(now.Add(time.Minute)); value != 0 || reason != ReasonAPIUnavailable {
		t.Fatal("expired verification remained healthy")
	}
}

func TestWorkloadKindIndexLifecycle(t *testing.T) {
	s := New()
	a := WorkloadRecord{UID: "a", Ref: WorkloadRef{Group: "batch", Kind: "Job"}}
	b := WorkloadRecord{UID: "b", Ref: WorkloadRef{Group: "apps", Kind: "Deployment"}}
	s.UpsertWorkload(a)
	s.UpsertWorkload(b)
	if got := s.WorkloadsOfKind("batch", "Job"); len(got) != 1 || got[0].UID != "a" {
		t.Fatalf("incorrect kind lookup: %v", got)
	}
	a.Ref.Kind = "CronJob"
	s.UpsertWorkload(a)
	if len(s.WorkloadsOfKind("batch", "Job")) != 0 || len(s.WorkloadsOfKind("batch", "CronJob")) != 1 {
		t.Fatal("old kind index survived replacement")
	}
	s.DeleteWorkload("a")
	if len(s.WorkloadsOfKind("batch", "CronJob")) != 0 || len(s.workloadsByKind) != 1 {
		t.Fatal("deletion retained kind index")
	}
}

func TestVerificationRetiresFailureEpochsAfterDeletion(t *testing.T) {
	s := New()
	s.UpsertWorkload(WorkloadRecord{UID: "deleted"})
	s.DeleteWorkload("deleted")
	// An in-flight Pod evaluation can publish after root deletion.
	s.UpsertPod(PodRecord{UID: "late", WorkloadUID: "deleted", Reason: ReasonEvaluationFailed})
	s.DeletePod("late")
	s.InvalidateHealth("removed.example", "Removed", ReasonAPIUnavailable)
	epoch := s.HealthEpoch()
	s.InvalidateWorkload("concurrent", ReasonUnresolvedOwner)
	s.InvalidateHealth("batch", "Job", ReasonPermissionDenied)
	s.SetHealth(nil, epoch)
	if len(s.workloadFailureEpoch) != 1 || s.workloadFailureEpoch["concurrent"].epoch <= epoch {
		t.Fatal("verification must retire old lifetime failures while retaining concurrent ones")
	}
	if len(s.collectionFailureEpoch) != 1 || s.collectionFailureEpoch[resourceKind{"batch", "Job"}].epoch <= epoch {
		t.Fatal("verification must retire old kind failures while retaining concurrent ones")
	}
	s.SetHealth(nil, s.HealthEpoch())
	if len(s.workloadFailureEpoch) != 0 || len(s.collectionFailureEpoch) != 0 {
		t.Fatal("completed verification retained failure epochs")
	}
}
