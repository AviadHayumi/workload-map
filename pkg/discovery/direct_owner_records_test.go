// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type ownerRecordReader struct {
	capturedReader
	gets      map[ObjectRef]int
	onGet     func(ObjectRef)
	onMapping func(schema.GroupVersionKind)
	readError error
	nilObject bool
}

func newOwnerRecordReader(objects ...*unstructured.Unstructured) *ownerRecordReader {
	r := &ownerRecordReader{capturedReader: capturedReader{objects: make(map[objectKey]*unstructured.Unstructured), mappings: make(map[schema.GroupVersionKind]*meta.RESTMapping)}, gets: make(map[ObjectRef]int)}
	for _, object := range objects {
		ref := reference(object)
		r.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}] = object
		scope := meta.RESTScopeNamespace
		if ref.Namespace == "" {
			scope = meta.RESTScopeRoot
		}
		r.mappings[ref.GVK] = indexTestMapping(ref.GVK, scope)
	}
	return r
}

func (r *ownerRecordReader) Mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	if r.onMapping != nil {
		r.onMapping(gvk)
	}
	return r.capturedReader.Mapping(ctx, gvk)
}

func (r *ownerRecordReader) Get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	r.gets[ref]++
	object, err := r.capturedReader.Get(ctx, ref)
	if r.onGet != nil {
		r.onGet(ref)
	}
	if r.readError != nil {
		return object, r.readError
	}
	if r.nilObject {
		return nil, nil
	}
	return object, err
}

func TestDirectOwnerRecordsPreserveCanonicalCacheKeys(t *testing.T) {
	root := indexTestObject("root", "", indexBundleGVK, nil)
	ref := reference(root)
	reader := newOwnerRecordReader(root)
	c := newCollection(reader)
	c.metadataOnly = true
	wrong := ref
	wrong.Namespace, wrong.UID = "team", "wrong"
	canonicalWrong := wrong
	canonicalWrong.Namespace = ""
	wantError := &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: canonicalWrong, Actual: ref}
	if _, err := c.lookup(context.Background(), wrong); !reflect.DeepEqual(err, wantError) {
		t.Fatalf("wrong UID error = %#v, want %#v", err, wantError)
	}
	var shared *ownership.Object
	for _, namespace := range []string{"team", "other", ""} {
		incoming := ref
		incoming.Namespace = namespace
		actual, err := c.lookup(context.Background(), incoming)
		if err != nil || actual.Ref != ref {
			t.Fatalf("lookup(%+v) = %+v, %v", incoming, actual, err)
		}
		cached := c.lookups[incoming]
		if cached == nil || (shared != nil && cached != shared) {
			t.Fatal("incoming namespace keys did not share the canonical observation")
		}
		shared = cached
		actual.Ref.Name = "changed value"
		actual.Namespaced = true
		actual.Owners = []metav1.OwnerReference{{Name: "changed value"}}
	}
	if reader.gets[canonicalWrong] != 1 || len(reader.gets) != 1 || len(c.directOwners) != 1 || len(c.directIdentities) != 1 || len(c.objects) != 0 || len(c.metadata) != 0 || len(c.lookups) != 3 {
		t.Fatal("direct owner retained a body, refetched canonical coordinates, or cached a failed lookup")
	}
	if _, err := c.lookup(context.Background(), wrong); !reflect.DeepEqual(err, wantError) {
		t.Fatalf("repeated wrong UID error = %#v, want %#v", err, wantError)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, namespace := range []string{"team", "uncached"} {
		incoming := ref
		incoming.Namespace = namespace
		if _, err := c.lookup(ctx, incoming); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled lookup error = %v", err)
		}
	}
	start := ownership.Object{Ref: ObjectRef{GVK: podGVK, Namespace: "team", Name: "pod", UID: "pod"}, Namespaced: true, Owners: []metav1.OwnerReference{indexTestOwner(root)}}
	start.Owners[0].UID = "wrong"
	_, err := ownership.Trace(context.Background(), start, c.lookup, 2)
	outer := &ownership.Error{Kind: nil, Expected: wrong, Actual: ObjectRef{}, Path: []ObjectRef{start.Ref}, Cause: wantError}
	if !reflect.DeepEqual(err, outer) {
		t.Fatalf("cluster traversal error = %#v, want %#v", err, outer)
	}
}

