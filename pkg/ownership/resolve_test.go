// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestResolvePreservesIndexedOwnerPolicy(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Workload"}
	candidate := Ref{Namespace: "team", UID: "candidate", Name: "candidate", GVK: gvk}
	owner := traceOwner("owner")
	ownerRef := Ref{UID: owner.UID, Name: owner.Name, GVK: gvk}
	firstPassive := traceOwner("passive")
	firstPassive.Controller = nil
	other := traceOwner("other")
	isRoot := func(kind schema.GroupKind) bool { return kind == gvk.GroupKind() }
	for _, tt := range []struct {
		name       string
		owners     []metav1.OwnerReference
		candidate  *Ref
		namespaces []string
		setup      func(*Index)
		want       WalkResult
	}{
		{
			name:      "terminal candidate retains identity",
			candidate: &candidate,
			want:      WalkResult{Outcome: OutcomeFound, Root: candidate, Resolution: "resolved"},
		},
		{
			name:   "first controller wins",
			owners: []metav1.OwnerReference{firstPassive, owner, other},
			setup:  func(index *Index) { index.UpsertObject(owner.UID, nil) },
			want:   WalkResult{Outcome: OutcomeFound, Root: ownerRef, Resolution: "resolved"},
		},
		{
			name:      "outer root replaces candidate",
			owners:    []metav1.OwnerReference{owner},
			candidate: &candidate,
			setup:     func(index *Index) { index.UpsertObject(owner.UID, nil) },
			want:      WalkResult{Outcome: OutcomeFound, Root: ownerRef, Resolution: "resolved"},
		},
		{
			name:      "missing owner rejects candidate",
			owners:    []metav1.OwnerReference{owner},
			candidate: &candidate,
			want:      WalkResult{Outcome: OutcomeMissing, Missing: owner.UID, MissingRef: ownerRef},
		},
		{
			name:      "candidate namespace rejects cross namespace owner",
			owners:    []metav1.OwnerReference{owner},
			candidate: &candidate,
			setup: func(index *Index) {
				index.UpsertTrackedObject(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
					UID: owner.UID, Name: owner.Name, Namespace: "other",
				}}, gvk)
			},
			want: WalkResult{Outcome: OutcomeMissing, Missing: owner.UID, MissingRef: ownerRef},
		},
		{
			name:       "explicit namespace overrides candidate namespace",
			owners:     []metav1.OwnerReference{owner},
			candidate:  &candidate,
			namespaces: []string{"other"},
			setup: func(index *Index) {
				index.UpsertTrackedObject(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
					UID: owner.UID, Name: owner.Name, Namespace: "other",
				}}, gvk)
			},
			want: WalkResult{Outcome: OutcomeFound, Root: ownerRef, Resolution: "resolved"},
		},
		{
			name:   "served version alias is accepted",
			owners: []metav1.OwnerReference{owner},
			setup: func(index *Index) {
				alias := gvk
				alias.Version = "v2"
				index.UpsertTrackedObject(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
					UID: owner.UID, Name: owner.Name, Namespace: "team",
				}}, alias)
			},
			want: WalkResult{Outcome: OutcomeFound, Root: ownerRef, Resolution: "resolved"},
		},
		{
			name:   "name mismatch is missing",
			owners: []metav1.OwnerReference{owner},
			setup: func(index *Index) {
				index.UpsertTrackedObject(&metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
					UID: owner.UID, Name: "different", Namespace: "team",
				}}, gvk)
			},
			want: WalkResult{Outcome: OutcomeMissing, Missing: owner.UID, MissingRef: ownerRef},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			index := New()
			if tt.setup != nil {
				tt.setup(index)
			}
			got := index.Resolve(tt.owners, isRoot, nil, tt.candidate, tt.namespaces...)
			if got != tt.want {
				t.Fatalf("Resolve() = %+v; want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveDepthBoundaryChecksIgnoredOwnerBeforeLookup(t *testing.T) {
	for _, tt := range []struct {
		name       string
		ownerCount int
		ignoreLast bool
		want       WalkOutcome
	}{
		{name: "eight owners", ownerCount: 8, want: OutcomeFound},
		{name: "ninth owner exceeds limit", ownerCount: 9, want: OutcomeDepthExceeded},
		{name: "ignored ninth owner stops before limit", ownerCount: 9, ignoreLast: true, want: OutcomeFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			index := New()
			var owners []metav1.OwnerReference
			for n := tt.ownerCount; n > 0; n-- {
				uid := types.UID(fmt.Sprintf("owner-%d", n))
				if !tt.ignoreLast || n != tt.ownerCount {
					index.UpsertObject(uid, owners)
				}
				owners = []metav1.OwnerReference{{
					APIVersion: "example.test/v1", Kind: fmt.Sprintf("Owner%d", n),
					Name: string(uid), UID: uid, Controller: ptr.To(true),
				}}
			}
			ignored := func(gvk schema.GroupVersionKind) bool {
				return tt.ignoreLast && gvk.Kind == fmt.Sprintf("Owner%d", tt.ownerCount)
			}
			got := index.Resolve(owners, func(schema.GroupKind) bool { return true }, ignored, nil)
			if got.Outcome != tt.want {
				t.Fatalf("Resolve() = %+v; want %s", got, tt.want)
			}
			if got.Outcome == OutcomeFound {
				if got.Root.UID != "owner-8" || (got.Resolution == "ignored_owner") != tt.ignoreLast {
					t.Fatalf("unexpected last confirmed root: %+v", got)
				}
			} else if got != (WalkResult{Outcome: OutcomeDepthExceeded}) {
				t.Fatalf("exhausted walk returned a partial root: %+v", got)
			}
		})
	}
}

func TestResolveCopiesIdentityFromFreshOwnerInputs(t *testing.T) {
	index := New()
	index.UpsertObject("owner", nil)
	isRoot := func(schema.GroupKind) bool { return true }
	var previous WalkResult
	for n := range 100 {
		refs := []metav1.OwnerReference{traceOwner("owner")}
		got := index.Resolve(refs, isRoot, nil, nil)
		if got.Outcome != OutcomeFound || got.Root.UID != "owner" {
			t.Fatalf("fresh input resolution %d: %+v", n, got)
		}
		refs[0].Name = "changed"
		refs[0].UID = "changed"
		if got.Root.Name != "owner" || (n > 0 && got != previous) {
			t.Fatalf("caller mutation changed a completed resolution: %+v", got)
		}
		previous = got
	}
}

func TestResolveReselectsStoredControllerAfterCallerMutation(t *testing.T) {
	index := New()
	inner := traceOwner("inner")
	outer := traceOwner("outer")
	index.UpsertObject(inner.UID, []metav1.OwnerReference{outer})
	index.UpsertObject(outer.UID, nil)

	for _, tt := range []struct {
		name       string
		controller bool
		wantUID    types.UID
	}{
		{name: "original controller", controller: true, wantUID: outer.UID},
		{name: "caller disables controller", controller: false, wantUID: inner.UID},
		{name: "caller restores controller", controller: true, wantUID: outer.UID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			*outer.Controller = tt.controller
			got := index.Resolve([]metav1.OwnerReference{inner}, func(schema.GroupKind) bool { return true }, nil, nil)
			if got.Outcome != OutcomeFound || got.Resolution != "resolved" || got.Root.UID != tt.wantUID {
				t.Fatalf("Resolve() = %+v; want resolved root %s", got, tt.wantUID)
			}
		})
	}
}
