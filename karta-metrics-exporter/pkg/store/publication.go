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
	for _, page := range record.PodPages {
		page.prepare()
	}
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

// prepare caches only derived immutable data. Publish calls it before locking.
func (p *PodPage) prepare() {
	p.prepared.Do(func() {
		p.dependencies = make(map[string]int)
		p.sorted = true
		for i, row := range p.Rows {
			if i > 0 && p.Rows[i-1].Record.UID >= row.Record.UID {
				p.sorted = false
			}
			for _, key := range row.Mapping.Dependencies {
				p.dependencies[key]++
			}
		}
	})
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
	pageIndex, rowIndex, found, sorted := p.podPosition(uid)
	if !found && replacement == nil {
		return &next
	}
	if len(p.PodPages) == 0 {
		next.PodPages = []*PodPage{{Rows: []PublishedPod{*replacement}}}
		return &next
	}
	rows := slices.Clone(p.PodPages[pageIndex].Rows)
	switch {
	case found && replacement == nil:
		rows = slices.Delete(rows, rowIndex, rowIndex+1)
	case found:
		rows[rowIndex] = *replacement
	default:
		rows = slices.Insert(rows, rowIndex, *replacement)
	}
	next.PodPages[pageIndex] = &PodPage{Rows: rows}
	switch {
	case len(rows) == 0:
		next.PodPages = slices.Delete(next.PodPages, pageIndex, pageIndex+1)
	case len(rows) > PodPageSize:
		middle := len(rows) / 2
		next.PodPages[pageIndex] = &PodPage{Rows: rows[:middle:middle]}
		next.PodPages = slices.Insert(next.PodPages, pageIndex+1, &PodPage{Rows: rows[middle:]})
	case replacement == nil && sorted && len(next.PodPages) > 1:
		left := pageIndex
		if left == len(next.PodPages)-1 {
			left--
		}
		a, b := next.PodPages[left], next.PodPages[left+1]
		if len(a.Rows)+len(b.Rows) <= PodPageSize {
			merged := make([]PublishedPod, 0, len(a.Rows)+len(b.Rows))
			merged = append(merged, a.Rows...)
			merged = append(merged, b.Rows...)
			next.PodPages[left] = &PodPage{Rows: merged}
			next.PodPages = slices.Delete(next.PodPages, left+1, left+2)
		}
	}
	return &next
}

func (p *Publication) Pod(uid types.UID) (PublishedPod, bool) {
	page, row, found, _ := p.podPosition(uid)
	if !found {
		return PublishedPod{}, false
	}
	return p.PodPages[page].Rows[row], true
}

// Public callers can supply arbitrary pages. Validate page bounds using cached
// metadata before taking the sorted fast path; never assume caller ordering.
func (p *Publication) podPosition(uid types.UID) (page, row int, found, sorted bool) {
	sorted = true
	for i, current := range p.PodPages {
		current.prepare()
		if !current.sorted || len(current.Rows) == 0 || (i > 0 && (len(p.PodPages[i-1].Rows) == 0 || p.PodPages[i-1].Rows[len(p.PodPages[i-1].Rows)-1].Record.UID >= current.Rows[0].Record.UID)) {
			sorted = false
			break
		}
	}
	if len(p.PodPages) == 0 {
		return 0, 0, false, sorted
	}
	if sorted {
		page, _ = slices.BinarySearchFunc(p.PodPages, uid, func(page *PodPage, key types.UID) int {
			return cmp.Compare(page.Rows[len(page.Rows)-1].Record.UID, key)
		})
		if page == len(p.PodPages) {
			page--
		}
		row, found = slices.BinarySearchFunc(p.PodPages[page].Rows, uid, func(row PublishedPod, key types.UID) int { return cmp.Compare(row.Record.UID, key) })
		return
	}
	for i, current := range p.PodPages {
		for j, entry := range current.Rows {
			if entry.Record.UID == uid {
				return i, j, true, false
			}
		}
	}
	page = len(p.PodPages) - 1
	row = len(p.PodPages[page].Rows)
	return
}

// BuildPodPages sorts a copy of the input and reuses unchanged immutable pages.
// Use this for initial publication and full refreshes instead of repeated WithPod.
func BuildPodPages(rows []PublishedPod, previous *Publication) []*PodPage {
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b PublishedPod) int { return cmp.Compare(a.Record.UID, b.Record.UID) })
	pages := make([]*PodPage, 0, (len(rows)+PodPageSize-1)/PodPageSize)
	for start := 0; start < len(rows); start += PodPageSize {
		end := min(start+PodPageSize, len(rows))
		i := start / PodPageSize
		if previous != nil && i < len(previous.PodPages) && slices.EqualFunc(previous.PodPages[i].Rows, rows[start:end], equalPod) {
			pages = append(pages, previous.PodPages[i])
		} else {
			pages = append(pages, &PodPage{Rows: slices.Clone(rows[start:end])})
		}
	}
	return pages
}

func equalPod(a, b PublishedPod) bool {
	x, y := a.Record, b.Record
	return x.UID == y.UID && x.Name == y.Name && x.Component == y.Component && x.Instance == y.Instance && x.Group == y.Group && x.OwnerResolution == y.OwnerResolution && maps.Equal(x.Labels, y.Labels) && a.Mapping.ExpiresAt.Equal(b.Mapping.ExpiresAt) && a.Mapping.Reason == b.Mapping.Reason && slices.Equal(a.Mapping.Dependencies, b.Mapping.Dependencies)
}
