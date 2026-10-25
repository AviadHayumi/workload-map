// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	inventory "github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

type testInventory map[schema.GroupKind]inventory.Selection

func (i testInventory) Lookup(gvk schema.GroupVersionKind) (inventory.Selection, error) {
	return i[gvk.GroupKind()], nil
}

type testReader struct {
	objects map[ObjectRef]*unstructured.Unstructured
	err     error
}

func (r *testReader) Get(_ context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	if r.err != nil {
		return nil, r.err
	}
	if o := r.objects[ref]; o != nil {
		return o.DeepCopy(), nil
	}
	return nil, ErrPending
}
func testDefinition(d *v1alpha1.Karta) testInventory {
	k := d.Spec.StructureDefinition.RootComponent.Kind
	gvk := schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind}
	return testInventory{gvk.GroupKind(): {Definition: d, GVK: gvk}}
}

var podKind = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
var jobKind = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

func testRef(kind schema.GroupVersionKind, name string) ObjectRef {
	return ObjectRef{GVK: kind, Namespace: "test", Name: name, UID: types.UID(name), ResourceVersion: "1"}
}
func testOwner(ref ObjectRef) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: ref.GVK.GroupVersion().String(), Kind: ref.GVK.Kind, Name: ref.Name, UID: ref.UID, Controller: ptr.To(true)}
}
func observe(d *Discovery, ref ObjectRef, parents ...ObjectRef) ObservedObject {
	o := ObservedObject{Ref: ref, WatchKey: CoverageKey{Resource: ref.GVK.GroupVersion().WithResource(ref.GVK.Kind), Namespace: ref.Namespace}, Run: 1}
	for _, p := range parents {
		o.Owners = append(o.Owners, testOwner(p))
	}
	if _, ok := d.coverage[o.WatchKey]; !ok {
		d.CoverageChanged(CoverageChange{Key: o.WatchKey, Run: 1, Ready: true, FullObject: true})
	}
	d.Upsert(o)
	return o
}
func resolveTest(t *testing.T, d *Discovery, ref ObjectRef) GenericWorkload {
	t.Helper()
	w, err := d.Resolve(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestResolveUsesTrueRoot(t *testing.T) {
	inv := testDefinition(kartas.Jobset())
	d := New(nil, inv)
	intermediate := testRef(inv[kartasKey(kartas.Jobset())].GVK, "jobset")
	root := testRef(schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Workflow"}, "workflow")
	pod := testRef(podKind, "pod")
	observe(d, root)
	observe(d, intermediate, root)
	observe(d, pod, intermediate)
	w := resolveTest(t, d, pod)
	if w.Root() != root || w.KartaWorkload() != nil || len(w.Path) != 3 {
		t.Fatalf("unexpected resolution: %+v", w)
	}
}
func kartasKey(d *v1alpha1.Karta) schema.GroupKind {
	k := d.Spec.StructureDefinition.RootComponent.Kind
	return schema.GroupKind{Group: k.Group, Kind: k.Kind}
}
func TestIgnoredOwnerIsExactGVK(t *testing.T) {
	ignored := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Wrapper"}
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			d := New(nil, testInventory{}, ignored)
			parent := testRef(ignored, "parent")
			parent.GVK.Version = version
			pod := testRef(podKind, "pod")
			observe(d, pod, parent)
			w, err := d.Resolve(t.Context(), pod)
			if version == "v1" {
				if err != nil || w.Root() != pod || w.Resolution != IgnoredOwner {
					t.Fatalf("%+v %v", w, err)
				}
			} else if !errors.Is(err, ErrPending) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
func TestLateParentAndMissingParent(t *testing.T) {
	r := &testReader{}
	d := New(r, testInventory{})
	root := testRef(jobKind, "job")
	pod := testRef(podKind, "pod")
	observe(d, pod, root)
	if _, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	r.err = apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, "job")
	if _, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrUnresolvedOwner) {
		t.Fatal(err)
	}
	observe(d, root)
	if resolveTest(t, d, pod).Root() != root {
		t.Fatal("late parent not resolved")
	}
}
func TestReplacementDeletionAndReparent(t *testing.T) {
	d := New(nil, testInventory{})
	a, b := testRef(jobKind, "a"), testRef(jobKind, "b")
	pod := testRef(podKind, "pod")
	observe(d, a)
	observe(d, b)
	o := observe(d, pod, a)
	old := resolveTest(t, d, pod)
	o.Owners = []metav1.OwnerReference{testOwner(b)}
	d.Upsert(o)
	if d.IsCurrent(old.State()) || resolveTest(t, d, pod).Root() != b {
		t.Fatal("reparent did not invalidate")
	}
	replacement := b
	replacement.UID = "replacement"
	observe(d, replacement)
	if _, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	d.Delete(b)
	if resolveTest(t, d, replacement).Root() != replacement {
		t.Fatal("old delete removed replacement")
	}
	d.Delete(replacement)
	if _, err := d.Resolve(t.Context(), replacement); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
}
func TestLongPathAndMalformedOwners(t *testing.T) {
	d := New(nil, testInventory{})
	root := testRef(jobKind, "root")
	observe(d, root)
	last := root
	for i := range 100 {
		ref := testRef(jobKind, fmt.Sprintf("child-%d", i))
		observe(d, ref, last)
		last = ref
	}
	if w := resolveTest(t, d, last); w.Root() != root || len(w.Path) != 101 {
		t.Fatal("path truncated")
	}
	observe(d, root, last)
	if _, err := d.Resolve(t.Context(), last); !errors.Is(err, ErrUnresolvedOwner) {
		t.Fatal(err)
	}
	observe(d, root)
	observe(d, last, root, testRef(jobKind, "other"))
	if _, err := d.Resolve(t.Context(), last); !errors.Is(err, ErrUnresolvedOwner) {
		t.Fatal(err)
	}
}
func TestWatchGenerationAndConflictingRepresentations(t *testing.T) {
	d := New(nil, testInventory{})
	root := testRef(jobKind, "root")
	o := observe(d, root)
	w := resolveTest(t, d, root)
	d.CoverageChanged(CoverageChange{Key: o.WatchKey, Run: 2, Ready: true, FullObject: true})
	o.Ref.ResourceVersion = "stale"
	if changes := d.Upsert(o); len(changes.Objects) != 0 {
		t.Fatal("accepted retired callback")
	}
	if d.IsCurrent(w.State()) {
		t.Fatal("retired token current")
	}
	o.Run = 2
	o.Ref = root
	d.Upsert(o)
	other := root
	other.GVK.Version = "v2"
	observe(d, other, testRef(jobKind, "parent"))
	if _, err := d.Resolve(t.Context(), root); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}
func TestWalkIncludesIntermediatePod(t *testing.T) {
	d := New(nil, testInventory{})
	root := testRef(schema.GroupVersionKind{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet"}, "lws")
	sts := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}
	path := []ObjectRef{root, testRef(sts, "leaders"), testRef(podKind, "leader"), testRef(sts, "workers"), testRef(podKind, "worker")}
	observe(d, root)
	for i := 1; i < len(path); i++ {
		observe(d, path[i], path[i-1])
	}
	w := resolveTest(t, d, path[4])
	visited := map[types.UID]bool{}
	state, err := w.Walk(t.Context(), func(ref ObjectRef, parent *ObjectRef) error {
		visited[ref.UID] = true
		if ref != root && parent == nil {
			t.Error("missing parent")
		}
		return nil
	})
	if err != nil || len(visited) != 5 {
		t.Fatalf("visited %v: %v", visited, err)
	}
	changes := d.Delete(path[2])
	if d.IsCurrent(state) {
		t.Fatal("deleted intermediate Pod did not invalidate walk")
	}
	found := false
	for _, ref := range changes.Objects {
		found = found || ref == path[4]
	}
	if !found {
		t.Fatal("worker not invalidated")
	}
}

func TestWalkCallbackCanMutateDiscovery(t *testing.T) {
	d := New(nil, testInventory{})
	root := testRef(jobKind, "root")
	observe(d, root)
	w := resolveTest(t, d, root)
	_, err := w.Walk(t.Context(), func(ObjectRef, *ObjectRef) error { observe(d, testRef(podKind, "new-child"), root); return nil })
	if !errors.Is(err, ErrStale) {
		t.Fatalf("got %v", err)
	}
}
