// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func TestCaptureFreezesTimeAndIndependentFailureEvidence(t *testing.T) {
	now := time.Unix(100, 0)
	s := New()
	s.BindPublication(nil, func() time.Time { return now })
	p := &Publication{
		Workload:   WorkloadRecord{UID: "root"},
		Status:     Evidence{ExpiresAt: now.Add(time.Minute), Dependencies: []string{"definition"}},
		Creation:   Evidence{ExpiresAt: now.Add(2 * time.Minute)},
		Membership: Evidence{ExpiresAt: now.Add(time.Minute), Dependencies: []string{"membership"}},
	}
	s.Publish(p)
	before := s.Capture()
	s.SetPublicationFailure("definition", ReasonEvaluationFailed)
	after := s.Capture()
	if valid, _ := before.Valid(p.Status); !valid {
		t.Fatal("a later failure changed an already captured view")
	}
	if valid, reason := after.Valid(p.Status); valid || reason != ReasonEvaluationFailed {
		t.Fatalf("status validity = %t, %s", valid, reason)
	}
	if valid, _ := after.Valid(p.Creation); !valid {
		t.Fatal("status failure invalidated independent creation evidence")
	}
	now = now.Add(2 * time.Minute)
	if valid, _ := after.Valid(p.Creation); !valid {
		t.Fatal("a retained view used a later clock")
	}
	if valid, _ := s.Capture().Valid(p.Creation); valid {
		t.Fatal("evidence did not expire at its deadline")
	}
	if !p.Creation.ExpiresAt.Equal(time.Unix(220, 0)) {
		t.Fatal("collection renewed evidence")
	}
}

func TestPublicationPagesRetainCapturedRows(t *testing.T) {
	p := &Publication{Workload: WorkloadRecord{UID: "root"}}
	for i := range PodPageSize + 1 {
		uid := types.UID(fmt.Sprintf("pod-%d", i))
		p = p.WithPod(uid, &PublishedPod{Record: PodRecord{UID: uid, Instance: "old"}})
	}
	s := New()
	s.Publish(p)
	before := s.Capture()
	changed := p.WithPod("pod-0", &PublishedPod{Record: PodRecord{UID: "pod-0", Instance: "new"}})
	s.Publish(changed)
	if before.Records[0].PodPages[0].Rows[0].Record.Instance != "old" {
		t.Fatal("replacement modified the captured page")
	}
	if p.PodPages[1] != changed.PodPages[1] {
		t.Fatal("unchanged page was copied")
	}
	removed := changed.WithPod("pod-0", nil)
	if _, exists := removed.Pod("pod-0"); exists {
		t.Fatal("deleted Pod retained")
	}
	if row, _ := changed.Pod("pod-0"); row.Record.Instance != "new" {
		t.Fatal("deletion modified the old view")
	}
	s.RemovePublication("root")
	if len(s.Capture().Records) != 0 || len(before.Records) != 1 {
		t.Fatal("deletion changed retained capture")
	}
}

func TestBulkPublicationReusesUnchangedPages(t *testing.T) {
	rows := make([]PublishedPod, 10_000)
	for i := range rows {
		rows[i].Record.UID = types.UID(fmt.Sprintf("pod-%05d", i))
	}
	original := &Publication{PodPages: BuildPodPages(rows, nil)}
	rows[500].Record.Instance = "changed"
	replacement := BuildPodPages(rows, original)
	for i := range replacement {
		if i == 500/PodPageSize {
			if replacement[i] == original.PodPages[i] {
				t.Fatal("changed page was shared")
			}
		} else if replacement[i] != original.PodPages[i] {
			t.Fatalf("unchanged page %d was copied", i)
		}
	}
	if original.PodPages[500/PodPageSize].Rows[500%PodPageSize].Record.Instance != "" {
		t.Fatal("bulk replacement mutated an existing page")
	}
}

func TestRetiredFailureKeysAreBoundedByCurrentPublications(t *testing.T) {
	s := New()
	for i := range 1000 {
		key := fmt.Sprintf("membership:%d", i)
		record := &Publication{Workload: WorkloadRecord{UID: "root"}, Membership: Evidence{Dependencies: []string{key}}}
		s.Publish(record)
		s.SetPublicationFailure(key, ReasonAPIUnavailable)
	}
	if len(s.publicationFailures) != 1 || len(s.publicationFailureRefs) != 1 {
		t.Fatalf("retired keys retained: failures=%d references=%d", len(s.publicationFailures), len(s.publicationFailureRefs))
	}
	captured := s.Capture()
	s.RemovePublication("root")
	if len(s.publicationFailures) != 0 || len(s.publicationFailureRefs) != 0 {
		t.Fatal("last publication left failure keys behind")
	}
	if _, reason := captured.Valid(captured.Records[0].Membership); reason != ReasonAPIUnavailable {
		t.Fatal("retiring a key changed the captured failure")
	}
}
