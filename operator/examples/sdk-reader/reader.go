// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package sdkreader is a consumer-owned controller-runtime adapter example.
// Copy it into the operator that owns the controller-runtime dependency.
package sdkreader

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type reader struct {
	source    client.Reader
	mapper    meta.RESTMapper
	ready     func() bool
	namespace string
	cached    bool
}

var _ discovery.Reader = (*reader)(nil)

var podGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}

// NewAPIReader adapts a direct reader, such as mgr.GetAPIReader(). The source
// must not hide resources through selectors. Pod pagination is exhausted.
func NewAPIReader(source client.Reader, mapper meta.RESTMapper) (discovery.Reader, error) {
	if isNil(source) || isNil(mapper) {
		return nil, discovery.ErrNoClient
	}
	return &reader{source: source, mapper: mapper, ready: func() bool { return true }}, nil
}

// NewCacheReader adapts an explicitly selected cache. Namespace declares its
// complete Pod scope; empty means every namespace. Ready must report whether all
// required typed-Pod and unstructured-owner informers are synchronized. The caller
// owns informer registration, startup and readiness. Selectors and transforms
// must not drop Pods, owner identity, or fields needed by Karta definitions.
func NewCacheReader(source client.Reader, mapper meta.RESTMapper, namespace string, ready func() bool) (discovery.Reader, error) {
	if isNil(source) || isNil(mapper) || ready == nil {
		return nil, discovery.ErrNoClient
	}
	return &reader{source: source, mapper: mapper, namespace: namespace, ready: ready, cached: true}, nil
}

func (r *reader) Mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	if err := r.checkReady(ctx); err != nil {
		return nil, err
	}
	if gvk.Version == "" || gvk.Kind == "" {
		return nil, discovery.ErrInvalidInput
	}
	mapping, err := r.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("map %s: %w", gvk, err)
	}
	if err := r.checkReady(ctx); err != nil {
		return nil, err
	}
	if mapping == nil || isNil(mapping.Scope) || mapping.GroupVersionKind != gvk || mapping.Resource.GroupVersion() != gvk.GroupVersion() || mapping.Resource.Resource == "" || strings.Contains(mapping.Resource.Resource, "/") {
		return nil, fmt.Errorf("%w: invalid exact mapping for %s", discovery.ErrInvalidInput, gvk)
	}
	copy := *mapping
	switch mapping.Scope.Name() {
	case meta.RESTScopeNameNamespace:
		copy.Scope = meta.RESTScopeNamespace
	case meta.RESTScopeNameRoot:
		copy.Scope = meta.RESTScopeRoot
	default:
		return nil, fmt.Errorf("%w: unsupported resource scope", discovery.ErrInvalidInput)
	}
	return &copy, nil
}

func (r *reader) Get(ctx context.Context, ref discovery.ObjectRef) (*unstructured.Unstructured, error) {
	if ref.Name == "" || ref.UID == "" {
		return nil, discovery.ErrInvalidInput
	}
	mapping, err := r.Mapping(ctx, ref.GVK)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if ref.Namespace == "" {
			return nil, ownership.ErrScope
		}
		if r.cached && r.namespace != "" && ref.Namespace != r.namespace {
			return nil, discovery.ErrInsufficientScope
		}
	} else {
		ref.Namespace = ""
	}
	key := client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(ref.GVK)
	if ref.GVK == podGVK {
		// A typed Pod read reuses the same informer as ListPods.
		pod := &corev1.Pod{}
		if err := r.source.Get(ctx, key, pod); err != nil {
			return nil, fmt.Errorf("get Pod %s: %w", key, err)
		}
		object.Object, err = runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
		if err != nil {
			return nil, fmt.Errorf("convert Pod %s: %w", key, err)
		}
		object.SetGroupVersionKind(podGVK)
	} else if err := r.source.Get(ctx, key, object); err != nil {
		return nil, fmt.Errorf("get %s %s: %w", ref.GVK, key, err)
	}
	if err := r.checkReady(ctx); err != nil {
		return nil, err
	}
	actual := discovery.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
	if actual.GVK != ref.GVK || actual.Namespace != ref.Namespace || actual.Name != ref.Name {
		return nil, discovery.ErrStaleInput
	}
	if actual.UID != ref.UID {
		return nil, &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: ref, Actual: actual}
	}
	return object.DeepCopy(), nil
}

func (r *reader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	if err := r.checkReady(ctx); err != nil {
		return nil, err
	}
	if r.cached && r.namespace != "" && namespace != r.namespace {
		return nil, discovery.ErrInsufficientScope
	}
	result := &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
	options := client.ListOptions{Namespace: namespace}
	if !r.cached {
		options.Limit = 500
	}
	seenTokens := map[string]bool{}
	seenPods := map[types.UID]bool{}
	first := true
	for {
		if err := r.checkReady(ctx); err != nil {
			return nil, err
		}
		page := &corev1.PodList{}
		if err := r.source.List(ctx, page, &options); err != nil {
			return nil, fmt.Errorf("list Pods in %q: %w", namespace, err)
		}
		if err := r.checkReady(ctx); err != nil {
			return nil, err
		}
		if !first && page.ResourceVersion != result.ResourceVersion {
			return nil, fmt.Errorf("%w: Pod list resource version changed between pages", discovery.ErrIncompleteOwnership)
		}
		if first {
			result.ResourceVersion = page.ResourceVersion
			first = false
		}
		for i := range page.Items {
			if err := r.checkReady(ctx); err != nil {
				return nil, err
			}
			pod := &page.Items[i]
			if pod.Name == "" || pod.Namespace == "" || pod.UID == "" || seenPods[pod.UID] {
				return nil, fmt.Errorf("%w: invalid or duplicate Pod identity", discovery.ErrInvalidInput)
			}
			if namespace != "" && pod.Namespace != namespace {
				return nil, discovery.ErrInsufficientScope
			}
			seenPods[pod.UID] = true
			result.Items = append(result.Items, *pod.DeepCopy())
		}
		if r.cached {
			// controller-runtime cache lists do not support pagination. They use
			// this sentinel even when no limit was requested and the list is full.
			if page.Continue != "" && page.Continue != "continue-not-supported" {
				return nil, fmt.Errorf("%w: unexpected cached Pod continuation token", discovery.ErrIncompleteOwnership)
			}
			return result, nil
		}
		if (options.Continue != "" || page.Continue != "") && page.ResourceVersion == "" {
			return nil, fmt.Errorf("%w: paginated Pod list has no resource version", discovery.ErrIncompleteOwnership)
		}
		if page.Continue == "" {
			return result, nil
		}
		if page.Continue == "continue-not-supported" || seenTokens[page.Continue] {
			return nil, fmt.Errorf("%w: unsupported or repeated Pod continuation token", discovery.ErrIncompleteOwnership)
		}
		seenTokens[page.Continue] = true
		options.Continue = page.Continue
	}
}

func (r *reader) checkReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.ready() {
		return discovery.ErrNotSynced
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	return (kind == reflect.Pointer || kind == reflect.Interface) && reflect.ValueOf(value).IsNil()
}
