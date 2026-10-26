// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package discovery resolves observed ownership and interprets workloads with Karta.
// The caller owns watches, fresh verification, and publication.
package discovery

import (
	"context"
	"errors"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

var (
	ErrPending           = kartadefinitioninventory.ErrPending
	ErrPermissionDenied  = kartadefinitioninventory.ErrPermissionDenied
	ErrUnavailable       = kartadefinitioninventory.ErrUnavailable
	ErrUnresolvedOwner   = errors.New("unresolved controller owner")
	ErrInvalidDefinition = kartadefinitioninventory.ErrInvalidDefinition
	ErrEvaluation        = errors.New("karta evaluation failed")
	ErrStale             = errors.New("discovery inputs changed")
)

type ObjectRef struct {
	GVK             schema.GroupVersionKind
	Namespace       string
	Name            string
	UID             types.UID
	ResourceVersion string
}

// CachedReader reads only informer stores. It must reject retired watch runs.
// Missing unsynchronized coverage returns ErrPending, not a NotFound error.
type CachedReader interface {
	Get(context.Context, ObjectRef) (*unstructured.Unstructured, error)
}

type OwnerKey struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	UID       types.UID
}

type CoverageKey struct {
	Resource  schema.GroupVersionResource
	Namespace string
}

type CoverageRequest struct {
	GVK        schema.GroupVersionKind
	Namespace  string
	FullObject bool
}

type CoverageChange struct {
	Key        CoverageKey
	Run        uint64
	FullObject bool
	Ready      bool
	Removed    bool
	Err        error
}

type ObservedObject struct {
	Ref      ObjectRef
	Owners   []metav1.OwnerReference
	WatchKey CoverageKey
	Run      uint64
}

type ChangeSet struct {
	// Objects changed or depend on a changed object. Roots also includes
	// unchanged ancestors whose membership needs reconciliation.
	Objects         []ObjectRef
	Roots           []ObjectRef
	MissingCoverage []CoverageRequest
}

type OwnerResolution string

const (
	Resolved     OwnerResolution = "resolved"
	IgnoredOwner OwnerResolution = "ignored_owner"
	UnknownOwner OwnerResolution = "unknown"
)

// CapturedState is an immutable input token, not fresh API verification.
// Its zero value is invalid. Only its originating Discovery can check it.
type CapturedState struct {
	discovery          *Discovery
	revision           uint64
	objects            []objectVersion
	definitionKind     schema.GroupKind
	definitionRevision uint64
	membershipUID      types.UID
	membershipRevision uint64
}

type GenericWorkload struct {
	root       ObjectRef
	Path       []ObjectRef
	Resolution OwnerResolution
	karta      *KartaWorkload
	state      CapturedState
}

type KartaWorkload struct {
	root      ObjectRef
	selection kartadefinitioninventory.Selection
	discovery *Discovery
	state     CapturedState
}

// EvaluationBatch shares root calculations only within one verification round.
// A new batch reevaluates expressions, including time-dependent expressions.
type EvaluationBatch struct {
	cache *evaluationCache
}

type StatusResult struct {
	Defined bool
	Matched []v1alpha1.ResourceStatus
	state   CapturedState
}

type PodMapping struct {
	Component string
	Instance  string
	Group     string
	state     CapturedState
}

func (w GenericWorkload) Root() ObjectRef { return w.root }

func (w GenericWorkload) KartaWorkload() *KartaWorkload { return w.karta }

func (w GenericWorkload) State() CapturedState { return w.state }

func (s StatusResult) State() CapturedState { return s.state }

func (m PodMapping) State() CapturedState { return m.state }

// Selection returns a caller-owned snapshot for controller provenance checks.
func (w *KartaWorkload) Selection() kartadefinitioninventory.Selection {
	selection := w.selection
	selection.Definition = selection.Definition.DeepCopy()
	return selection
}

type objectVersion struct {
	ref              ObjectRef
	key              CoverageKey
	run              uint64
	revision         uint64
	edgeRevision     uint64
	coverageRevision uint64
}

// Discovery is safe for concurrent use. It never calls dependencies while
// holding its ownership lock.
type Discovery struct {
	mu          sync.RWMutex
	reader      CachedReader
	inventory   kartadefinitioninventory.Interface
	ownership   *OwnershipIndex
	coverage    map[CoverageKey]coverageState
	definitions map[schema.GroupKind]uint64
	revision    uint64
}

type OwnershipIndex struct {
	nodes         map[types.UID]*OwnerNode
	children      map[types.UID]map[types.UID]struct{}
	byName        map[objectName]types.UID
	byKind        map[schema.GroupKind]map[types.UID]struct{}
	ignoredOwners map[schema.GroupVersionKind]struct{}
}

type OwnerNode struct {
	objects            map[schema.GroupVersionKind]observation
	parents            map[types.UID]struct{}
	edgeRevision       uint64
	membershipRevision uint64
}

type observation struct {
	ObservedObject
	revision uint64
}

type objectName struct {
	gvk       schema.GroupVersionKind
	namespace string
	name      string
}

type coverageState struct {
	CoverageChange
	revision uint64
}

// New constructs discovery without starting watches or goroutines.
func New(reader CachedReader, inventory kartadefinitioninventory.Interface, ignoredOwners ...schema.GroupVersionKind) *Discovery {
	d := &Discovery{
		reader: reader, inventory: inventory,
		ownership: &OwnershipIndex{
			nodes:         make(map[types.UID]*OwnerNode),
			children:      make(map[types.UID]map[types.UID]struct{}),
			byName:        make(map[objectName]types.UID),
			byKind:        make(map[schema.GroupKind]map[types.UID]struct{}),
			ignoredOwners: make(map[schema.GroupVersionKind]struct{}),
		},
		coverage:    make(map[CoverageKey]coverageState),
		definitions: make(map[schema.GroupKind]uint64),
	}
	for _, gvk := range ignoredOwners {
		d.ownership.ignoredOwners[gvk] = struct{}{}
	}
	return d
}
