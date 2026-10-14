// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package owner

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestDescendantsTrackChangedAndDeletedOwner(t *testing.T) {
	index := New()
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	for _, name := range []string{"root", "other", "child", "pod"} {
		obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name)}}
		switch name {
		case "child":
			obj.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "root", UID: "root", Controller: ptr.To(true)}}
		case "pod":
			obj.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "child", UID: "child", Controller: ptr.To(true)}}
		}
		index.Track(obj, gvk)
		index.UpsertObject(obj.UID, obj.OwnerReferences)
	}
	if got := len(index.Descendants("root")); got != 2 {
		t.Fatalf("descendants = %d", got)
	}
	index.UpsertObject("child", []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "other", UID: "other", Controller: ptr.To(true)}})
	if len(index.Descendants("root")) != 0 || len(index.Descendants("other")) != 2 {
		t.Fatal("edge move left stale reverse index")
	}
	index.DeleteObject("child")
	if len(index.Descendants("child")) != 1 {
		t.Fatal("deletion lost unresolved descendants")
	}
}

func TestRootBoundaryIsExactAndStopsBeforeLookup(t *testing.T) {
	index := New()
	candidate := &Ref{UID: "candidate", Namespace: "test", Name: "candidate"}
	gvk := schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Wrapper"}
	refs := []metav1.OwnerReference{{APIVersion: "example.com/v1", Kind: "Wrapper", Name: "wrapper", UID: "wrapper", Controller: ptr.To(true)}}
	isRoot := func(schema.GroupKind) bool { return true }
	result := index.Resolve(refs, isRoot, func(actual schema.GroupVersionKind) bool { return actual == gvk }, candidate)
	if result.Outcome != OutcomeFound || result.Root.UID != candidate.UID || result.Resolution != "ignored_owner" {
		t.Fatalf("boundary result: %+v", result)
	}
	gvk.Version = "v2"
	result = index.Resolve(refs, isRoot, func(actual schema.GroupVersionKind) bool { return actual == gvk }, candidate)
	if result.Outcome != OutcomeMissing {
		t.Fatal("different version incorrectly ignored")
	}
}

func TestLargePendingIndexForgetsOnlyRequestedPod(t *testing.T) {
	index := New()
	for n := 0; n < 100000; n++ {
		index.MarkPending(types.UID(fmt.Sprintf("owner-%d", n)), fmt.Sprintf("test/pod-%d", n))
	}
	index.ForgetPending("test/pod-12345")
	if index.PendingCount() != 99999 {
		t.Fatal("pending index count wrong")
	}
	if got := index.UpsertObject("owner-99999", nil); len(got) != 1 || got[0] != "test/pod-99999" {
		t.Fatalf("wrong pending owner drained: %v", got)
	}
}

func BenchmarkAffectedDescendants(b *testing.B) {
	for _, count := range []int{1000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			index := New()
			gvk := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
			for n := 0; n < count; n++ {
				uid := types.UID(fmt.Sprintf("root-%d", n))
				pod := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pod-%d", n), Namespace: "test", UID: types.UID(fmt.Sprintf("pod-%d", n)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: string(uid), UID: uid, Controller: ptr.To(true)}}}}
				index.Track(pod, gvk)
				index.UpsertObject(pod.UID, pod.OwnerReferences)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				if got := index.Descendants("root-0"); len(got) != 1 {
					b.Fatal(len(got))
				}
			}
		})
	}
}

func TestTrackedOwnerPublicationIsAtomic(t *testing.T) {
	index := New()
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "test", UID: "child", OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "missing", UID: "missing", Controller: ptr.To(true)}}}}
	refs := []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "child", UID: "child", Controller: ptr.To(true)}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 5000 {
			index.UpsertTrackedObject(obj, gvk)
			index.DeleteObject(obj.UID)
		}
	}()
	for range 10000 {
		result := index.Resolve(refs, func(schema.GroupKind) bool { return true }, nil, nil, "test")
		if result.Outcome != OutcomeMissing {
			t.Errorf("partially published owner became a confirmed root: %+v", result)
			break
		}
	}
	<-done
}

func TestOwnerReferenceInputIsCopied(t *testing.T) {
	index := New()
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	index.UpsertObject("first", nil)
	index.UpsertObject("second", nil)
	obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "test", UID: "child", OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "first", UID: "first", Controller: ptr.To(true)}}}}
	index.UpsertTrackedObject(obj, gvk)
	obj.OwnerReferences[0].UID = "second"
	obj.OwnerReferences[0].Name = "second"
	refs := []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "child", UID: "child", Controller: ptr.To(true)}}
	result := index.Resolve(refs, func(schema.GroupKind) bool { return true }, nil, nil, "test")
	if result.Root.UID != "first" || len(index.Descendants("first")) != 1 || len(index.Descendants("second")) != 0 {
		t.Fatal("source slice mutation changed a committed owner edge")
	}
	index.UpsertTrackedObject(obj, gvk)
	result = index.Resolve(refs, func(schema.GroupKind) bool { return true }, nil, nil, "test")
	if result.Root.UID != "second" || len(index.Descendants("first")) != 0 || len(index.Descendants("second")) != 1 {
		t.Fatal("committed owner update did not change reverse edges")
	}
}
