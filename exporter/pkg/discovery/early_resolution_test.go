// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"errors"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

func TestResolveAcceptedOwnerBeforeInitialSync(t *testing.T) {
	d, original, pod, _ := evaluationFixture(t, kartas.Jobset(), jobsetBody(), map[string]string{
		"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "2",
	}, nil)
	root := original.root
	job := testRef(jobKind, "job")
	event := ObservedObject{Ref: job, Owners: []metav1.OwnerReference{testOwner(root)},
		WatchKey: CoverageKey{Resource: jobKind.GroupVersion().WithResource("Job"), Namespace: job.Namespace}, Run: 1}
	d.CoverageChanged(CoverageChange{Key: event.WatchKey, Run: event.Run})
	d.Upsert(event)
	podRef := testRef(podKind, "pod")
	observe(d, podRef, job)

	workload, err := d.Resolve(t.Context(), podRef)
	if err != nil || workload.Root() != root || !slices.Equal(workload.Path, []ObjectRef{podRef, job, root}) || !d.IsCurrent(workload.State()) {
		t.Fatalf("accepted owner before sync: workload=%+v err=%v", workload, err)
	}
	if _, err := workload.Walk(t.Context(), func(ObjectRef, *ObjectRef) error { return nil }); !errors.Is(err, ErrPending) {
		t.Fatalf("unsynchronized subtree walk: %v", err)
	}
	if _, err := workload.KartaWorkload().PodMapping(t.Context(), pod, d.NewEvaluationBatch()); !errors.Is(err, ErrPending) {
		t.Fatalf("mapping through unsynchronized owner: %v", err)
	}
	if status, err := workload.KartaWorkload().Status(t.Context(), d.NewEvaluationBatch()); err != nil || !status.Defined {
		t.Fatalf("synchronized root status: %+v err=%v", status, err)
	}

	d.CoverageChanged(CoverageChange{Key: event.WatchKey, Run: event.Run, Ready: true})
	if d.IsCurrent(workload.State()) {
		t.Fatal("sync transition retained the earlier path token")
	}
	workload = resolveTest(t, d, podRef)
	if _, err := workload.KartaWorkload().PodMapping(t.Context(), pod, d.NewEvaluationBatch()); err != nil {
		t.Fatalf("synchronized mapping: %v", err)
	}
	if _, err := workload.Walk(t.Context(), func(ObjectRef, *ObjectRef) error { return nil }); err != nil {
		t.Fatalf("synchronized subtree walk: %v", err)
	}
}

func TestResolveAcceptedRootKeepsEvaluationGated(t *testing.T) {
	d, original, pod, _ := evaluationFixture(t, kartas.Jobset(), jobsetBody(), map[string]string{
		"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "0",
	}, nil)
	root := original.root
	event := d.ownership.nodes[root.UID].objects[root.GVK]
	d.CoverageChanged(CoverageChange{Key: event.WatchKey, Run: event.Run, FullObject: true})
	workload, err := d.Resolve(t.Context(), root)
	if err != nil || workload.Root() != root || !d.IsCurrent(workload.State()) {
		t.Fatalf("accepted root before sync: workload=%+v err=%v", workload, err)
	}
	if _, err := workload.KartaWorkload().Status(t.Context(), d.NewEvaluationBatch()); !errors.Is(err, ErrPending) {
		t.Fatalf("unsynchronized root status: %v", err)
	}
	if _, err := workload.KartaWorkload().PodMapping(t.Context(), pod, d.NewEvaluationBatch()); !errors.Is(err, ErrPending) {
		t.Fatalf("unsynchronized root mapping: %v", err)
	}
	if _, err := workload.Walk(t.Context(), func(ObjectRef, *ObjectRef) error { return nil }); !errors.Is(err, ErrPending) {
		t.Fatalf("unsynchronized root subtree walk: %v", err)
	}
}

func TestResolveAcceptedOwnerWatchLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name          string
		change        CoverageChange
		acceptCurrent bool
		want          error
	}{
		{name: "current initial run", change: CoverageChange{Run: 1}, acceptCurrent: true},
		{name: "old run", change: CoverageChange{Run: 2}, want: ErrPending},
		{name: "current replacement run", change: CoverageChange{Run: 2}, acceptCurrent: true},
		{name: "removed", change: CoverageChange{Run: 1, Removed: true}, want: ErrPending},
		{name: "permission failure", change: CoverageChange{Run: 1, Err: apierrors.NewForbidden(schema.GroupResource{Resource: "jobs"}, "job", errors.New("denied"))}, want: ErrPermissionDenied},
		{name: "API failure", change: CoverageChange{Run: 1, Err: apierrors.NewServiceUnavailable("unavailable")}, want: ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := New(nil, testInventory{})
			root := testRef(schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Workload"}, "root")
			job, pod := testRef(jobKind, "job"), testRef(podKind, "pod")
			observe(d, root)
			event := observe(d, job, root)
			observe(d, pod, job)
			previous := resolveTest(t, d, pod)
			tc.change.Key = event.WatchKey
			d.CoverageChanged(tc.change)
			if d.IsCurrent(previous.State()) {
				t.Fatal("watch transition retained the old path token")
			}
			if tc.acceptCurrent {
				event.Run = tc.change.Run
				d.Upsert(event)
			}
			retired := event
			retired.Run = event.Run - 1
			retired.Owners = nil
			d.Upsert(retired)
			workload, err := d.Resolve(t.Context(), pod)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || workload.Root() != (ObjectRef{}) {
					t.Fatalf("invalid coverage returned workload=%+v err=%v; want %v", workload, err, tc.want)
				}
			} else if err != nil || workload.Root() != root || !d.IsCurrent(workload.State()) {
				t.Fatalf("current accepted owner: workload=%+v err=%v", workload, err)
			}
		})
	}
}

func TestResolveAcceptedOwnerKeepsAliasConflict(t *testing.T) {
	d := New(nil, testInventory{})
	first, second := testRef(jobKind, "first"), testRef(jobKind, "second")
	job, pod := testRef(jobKind, "job"), testRef(podKind, "pod")
	observe(d, first)
	observe(d, second)
	observe(d, job, first)
	observe(d, pod, job)
	alias := job
	alias.GVK.Version = "v2"
	event := ObservedObject{Ref: alias, Owners: []metav1.OwnerReference{testOwner(second)},
		WatchKey: CoverageKey{Resource: alias.GVK.GroupVersion().WithResource("Job"), Namespace: alias.Namespace}, Run: 1}
	d.CoverageChanged(CoverageChange{Key: event.WatchKey, Run: 1})
	d.Upsert(event)
	if workload, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrStale) || workload.Root() != (ObjectRef{}) {
		t.Fatalf("conflicting accepted alias returned workload=%+v err=%v", workload, err)
	}
}

func TestResolveMissingOwnerRemainsPendingUntilSync(t *testing.T) {
	reader := &testReader{err: ErrPending}
	d := New(reader, testInventory{})
	job, pod := testRef(jobKind, "job"), testRef(podKind, "pod")
	key := CoverageKey{Resource: jobKind.GroupVersion().WithResource("Job"), Namespace: job.Namespace}
	d.CoverageChanged(CoverageChange{Key: key, Run: 1})
	observe(d, pod, job)
	if workload, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrPending) || workload.Root() != (ObjectRef{}) {
		t.Fatalf("unseen owner before sync: workload=%+v err=%v", workload, err)
	}
	d.CoverageChanged(CoverageChange{Key: key, Run: 1, Ready: true})
	reader.err = apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, job.Name)
	if workload, err := d.Resolve(t.Context(), pod); !errors.Is(err, ErrUnresolvedOwner) || workload.Root() != (ObjectRef{}) {
		t.Fatalf("confirmed absent owner: workload=%+v err=%v", workload, err)
	}
}
