// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"reflect"
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
	var shared *ownership.Object
	for _, namespace := range []string{"team", "other", ""} {
		ref := reference(root)
		ref.Namespace = namespace
		actual, err := collection.lookup(context.Background(), ref)
		if err != nil || actual.Ref != reference(root) {
			t.Fatalf("lookup(%+v) = %+v, %v, want cluster root", ref, actual, err)
		}
		cached := collection.lookups[ref]
		if cached == nil {
			t.Fatalf("lookup(%+v) did not preserve the dependent namespace", ref)
		}
		if shared != nil && cached != shared {
			t.Fatal("cluster owner lookup allocated separate ownership records per namespace")
		}
		shared = cached
		object := collection.objects[objectKey{gvk: indexBundleGVK, name: "root"}]
		if collection.metadata[object] != cached || len(collection.metadata) != 1 {
			t.Fatal("lookup and object caches did not share the observed ownership record")
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
				if !errors.Is(err, ErrStaleInput) || len(collection.lookups) != 1 || len(collection.metadata) != 1 {
					t.Fatalf("alias lookup error = %v, cached lookups = %d, want ErrStaleInput, 1", err, len(collection.lookups))
				}
				return
			}
			if err != nil || actual.Ref != reference(alias) || len(collection.lookups) != 2 {
				t.Fatalf("alias lookup = %+v, %v, cached lookups = %d, want v2 alias, nil, 2", actual, err, len(collection.lookups))
			}
			original := collection.lookups[reference(root)]
			served := collection.lookups[reference(alias)]
			if original == served || original.Ref != reference(root) || served.Ref != reference(alias) || len(collection.metadata) != 2 {
				t.Fatal("served-version observations shared or rewrote an ownership record")
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collection.lookup(ctx, reference(root)); !errors.Is(err, context.Canceled) || len(collection.lookups) != 0 || len(collection.metadata) != 0 {
		t.Fatalf("canceled uncached lookup error = %v, want context.Canceled and empty caches", err)
	}
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

func TestCollectionLookupDetachesSourceAndReturnsValue(t *testing.T) {
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	child := indexTestObject("child", "team", indexWorkerGVK, root)
	rawOwner := child.Object["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)
	rawOwner["blockOwnerDeletion"] = true
	ref := reference(child)
	mapping := indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace)
	want, err := ownershipObject(child.DeepCopy(), mapping)
	if err != nil {
		t.Fatal(err)
	}
	reader := &capturedReader{
		objects:  map[objectKey]*unstructured.Unstructured{{gvk: ref.GVK, namespace: ref.Namespace, name: ref.Name}: child},
		mappings: map[schema.GroupVersionKind]*meta.RESTMapping{ref.GVK: mapping},
	}
	collection := newCollection(reader)
	actual, err := collection.lookup(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	actual.Ref.Name = "changed returned value"
	actual.Namespaced = false
	actual.Owners = nil
	child.SetName("changed source")
	for _, key := range []string{"apiVersion", "kind", "name", "uid"} {
		rawOwner[key] = "changed"
	}
	rawOwner["controller"] = false
	rawOwner["blockOwnerDeletion"] = false
	actual, err = collection.lookup(context.Background(), ref)
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatalf("cached lookup after mutation = %+v, %v, want %+v", actual, err, want)
	}
}

func TestCollectionMetadataCachesOnlyValidObservedBodies(t *testing.T) {
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	object := indexTestObject("child", "team", indexWorkerGVK, root)
	collection := newCollection(nil)
	if actual, err := collection.ownershipMetadata(object); actual != nil || !errors.Is(err, ErrInvalidInput) || len(collection.metadata) != 0 {
		t.Fatalf("metadata without mapping = %+v, %v, want nil, ErrInvalidInput and empty cache", actual, err)
	}
	collection.mappings[indexWorkerGVK] = indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace)
	first, err := collection.ownershipMetadata(object)
	if err != nil {
		t.Fatal(err)
	}
	otherBody := object.DeepCopy()
	otherBody.SetResourceVersion("2")
	otherBody.SetOwnerReferences(nil)
	second, err := collection.ownershipMetadata(otherBody)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.Ref != second.Ref || len(first.Owners) != 1 || len(second.Owners) != 0 || len(collection.metadata) != 2 {
		t.Fatal("distinct observed bodies reused metadata based only on their identity")
	}
	repeated, err := collection.ownershipMetadata(object)
	if err != nil || repeated != first {
		t.Fatal("the same observed body did not reuse its ownership record")
	}
}
