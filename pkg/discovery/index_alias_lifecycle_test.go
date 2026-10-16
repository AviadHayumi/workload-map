// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

func TestIndexAliasBodiesAcrossPublications(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			ctx := context.Background()
			objects := make([]*unstructured.Unstructured, 3)
			mappings := map[schema.GroupVersionKind]*meta.RESTMapping{podGVK: indexTestMapping(podGVK, meta.RESTScopeNamespace)}
			for n := range objects {
				gvk := indexBundleGVK
				gvk.Version = fmt.Sprintf("v%d", n+1)
				objects[n] = indexTestObject("root", "team", gvk, nil)
				objects[n].Object["unknown"] = map[string]any{"version": gvk.Version, "nested": []any{nil, map[string]any{"value": int64(n)}}}
				mappings[gvk] = indexTestMapping(gvk, meta.RESTScopeNamespace)
			}
			ordered := func(count int) []*unstructured.Unstructured {
				result := append([]*unstructured.Unstructured(nil), objects[:count]...)
				if reverse {
					for a, b := 0, len(result)-1; a < b; a, b = a+1, b-1 {
						result[a], result[b] = result[b], result[a]
					}
				}
				return result
			}
			index := NewIndex()
			generation, err := index.Replace(ctx, Dataset{Namespace: "team", Objects: ordered(1), Mappings: mappings})
			if err != nil {
				t.Fatal(err)
			}
			resolver, err := New(index, Options{})
			if err != nil {
				t.Fatal(err)
			}
			var old []*Capture
			for _, count := range []int{1, 2, 3, 1, 3} {
				if len(old) > 0 {
					generation, err = index.Apply(ctx, generation, Changes{Upserts: ordered(count)})
					if err != nil {
						t.Fatal(err)
					}
				}
				capture := indexTestCapture(t, index, reference(objects[0]))
				if len(capture.Objects) != count || len(capture.Mappings) != count+1 || capture.Observation.Generation != generation {
					t.Fatalf("capture after %d aliases: %+v", count, capture)
				}
				for n, object := range objects {
					got, err := index.Get(ctx, reference(object))
					if n >= count {
						if !apierrors.IsNotFound(err) {
							t.Fatalf("removed alias %d: %v", n, err)
						}
						continue
					}
					if err != nil || !reflect.DeepEqual(got, object) || !reflect.DeepEqual(capture.Objects[n], object) {
						t.Fatalf("exact alias %d body changed: %v", n, err)
					}
					workload, err := resolver.Workload(ctx, reference(object))
					if err != nil || !reflect.DeepEqual(workload.Object(), object) {
						t.Fatalf("exact root alias %d changed: %v", n, err)
					}
					got.Object["unknown"].(map[string]any)["version"] = "mutated"
					fresh, err := index.Get(ctx, reference(object))
					if err != nil || !reflect.DeepEqual(fresh, object) {
						t.Fatalf("Get output aliased index: %v", err)
					}
				}
				old = append(old, capture)
			}
			for n, count := range []int{1, 2, 3, 1, 3} {
				if len(old[n].Objects) != count || old[n].Observation.Generation != uint64(n+1) {
					t.Fatal("old capture changed after alias replacement")
				}
				for k, object := range old[n].Objects {
					if !reflect.DeepEqual(object, objects[k]) {
						t.Fatal("old capture body changed")
					}
				}
			}
			changed := objects[0].DeepCopy()
			changed.SetResourceVersion("2")
			actual, err := index.Apply(ctx, generation, Changes{Upserts: []*unstructured.Unstructured{changed}, Deletes: []ObjectRef{{}}})
			if err == nil || actual != generation {
				t.Fatalf("invalid batch published: %d, %v", actual, err)
			}
			for _, object := range objects {
				got, err := index.Get(ctx, reference(object))
				if err != nil || !reflect.DeepEqual(got, object) {
					t.Fatalf("failed batch changed alias: %v", err)
				}
			}
		})
	}
}

