// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"maps"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

const (
	ReasonNone              = "none"
	ReasonPermissionDenied  = "permission_denied"
	ReasonAPIUnavailable    = "api_unavailable"
	ReasonInvalidDefinition = "invalid_definition"
	ReasonEvaluationFailed  = "evaluation_failed"
	ReasonUnresolvedOwner   = "unresolved_owner"
)

// WorkloadRef identifies a workload object for metric labels.
type WorkloadRef struct {
	Namespace string
	Name      string
	Group     string
	Version   string
	Kind      string
}

// ComponentState identifies one Pod-producing component instance.
type ComponentState struct {
	Component string
	Instance  string
}

// WorkloadRecord is the stored state of one Karta-described workload.
// Records are immutable once inserted; callers build a fresh record per event.
type WorkloadRecord struct {
	UID              types.UID
	Ref              WorkloadRef
	Karta            string
	KartaSource      string
	OwnerResolution  string
	OwnerFailure     string
	Generation       int64
	CreatedTimestamp int64
	HasStatus        bool
	EvaluationFailed bool
	Phases           []v1alpha1.ResourceStatus
	Components       []ComponentState
}

// PodRecord is the stored attribution of one pod.
// Records are immutable once inserted; callers build a fresh record per event.
type PodRecord struct {
	UID             types.UID
	Namespace       string
	Name            string
	WorkloadUID     types.UID
	Workload        WorkloadRef
	OwnerResolution string
	Component       string
	Instance        string
	Replica         string
	Reason          string
	Phase           corev1.PodPhase
}

// HealthRecord describes a verified observation, not application readiness.
type HealthRecord struct {
	Scope        string
	Ref          WorkloadRef
	UID          types.UID
	Healthy      bool
	Reason       string
	VerifiedAt   time.Time
	MaxAge       time.Duration
	Dependencies []WorkloadRef
}

// Snapshot is a consistent view of the store, safe to read without locks.
type Snapshot struct {
	Identities  map[types.UID]IdentityRecord
	Workloads   []WorkloadRecord
	Pods        []PodRecord
	Health      []HealthRecord
	HealthEpoch uint64
}

// Store holds workload and pod records keyed by UID, with a reverse
// workload-to-pods index so workload events can re-attribute only their pods.
type Store struct {
	identities             map[types.UID]IdentityRecord
	mu                     sync.RWMutex
	health                 []HealthRecord
	healthEpoch            uint64
	workloads              map[types.UID]WorkloadRecord
	workloadsByKind        map[resourceKind]map[types.UID]struct{}
	pods                   map[types.UID]PodRecord
	podsByWorkload         map[types.UID]map[types.UID]struct{}
	collectionFailureEpoch map[resourceKind]healthFailure
	workloadFailureEpoch   map[types.UID]healthFailure
	healthByWorkload       map[types.UID]int
	healthByDependency     map[resourceKind][]int
	namespaceFailureEpoch  map[string]healthFailure
	healthVerifiedEpoch    uint64
}

type resourceKind struct {
	group string
	kind  string
}

type healthFailure struct {
	epoch  uint64
	reason string
}

func New() *Store {
	return &Store{
		workloads:              make(map[types.UID]WorkloadRecord),
		workloadsByKind:        make(map[resourceKind]map[types.UID]struct{}),
		workloadFailureEpoch:   make(map[types.UID]healthFailure),
		collectionFailureEpoch: make(map[resourceKind]healthFailure),
		healthByWorkload:       make(map[types.UID]int),
		healthByDependency:     make(map[resourceKind][]int),
		namespaceFailureEpoch:  make(map[string]healthFailure),
		pods:                   make(map[types.UID]PodRecord),
		podsByWorkload:         make(map[types.UID]map[types.UID]struct{}),
	}
}

