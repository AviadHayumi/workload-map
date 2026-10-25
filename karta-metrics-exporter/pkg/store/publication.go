// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"maps"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

type Store struct {
	mu           sync.RWMutex
	now          func() time.Time
	publications map[types.UID]*Publication
	failures     map[string]string
	failureRefs  map[string]int
	health       []HealthRecord
}

const PodPageSize = 256

const (
	ReasonNone              = "none"
	ReasonPermissionDenied  = "permission_denied"
	ReasonAPIUnavailable    = "api_unavailable"
	ReasonInvalidDefinition = "invalid_definition"
	ReasonUnresolvedOwner   = "unresolved_owner"
	ReasonEvaluationFailed  = "evaluation_failed"
)

func New(options Options) Interface {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, publications: make(map[types.UID]*Publication), failures: make(map[string]string), failureRefs: make(map[string]int)}
}

// Publish transfers ownership of the publication and all nested maps and slices.
// Callers must never mutate them after publishing, including through returned views.
func (s *Store) Publish(record *Publication) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.publications[record.Workload.UID]
	// Add references first so replacement cannot clear a still-live failure.
	s.adjustPublicationReferences(record, previous, 1)
	s.adjustPublicationReferences(previous, record, -1)
	s.publications[record.Workload.UID] = record
}

func (s *Store) Published(uid types.UID) *Publication {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publications[uid]
}

func (s *Store) RemovePublication(uid types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adjustPublicationReferences(s.publications[uid], nil, -1)
	delete(s.publications, uid)
}

func (s *Store) SetPublicationFailure(key, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reason == "" {
		delete(s.failures, key)
	} else {
		s.failures[key] = reason
	}
}

func (s *Store) SetPublicationHealth(health []HealthRecord) {
	next := slices.Clone(health)
	for i := range next {
		next[i].Evidence.Dependencies = slices.Clone(next[i].Evidence.Dependencies)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, health := range next {
		s.adjustEvidenceReferences(health.Evidence, 1)
	}
	for _, health := range s.health {
		s.adjustEvidenceReferences(health.Evidence, -1)
	}
	s.health = next
}

// Shared immutable pages keep their existing dependency references. A single-Pod
// update visits only changed rows and the page pointer lists, not every Pod.
func (s *Store) adjustPublicationReferences(record, sharedWith *Publication, delta int) {
	if record == nil {
		return
	}
	s.adjustEvidenceReferences(record.Status, delta)
	s.adjustEvidenceReferences(record.Creation, delta)
	var shared map[*PodPage]bool
	if sharedWith != nil {
		shared = make(map[*PodPage]bool, len(sharedWith.PodPages))
		for _, page := range sharedWith.PodPages {
			shared[page] = true
		}
	}
	for _, page := range record.PodPages {
		if shared[page] {
			continue
		}
		for _, row := range page.Rows {
			s.adjustEvidenceReferences(row.Mapping, delta)
		}
	}
}

func (s *Store) adjustEvidenceReferences(e Evidence, delta int) {
	for _, key := range e.Dependencies {
		s.failureRefs[key] += delta
		if s.failureRefs[key] == 0 {
			delete(s.failureRefs, key)
			delete(s.failures, key)
		}
	}
}

// Capture shares immutable publications and copies the mutable failure state.
func (s *Store) Capture() CapturedView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	view := CapturedView{At: s.now(), Records: make([]*Publication, 0, len(s.publications)), Health: slices.Clone(s.health), failures: maps.Clone(s.failures)}
	for _, record := range s.publications {
		view.Records = append(view.Records, record)
	}
	return view
}

func (v CapturedView) Valid(e Evidence) (bool, string) {
	if e.Reason != "" && e.Reason != ReasonNone {
		return false, e.Reason
	}
	for _, key := range e.Dependencies {
		if reason := v.failures[key]; reason != "" && reason != ReasonNone {
			return false, reason
		}
	}
	if e.ExpiresAt.IsZero() || !v.At.Before(e.ExpiresAt) {
		return false, ReasonAPIUnavailable
	}
	return true, ReasonNone
}

// WithPod replaces only the page containing a changed Pod. A nil replacement
// removes the row. Existing captured views retain their original pages.
func (p *Publication) WithPod(uid types.UID, replacement *PublishedPod) *Publication {
	next := *p
	next.PodPages = slices.Clone(p.PodPages)
	for i, page := range p.PodPages {
		for j, row := range page.Rows {
			if row.Record.UID != uid {
				continue
			}
			rows := slices.Clone(page.Rows)
			if replacement == nil {
				rows = slices.Delete(rows, j, j+1)
			} else {
				rows[j] = *replacement
			}
			next.PodPages[i] = &PodPage{Rows: rows}
			if len(rows) == 0 {
				next.PodPages = slices.Delete(next.PodPages, i, i+1)
			}
			return &next
		}
	}
	if replacement == nil {
		return &next
	}
	if len(next.PodPages) != 0 {
		last := len(next.PodPages) - 1
		if len(next.PodPages[last].Rows) < PodPageSize {
			rows := slices.Clone(next.PodPages[last].Rows)
			next.PodPages[last] = &PodPage{Rows: append(rows, *replacement)}
			return &next
		}
	}
	next.PodPages = append(next.PodPages, &PodPage{Rows: []PublishedPod{*replacement}})
	return &next
}

func (p *Publication) Pod(uid types.UID) (PublishedPod, bool) {
	for _, page := range p.PodPages {
		for _, row := range page.Rows {
			if row.Record.UID == uid {
				return row, true
			}
		}
	}
	return PublishedPod{}, false
}

// BuildPodPages creates a full replacement in linear space, sharing pages whose
// rows are unchanged. Sorting keeps page positions stable across map iteration.
func BuildPodPages(rows []PublishedPod, previous *Publication) []*PodPage {
	rows = slices.Clone(rows)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Record.UID < rows[j].Record.UID })
	pages := make([]*PodPage, 0, (len(rows)+PodPageSize-1)/PodPageSize)
	for start := 0; start < len(rows); start += PodPageSize {
		end := min(start+PodPageSize, len(rows))
		i := start / PodPageSize
		if previous != nil && i < len(previous.PodPages) && reflect.DeepEqual(previous.PodPages[i].Rows, rows[start:end]) {
			pages = append(pages, previous.PodPages[i])
		} else {
			pages = append(pages, &PodPage{Rows: slices.Clone(rows[start:end])})
		}
	}
	return pages
}
