// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
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

// WorkloadRecord is the stored state of one resolved workload.
// Records are immutable once inserted; callers build a fresh record per event.
type WorkloadRecord struct {
	UID              types.UID
	Ref              WorkloadRef
	Karta            string
	KartaSource      string
	OwnerResolution  string
	CreatedTimestamp int64
	HasStatus        bool
	Phases           []v1alpha1.ResourceStatus
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
}

// HealthRecord describes a verified observation, not application readiness.
type HealthRecord struct {
	Scope      string
	Ref        WorkloadRef
	UID        types.UID
	Healthy    bool
	Reason     string
	VerifiedAt time.Time
	MaxAge     time.Duration
}

// Store keeps immutable publications keyed by workload UID.
type Store struct {
	publicationLock        *sync.Mutex
	mu                     sync.RWMutex
	publications           map[types.UID]*Publication
	publicationFailures    map[string]string
	publicationFailureRefs map[string]int
	publicationHealth      []HealthRecord
	publicationNow         func() time.Time
}

func New() *Store {
	return &Store{
		publications:           make(map[types.UID]*Publication),
		publicationFailures:    make(map[string]string),
		publicationFailureRefs: make(map[string]int),
		publicationNow:         time.Now,
	}
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