func (s *Store) UpsertWorkload(record WorkloadRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := resourceKind{group: record.Ref.Group, kind: record.Ref.Kind}
	if previous, ok := s.workloads[record.UID]; ok {
		oldKey := resourceKind{group: previous.Ref.Group, kind: previous.Ref.Kind}
		if oldKey != key {
			delete(s.workloadsByKind[oldKey], record.UID)
			if len(s.workloadsByKind[oldKey]) == 0 {
				delete(s.workloadsByKind, oldKey)
			}
		}
	}
	if s.workloadsByKind[key] == nil {
		s.workloadsByKind[key] = make(map[types.UID]struct{})
	}
	s.workloadsByKind[key][record.UID] = struct{}{}
	s.workloads[record.UID] = record
	if record.OwnerFailure != "" {
		s.invalidateWorkloadLocked(record.UID, record.OwnerFailure)
	} else if record.EvaluationFailed {
		s.invalidateWorkloadLocked(record.UID, ReasonEvaluationFailed)
	}
}

func (s *Store) DeleteWorkload(uid types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.workloads[uid]; ok {
		key := resourceKind{group: record.Ref.Group, kind: record.Ref.Kind}
		delete(s.workloadsByKind[key], uid)
		if len(s.workloadsByKind[key]) == 0 {
			delete(s.workloadsByKind, key)
		}
	}
	delete(s.workloads, uid)
	delete(s.workloadFailureEpoch, uid)
	for podUID := range s.podsByWorkload[uid] {
		delete(s.pods, podUID)
	}
	delete(s.podsByWorkload, uid)
}

func (s *Store) UpsertPod(record PodRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.pods[record.UID]; ok && existing.WorkloadUID != record.WorkloadUID {
		s.unindexPodLocked(existing)
	}
	s.pods[record.UID] = record
	if record.Reason != "" {
		s.invalidateWorkloadLocked(record.WorkloadUID, ReasonEvaluationFailed)
	}

	pods, ok := s.podsByWorkload[record.WorkloadUID]
	if !ok {
		pods = make(map[types.UID]struct{})
		s.podsByWorkload[record.WorkloadUID] = pods
	}
	pods[record.UID] = struct{}{}
}

func (s *Store) DeletePod(uid types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.pods[uid]
	if !ok {
		return
	}
	delete(s.pods, uid)
	s.unindexPodLocked(record)
}

func (s *Store) unindexPodLocked(record PodRecord) {
	pods, ok := s.podsByWorkload[record.WorkloadUID]
	if !ok {
		return
	}
	delete(pods, record.UID)
	if len(pods) == 0 {
		delete(s.podsByWorkload, record.WorkloadUID)
	}
}

// PodsOfWorkload returns copies of the pod records attributed to a workload.
func (s *Store) PodsOfWorkload(uid types.UID) []PodRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pods := make([]PodRecord, 0, len(s.podsByWorkload[uid]))
	for podUID := range s.podsByWorkload[uid] {
		pods = append(pods, s.pods[podUID])
	}
	return pods
}

// Pod returns the stored record for a pod UID.
func (s *Store) Pod(uid types.UID) (PodRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.pods[uid]
	return record, ok
}

// Workload returns the stored record for a workload UID.
func (s *Store) Workload(uid types.UID) (WorkloadRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.workloads[uid]
	return record, ok
}

// Workloads returns the workload records without copying pod records.
func (s *Store) Workloads() []WorkloadRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]WorkloadRecord, 0, len(s.workloads))
	for _, record := range s.workloads {
		records = append(records, record)
	}
	return records
}

func (s *Store) WorkloadsOfKind(group, kind string) []WorkloadRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	uids := s.workloadsByKind[resourceKind{group: group, kind: kind}]
	records := make([]WorkloadRecord, 0, len(uids))
	for uid := range uids {
		records = append(records, s.workloads[uid])
	}
	return records
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := Snapshot{
		Identities:  maps.Clone(s.identities),
		Workloads:   make([]WorkloadRecord, 0, len(s.workloads)),
		Health:      append([]HealthRecord(nil), s.health...),
		HealthEpoch: s.healthEpoch,
		Pods:        make([]PodRecord, 0, len(s.pods)),
	}
	for _, record := range s.workloads {
		snapshot.Workloads = append(snapshot.Workloads, record)
	}
	for _, record := range s.pods {
		snapshot.Pods = append(snapshot.Pods, record)
	}
	for i := range snapshot.Health {
		row := &snapshot.Health[i]
		if failure := s.namespaceFailureEpoch[row.Ref.Namespace]; row.Scope == "workload" && failure.epoch > s.healthVerifiedEpoch {
			row.Healthy = false
			row.Reason = failure.reason
		}
	}
	return snapshot
}

