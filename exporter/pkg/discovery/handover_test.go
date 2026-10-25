// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"errors"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	inventory "github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

func TestReplacementCoverageRetiresAbsentObjects(t *testing.T) {
	reader := &testReader{err: apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, "root")}
	d := New(reader, testInventory{})
	root, pod := testRef(jobKind, "root"), testRef(podKind, "pod")
	rootEvent := observe(d, root)
	observe(d, pod, root)
	workload := resolveTest(t, d, pod)
	d.CoverageChanged(CoverageChange{Key: rootEvent.WatchKey, Run: 2})
	if _, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrPending) {
		t.Fatalf("unsynchronized replacement: %v", err)
	}
	changes := d.CoverageChanged(CoverageChange{Key: rootEvent.WatchKey, Run: 2, Ready: true, FullObject: true})
	if !slices.Contains(changes.Roots, root) || !slices.Contains(changes.Objects, pod) {
		t.Fatalf("retired root and surviving Pod missing from change set: %+v", changes)
	}
	if _, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrUnresolvedOwner) {
		t.Fatalf("synchronized absent owner: %v", err)
	}
	if d.IsCurrent(workload.State()) || d.ownership.nodes[pod.UID] == nil {
		t.Fatal("handover retained evidence or removed surviving child")
	}
}

func TestReplacementFailureIsNotPending(t *testing.T) {
	d := New(nil, testInventory{})
	root := testRef(jobKind, "root")
	event := observe(d, root)
	d.CoverageChanged(CoverageChange{
		Key: event.WatchKey, Run: 2,
		Err: apierrors.NewForbidden(schema.GroupResource{Resource: "jobs"}, root.Name, errors.New("denied")),
	})
	if _, err := d.Resolve(t.Context(), root); !errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrPending) {
		t.Fatalf("replacement permission failure: %v", err)
	}
}

func TestRetiredAliasDoesNotControlParentOrWalk(t *testing.T) {
	d := New(nil, testInventory{})
	root := testRef(jobKind, "root")
	first := observe(d, root)
	alias := root
	alias.GVK.Version = "v2"
	observe(d, alias)
	pod := testRef(podKind, "pod")
	observe(d, pod, alias)
	d.CoverageChanged(CoverageChange{Key: first.WatchKey, Run: 1, Removed: true})
	workload := resolveTest(t, d, pod)
	var visited []ObjectRef
	if _, err := workload.Walk(t.Context(), func(ref ObjectRef, _ *ObjectRef) error {
		visited = append(visited, ref)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(visited) != 2 || visited[0] != alias || visited[1] != pod {
		t.Fatalf("Walk() = %+v", visited)
	}
	grandparent := testRef(alias.GVK, "grandparent")
	observe(d, grandparent)
	observe(d, alias, grandparent)
	if resolved := resolveTest(t, d, pod); resolved.Root() != grandparent {
		t.Fatalf("retired alias affected parent: %+v", resolved)
	}
}

type failingInventory struct{ err error }

func (i failingInventory) Lookup(schema.GroupVersionKind) (inventory.Selection, error) {
	return inventory.Selection{}, i.err
}

func TestSelectionFailurePreservesVerifiedRoot(t *testing.T) {
	for _, failure := range []error{ErrPending, ErrUnavailable, ErrPermissionDenied, ErrInvalidDefinition} {
		t.Run(failure.Error(), func(t *testing.T) {
			d := New(nil, failingInventory{err: failure})
			root, pod := testRef(jobKind, "root"), testRef(podKind, "pod")
			observe(d, root)
			observe(d, pod, root)
			workload, err := d.Resolve(t.Context(), pod)
			if !errors.Is(err, failure) || workload.Root() != root || !d.IsCurrent(workload.State()) || workload.KartaWorkload() != nil {
				t.Fatalf("selection failure lost ownership: workload=%+v err=%v", workload, err)
			}
			d.Delete(root)
			workload, err = d.Resolve(t.Context(), pod)
			if !errors.Is(err, ErrPending) || workload.Root() != (ObjectRef{}) || workload.Path != nil {
				t.Fatalf("ownership failure returned partial root: workload=%+v err=%v", workload, err)
			}
		})
	}
}

func TestNameReplacementReportsRetiredRoot(t *testing.T) {
	d := New(nil, testInventory{})
	root := testRef(jobKind, "root")
	event := observe(d, root)
	replacement := event
	replacement.Ref.UID = "replacement"
	changes := d.Upsert(replacement)
	if !slices.Contains(changes.Roots, root) || !slices.Contains(changes.Roots, replacement.Ref) {
		t.Fatalf("replacement did not report both lifetimes: %+v", changes)
	}
}

func TestResolvedPathIsCallerOwned(t *testing.T) {
	d := New(nil, testInventory{})
	root, pod := testRef(jobKind, "root"), testRef(podKind, "pod")
	observe(d, root)
	observe(d, pod, root)
	workload := resolveTest(t, d, pod)
	workload.Path[0].Name = "mutated"
	next := resolveTest(t, d, pod)
	if next.Path[0] != pod || !d.IsCurrent(workload.State()) {
		t.Fatalf("caller mutated accepted path: %+v", next.Path)
	}
}

func TestGenericLWSDoesNotStopAtStatefulSetDefinition(t *testing.T) {
	d := New(nil, testDefinition(kartas.StatefulSet()))
	root := testRef(schema.GroupVersionKind{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet"}, "lws")
	parent := testRef(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, "sts")
	pod := testRef(podKind, "pod")
	observe(d, root)
	observe(d, parent, root)
	observe(d, pod, parent)
	workload := resolveTest(t, d, pod)
	if workload.Root() != root || workload.KartaWorkload() != nil {
		t.Fatalf("intermediate definition changed root: %+v", workload)
	}
}
