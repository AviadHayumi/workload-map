// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"testing"
	"time"
)

func TestHealthFreshnessAndFailureIsolation(t *testing.T) {
	now := time.Now()
	h := HealthRecord{Scope: "workload", UID: "a", Healthy: true, VerifiedAt: now, MaxAge: time.Minute, Dependencies: []WorkloadRef{{Kind: "Pod"}}}
	if h.Value(now) != 1 || h.Value(now.Add(time.Minute)) != 0 || h.Value(now.Add(-time.Second)) != 0 {
		t.Fatal("freshness boundary is not fail closed")
	}
	s := New()
	s.UpsertWorkload(WorkloadRecord{UID: "a"})
	s.UpsertWorkload(WorkloadRecord{UID: "b"})
	b := h
	b.UID = "b"
	s.SetHealth([]HealthRecord{h, b}, 0)
	s.UpsertPod(PodRecord{UID: "bad-pod", WorkloadUID: "a", Reason: "jq_error"})
	s.SetHealth([]HealthRecord{h, b}, 0)
	got := s.Snapshot()
	if got.Health[0].Healthy || !got.Health[1].Healthy {
		t.Fatal("a workload failure must not poison another workload or be overwritten by an older pass")
	}
	s.UpsertPod(PodRecord{UID: "bad-pod", WorkloadUID: "a"})
	if s.Snapshot().Health[0].Healthy {
		t.Fatal("an event must not recover health")
	}
	s.SetHealth([]HealthRecord{h, b}, got.HealthEpoch)
	if !s.Snapshot().Health[0].Healthy {
		t.Fatal("fresh verification must recover health")
	}
	epoch := s.Snapshot().HealthEpoch
	s.InvalidateHealth("missing.example", "Unrelated")
	s.SetHealth([]HealthRecord{h, b}, epoch)
	for _, row := range s.Snapshot().Health {
		if !row.Healthy {
			t.Fatal("unrelated kind failure must not invalidate a workload")
		}
	}
	epoch = s.Snapshot().HealthEpoch
	s.InvalidateHealth("", "Pod")
	s.SetHealth([]HealthRecord{h, b}, epoch)
	for _, row := range s.Snapshot().Health {
		if row.Healthy {
			t.Fatal("concurrent collection failure must invalidate a pass")
		}
	}
}
