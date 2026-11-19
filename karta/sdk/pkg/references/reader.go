// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package references resolves declared objects without depending on a Kubernetes client.
package references

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
)

var ErrNotFound = errors.New("reference object not found")

type ResourceReader interface {
	Get(context.Context, schema.GroupVersionKind, string, string) (*unstructured.Unstructured, error)
	List(context.Context, schema.GroupVersionKind, ListQuery) ([]unstructured.Unstructured, error)
}
type ListQuery struct {
	Namespace string
	Selector  labels.Selector
}
type ReferenceValue struct {
	Object *unstructured.Unstructured
	List   []unstructured.Unstructured
}
type ResolvedReferences map[string]ReferenceValue

func NewLookupValue(object *unstructured.Unstructured) ReferenceValue {
	if object == nil {
		return ReferenceValue{}
	}
	return ReferenceValue{Object: object.DeepCopy()}
}
func NewListValue(objects []unstructured.Unstructured) ReferenceValue {
	out := make([]unstructured.Unstructured, len(objects))
	for i := range objects {
		out[i] = *objects[i].DeepCopy()
	}
	sortObjects(out)
	return ReferenceValue{List: out}
}
func sortObjects(objects []unstructured.Unstructured) {
	sort.Slice(objects, func(i, j int) bool {
		a, b := &objects[i], &objects[j]
		if a.GetNamespace() != b.GetNamespace() {
			return a.GetNamespace() < b.GetNamespace()
		}
		return a.GetName() < b.GetName()
	})
}
func (r ResolvedReferences) Copy() (ResolvedReferences, error) {
	result := make(ResolvedReferences, len(r))
	for name, value := range r {
		if value.Object != nil && value.List != nil {
			return nil, fmt.Errorf("reference %q has both object and list", name)
		}
		if value.List != nil {
			result[name] = NewListValue(value.List)
		} else {
			result[name] = NewLookupValue(value.Object)
		}
	}
	return result, nil
}
func (r ResolvedReferences) Bindings() (map[string]any, error) {
	copied, err := r.Copy()
	if err != nil {
		return nil, err
	}
	bindings := map[string]any{}
	for name, value := range copied {
		if value.Object != nil {
			v, err := cel.CopyJSON(value.Object.Object)
			if err != nil {
				return nil, fmt.Errorf("reference %q: %w", name, err)
			}
			bindings[name] = v
		} else if value.List != nil {
			items := make([]any, 0, len(value.List))
			for _, object := range value.List {
				v, err := cel.CopyJSON(object.Object)
				if err != nil {
					return nil, fmt.Errorf("reference %q: %w", name, err)
				}
				items = append(items, v)
			}
			bindings[name] = items
		}
	}
	return bindings, nil
}

// ValidateSupplied checks declaration names, binding shapes and object kinds.
// The caller's snapshot determines selection completeness; no reader fallback runs.
func ValidateSupplied(definitions []api.Reference, supplied ResolvedReferences) error {
	declarations := map[string]api.Reference{}
	for _, d := range definitions {
		declarations[d.Name] = d
	}
	for name, value := range supplied {
		d, ok := declarations[name]
		if !ok {
			return fmt.Errorf("unknown supplied reference %q", name)
		}
		if value.Object != nil && value.List != nil {
			return fmt.Errorf("reference %q has both object and list", name)
		}
		if d.Lookup != nil && value.List != nil {
			return fmt.Errorf("reference %q requires a lookup result", name)
		}
		if d.List != nil && value.List == nil {
			return fmt.Errorf("reference %q requires a list result", name)
		}
		objects := value.List
		if value.Object != nil {
			objects = []unstructured.Unstructured{*value.Object}
		}
		for _, object := range objects {
			if object.GetAPIVersion() != d.APIVersion || object.GetKind() != d.Kind {
				return fmt.Errorf("reference %q has an unexpected object kind", name)
			}
		}
	}
	return nil
}
