// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
)

func TestInformerReaderMissingOwnerBeforeInitialSync(t *testing.T) {
	reader := NewInformerReader()
	job := runtimeJob("missing", "job")
	ref := objectReference(job, job.GroupVersionKind())
	key := discovery.CoverageKey{Resource: invariantJobs, Namespace: "team"}
	stream := &resourceStream{
		key: key, gvk: ref.GVK, run: 1,
		informer: cache.NewSharedIndexInformer(&cache.ListWatch{}, &unstructured.Unstructured{}, 0, cache.Indexers{}),
	}
	reader.streams[key] = stream
	if _, err := reader.Get(t.Context(), ref); !errors.Is(err, discovery.ErrPending) {
		t.Fatalf("empty initial cache returned %v, want pending", err)
	}
	if err := stream.informer.GetStore().Add(job); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Get(t.Context(), ref); !errors.Is(err, discovery.ErrPending) {
		t.Fatalf("unaccepted object before sync returned %v, want pending", err)
	}
	stream.ready = true
	if _, err := reader.Get(t.Context(), ref); !errors.Is(err, discovery.ErrStale) {
		t.Fatalf("cached object without accepted callback returned %v, want stale", err)
	}
	if err := stream.informer.GetStore().Delete(job); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Get(t.Context(), ref); !errors.Is(err, discovery.ErrUnresolvedOwner) {
		t.Fatalf("absent owner after sync returned %v, want unresolved owner", err)
	}
}

func TestRuntimeEarlyOwnerResolutionKeepsPublicationGated(t *testing.T) {
	root := runtimeObject(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"}, "work", "root")
	root.SetAnnotations(map[string]string{"submitter": "alice"})
	job := runtimeJob("worker-job", "job")
	job.SetOwnerReferences(runtimePod("unused", "unused", root).GetOwnerReferences())
	pod := runtimePod("worker", "pod", job)
	fixture := startConnected(t, []*unstructured.Unstructured{root, job, pod}, func(options *RuntimeOptions) {
		options.Definitions.Catalog.Enabled = false
	}, `"submitter" in annotations ? annotations["submitter"] : ""`)
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, root.GetUID())
		if record == nil {
			return false
		}
		row, exists := record.Pod(pod.GetUID())
		mapping, _ := view.Valid(row.Mapping)
		membership, _ := view.Valid(record.Membership)
		return exists && mapping && membership && record.SubmittedBy == "alice"
	})
	fixture.controller.queue.ShutDownWithDrain()
	c := fixture.controller
	stream := invariantStream(t, fixture, invariantJobs)
	podRef := objectReference(pod, pod.GroupVersionKind())
	jobRef := objectReference(job, job.GroupVersionKind())
	rootRef := objectReference(root, root.GroupVersionKind())

	// Freeze dispatch and recreate the boundary where this run has delivered
	// the needed Job callback but has not finished its initial callbacks.
	c.mu.Lock()
	stream.ready = false
	c.discovery.CoverageChanged(discovery.CoverageChange{Key: stream.key, Run: stream.run, FullObject: stream.full})
	delete(c.proofs, refKey(jobRef))
	c.invalidateMembershipLocked("team")
	c.removeRootLocked(root.GetUID(), "team")
	c.mu.Unlock()

	workload, err := c.discovery.Resolve(t.Context(), podRef)
	if err != nil || workload.Root() != rootRef || !slices.Equal(workload.Path, []discovery.ObjectRef{podRef, jobRef, rootRef}) {
		t.Fatalf("accepted owner path before sync: workload=%+v err=%v", workload, err)
	}
	if _, err := c.reader.Get(t.Context(), jobRef); !errors.Is(err, discovery.ErrPending) {
		t.Fatalf("unready body read returned %v, want pending", err)
	}
	if err := c.reconcileObject(t.Context(), queuedWork{GVK: podRef.GVK, Namespace: podRef.Namespace, Name: podRef.Name, UID: podRef.UID}); err != nil {
		t.Fatalf("early association: %v", err)
	}
	c.mu.Lock()
	associated := c.associations[podRef.UID]
	c.mu.Unlock()
	if associated != rootRef.UID {
		t.Fatalf("early association = %s, want %s", associated, rootRef.UID)
	}
	if err := c.verifyStream(t.Context(), queuedWork{GVK: jobRef.GVK, Namespace: "team"}); !errors.Is(err, discovery.ErrPending) {
		t.Fatalf("unready verification returned %v, want pending", err)
	}
	if err := c.verifyStream(t.Context(), queuedWork{GVK: podRef.GVK, Namespace: "team"}); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyMembership(t.Context(), "team"); !errors.Is(err, discovery.ErrPending) {
		t.Fatalf("membership with unverified owner returned %v, want pending", err)
	}
	if err := c.reconcileRoot(t.Context(), rootRef.UID); !errors.Is(err, discovery.ErrPending) {
		t.Fatalf("unverified mapping reconciliation returned %v, want pending", err)
	}
	view, record := invariantPublication(fixture, rootRef.UID)
	if record == nil {
		t.Fatal("independent root publication disappeared")
	}
	if _, exists := record.Pod(podRef.UID); exists {
		t.Fatal("early resolution published an unverified Pod mapping")
	}
	if valid, _ := view.Valid(record.Membership); valid || record.SubmittedBy != "" {
		t.Fatal("early resolution published complete membership or submitter identity")
	}

	c.mu.Lock()
	stream.ready = true
	c.discovery.CoverageChanged(discovery.CoverageChange{Key: stream.key, Run: stream.run, FullObject: stream.full, Ready: true})
	c.mu.Unlock()
	if c.discovery.IsCurrent(workload.State()) {
		t.Fatal("readiness transition retained the early resolution token")
	}
	if err := c.verifyStream(t.Context(), queuedWork{GVK: jobRef.GVK, Namespace: "team"}); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyMembership(t.Context(), "team"); err != nil {
		t.Fatal(err)
	}
	if err := c.reconcileRoot(t.Context(), rootRef.UID); err != nil {
		t.Fatal(err)
	}
	view, record = invariantPublication(fixture, rootRef.UID)
	if record == nil {
		t.Fatal("missing publication after verification")
	}
	row, exists := record.Pod(podRef.UID)
	mapping, _ := view.Valid(row.Mapping)
	membership, _ := view.Valid(record.Membership)
	if !exists || !mapping || !membership || record.SubmittedBy != "alice" {
		t.Fatalf("verification did not restore mapping and identity: row=%v mapping=%v membership=%v submitter=%q", exists, mapping, membership, record.SubmittedBy)
	}
}
