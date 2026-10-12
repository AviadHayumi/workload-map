// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

// WorkloadRef identifies a workload object for metric labels.
type WorkloadRef struct {
	Namespace string
	Name      string
	Group     string
	Version   string
	Kind      string
}

// ComponentState holds the desired state of one component instance.
type ComponentState struct {
	Component string
	Instance  string
	Replicas  *int32
}

// WorkloadRecord is the stored state of one Karta-described workload.
// Records are immutable once inserted; callers build a fresh record per event.
type WorkloadRecord struct {
	UID              types.UID
	Ref              WorkloadRef
	Karta            string
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
	UID         types.UID
	Namespace   string
	Name        string
	WorkloadUID types.UID
	Workload    WorkloadRef
	Component   string
	Instance    string
	Replica     string
	Reason      string
	Phase       corev1.PodPhase
}

// HealthRecord describes a verified observation, not application readiness.
type HealthRecord struct {
	Scope        string
	Ref          WorkloadRef
	UID          types.UID
	Healthy      bool
	VerifiedAt   time.Time
	MaxAge       time.Duration
	Dependencies []WorkloadRef
}

// Snapshot is a consistent view of the store, safe to read without locks.
type Snapshot struct {
	Workloads   []WorkloadRecord
	Pods        []PodRecord
	Health      []HealthRecord
	HealthEpoch uint64
}

// Store holds workload and pod records keyed by UID, with a reverse
// workload-to-pods index so workload events can re-attribute only their pods.
type Store struct {
	mu                     sync.RWMutex
	health                 []HealthRecord
	healthEpoch            uint64
	workloads              map[types.UID]WorkloadRecord
	pods                   map[types.UID]PodRecord
	podsByWorkload         map[types.UID]map[types.UID]struct{}
	collectionFailureEpoch map[string]uint64
	workloadFailureEpoch   map[types.UID]uint64
}

func New() *Store {
	return &Store{
		workloads:              make(map[types.UID]WorkloadRecord),
		workloadFailureEpoch:   make(map[types.UID]uint64),
		collectionFailureEpoch: make(map[string]uint64),
		pods:                   make(map[types.UID]PodRecord),
		podsByWorkload:         make(map[types.UID]map[types.UID]struct{}),
	}
}

func (s *Store) UpsertWorkload(record WorkloadRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workloads[record.UID] = record
	if record.EvaluationFailed {
		s.invalidateWorkloadLocked(record.UID)
	}
}

func (s *Store) DeleteWorkload(uid types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		s.invalidateWorkloadLocked(record.WorkloadUID)
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

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := Snapshot{
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
	return snapshot
}

func (h HealthRecord) Value(now time.Time) float64 {
	if h.Healthy && !h.VerifiedAt.IsZero() && now.Sub(h.VerifiedAt) < h.MaxAge && !now.Before(h.VerifiedAt) {
		return 1
	}
	return 0
}

// SetHealth publishes a verification pass. A concurrent failure cannot be
// overwritten by a verification that started before that failure.
func (s *Store) SetHealth(records []HealthRecord, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range records {
		if (records[i].Scope == "kind" && s.collectionFailureEpoch[records[i].Ref.Group+"\x00"+records[i].Ref.Kind] > epoch) || (records[i].Scope == "workload" && s.workloadFailureEpoch[records[i].UID] > epoch) {
			records[i].Healthy = false
		}
		for _, dep := range records[i].Dependencies {
			if s.collectionFailureEpoch[dep.Group+"\x00"+dep.Kind] > epoch {
				records[i].Healthy = false
			}
		}
	}
	s.health = append([]HealthRecord(nil), records...)
}

func (s *Store) invalidateWorkloadLocked(uid types.UID) {
	s.healthEpoch++
	s.workloadFailureEpoch[uid] = s.healthEpoch
	for i := range s.health {
		if s.health[i].UID == uid {
			s.health[i].Healthy = false
		}
	}
}

// InvalidateHealth immediately marks collection failures until fresh verification.
func (s *Store) InvalidateHealth(group, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthEpoch++
	s.collectionFailureEpoch[group+"\x00"+kind] = s.healthEpoch
	for i := range s.health {
		if s.health[i].Scope == "kind" && s.health[i].Ref.Group == group && s.health[i].Ref.Kind == kind {
			s.health[i].Healthy = false
		}
		for _, dep := range s.health[i].Dependencies {
			if dep.Group == group && dep.Kind == kind {
				s.health[i].Healthy = false
			}
		}
	}
}