func TestDirectOwnerRecordsDetachBeforeReaderCallbacks(t *testing.T) {
	for _, callback := range []string{"get", "mapping"} {
		t.Run(callback, func(t *testing.T) {
			root := indexTestObject("root", "team", indexBundleGVK, nil)
			child := indexTestObject("child", "team", indexWorkerGVK, root)
			rawOwner := child.Object["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)
			rawOwner["blockOwnerDeletion"] = true
			ref := reference(child)
			want, err := ownershipObject(child, indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace))
			if err != nil {
				t.Fatal(err)
			}
			reader := newOwnerRecordReader(child, root)
			c := newCollection(reader)
			c.metadataOnly = true
			if _, err := c.lookup(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			mutated := false
			mutate := func() {
				mutated = true
				child.SetName("changed")
				child.SetUID("changed")
				child.SetResourceVersion("changed")
				for _, field := range []string{"apiVersion", "kind", "name", "uid"} {
					rawOwner[field] = "changed"
				}
				rawOwner["controller"], rawOwner["blockOwnerDeletion"] = false, false
			}
			if callback == "get" {
				reader.onGet = func(ObjectRef) { mutate() }
			} else {
				reader.onMapping = func(schema.GroupVersionKind) { mutate() }
			}
			if _, err := c.lookup(context.Background(), reference(root)); err != nil {
				t.Fatal(err)
			}
			actual, err := c.lookup(context.Background(), ref)
			if !mutated || err != nil || !reflect.DeepEqual(actual, want) || c.directIdentities[ref.UID].resourceVersion != "1" {
				t.Fatalf("reader mutation changed detached ownership: %+v, %v", actual, err)
			}
		})
	}
}

func TestDirectOwnerRecordsKeepServedAliasesSeparate(t *testing.T) {
	for _, priorFull := range []bool{false, true} {
		for _, conflict := range []string{"none", "resource_version", "owners"} {
			t.Run(conflict+"/full="+map[bool]string{false: "false", true: "true"}[priorFull], func(t *testing.T) {
				root := indexTestObject("root", "team", indexBundleGVK, nil)
				alias := root.DeepCopy()
				alias.SetAPIVersion(indexBundleGVK.Group + "/v2")
				if conflict == "resource_version" {
					alias.SetResourceVersion("2")
				}
				if conflict == "owners" {
					alias.SetOwnerReferences([]metav1.OwnerReference{{Name: "non-controller"}})
				}
				reader := newOwnerRecordReader(root, alias)
				c := newCollection(reader)
				c.metadataOnly = !priorFull
				if _, err := c.lookup(context.Background(), reference(root)); err != nil {
					t.Fatal(err)
				}
				c.metadataOnly = true
				actual, err := c.lookup(context.Background(), reference(alias))
				if conflict != "none" {
					if !errors.Is(err, ErrStaleInput) || c.lookups[reference(alias)] != nil {
						t.Fatalf("conflicting alias = %+v, %v", actual, err)
					}
					return
				}
				original, served := c.lookups[reference(root)], c.lookups[reference(alias)]
				if err != nil || original == served || original.Ref != reference(root) || actual.Ref != reference(alias) {
					t.Fatalf("served alias = %+v, %v", actual, err)
				}
				if priorFull && !reflect.DeepEqual(c.objects[objectKey{indexBundleGVK, "team", "root"}], root) {
					t.Fatal("alias replaced the full original body")
				}
			})
		}
	}
}

func TestDirectOwnerRecordsPreserveEmptyUIDObservations(t *testing.T) {
	for _, rawUID := range []any{nil, "", int64(7)} {
		var wantFirst, wantSecond error
		for _, direct := range []bool{false, true} {
			root := indexTestObject("root", "", indexBundleGVK, nil)
			ref := reference(root)
			root.Object["metadata"].(map[string]any)["uid"] = rawUID
			other := indexTestObject("other", "", indexBundleGVK, nil)
			otherRef := reference(other)
			other.SetUID("")
			reader := newOwnerRecordReader(root, other)
			c := newCollection(reader)
			c.metadataOnly = direct
			_, first := c.lookup(context.Background(), ref)
			if !errors.Is(first, ownership.ErrUIDMismatch) {
				t.Fatalf("empty UID error = %v", first)
			}
			root.SetUID(ref.UID)
			_, repeated := c.lookup(context.Background(), ref)
			if !reflect.DeepEqual(first, repeated) || reader.gets[ref] != 1 || len(c.lookups) != 0 {
				t.Fatal("failed UID request lost its observed body or entered successful cache")
			}
			_, second := c.lookup(context.Background(), otherRef)
			if !errors.Is(second, ErrStaleInput) {
				t.Fatalf("empty UID alias collision = %v", second)
			}
			if !direct {
				wantFirst, wantSecond = first, second
			} else if !reflect.DeepEqual(first, wantFirst) || !reflect.DeepEqual(second, wantSecond) {
				t.Fatal("direct record changed empty UID error ordering")
			}
		}
	}
}

func TestDirectOwnerRecordsDoNotCacheReadFailures(t *testing.T) {
	for _, failure := range []string{"missing", "error_with_object", "nil_object"} {
		t.Run(failure, func(t *testing.T) {
			root := indexTestObject("root", "team", indexBundleGVK, nil)
			ref := reference(root)
			reader := newOwnerRecordReader(root)
			readError := errors.New("reader failed")
			want := error(ErrInvalidInput)
			switch failure {
			case "missing":
				delete(reader.objects, objectKey{ref.GVK, ref.Namespace, ref.Name})
				want = ownership.ErrMissing
			case "error_with_object":
				reader.readError = readError
				want = readError
			case "nil_object":
				reader.nilObject = true
			}
			c := newCollection(reader)
			c.metadataOnly = true
			if _, err := c.lookup(context.Background(), ref); !errors.Is(err, want) {
				t.Fatalf("read failure = %v, want %v", err, want)
			}
			if len(c.directOwners) != 0 || len(c.directIdentities) != 0 || len(c.lookups) != 0 {
				t.Fatal("read failure entered a cache")
			}
			reader.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}] = root
			reader.readError, reader.nilObject = nil, false
			if _, err := c.lookup(context.Background(), ref); err != nil || reader.gets[ref] != 2 {
				t.Fatalf("retry = %v, reads %d", err, reader.gets[ref])
			}
		})
	}
}

