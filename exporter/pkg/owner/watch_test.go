// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package owner

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
)

func TestOwnerKindReferencesFollowEdgeLifecycle(t *testing.T) {
	index := New()
	first := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Root"}
	second := first
	second.Version = "v2"
	ref := metav1.OwnerReference{APIVersion: first.GroupVersion().String(), Kind: first.Kind, Name: "root", UID: "root", Controller: ptr.To(true)}
	index.UpsertObject("a", []metav1.OwnerReference{ref})
	index.UpsertObject("a", []metav1.OwnerReference{ref})
	index.UpsertObject("b", []metav1.OwnerReference{ref})
	index.DeleteObject("a")
	if !index.HasOwnerKind(first) || index.HasOwnerKind(second) {
		t.Fatal("one descendant must retain only its exact owner API")
	}
	ref.APIVersion = second.GroupVersion().String()
	index.UpsertObject("b", []metav1.OwnerReference{ref})
	if index.HasOwnerKind(first) || !index.HasOwnerKind(second) {
		t.Fatal("changed owner API retained the obsolete watch")
	}
	index.UpsertObject("b", nil)
	index.DeleteObject("b")
	index.DeleteObject("b")
	if len(index.ownerKinds) != 0 {
		t.Fatalf("deleted edges retained owner kinds: %v", index.ownerKinds)
	}
}

func TestForgetKindRetiresOutgoingEdgesAndPreservesVersionAliases(t *testing.T) {
	index := New()
	first := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Middle"}
	second := first
	second.Version = "v2"
	parent := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Parent"}
	obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "middle", Namespace: "test", UID: "middle", OwnerReferences: []metav1.OwnerReference{{APIVersion: parent.GroupVersion().String(), Kind: parent.Kind, Name: "parent", UID: "parent", Controller: ptr.To(true)}}}}
	index.UpsertTrackedObject(obj, first)
	index.UpsertTrackedObject(obj, second)
	index.ForgetKind(first)
	if !index.HasOwnerKind(parent) || len(index.nodes) != 1 {
		t.Fatal("stopping an old alias removed the current observer's node")
	}
	index.ForgetKind(second)
	if index.HasOwnerKind(parent) || len(index.nodes) != 0 {
		t.Fatal("stopped watcher retained nodes or outgoing dependencies")
	}
}
