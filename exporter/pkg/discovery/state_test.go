// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"errors"
	"slices"
	"testing"
)

func TestCapturedStateTracksAcceptedInputs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*Discovery, ObservedObject, ObservedObject)
		path, walk bool
	}{
		{"unchanged", func(*Discovery, ObservedObject, ObservedObject) {}, true, true},
		{"unrelated object", func(d *Discovery, _, _ ObservedObject) {
			observe(d, testRef(jobKind, "other"))
		}, true, true},
		{"same observation", func(d *Discovery, root, _ ObservedObject) {
			d.Upsert(root)
		}, true, true},
		{"retired run", func(d *Discovery, _, pod ObservedObject) {
			pod.Run++
			pod.Ref.ResourceVersion = "2"
			d.Upsert(pod)
		}, true, true},
		{"resource version", func(d *Discovery, _, pod ObservedObject) {
			pod.Ref.ResourceVersion = "2"
			d.Upsert(pod)
		}, false, false},
		{"owner edge", func(d *Discovery, _, pod ObservedObject) {
			pod.Owners = nil
			d.Upsert(pod)
		}, false, false},
		{"root replacement", func(d *Discovery, root, _ ObservedObject) {
			root.Ref.UID = "replacement"
			d.Upsert(root)
		}, false, false},
		{"root deletion", func(d *Discovery, root, _ ObservedObject) {
			d.Delete(root.Ref)
		}, false, false},
		{"unrelated definition", func(d *Discovery, _, pod ObservedObject) {
			d.DefinitionChanged(pod.Ref.GVK.GroupKind())
		}, true, true},
		{"root definition", func(d *Discovery, root, _ ObservedObject) {
			d.DefinitionChanged(root.Ref.GVK.GroupKind())
		}, false, false},
		{"coverage", func(d *Discovery, root, _ ObservedObject) {
			d.CoverageChanged(CoverageChange{Key: root.WatchKey, Run: root.Run, Ready: false})
		}, false, false},
		{"membership", func(d *Discovery, root, _ ObservedObject) {
			observe(d, testRef(podKind, "sibling"), root.Ref)
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := New(nil, testInventory{})
			root := observe(d, testRef(jobKind, "root"))
			pod := observe(d, testRef(podKind, "pod"), root.Ref)
			workload := resolveTest(t, d, pod.Ref)
			walk, err := workload.Walk(t.Context(), func(ObjectRef, *ObjectRef) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			tc.change(d, root, pod)
			if got := d.IsCurrent(workload.State()); got != tc.path {
				t.Fatalf("path current = %v, want %v", got, tc.path)
			}
			if got := d.IsCurrent(walk); got != tc.walk {
				t.Fatalf("walk current = %v, want %v", got, tc.walk)
			}
			if d.IsCurrent(CapturedState{}) || New(nil, testInventory{}).IsCurrent(workload.State()) {
				t.Fatal("accepted a zero or foreign state")
			}
		})
	}
}

func TestResolutionTracksParentLifetime(t *testing.T) {
	d := New(nil, testInventory{})
	root, parent, pod := testRef(jobKind, "root"), testRef(jobKind, "parent"), testRef(podKind, "pod")
	observe(d, root)
	event := observe(d, parent, root)
	observe(d, pod, parent)
	original := resolveTest(t, d, pod)
	resolveTest(t, d, pod)
	event.Owners = nil
	d.Upsert(event)
	if d.IsCurrent(original.State()) {
		t.Fatal("parent change retained the old state")
	}
	reparented := resolveTest(t, d, pod)
	if reparented.Root() != parent {
		t.Fatalf("reparented root = %+v", reparented.Root())
	}
	d.Delete(parent)
	if d.IsCurrent(reparented.State()) {
		t.Fatal("parent deletion retained the old state")
	}
	if _, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrPending) {
		t.Fatalf("deleted parent resolution = %v, want pending", err)
	}
	replacement := parent
	replacement.UID = "replacement"
	observe(d, replacement, root)
	event = observe(d, pod, replacement)
	if workload := resolveTest(t, d, event.Ref); workload.Root() != root {
		t.Fatalf("new lifetime root = %+v", workload.Root())
	}
}

func TestUnchangedObservationReportsDescendants(t *testing.T) {
	d := New(nil, testInventory{})
	root := observe(d, testRef(jobKind, "root"))
	pod := observe(d, testRef(podKind, "pod"), root.Ref)
	changes := d.Upsert(root)
	if !slices.Contains(changes.Objects, pod.Ref) {
		t.Fatal("unchanged root did not report its descendant")
	}
}
