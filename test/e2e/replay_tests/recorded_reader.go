// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package replay

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

// recordedReader serves the referenced CRs a recording captured with one state, so a definition
// that declares references replays offline exactly as it would read a live cluster.
type recordedReader struct {
	objects []*unstructured.Unstructured
}

func (r *recordedReader) Get(_ context.Context, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	for _, object := range r.objects {
		if !matchesGVK(object, gvk) || object.GetName() != name {
			continue
		}
		// A cluster-scoped capture has no namespace and matches any request; a namespaced one
		// must match exactly, the way a live GET would.
		if object.GetNamespace() != "" && object.GetNamespace() != namespace {
			continue
		}

		return object, nil
	}

	return nil, references.ErrNotFound
}

func (r *recordedReader) List(_ context.Context, gvk schema.GroupVersionKind, query references.ListQuery) ([]unstructured.Unstructured, error) {
	var out []unstructured.Unstructured
	for _, object := range r.objects {
		if !matchesGVK(object, gvk) {
			continue
		}
		if object.GetNamespace() != "" && object.GetNamespace() != query.Namespace {
			continue
		}
		if query.Selector != nil && !query.Selector.Matches(labels.Set(object.GetLabels())) {
			continue
		}
		out = append(out, *object)
	}

	return out, nil
}

func matchesGVK(object *unstructured.Unstructured, gvk schema.GroupVersionKind) bool {
	return object.GroupVersionKind() == gvk
}
