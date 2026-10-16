// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func TestSubmitterConflictRecoveryAndUnknownRoot(t *testing.T) {
	s := fixtureStore()
	a := identity.Result{Submitter: "alice", Source: "tenant-a", Type: "unknown", Status: "complete", Rule: "test"}
	s.UpsertIdentity(store.IdentityRecord{UID: "w1", Ref: store.WorkloadRef{Name: "llm", Namespace: "team-a"}, Result: a})
	s.UpsertIdentity(store.IdentityRecord{UID: "p1", Ref: store.WorkloadRef{Name: "prefill", Namespace: "team-a", Kind: "Pod"}, Result: a})
	b := a
	b.Submitter = "bob"
	s.UpsertIdentity(store.IdentityRecord{UID: "p2", Result: b})
	m := gather(t, s, Options{Identity: true})
	row := m["karta_workload_submitter_info"][0]
	if label(row, "status") != "conflict" || label(row, "submitter") != "" {
		t.Fatalf("conflict hidden: %v", row)
	}
	s.DeleteIdentity("p2")
	m = gather(t, s, Options{Identity: true})
	row = m["karta_workload_submitter_info"][0]
	if label(row, "submitter") != "alice" {
		t.Fatalf("failed recovery: %v", row)
	}
	s.UpsertIdentity(store.IdentityRecord{UID: "unresolved", Ref: store.WorkloadRef{Name: "unknown-root", Kind: "Pod"}, Result: a})
	m = gather(t, s, Options{Identity: true})
	if len(m["karta_resource_submitter_info"]) != 3 || len(m["karta_workload_submitter_info"]) != 1 {
		t.Fatal("unresolved identity invented a root")
	}
	s.DeleteIdentity("unresolved")
	if len(gather(t, s, Options{Identity: true})["karta_resource_submitter_info"]) != 2 {
		t.Fatal("deleted UID survived")
	}
}

func TestStaleIdentityDoesNotExposePrincipal(t *testing.T) {
	s := fixtureStore()
	s.UpsertIdentity(store.IdentityRecord{UID: "w1", Result: identity.Result{Submitter: "alice", Source: "tenant", Type: "unknown", Status: "complete"}})
	s.InvalidateWorkload("w1", store.ReasonPermissionDenied)
	metrics := gather(t, s, Options{Identity: true})
	for _, family := range []string{"karta_resource_submitter_info", "karta_workload_submitter_info"} {
		for _, row := range metrics[family] {
			if label(row, "status") != "stale" || label(row, "submitter") != "" {
				t.Fatalf("stale identity exposed: %v", row)
			}
		}
	}
}