func TestIndexAliasDuplicateErrorOrdering(t *testing.T) {
	ctx := context.Background()
	base := indexTestObject("root", "team", indexBundleGVK, nil)
	second := base.DeepCopy()
	gvk := indexBundleGVK
	gvk.Version = "v2"
	second.SetGroupVersionKind(gvk)
	for _, test := range []struct {
		name      string
		prefix    []*unstructured.Unstructured
		duplicate *unstructured.Unstructured
	}{
		{"singleton_primary", []*unstructured.Unstructured{base}, base},
		{"promoted_primary", []*unstructured.Unstructured{base, second}, base},
		{"promoted_secondary", []*unstructured.Unstructured{base, second}, second},
		{"reversed_primary", []*unstructured.Unstructured{second, base}, second},
	} {
		for _, inconsistent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inconsistent=%t", test.name, inconsistent), func(t *testing.T) {
				index := NewIndex()
				data := Dataset{Namespace: "team", Objects: []*unstructured.Unstructured{base}, Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{podGVK: indexTestMapping(podGVK, meta.RESTScopeNamespace), indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace), gvk: indexTestMapping(gvk, meta.RESTScopeNamespace)}}
				generation, err := index.Replace(ctx, data)
				if err != nil {
					t.Fatal(err)
				}
				duplicate := test.duplicate.DeepCopy()
				want := ErrInvalidInput
				message := fmt.Sprintf("%v: duplicate object coordinates", want)
				if inconsistent {
					duplicate.SetResourceVersion("different")
					want = ErrStaleInput
					message = fmt.Sprintf("%v: inconsistent served-version aliases for UID %s", want, base.GetUID())
				}
				upserts := append(append([]*unstructured.Unstructured(nil), test.prefix...), duplicate)
				for _, replace := range []bool{false, true} {
					var actual uint64
					if replace {
						data.Objects = upserts
						actual, err = index.Replace(ctx, data)
					} else {
						actual, err = index.Apply(ctx, generation, Changes{Upserts: upserts})
					}
					wantGeneration := generation
					if replace {
						wantGeneration = 0
					}
					if !errors.Is(err, want) || err.Error() != message || actual != wantGeneration {
						t.Fatalf("replace=%t: %d %v; want %d %s", replace, actual, err, wantGeneration, message)
					}
					if capture := indexTestCapture(t, index, reference(base)); capture.Observation.Generation != generation {
						t.Fatal("failed publication advanced generation")
					}
					got, err := index.Get(ctx, reference(base))
					if err != nil || !reflect.DeepEqual(got, base) {
						t.Fatalf("failed publication changed original: %v", err)
					}
					if _, err := index.Get(ctx, reference(second)); !apierrors.IsNotFound(err) {
						t.Fatalf("failed publication exposed another alias: %v", err)
					}
				}
			})
		}
	}
}

func TestIndexPodAliasDepthDiagnostics(t *testing.T) {
	ctx := context.Background()
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	child := indexTestObject("child", "team", indexWorkerGVK, root)
	pod, err := podObject(indexTestPod("pod", "team", child))
	if err != nil {
		t.Fatal(err)
	}
	alias := pod.DeepCopy()
	aliasGVK := podGVK
	aliasGVK.Version = "v2"
	alias.SetGroupVersionKind(aliasGVK)
	alias.Object["extension"] = "different served body"
	for _, order := range []string{"singleton", "core_first", "core_second"} {
		t.Run(order, func(t *testing.T) {
			objects := []*unstructured.Unstructured{root, child}
			switch order {
			case "singleton":
				objects = append(objects, pod)
			case "core_first":
				objects = append(objects, pod, alias)
			case "core_second":
				objects = append(objects, alias, pod)
			}
			index := NewIndex()
			_, err := index.Replace(ctx, Dataset{Namespace: "team", Objects: objects, Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{podGVK: indexTestMapping(podGVK, meta.RESTScopeNamespace), aliasGVK: indexTestMapping(aliasGVK, meta.RESTScopeNamespace), indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace), indexWorkerGVK: indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace)}})
			if err != nil {
				t.Fatal(err)
			}
			resolver, err := New(index, Options{MaxDepth: 1})
			if err != nil {
				t.Fatal(err)
			}
			workload, err := resolver.Workload(ctx, reference(root))
			var detail *ownership.Error
			wantPath := []ObjectRef{reference(pod), reference(child)}
			if workload != nil || !errors.Is(err, ErrIncompleteOwnership) || !errors.Is(err, ownership.ErrDepthExceeded) || !errors.As(err, &detail) {
				t.Fatalf("depth failure lost: %v", err)
			}
			if detail.Expected != reference(root) || detail.Actual != (ObjectRef{}) || !reflect.DeepEqual(detail.Path, wantPath) {
				t.Fatalf("depth error used wrong served Pod: %+v, want path %+v", detail, wantPath)
			}
		})
	}
}
