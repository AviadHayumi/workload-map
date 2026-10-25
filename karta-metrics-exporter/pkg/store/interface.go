// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Writer is used under Runtime's publication mutex after input verification.
type Writer interface {
	BindPublication(lock *sync.Mutex, now func() time.Time)
	Publish(*Publication)
	Published(types.UID) *Publication
	RemovePublication(types.UID)
	SetPublicationFailure(key, reason string)
	SetPublicationHealth([]HealthRecord)
}

// Capturer returns an immutable view. It acquires the publication mutex itself.
type Capturer interface {
	Capture() CapturedView
}

type Interface interface {
	Writer
	Capturer
}

type Publication struct {
	Workload       WorkloadRecord
	Status         Evidence
	Creation       Evidence
	PodPages       []*PodPage
	FailureReasons []string
}

type WorkloadRecord struct {
	UID              types.UID
	Ref              ObjectRef
	Karta            string
	KartaSource      string
	OwnerResolution  OwnerResolution
	HasStatus        bool
	Phases           []string
	CreatedTimestamp int64
	StatusLabels     map[string]string
}

type ObjectRef struct {
	GVK             schema.GroupVersionKind
	Namespace       string
	Name            string
	UID             types.UID
	ResourceVersion string
}

type OwnerResolution string

const (
	Resolved     OwnerResolution = "resolved"
	IgnoredOwner OwnerResolution = "ignored_owner"
	UnknownOwner OwnerResolution = "unknown"
)

type PodRecord struct {
	UID             types.UID
	Name            string
	Component       string
	Instance        string
	Group           string
	OwnerResolution OwnerResolution
	Labels          map[string]string
}

type PublishedPod struct {
	Record  PodRecord
	Mapping Evidence
}

type PodPage struct{ Rows []PublishedPod }

type Evidence struct {
	ExpiresAt    time.Time
	Reason       string
	Dependencies []string
}

type CapturedView struct {
	At      time.Time
	Records []*Publication
	Health  []HealthRecord
}

type HealthRecord struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Evidence  Evidence
}
