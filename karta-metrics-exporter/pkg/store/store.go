// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Writer owns synchronization. Each method is atomic and safe for concurrent use.
type Writer interface {
	Publish(*Publication)
	Published(types.UID) *Publication
	RemovePublication(types.UID)
	SetPublicationFailure(key, reason string)
	// RetirePublicationFailure drops an abandoned key only when no current publication or health record refers to it.
	RetirePublicationFailure(key string)
	SetPublicationHealth([]HealthRecord)
}

// Capturer returns an immutable view under Store's own lock.
type Capturer interface {
	Capture() CapturedView
}

// Interface combines publication updates with snapshot collection.
type Interface interface {
	Writer
	Capturer
}

// Publication contains the observations for one workload and its Pods.
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
	WorkloadGVK     schema.GroupVersionKind
	Namespace       string
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

type PodPage struct {
	Rows         []PublishedPod
	prepared     sync.Once
	dependencies map[string]int
	sorted       bool
}

type Evidence struct {
	ExpiresAt    time.Time
	Reason       string
	Dependencies []string
}

// CapturedView is an immutable snapshot evaluated at At.
type CapturedView struct {
	At       time.Time
	Records  []*Publication
	Health   []HealthRecord
	failures map[string]string
}

type HealthRecord struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Evidence  Evidence
}

// Options supplies the Store clock. Nil uses time.Now.
type Options struct {
	Now func() time.Time
}
