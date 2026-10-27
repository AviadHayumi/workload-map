// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"cmp"
	"maps"
	"slices"
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
	if reason == "" || reason == ReasonNone {
		delete(s.failures, key)
	} else {
		s.failures[key] = reason
	}
}

// RetirePublicationFailure releases an abandoned generation key only when no
// current publication or health record refers to it. Runtime must ensure that
// future accepted publications will not reuse the retired generation.
func (s *Store) RetirePublicationFailure(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failureRefs[key] == 0 {
		delete(s.failures, key)
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
		for key, count := range page.dependencies {
			s.adjustFailureReferences(key, count*delta)
		}
	}
}

func (s *Store) adjustEvidenceReferences(e Evidence, delta int) {
	for _, key := range e.Dependencies {
		s.adjustFailureReferences(key, delta)
	}
}

func (s *Store) adjustFailureReferences(key string, delta int) {
	s.failureRefs[key] += delta
	if s.failureRefs[key] == 0 {
		delete(s.failureRefs, key)
		delete(s.failures, key)
	}
}

// Rows returns immutable rows. Callers must not modify the slice or nested data.
func (p *PodPage) Rows() []PublishedPod { return p.rows }

func newPodPage(rows []PublishedPod) *PodPage {
	page := &PodPage{rows: rows, dependencies: make(map[string]int)}
	for _, row := range rows {
		for _, key := range row.Mapping.Dependencies {
			page.dependencies[key]++
		}
	}
	return page
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

// WithPod replaces a Pod using copy-on-write pages. Bulk initialization and
// refreshes use BuildPodPages. Sorted pages stay sorted and bounded in size.
func (p *Publication) WithPod(uid types.UID, replacement *PublishedPod) *Publication {
	next := *p
	next.PodPages = slices.Clone(p.PodPages)
	pageIndex, rowIndex, found := p.podPosition(uid)
	if !found && replacement == nil {
		return &next
	}
	if len(p.PodPages) == 0 {
		next.PodPages = []*PodPage{newPodPage([]PublishedPod{*replacement})}
		return &next
	}
	rows := slices.Clone(p.PodPages[pageIndex].rows)
	switch {
	case found && replacement == nil:
		rows = slices.Delete(rows, rowIndex, rowIndex+1)
	case found:
		rows[rowIndex] = *replacement
	default:
		rows = slices.Insert(rows, rowIndex, *replacement)
	}
	switch {
	case len(rows) == 0:
		next.PodPages = slices.Delete(next.PodPages, pageIndex, pageIndex+1)
	case len(rows) > PodPageSize:
		middle := len(rows) / 2
		next.PodPages[pageIndex] = newPodPage(rows[:middle:middle])
		next.PodPages = slices.Insert(next.PodPages, pageIndex+1, newPodPage(rows[middle:]))
	default:
		next.PodPages[pageIndex] = newPodPage(rows)
	}
	if replacement == nil && len(rows) > 0 && len(next.PodPages) > 1 {
		left := pageIndex
		if left == len(next.PodPages)-1 {
			left--
		}
		a, b := next.PodPages[left], next.PodPages[left+1]
		if len(a.rows)+len(b.rows) <= PodPageSize {
			merged := make([]PublishedPod, 0, len(a.rows)+len(b.rows))
			merged = append(merged, a.rows...)
			merged = append(merged, b.rows...)
			next.PodPages[left] = newPodPage(merged)
			next.PodPages = slices.Delete(next.PodPages, left+1, left+2)
		}
	}
	return &next
}

func (p *Publication) Pod(uid types.UID) (PublishedPod, bool) {
	page, row, found := p.podPosition(uid)
	if !found {
		return PublishedPod{}, false
	}
	return p.PodPages[page].rows[row], true
}

func (p *Publication) podPosition(uid types.UID) (page, row int, found bool) {
	if len(p.PodPages) == 0 {
		return 0, 0, false
	}
	page, _ = slices.BinarySearchFunc(p.PodPages, uid, func(page *PodPage, key types.UID) int {
		return cmp.Compare(page.rows[len(page.rows)-1].Record.UID, key)
	})
	if page == len(p.PodPages) {
		page--
	}
	row, found = slices.BinarySearchFunc(p.PodPages[page].rows, uid, func(row PublishedPod, key types.UID) int {
		return cmp.Compare(row.Record.UID, key)
	})
	return
}

// BuildPodPages sorts a copy of the input and reuses unchanged immutable pages.
// Input rows must have unique UIDs; returned pages must keep their order.
// Use this for initial publication and full refreshes instead of repeated WithPod.
func BuildPodPages(rows []PublishedPod, previous *Publication) []*PodPage {
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b PublishedPod) int { return cmp.Compare(a.Record.UID, b.Record.UID) })
	pages := make([]*PodPage, 0, (len(rows)+PodPageSize-1)/PodPageSize)
	for start := 0; start < len(rows); start += PodPageSize {
		end := min(start+PodPageSize, len(rows))
		i := start / PodPageSize
		if previous != nil && i < len(previous.PodPages) && slices.EqualFunc(previous.PodPages[i].rows, rows[start:end], equalPod) {
			pages = append(pages, previous.PodPages[i])
		} else {
			pages = append(pages, newPodPage(slices.Clone(rows[start:end])))
		}
	}
	return pages
}

func equalPod(a, b PublishedPod) bool {
	x, y := a.Record, b.Record
	return x.WorkloadGVK == y.WorkloadGVK && x.Namespace == y.Namespace && x.UID == y.UID && x.Name == y.Name && x.Component == y.Component && x.Instance == y.Instance && x.Group == y.Group && x.OwnerResolution == y.OwnerResolution && maps.Equal(x.Labels, y.Labels) && a.Mapping.ExpiresAt.Equal(b.Mapping.ExpiresAt) && a.Mapping.Reason == b.Mapping.Reason && slices.Equal(a.Mapping.Dependencies, b.Mapping.Dependencies)
}
