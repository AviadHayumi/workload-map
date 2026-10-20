// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"errors"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestOwnershipNamespaceScope(t *testing.T) {
	for _, tc := range []struct {
		name, childNamespace, parentNamespace, rootNamespace string
		valid                                                bool
	}{
		{"namespaced to cluster", "team", "", "", true},
		{"same namespace", "team", "team", "team", true},
		{"cross namespace", "team", "other", "other", false},
		{"cluster to namespaced", "", "team", "team", false},
		{"cluster intermediate to namespaced", "team", "", "team", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := New(nil, testInventory{})
			root := testRef(schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Root"}, "root")
			parent := testRef(jobKind, "parent")
			child := testRef(podKind, "child")
			root.Namespace, parent.Namespace, child.Namespace = tc.rootNamespace, tc.parentNamespace, tc.childNamespace
			observe(d, root)
			observe(d, parent, root)
			observe(d, child, parent)
			workload, err := d.Resolve(t.Context(), child)
			if tc.valid {
				if err != nil || workload.Root() != root || len(workload.Path) != 3 || !d.IsCurrent(workload.State()) {
					t.Fatalf("valid ownership path = %+v, err = %v", workload, err)
				}
			} else if !errors.Is(err, ErrUnresolvedOwner) {
				t.Fatalf("invalid namespace edge resolved as %+v, err = %v", workload, err)
			}
		})
	}
}

func TestOwnershipCopiesAcceptedOwnerReferences(t *testing.T) {
	d := New(nil, testInventory{})
	first, second, child := testRef(jobKind, "first"), testRef(jobKind, "second"), testRef(podKind, "child")
	observe(d, first)
	observe(d, second)
	input := observe(d, child, first)
	original := resolveTest(t, d, child)
	input.Owners[0].Name = second.Name
	input.Owners[0].UID = second.UID
	*input.Owners[0].Controller = false
	if got := resolveTest(t, d, child); got.Root() != first || !d.IsCurrent(original.State()) {
		t.Fatalf("caller mutation changed accepted ownership: %+v", got)
	}
	*input.Owners[0].Controller = true
	d.Upsert(input)
	if got := resolveTest(t, d, child); got.Root() != second || d.IsCurrent(original.State()) {
		t.Fatalf("accepted reparenting did not replace ownership: %+v", got)
	}
	for _, root := range []ObjectRef{first, second} {
		seenChild := false
		_, err := resolveTest(t, d, root).Walk(t.Context(), func(ref ObjectRef, _ *ObjectRef) error {
			seenChild = seenChild || ref.UID == child.UID
			return nil
		})
		if err != nil || seenChild != (root == second) {
			t.Fatalf("root %s child membership = %v, err = %v", root.Name, seenChild, err)
		}
	}
}

func TestOwnershipConcurrentPublicationNeverInventsRoot(t *testing.T) {
	d := New(nil, testInventory{})
	child, parent, missing := testRef(podKind, "child"), testRef(jobKind, "parent"), testRef(jobKind, "missing")
	observe(d, child, parent)
	input := ObservedObject{Ref: parent, WatchKey: CoverageKey{Resource: jobKind.GroupVersion().WithResource(jobKind.Kind), Namespace: parent.Namespace}, Run: 1,
		Owners: []metav1.OwnerReference{testOwner(missing)}}
	d.CoverageChanged(CoverageChange{Key: input.WatchKey, Run: 1, Ready: true, FullObject: true})
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		for range 2000 {
			d.Upsert(input)
			d.Delete(parent)
		}
	})
	close(start)
	for range 4000 {
		workload, err := d.Resolve(t.Context(), child)
		if !errors.Is(err, ErrPending) {
			t.Errorf("incomplete owner path became a root: %+v, err = %v", workload, err)
			break
		}
	}
	wg.Wait()
}