func TestDirectOwnerRecordsKeepExactPodBodyPath(t *testing.T) {
	pod := indexTestPod("same", "team", nil)
	pod.Spec.Containers = []corev1.Container{{Name: "main", Image: "ghcr.io/example/workload:v1"}}
	pod.Status.Phase = corev1.PodRunning
	body, err := podObject(pod)
	if err != nil {
		t.Fatal(err)
	}
	customGVK := schema.GroupVersionKind{Group: "custom.test", Version: "v1", Kind: "Pod"}
	custom := indexTestObject(pod.Name, pod.Namespace, customGVK, nil)
	custom.SetUID("custom")
	reader := newOwnerRecordReader(body, custom)
	c := newCollection(reader)
	c.metadataOnly = true
	c.pods[types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}] = pod
	c.podUIDs[pod.UID] = pod
	for _, object := range []*unstructured.Unstructured{body, custom} {
		ref := reference(object)
		actual, err := c.lookup(context.Background(), ref)
		if err != nil || actual.Ref != ref {
			t.Fatalf("lookup(%+v) = %+v, %v", ref, actual, err)
		}
	}
	if !reflect.DeepEqual(c.objects[objectKey{podGVK, pod.Namespace, pod.Name}], body) || reader.gets[reference(body)] != 0 {
		t.Fatal("core Pod lookup lost its full listed body")
	}
	key := objectKey{customGVK, pod.Namespace, pod.Name}
	if c.objects[key] != nil || c.directOwners[key] == nil || reader.gets[reference(custom)] != 1 {
		t.Fatal("custom-group Pod was substituted with the typed core Pod")
	}
}

func TestDirectOwnerRecordsRejectLaterUnlistedPodAlias(t *testing.T) {
	owner := indexTestObject("owner", "team", indexWorkerGVK, nil)
	owner.SetUID("shared")
	pod := indexTestPod("owner", "team", nil)
	pod.UID = types.UID("shared")
	invalid := indexTestOwner(indexTestObject("missing", "team", indexBundleGVK, nil))
	invalid.UID = ""
	pod.OwnerReferences = []metav1.OwnerReference{invalid}
	body, err := podObject(pod)
	if err != nil {
		t.Fatal(err)
	}
	start := ownership.Object{Ref: ObjectRef{GVK: podGVK, Namespace: "team", Name: "child", UID: "child"}, Namespaced: true, Owners: []metav1.OwnerReference{indexTestOwner(body)}}
	var want error
	for _, direct := range []bool{false, true} {
		reader := newOwnerRecordReader(owner, body)
		c := newCollection(reader)
		c.metadataOnly = direct
		if _, err := c.lookup(context.Background(), reference(owner)); err != nil {
			t.Fatal(err)
		}
		_, err := ownership.Trace(context.Background(), start, c.lookup, 4)
		if !errors.Is(err, ErrStaleInput) || c.lookups[reference(body)] != nil {
			t.Fatalf("later unlisted Pod alias error = %v", err)
		}
		if !direct {
			want = err
		} else if !reflect.DeepEqual(err, want) {
			t.Fatalf("later Pod error = %#v, want %#v", err, want)
		}
	}
	for _, transfer := range []bool{false, true} {
		root := indexTestObject("root", "team", indexBundleGVK, nil)
		dataset := Dataset{Objects: []*unstructured.Unstructured{root, owner, body}, Pods: []*corev1.Pod{indexTestPod("selected", "team", root), indexTestPod("record-first", "team", owner), indexTestPod("unlisted-second", "team", body)}, Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
			podGVK: indexTestMapping(podGVK, meta.RESTScopeNamespace), indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace), indexWorkerGVK: indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace),
		}}
		reader := ownerReferenceReader(t, dataset)
		var input Reader = reader
		if transfer {
			input = &ownerReferenceTransferReader{reader}
		}
		resolver, err := New(input, Options{})
		if err != nil {
			t.Fatal(err)
		}
		workload, err := resolver.Workload(context.Background(), reference(root))
		if workload != nil || !errors.Is(err, ErrStaleInput) {
			t.Fatalf("direct transfer=%v returned %v, %v, want no partial workload and alias error", transfer, workload, err)
		}
	}
}
