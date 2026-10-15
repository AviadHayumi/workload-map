// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package discovery observes complete workload Pod membership through an injected
// reader. Offline extraction remains in resource and tree.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type ObjectRef = ownership.Ref

// Reader is caller-owned. Lists must include every Pod in the requested
// namespace, across all phases, with pagination exhausted. Empty namespace means
// all namespaces. Returned objects must be detached from mutable cache storage.
// Cached readers must reject requests until their declared scope is complete.
type Reader interface {
	Mapping(context.Context, schema.GroupVersionKind) (*meta.RESTMapping, error)
	Get(context.Context, ObjectRef) (*unstructured.Unstructured, error)
	ListPods(context.Context, string) (*corev1.PodList, error)
}

// PodTransferReader optionally transfers exclusive ownership of a complete Pod
// list. ListPodsForTransfer has the same coverage and error rules as ListPods.
// The list, its Items array, and all reachable mutable data must not be retained,
// reused, or mutated by the reader after return. Pods must not share mutable data
// with each other or with earlier or later results. The caller may retain and
// mutate the result. Readers without this capability use defensive list copies.
type PodTransferReader interface {
	Reader
	ListPodsForTransfer(context.Context, string) (*corev1.PodList, error)
}

// IndexedReader captures the seed, its complete terminal workload, and all
// relevant owner objects from one local generation. Errors never trigger an
// implicit direct-read fallback.
type IndexedReader interface {
	Reader
	Capture(context.Context, ObjectRef) (*Capture, error)
}

type Capture struct {
	Seed        ObjectRef
	Objects     []*unstructured.Unstructured
	Pods        []*corev1.Pod
	Mappings    map[schema.GroupVersionKind]*meta.RESTMapping
	Observation Observation
}

// Dataset declares complete, unfiltered Pod coverage for Namespace and owner
// closure for those Pods. Explicit root objects may represent empty workloads.
type Dataset struct {
	Namespace          string
	Objects            []*unstructured.Unstructured
	Pods               []*corev1.Pod
	Mappings           map[schema.GroupVersionKind]*meta.RESTMapping
	PodResourceVersion string
}

type Changes struct {
	Upserts  []*unstructured.Unstructured
	Deletes  []ObjectRef
	Mappings map[schema.GroupVersionKind]*meta.RESTMapping
}

// Observation describes acquisition, not application health or a cross-resource
// Kubernetes transaction. Indexed timestamps describe local capture, not the age
// of the caller's informer feed.
type Observation struct {
	Source             string
	Namespace          string
	Started, Completed time.Time
	PodResourceVersion string
	Generation         uint64
}

var (
	ErrNoClient              = errors.New("discovery client is required")
	ErrInvalidInput          = errors.New("invalid discovery input")
	ErrNotSynced             = errors.New("discovery index is not synchronized")
	ErrIncompleteOwnership   = errors.New("workload ownership is incomplete")
	ErrGenerationConflict    = errors.New("discovery index generation changed")
	ErrRetiredUID            = errors.New("object UID was retired")
	ErrInsufficientScope     = errors.New("discovery scope does not cover the workload")
	ErrNotWorkloadRoot       = errors.New("object is not the terminal workload root")
	ErrStaleInput            = errors.New("discovery input refers to a different observation")
	ErrUnsupported           = errors.New("operation requires a Karta definition")
	ErrComponentNotFound     = errors.New("component does not exist")
	ErrInstanceNotFound      = errors.New("component instance does not exist")
	ErrNoReplicaAxis         = errors.New("component has no replica axis")
	ErrIncompleteAttribution = errors.New("component membership is incomplete")
)

var podGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	}
	return false
}

func reference(object *unstructured.Unstructured) ObjectRef {
	return ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
}

func validateRef(ref ObjectRef) error {
	if ref.GVK.Version == "" || ref.GVK.Kind == "" || ref.Name == "" || ref.UID == "" {
		return fmt.Errorf("%w: GVK, name and UID are required", ErrInvalidInput)
	}
	return nil
}

func copyMapping(mapping *meta.RESTMapping) (*meta.RESTMapping, error) {
	if mapping == nil || isNil(mapping.Scope) || mapping.GroupVersionKind.Version == "" || mapping.GroupVersionKind.Kind == "" || mapping.Resource.Resource == "" || strings.Contains(mapping.Resource.Resource, "/") {
		return nil, fmt.Errorf("%w: incomplete resource mapping", ErrInvalidInput)
	}
	if mapping.Resource.Group != mapping.GroupVersionKind.Group || mapping.Resource.Version != mapping.GroupVersionKind.Version {
		return nil, fmt.Errorf("%w: resource and kind mapping versions differ", ErrInvalidInput)
	}
	result := *mapping
	switch mapping.Scope.Name() {
	case meta.RESTScopeNameNamespace:
		result.Scope = meta.RESTScopeNamespace
	case meta.RESTScopeNameRoot:
		result.Scope = meta.RESTScopeRoot
	default:
		return nil, fmt.Errorf("%w: unsupported resource scope %q", ErrInvalidInput, mapping.Scope.Name())
	}
	return &result, nil
}

func ownershipObject(object *unstructured.Unstructured, mapping *meta.RESTMapping) (ownership.Object, error) {
	if object == nil {
		return ownership.Object{}, fmt.Errorf("%w: reader returned nil object", ErrInvalidInput)
	}
	if err := validateRef(reference(object)); err != nil {
		return ownership.Object{}, err
	}
	if mapping == nil || isNil(mapping.Scope) {
		return ownership.Object{}, fmt.Errorf("%w: missing mapping for %s", ErrInvalidInput, object.GroupVersionKind())
	}
	if mapping.GroupVersionKind != object.GroupVersionKind() {
		return ownership.Object{}, fmt.Errorf("%w: mapping differs from object GVK", ErrInvalidInput)
	}
	if _, err := copyMapping(mapping); err != nil {
		return ownership.Object{}, err
	}
	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
	if namespaced != (object.GetNamespace() != "") {
		return ownership.Object{}, fmt.Errorf("%w: namespace disagrees with scope of %s", ownership.ErrScope, object.GetName())
	}
	return ownership.Object{Ref: reference(object), Owners: object.GetOwnerReferences(), Namespaced: namespaced}, nil
}

func podObject(pod *corev1.Pod) (*unstructured.Unstructured, error) {
	if pod == nil {
		return nil, fmt.Errorf("%w: nil Pod", ErrInvalidInput)
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		return nil, fmt.Errorf("convert Pod: %w", err)
	}
	result := &unstructured.Unstructured{Object: object}
	result.SetGroupVersionKind(podGVK)
	return result, nil
}

func objectPod(object *unstructured.Unstructured) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, pod); err != nil {
		return nil, fmt.Errorf("convert Pod: %w", err)
	}
	pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	return pod, nil
}

// JSON omitempty normalizes an empty owner-reference list to nil. Comparing
// elements preserves identity checks without mistaking that for a state change.
func sameOwnerReferences(a, b []metav1.OwnerReference) bool {
	return slices.EqualFunc(a, b, func(left, right metav1.OwnerReference) bool { return reflect.DeepEqual(left, right) })
}
