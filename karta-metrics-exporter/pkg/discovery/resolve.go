// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
)

type Config struct{ IgnoredTopOwners []schema.GroupVersionKind }

type discovery struct {
	dependencies Dependencies
	ignored      map[schema.GroupVersionKind]bool
}

func New(dependencies Dependencies, config Config) (Discoverer, error) {
	if dependencies.Reader == nil || dependencies.Inventory == nil {
		return nil, errors.New("reader and inventory required")
	}
	d := &discovery{dependencies: dependencies, ignored: make(map[schema.GroupVersionKind]bool)}
	for _, gvk := range config.IgnoredTopOwners {
		d.ignored[gvk] = true
	}
	return d, nil
}

func (d *discovery) Get(ctx context.Context, ref ObjectRef) (Result, error) {
	workload := &GenericWorkload{Resolution: Resolved}
	seen := make(map[ObjectRef]bool)
	var rootCached bool
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		key := ref
		key.ResourceVersion = ""
		if seen[key] {
			return Result{}, fmt.Errorf("%w: ownership cycle at %s", ErrUnresolvedOwner, ref.Name)
		}
		seen[key] = true
		if len(seen) > 1024 {
			return Result{}, fmt.Errorf("%w: owner chain exceeds limit", ErrUnresolvedOwner)
		}
		var object metav1.Object
		cached, ok := d.dependencies.Reader.Cached(ref)
		ok = ok && cached != nil
		if ok {
			object = cached
		} else {
			metadata, err := d.dependencies.Reader.GetMetadata(ctx, ref)
			if err != nil {
				return Result{}, readError(err)
			}
			if metadata == nil {
				return Result{}, fmt.Errorf("%w: empty metadata", ErrUnavailable)
			}
			object = metadata
		}
		// The reader resolves API scope; a cluster-scoped owner has no namespace.
		if len(workload.Path) > 0 && object.GetNamespace() == "" {
			ref.Namespace = ""
		}
		if ref.Name != object.GetName() || ref.Namespace != object.GetNamespace() || object.GetUID() == "" || (ref.UID != "" && ref.UID != object.GetUID()) {
			return Result{}, fmt.Errorf("%w: object identity changed", ErrStale)
		}
		if ref.ResourceVersion != "" && ref.ResourceVersion != object.GetResourceVersion() {
			return Result{}, fmt.Errorf("%w: resource version changed", ErrStale)
		}
		ref.UID = object.GetUID()
		ref.ResourceVersion = object.GetResourceVersion()
		workload.Path = append(workload.Path, ref)
		var owner *metav1.OwnerReference
		for _, candidate := range object.GetOwnerReferences() {
			if candidate.Controller != nil && *candidate.Controller {
				if owner != nil {
					return Result{}, fmt.Errorf("%w: multiple controller owners", ErrUnresolvedOwner)
				}
				owner = &candidate
			}
		}
		if owner == nil {
			rootCached = ok
			break
		}
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil || owner.Kind == "" || owner.Name == "" || owner.UID == "" {
			return Result{}, fmt.Errorf("%w: invalid controller reference", ErrUnresolvedOwner)
		}
		gvk := gv.WithKind(owner.Kind)
		if d.ignored[gvk] {
			workload.Resolution = IgnoredOwner
			rootCached = ok
			break
		}
		ref = ObjectRef{GVK: gvk, Namespace: ref.Namespace, Name: owner.Name, UID: owner.UID}
	}
	workload.Root = ref
	result := Result{Workload: workload}
	if !rootCached {
		result.MissingCoverage = []WatchRequest{{GVK: ref.GVK, Namespace: ref.Namespace}}
	}
	_, err := d.dependencies.Inventory.Lookup(ref.GVK)
	if err != nil {
		return result, readError(err)
	}
	return result, nil
}

func readError(err error) error {
	switch {
	case errors.Is(err, ErrPending), errors.Is(err, inventory.ErrPending):
		return fmt.Errorf("%w: %w", ErrPending, err)
	case errors.Is(err, ErrStale):
		return err
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err), errors.Is(err, ErrPermissionDenied):
		return fmt.Errorf("%w: %w", ErrPermissionDenied, err)
	case apierrors.IsNotFound(err), errors.Is(err, ErrUnresolvedOwner):
		return fmt.Errorf("%w: %w", ErrUnresolvedOwner, err)
	default:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
}
