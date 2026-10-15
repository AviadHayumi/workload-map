// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package clientgo adapts an injected Kubernetes dynamic client to discovery.Reader.
package clientgo

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
	"k8s.io/client-go/dynamic"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type reader struct {
	client dynamic.Interface
	mapper meta.RESTMapper
}

const podPageSize = 500

// NewReader uses caller-owned dependencies and performs no API discovery or
// background work. The mapper must already know the requested exact versions;
// RESTMapper has no context parameter, so mapping itself cannot be interrupted.
func NewReader(client dynamic.Interface, mapper meta.RESTMapper) (discovery.Reader, error) {
	if isNil(client) || isNil(mapper) {
		return nil, fmt.Errorf("dynamic client and REST mapper are required: %w", discovery.ErrNoClient)
	}
	return &reader{client: client, mapper: mapper}, nil
}

func (r *reader) Mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("map %s: %w", gvk, err)
	}
	if gvk.Version == "" || gvk.Kind == "" {
		return nil, fmt.Errorf("%w: version and kind are required", discovery.ErrInvalidInput)
	}
	mapping, err := r.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("map %s: %w", gvk, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("map %s: %w", gvk, err)
	}
	if mapping == nil || isNil(mapping.Scope) || mapping.GroupVersionKind != gvk || mapping.Resource.GroupVersion() != gvk.GroupVersion() || mapping.Resource.Resource == "" || strings.Contains(mapping.Resource.Resource, "/") {
		return nil, fmt.Errorf("%w: mapper returned an invalid exact mapping for %s", discovery.ErrInvalidInput, gvk)
	}
	result := *mapping
	switch mapping.Scope.Name() {
	case meta.RESTScopeNameNamespace:
		result.Scope = meta.RESTScopeNamespace
	case meta.RESTScopeNameRoot:
		result.Scope = meta.RESTScopeRoot
	default:
		return nil, fmt.Errorf("%w: unsupported scope %q", discovery.ErrInvalidInput, mapping.Scope.Name())
	}
	return &result, nil
}

func (r *reader) Get(ctx context.Context, ref discovery.ObjectRef) (*unstructured.Unstructured, error) {
	if ref.Name == "" || ref.UID == "" {
		return nil, fmt.Errorf("%w: object name and UID are required", discovery.ErrInvalidInput)
	}
	mapping, err := r.Mapping(ctx, ref.GVK)
	if err != nil {
		return nil, err
	}
	resource := r.client.Resource(mapping.Resource)
	var scoped dynamic.ResourceInterface = resource
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if ref.Namespace == "" {
			return nil, fmt.Errorf("%w: namespace required for %s", ownership.ErrScope, ref.GVK)
		}
		scoped = resource.Namespace(ref.Namespace)
	} else {
		ref.Namespace = ""
	}
	object, err := scoped.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s %s/%s: %w", ref.GVK, ref.Namespace, ref.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("get %s: %w", ref.Name, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%w: nil object for %s", discovery.ErrInvalidInput, ref.Name)
	}
	actual := discovery.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
	if actual.GVK != ref.GVK || actual.Namespace != ref.Namespace || actual.Name != ref.Name {
		return nil, fmt.Errorf("%w: returned coordinates disagree for %s", discovery.ErrStaleInput, ref.Name)
	}
	if actual.UID != ref.UID {
		return nil, &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: ref, Actual: actual}
	}
	return object.DeepCopy(), nil
}

func (r *reader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	result := &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
	resource := r.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace)
	options := metav1.ListOptions{Limit: podPageSize}
	seenTokens := make(map[string]bool)
	seenPods := make(map[types.UID]bool)
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list Pods: %w", err)
		}
		page, err := resource.List(ctx, options)
		if err != nil {
			return nil, fmt.Errorf("list Pods in %q: %w", namespace, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list Pods: %w", err)
		}
		if page == nil {
			return nil, fmt.Errorf("%w: nil Pod page", discovery.ErrIncompleteOwnership)
		}
		if !first && page.GetResourceVersion() != result.ResourceVersion {
			return nil, fmt.Errorf("%w: Pod resource version changed between pages", discovery.ErrIncompleteOwnership)
		}
		if (options.Continue != "" || page.GetContinue() != "") && page.GetResourceVersion() == "" {
			return nil, fmt.Errorf("%w: paginated Pod list has no resource version", discovery.ErrIncompleteOwnership)
		}
		if first {
			result.ResourceVersion = page.GetResourceVersion()
			first = false
		}
		for i := range page.Items {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("list Pods: %w", err)
			}
			object := &page.Items[i]
			if object.GroupVersionKind() != (schema.GroupVersionKind{Version: "v1", Kind: "Pod"}) {
				return nil, fmt.Errorf("%w: Pod list contains %s", discovery.ErrInvalidInput, object.GroupVersionKind())
			}
			pod := corev1.Pod{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &pod); err != nil {
				return nil, fmt.Errorf("decode Pod %s: %w", object.GetName(), err)
			}
			if pod.Name == "" || pod.Namespace == "" || pod.UID == "" {
				return nil, fmt.Errorf("%w: incomplete Pod identity", discovery.ErrInvalidInput)
			}
			if namespace != "" && pod.Namespace != namespace {
				return nil, fmt.Errorf("%w: Pod outside requested namespace", discovery.ErrInsufficientScope)
			}
			if seenPods[pod.UID] {
				return nil, fmt.Errorf("%w: duplicate Pod UID %s", discovery.ErrInvalidInput, pod.UID)
			}
			seenPods[pod.UID] = true
			result.Items = append(result.Items, pod)
		}
		next := page.GetContinue()
		if next == "" {
			return result, nil
		}
		if seenTokens[next] {
			return nil, fmt.Errorf("%w: repeated Pod continuation token", discovery.ErrIncompleteOwnership)
		}
		seenTokens[next] = true
		options.Continue = next
	}
}

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