func (h HealthRecord) Value(now time.Time) float64 {
	if h.Healthy && !h.VerifiedAt.IsZero() && now.Sub(h.VerifiedAt) < h.MaxAge && !now.Before(h.VerifiedAt) {
		return 1
	}
	return 0
}

// Status supplies the reason for the same verification used to compute Value.
func (h HealthRecord) Status(now time.Time) (float64, string) {
	if h.Value(now) == 1 {
		return 1, ReasonNone
	}
	if !h.Healthy && h.Reason != "" && h.Reason != ReasonNone {
		return 0, h.Reason
	}
	return 0, ReasonAPIUnavailable
}

func (s *Store) HealthEpoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.healthEpoch
}

// SetHealth publishes a verification pass. A concurrent failure cannot be
// overwritten by a verification that started before that failure.
func (s *Store) SetHealth(records []HealthRecord, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records = slices.Clone(records)
	s.healthByWorkload = make(map[types.UID]int, len(records))
	s.healthByDependency = make(map[resourceKind][]int)
	for i := range records {
		row := &records[i]
		var failure healthFailure
		switch row.Scope {
		case "resource_type":
			key := resourceKind{row.Ref.Group, row.Ref.Kind}
			failure = s.collectionFailureEpoch[key]
			s.healthByDependency[key] = append(s.healthByDependency[key], i)
		case "workload":
			failure = s.workloadFailureEpoch[row.UID]
			if namespaceFailure := s.namespaceFailureEpoch[row.Ref.Namespace]; namespaceFailure.epoch > failure.epoch {
				failure = namespaceFailure
			}
			s.healthByWorkload[row.UID] = i
		}
		for _, dep := range records[i].Dependencies {
			key := resourceKind{dep.Group, dep.Kind}
			s.healthByDependency[key] = append(s.healthByDependency[key], i)
			if dependencyFailure := s.collectionFailureEpoch[key]; dependencyFailure.epoch > failure.epoch {
				failure = dependencyFailure
			}
		}
		if failure.epoch > epoch {
			row.Healthy = false
			row.Reason = failure.reason
		}
	}
	s.health = records
	s.healthVerifiedEpoch = epoch
	for uid, failure := range s.workloadFailureEpoch {
		if failure.epoch <= epoch {
			delete(s.workloadFailureEpoch, uid)
		}
	}
	for kind, failure := range s.collectionFailureEpoch {
		if failure.epoch <= epoch {
			delete(s.collectionFailureEpoch, kind)
		}
	}
	for namespace, failure := range s.namespaceFailureEpoch {
		if failure.epoch <= epoch {
			delete(s.namespaceFailureEpoch, namespace)
		}
	}
}

func (s *Store) invalidateWorkloadLocked(uid types.UID, reason string) {
	s.healthEpoch++
	s.workloadFailureEpoch[uid] = healthFailure{epoch: s.healthEpoch, reason: reason}
	if i, ok := s.healthByWorkload[uid]; ok {
		s.health[i].Healthy = false
		s.health[i].Reason = reason
	}
}

func (s *Store) InvalidateWorkload(uid types.UID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateWorkloadLocked(uid, reason)
}

func (s *Store) InvalidateNamespace(namespace, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Pending Pod bursts must not scan every workload once per missing owner.
	// Snapshot and SetHealth apply this failure during their existing linear pass.
	s.healthEpoch++
	s.namespaceFailureEpoch[namespace] = healthFailure{epoch: s.healthEpoch, reason: reason}
}

// InvalidateHealth immediately marks collection failures until fresh verification.
func (s *Store) InvalidateHealth(group, kind string, reasons ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := ReasonAPIUnavailable
	if len(reasons) != 0 && reasons[0] != "" {
		reason = reasons[0]
	}
	s.healthEpoch++
	key := resourceKind{group: group, kind: kind}
	s.collectionFailureEpoch[key] = healthFailure{epoch: s.healthEpoch, reason: reason}
	for _, i := range s.healthByDependency[key] {
		s.health[i].Healthy = false
		s.health[i].Reason = reason
	}
}
