// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

func TestCollectionLookupKeepsIncomingNamespaceAndUID(t *testing.T) {
	root := indexTestObject("root", "", indexBundleGVK, nil)
	reader := &capturedReader{
		objects:  map[objectKey]*unstructured.Unstructured{{gvk: indexBundleGVK, name: "root"}: root},
		mappings: map[schema.GroupVersionKind]*meta.RESTMapping{indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeRoot)},
	}
	collection := newCollection(reader)
	for _, namespace := range []string{"team", "other", ""} {
		ref := reference(root)
		ref.Namespace = namespace
		actual, err := collection.lookup(context.Background(), ref)
		if err != nil || actual.Ref != reference(root) {
			t.Fatalf("lookup(%+v) = %+v, %v, want cluster root", ref, actual, err)
		}
		if _, exists := collection.lookups[ref]; !exists {
			t.Fatalf("lookup(%+v) did not preserve the dependent namespace", ref)
		}
	}
	if len(collection.lookups) != 3 {
		t.Fatalf("cached lookups = %d, want 3", len(collection.lookups))
	}
	wrongUID := reference(root)
	wrongUID.UID = "replacement"
	if _, err := collection.lookup(context.Background(), wrongUID); !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("lookup(%+v) error = %v, want ErrUIDMismatch", wrongUID, err)
	}
	if _, exists := collection.lookups[wrongUID]; exists {
		t.Fatal("failed UID lookup was cached")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collection.lookup(ctx, reference(root)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cached lookup error = %v, want context.Canceled", err)
	}
}

func TestCollectionLookupValidatesEachServedVersion(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
	}{
		{name: "consistent"},
		{name: "resource_version", mutate: func(object *unstructured.Unstructured) { object.SetResourceVersion("2") }},
		{name: "owner_references", mutate: func(object *unstructured.Unstructured) {
			object.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(indexTestObject("other", "team", indexBundleGVK, nil))})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := indexTestObject("root", "team", indexBundleGVK, nil)
			alias := root.DeepCopy()
			aliasGVK := indexBundleGVK
			aliasGVK.Version = "v2"
			alias.SetGroupVersionKind(aliasGVK)
			if test.mutate != nil {
				test.mutate(alias)
			}
			reader := &capturedReader{
				objects: map[objectKey]*unstructured.Unstructured{
					{gvk: indexBundleGVK, namespace: "team", name: "root"}: root,
					{gvk: aliasGVK, namespace: "team", name: "root"}:       alias,
				},
				mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
					indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace),
					aliasGVK:       indexTestMapping(aliasGVK, meta.RESTScopeNamespace),
				},
			}
			collection := newCollection(reader)
			if _, err := collection.lookup(context.Background(), reference(root)); err != nil {
				t.Fatal(err)
			}
			actual, err := collection.lookup(context.Background(), reference(alias))
			if test.mutate != nil {
				if !errors.Is(err, ErrStaleInput) || len(collection.lookups) != 1 {
					t.Fatalf("alias lookup error = %v, cached lookups = %d, want ErrStaleInput, 1", err, len(collection.lookups))
				}
				return
			}
			if err != nil || actual.Ref != reference(alias) || len(collection.lookups) != 2 {
				t.Fatalf("alias lookup = %+v, %v, cached lookups = %d, want v2 alias, nil, 2", actual, err, len(collection.lookups))
			}
		})
	}
}

func TestCollectionLookupReusesSiblingOwners(t *testing.T) {
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	child := indexTestObject("child", "team", indexWorkerGVK, root)
	reader := &capturedReader{
		objects: map[objectKey]*unstructured.Unstructured{
			{gvk: indexBundleGVK, namespace: "team", name: "root"}:  root,
			{gvk: indexWorkerGVK, namespace: "team", name: "child"}: child,
		},
		mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
			podGVK:         indexTestMapping(podGVK, meta.RESTScopeNamespace),
			indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace),
			indexWorkerGVK: indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace),
		},
	}
	collection := newCollection(reader)
	for _, name := range []string{"first", "second"} {
		pod := indexTestPod(name, "team", child)
		path, err := collection.tracePod(context.Background(), pod, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(path) != 3 || path[0].Ref != podReference(pod) || path[1].Ref != reference(child) || path[2].Ref != reference(root) {
			t.Fatalf("tracePod(%s) path = %+v, want Pod, child, root", name, path)
		}
		if len(collection.lookups) != 2 {
			t.Fatalf("tracePod(%s) cached lookups = %d, want 2", name, len(collection.lookups))
		}
	}
	outside := indexTestPod("outside", "other", child)
	if _, err := collection.tracePod(context.Background(), outside, 2); !errors.Is(err, ownership.ErrMissing) {
		t.Fatalf("cross-namespace sibling error = %v, want ErrMissing", err)
	}
	shallow := indexTestPod("shallow", "team", child)
	if _, err := collection.tracePod(context.Background(), shallow, 1); !errors.Is(err, ownership.ErrDepthExceeded) {
		t.Fatalf("shallow sibling error = %v, want ErrDepthExceeded", err)
	}
}

func TestCollectionLookupDoesNotCacheReadFailures(t *testing.T) {
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	reader := &capturedReader{
		objects:  make(map[objectKey]*unstructured.Unstructured),
		mappings: map[schema.GroupVersionKind]*meta.RESTMapping{indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace)},
	}
	collection := newCollection(reader)
	if _, err := collection.lookup(context.Background(), reference(root)); !errors.Is(err, ownership.ErrMissing) {
		t.Fatalf("missing lookup error = %v, want ErrMissing", err)
	}
	if len(collection.lookups) != 0 {
		t.Fatal("failed lookup was cached")
	}
	reader.objects[objectKey{gvk: indexBundleGVK, namespace: "team", name: "root"}] = root
	actual, err := collection.lookup(context.Background(), reference(root))
	if err != nil || actual.Ref != reference(root) {
		t.Fatalf("retried lookup = %+v, %v, want root, nil", actual, err)
	}
	root.SetName("source mutation")
	if actual.Ref.Name != "root" || collection.objects[objectKey{gvk: indexBundleGVK, namespace: "team", name: "root"}].GetName() != "root" {
		t.Fatal("source mutation changed cached ownership")
	}
}
