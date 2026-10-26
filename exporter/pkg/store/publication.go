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

const PodPageSize = 256

// Evidence belongs to one independently verified field. Dependencies are
// controller-owned failure keys, captured with the immutable records.
type Evidence struct {
	ExpiresAt    time.Time
	Reason       string
	Dependencies []string
}

type PublishedPod struct {
	Record  PodRecord
	Mapping Evidence
}

type PodPage struct {
	Rows []PublishedPod
}

// Publication and every page are immutable after Publish.
type Publication struct {
	Workload       WorkloadRecord
	Status         Evidence
	Creation       Evidence
	Membership     Evidence
	SubmittedBy    string
	FailureReasons []string
	PodPages       []*PodPage
}

// CapturedView fixes the time and failure state used by every metric family.
type CapturedView struct {
	At       time.Time
	Records  []*Publication
	Health   []HealthRecord
	failures map[string]string
}

func (v CapturedView) Valid(e Evidence) (bool, string) {
	if e.Reason != "" && e.Reason != ReasonNone {
		return false, e.Reason
	}
	for _, key := range e.Dependencies {
		if reason := v.failures[key]; reason != "" {
			return false, reason
		}
	}
	if e.ExpiresAt.IsZero() || !v.At.Before(e.ExpiresAt) {
		return false, ReasonAPIUnavailable
	}
	return true, ReasonNone
}

// Capture copies pointers, not Pod rows. Published records and pages remain
// immutable while a reader formats this view outside the lock.
func (s *Store) Capture() CapturedView {
	if s.publicationLock != nil {
		s.publicationLock.Lock()
		defer s.publicationLock.Unlock()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	view := CapturedView{
		At: s.publicationNow(), Records: make([]*Publication, 0, len(s.publications)),
		Health: slices.Clone(s.publicationHealth), failures: maps.Clone(s.publicationFailures),
	}
	for _, record := range s.publications {
		view.Records = append(view.Records, record)
	}
	return view
}

// BindPublication is called before starting workers or registering collection.
// Sharing the controller lock makes a multi-record event atomic to capture.
func (s *Store) BindPublication(lock *sync.Mutex, now func() time.Time) {
	s.publicationLock = lock
	if now != nil {
		s.publicationNow = now
	}
}

// Publish transfers ownership of immutable record data to the store. It must
// be called under the controller publication lock after checking dependencies.
func (s *Store) Publish(record *Publication) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.publications[record.Workload.UID]
	// Add new references before dropping old ones so a still-live failure
	// cannot disappear while one immutable record replaces another.
	s.adjustEvidenceReferences(record, previous, 1)
	s.adjustEvidenceReferences(previous, record, -1)
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
	s.adjustEvidenceReferences(s.publications[uid], nil, -1)
	delete(s.publications, uid)
}

func (s *Store) SetPublicationFailure(key, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reason == "" {
		delete(s.publicationFailures, key)
	} else if s.publicationFailureRefs[key] > 0 {
		s.publicationFailures[key] = reason
	}
}

func (s *Store) adjustEvidenceReferences(record, sharedWith *Publication, delta int) {
	if record == nil {
		return
	}
	update := func(e Evidence) {
		for _, key := range e.Dependencies {
			s.publicationFailureRefs[key] += delta
			if s.publicationFailureRefs[key] == 0 {
				delete(s.publicationFailureRefs, key)
				delete(s.publicationFailures, key)
			}
		}
	}
	update(record.Status)
	update(record.Creation)
	update(record.Membership)
	sharedPages := map[*PodPage]bool{}
	if sharedWith != nil {
		for _, page := range sharedWith.PodPages {
			sharedPages[page] = true
		}
	}
	for _, page := range record.PodPages {
		if sharedPages[page] {
			continue
		}
		for _, row := range page.Rows {
			update(row.Mapping)
		}
	}
}

func (s *Store) SetPublicationHealth(health []HealthRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicationHealth = slices.Clone(health)
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
