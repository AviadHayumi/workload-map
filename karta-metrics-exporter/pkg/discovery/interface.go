// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
)

type Discoverer interface {
	Get(context.Context, ObjectRef) (Result, error)
}

type Result struct {
	Workload        *GenericWorkload
	MissingCoverage []WatchRequest
}

type GenericWorkload struct {
	Root       ObjectRef
	Path       []ObjectRef
	Resolution OwnerResolution
}

type ObjectRef struct {
	GVK             schema.GroupVersionKind
	Namespace       string
	Name            string
	UID             types.UID
	ResourceVersion string
}

type WatchRequest struct {
	GVK       schema.GroupVersionKind
	Namespace string
}

type OwnerResolution string

const (
	Resolved     OwnerResolution = "resolved"
	IgnoredOwner OwnerResolution = "ignored_owner"
	UnknownOwner OwnerResolution = "unknown"
)

type ObjectReader interface {
	Cached(ObjectRef) (*unstructured.Unstructured, bool)
	GetMetadata(context.Context, ObjectRef) (*metav1.PartialObjectMetadata, error)
}

var (
	ErrPending           = errors.New("discovery inputs not synchronized")
	ErrPermissionDenied  = errors.New("discovery read denied")
	ErrUnavailable       = errors.New("discovery source unavailable")
	ErrUnresolvedOwner   = errors.New("owner chain unresolved")
	ErrInvalidDefinition = errors.New("invalid Karta definition")
	ErrEvaluation        = errors.New("karta evaluation failed")
	ErrStale             = errors.New("discovery inputs changed")
)

type Dependencies struct {
	Reader    ObjectReader
	Inventory inventory.Interface
}
