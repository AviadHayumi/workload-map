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

func TestCompatibilityUsesFirstController(t *testing.T) {
	index := New()
	first := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: "first", UID: "first", Controller: ptr.To(true)}
	second := first
	second.Name, second.UID = "second", "second"
	index.UpsertObject("first", nil)
	index.UpsertObject("second", nil)
	result := index.RootFor([]metav1.OwnerReference{first, second}, func(schema.GroupKind) bool { return true })
	if result.Outcome != OutcomeFound || result.Root.UID != first.UID || result.Resolution != "resolved" {
		t.Fatalf("exporter first-controller behavior changed: %+v", result)
	}
}

func TestCompatibilityDepthAndIgnoredBoundary(t *testing.T) {
	index := New()
	refs := make([]metav1.OwnerReference, 9)
	for n := range refs {
		name := fmt.Sprintf("owner-%d", n)
		refs[n] = metav1.OwnerReference{APIVersion: "example.test/v1", Kind: "Workload", Name: name, UID: types.UID(name), Controller: ptr.To(true)}
	}
	for n := 0; n < 7; n++ {
		index.UpsertObject(refs[n].UID, refs[n+1:n+2])
	}
	index.UpsertObject(refs[7].UID, nil)
	isRoot := func(schema.GroupKind) bool { return true }
	result := index.Resolve(refs[:1], isRoot, nil, nil, "team")
	if result.Outcome != OutcomeFound || result.Root.UID != refs[7].UID || result.Root.Namespace != "" {
		t.Fatalf("eight-hop terminal root changed: %+v", result)
	}
	refs[8].Kind = "Ignored"
	index.UpsertObject(refs[7].UID, refs[8:])
	result = index.Resolve(refs[:1], isRoot, nil, nil, "team")
	if result.Outcome != OutcomeDepthExceeded {
		t.Fatalf("ninth hop did not exceed legacy depth: %+v", result)
	}
	result = index.Resolve(refs[:1], isRoot, func(gvk schema.GroupVersionKind) bool { return gvk.Kind == "Ignored" }, nil, "team")
	if result.Outcome != OutcomeFound || result.Root.UID != refs[7].UID || result.Resolution != "ignored_owner" {
		t.Fatalf("ignored boundary must precede depth and lookup: %+v", result)
	}
}
