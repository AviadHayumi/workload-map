// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

func TestRuntimeStaleMembershipScanPreservesAcceptedAgreement(t *testing.T) {
	job := runtimeJob("work", "root")
	job.SetAnnotations(map[string]string{"submitter": "alice"})
	pod := runtimePod("worker", "pod", job)
	fixture := startConnected(t, []*unstructured.Unstructured{job, pod}, nil,
		`"submitter" in annotations ? annotations["submitter"] : ""`)
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		valid, _ := view.Valid(record.Membership)
		return valid && record.SubmittedBy == "alice"
	})
	fixture.controller.queue.ShutDownWithDrain()
	before, record := invariantPublication(fixture, "root")
	fixture.controller.mu.Lock()
	revision := fixture.controller.membershipRevision["team"]
	fixture.controller.mu.Unlock()

	// Only the Pod body version changes. The already verified ownership and
	// submitter inputs remain the same, but an older LIST now has a stale ref.
	pod.SetResourceVersion("2")
	if _, err := fixture.client.Resource(invariantPods).Namespace("team").Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		fixture.controller.mu.Lock()
		defer fixture.controller.mu.Unlock()
		return fixture.controller.reader.objects[refKey(objectReference(pod, runtimePodGVK))].observation.Ref.ResourceVersion == "2"
	})
	if err := fixture.controller.verifyMembership(t.Context(), "team"); !errors.Is(err, discovery.ErrStale) {
		t.Fatalf("obsolete membership scan returned %v, want ErrStale", err)
	}
	fixture.controller.mu.Lock()
	afterRevision := fixture.controller.membershipRevision["team"]
	fixture.controller.mu.Unlock()
	after := fixture.store.Capture()
	if valid, _ := before.Valid(record.Membership); !valid {
		t.Fatal("initial membership was not verified")
	}
	if valid, reason := after.Valid(record.Membership); !valid || afterRevision != revision {
		t.Fatalf("stale scan revoked accepted membership: valid=%v reason=%s revision=%d -> %d", valid, reason, revision, afterRevision)
	}
}

func TestRuntimeWatchClosureUsesReflectorClassification(t *testing.T) {
	for _, mode := range []string{"normal", "short", "failed-reconnect"} {
		t.Run(mode, func(t *testing.T) {
			job := runtimeJob("work", "root")
			fixture := startConnected(t, []*unstructured.Unstructured{job}, nil, "")
			awaitRuntime(t, func() bool {
				view, record := invariantPublication(fixture, "root")
				if record == nil {
					return false
				}
				valid, _ := view.Valid(record.Status)
				return valid
			})
			fixture.controller.queue.ShutDownWithDrain()
			stream := invariantStream(t, fixture, invariantJobs)
			before, record := invariantPublication(fixture, "root")
			fixture.controller.mu.Lock()
			proof := fixture.controller.proofs[refKey(objectReference(job, job.GroupVersionKind()))]
			fixture.controller.mu.Unlock()
			fixture.now.Add(int64(time.Minute))

			ctx, cancel := context.WithCancel(t.Context())
			source := watch.NewRaceFreeFake()
			reconnected := make(chan struct{})
			reported := make(chan error, 1)
			watches := 0
			listWatch := &cache.ListWatch{
				ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
					list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*job.DeepCopy()}}
					list.SetResourceVersion("1")
					return list, nil
				},
				WatchFuncWithContext: func(requestCtx context.Context, _ metav1.ListOptions) (watch.Interface, error) {
					watches++
					if watches == 1 {
						return fixture.controller.monitorWatch(ctx, stream, source), nil
					}
					close(reconnected)
					if mode == "failed-reconnect" {
						return nil, apierrors.NewForbidden(invariantJobs.GroupResource(), "", errors.New("test reconnect denied"))
					}
					<-requestCtx.Done()
					return nil, requestCtx.Err()
				},
			}
			informer := cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(listWatch, fixture.client),
				&unstructured.Unstructured{}, 0, cache.Indexers{})
			if err := informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
				fixture.controller.failStream(stream, err)
				select {
				case reported <- err:
				default:
				}
			}); err != nil {
				t.Fatal(err)
			}
			stopped := make(chan struct{})
			go func() { defer close(stopped); informer.Run(ctx.Done()) }()
			t.Cleanup(func() { cancel(); <-stopped })
			if mode != "short" {
				bookmark := job.DeepCopy()
				bookmark.SetResourceVersion("2")
				source.Action(watch.Bookmark, bookmark)
				awaitRuntime(t, func() bool { return informer.LastSyncResourceVersion() == "2" })
			}
			source.Stop()
			switch mode {
			case "normal":
				select {
				case <-reconnected:
				case <-time.After(5 * time.Second):
					t.Fatal("normal closure did not reconnect")
				}
			case "short":
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					var short *cache.VeryShortWatchError
					return errors.As(stream.err, &short)
				})
			default:
				select {
				case err := <-reported:
					if !apierrors.IsForbidden(err) {
						t.Fatalf("reconnect returned %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("watch failure did not reach the error handler")
				}
			}
			after := fixture.store.Capture()
			valid, reason := after.Valid(record.Status)
			if valid != (mode == "normal") {
				t.Fatalf("status validity after %s closure = %v (%s)", mode, valid, reason)
			}
			if mode == "failed-reconnect" && reason != store.ReasonPermissionDenied {
				t.Fatalf("failed reconnect reason = %s", reason)
			}
			fixture.controller.mu.Lock()
			afterProof := fixture.controller.proofs[refKey(objectReference(job, job.GroupVersionKind()))]
			fixture.controller.mu.Unlock()
			if afterProof != proof {
				t.Fatal("watch closure or reconnect renewed freshness")
			}
			if valid, _ := before.Valid(record.Status); !valid {
				t.Fatal("watch closure mutated the earlier capture")
			}
		})
	}
}

func TestRuntimeAcceptedChildChangePreservesIndependentFields(t *testing.T) {
	for _, mode := range []string{"pod-update", "owner-update", "owner-delete-root-first", "owner-delete-pod-first"} {
		t.Run(mode, func(t *testing.T) {
			root := runtimeJob("work", "root")
			owner := runtimeObject(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"}, "group", "group")
			owner.SetOwnerReferences(runtimePod("unused", "unused", root).GetOwnerReferences())
			pod := runtimePod("worker", "pod", owner)
			sibling := runtimePod("sibling", "sibling", root)
			fixture := startConnected(t, []*unstructured.Unstructured{root, owner, pod, sibling}, nil, "")
			awaitRuntime(t, func() bool {
				view, record := invariantPublication(fixture, "root")
				if record == nil {
					return false
				}
				first, firstExists := record.Pod("pod")
				second, secondExists := record.Pod("sibling")
				firstValid, _ := view.Valid(first.Mapping)
				secondValid, _ := view.Valid(second.Mapping)
				statusValid, _ := view.Valid(record.Status)
				return firstExists && secondExists && firstValid && secondValid && statusValid && record.Workload.HasStatus
			})
			fixture.controller.queue.ShutDownWithDrain()
			_, before := invariantPublication(fixture, "root")
			changed, resource := owner, schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "trainings"}
			if mode == "pod-update" {
				changed, resource = pod, invariantPods
			}
			changed = changed.DeepCopy()
			changed.SetResourceVersion("2")
			stream := invariantStream(t, fixture, resource)
			deleted := mode == "owner-delete-root-first" || mode == "owner-delete-pod-first"
			if deleted {
				if err := fixture.client.Resource(resource).Namespace("team").Delete(t.Context(), changed.GetName(), metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					_, exists := fixture.controller.reader.objects[refKey(objectReference(changed, changed.GroupVersionKind()))]
					return !exists
				})
			} else {
				fixture.controller.acceptObject(stream, changed, false)
			}

			view, after := invariantPublication(fixture, "root")
			if after == nil {
				t.Fatal("child event removed the known root")
			}
			status, _ := view.Valid(after.Status)
			creation, _ := view.Valid(after.Creation)
			if !after.Workload.HasStatus || !status || !creation || after.Status.ExpiresAt != before.Status.ExpiresAt || after.Creation.ExpiresAt != before.Creation.ExpiresAt {
				t.Fatalf("child event changed independent root fields: %+v", after)
			}
			row, exists := after.Pod("sibling")
			valid, _ := view.Valid(row.Mapping)
			if !exists || !valid {
				t.Fatal("child event suppressed an independent sibling mapping")
			}
			row, exists = after.Pod("pod")
			if valid, _ := view.Valid(row.Mapping); exists && valid {
				t.Fatal("accepted child change left its dependent mapping current")
			}
			if deleted {
				reconcileRoot := func() {
					err := fixture.controller.reconcileRoot(t.Context(), root.GetUID())
					if err != nil && !errors.Is(err, discovery.ErrPending) {
						t.Fatal(err)
					}
				}
				reconcilePod := func() {
					err := fixture.controller.reconcileObject(t.Context(), queuedWork{GVK: runtimePodGVK,
						Namespace: pod.GetNamespace(), Name: pod.GetName(), UID: pod.GetUID()})
					if !errors.Is(err, discovery.ErrUnresolvedOwner) {
						t.Fatalf("detached Pod resolution = %v", err)
					}
				}
				if mode == "owner-delete-root-first" {
					reconcileRoot()
					reconcilePod()
				} else {
					reconcilePod()
					reconcileRoot()
				}
				// A subsequent root retry must retain the same invalid history.
				reconcileRoot()
				view, after = invariantPublication(fixture, "root")
				row, exists = after.Pod("pod")
				valid, reason := view.Valid(row.Mapping)
				status, _ = view.Valid(after.Status)
				creation, _ = view.Valid(after.Creation)
				if !exists || valid || reason != store.ReasonUnresolvedOwner || row.Record.OwnerResolution != "unknown" ||
					!after.Workload.HasStatus || !status || !creation {
					t.Fatalf("reconciliation discarded applicable unresolved-owner history: exists=%v row=%+v root=%+v", exists, row, after)
				}
			}
		})
	}
}

func TestRuntimeHandoverRetiresObjectsMissingFromReplacement(t *testing.T) {
	root := runtimeJob("work", "root")
	fixture := startConnected(t, []*unstructured.Unstructured{root, runtimePod("worker", "pod", root)},
		func(options *RuntimeOptions) { options.Definitions.Catalog.Enabled = false }, "")
	awaitInvariantMapping(t, fixture, "root", "pod", "<unknown>")
	fixture.controller.queue.ShutDownWithDrain()
	old := invariantStream(t, fixture, invariantJobs)
	if old.full {
		t.Fatal("generic owner did not start with metadata coverage")
	}
	listed := make(chan struct{})
	var lists atomic.Int64
	fixture.client.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		// The old watch has stopped before this replacement initial LIST.
		if lists.Add(1) == 1 {
			if err := fixture.client.Tracker().Delete(invariantJobs, "team", root.GetName()); err != nil {
				t.Error(err)
			}
			close(listed)
		} else if options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions(); options.ResourceVersion != "" {
			t.Errorf("confirmed absence used resourceVersion=%q", options.ResourceVersion)
		}
		page := &unstructured.UnstructuredList{}
		page.SetResourceVersion("2")
		return true, page, nil
	})
	if err := fixture.controller.ensureCoverage(t.Context(), discovery.CoverageRequest{
		GVK: root.GroupVersionKind(), Namespace: "team", FullObject: true,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement initial LIST did not start")
	}
	awaitRuntime(t, func() bool {
		fixture.controller.mu.Lock()
		defer fixture.controller.mu.Unlock()
		stream := fixture.controller.reader.streams[old.key]
		return stream != old && stream.ready
	})
	if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: root.GroupVersionKind(), Namespace: "team"}); err != nil {
		t.Fatal(err)
	}
	fixture.controller.mu.Lock()
	_, hasFact := fixture.controller.reader.objects[refKey(objectReference(root, root.GroupVersionKind()))]
	_, hasUID := fixture.controller.reader.byUID[root.GetUID()]
	_, hasProof := fixture.controller.proofs[refKey(objectReference(root, root.GroupVersionKind()))]
	_, hasHistory := fixture.controller.history[root.GetUID()]
	_, hasRoot := fixture.controller.rootsByNamespace["team"][root.GetUID()]
	_, hasMembers := fixture.controller.podsByRoot[root.GetUID()]
	_, hasGuard := fixture.controller.retired[root.GetUID()]
	associated := fixture.controller.associations["pod"]
	fixture.controller.mu.Unlock()
	_, record := invariantPublication(fixture, "root")
	if hasFact || hasUID || hasProof || hasHistory || hasRoot || hasMembers || hasGuard || associated != "" || record != nil {
		t.Fatalf("replacement absence retained old lifetime: fact=%v uid=%v proof=%v history=%v root=%v members=%v guard=%v association=%s publication=%v",
			hasFact, hasUID, hasProof, hasHistory, hasRoot, hasMembers, hasGuard, associated, record != nil)
	}
	if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); err != nil {
		t.Fatalf("queued work for absent root keeps retrying: %v", err)
	}
	fixture.controller.acceptObject(old, root, false)
	if _, record := invariantPublication(fixture, "root"); record != nil {
		t.Fatal("late old-generation callback restored retired root")
	}
}

func TestRuntimeHandoverInitialOmissionDoesNotRetireLiveUID(t *testing.T) {
	root := runtimeJob("work", "root")
	alias := root.DeepCopy()
	alias.SetAPIVersion("batch/v2")
	fixture := startConnected(t, []*unstructured.Unstructured{
		root, alias, runtimePod("first", "first", root), runtimePod("second", "second", alias),
	}, func(options *RuntimeOptions) { options.Definitions.Catalog.Enabled = false }, "")
	awaitInvariantMapping(t, fixture, "root", "first", "<unknown>")
	awaitInvariantMapping(t, fixture, "root", "second", "<unknown>")
	fixture.controller.queue.ShutDownWithDrain()
	old := invariantStream(t, fixture, invariantJobs)
	if old.full {
		t.Fatal("generic workload already has full coverage")
	}
	catchup := watch.NewRaceFreeFake()
	fixture.client.PrependWatchReactor("jobs", func(action ktesting.Action) (bool, watch.Interface, error) {
		if action.GetResource() == invariantJobs {
			return true, catchup, nil
		}
		return false, nil, nil
	})
	var omitted atomic.Bool
	fixture.client.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != invariantJobs || omitted.Swap(true) {
			return false, nil, nil
		}
		// An RV=0 initial cache snapshot can predate this live UID. The
		// following most-recent verifier must see the tracker object.
		page := &unstructured.UnstructuredList{}
		page.SetResourceVersion("0")
		return true, page, nil
	})
	if err := fixture.controller.ensureCoverage(t.Context(), discovery.CoverageRequest{
		GVK: root.GroupVersionKind(), Namespace: "team", FullObject: true,
	}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		fixture.controller.mu.Lock()
		defer fixture.controller.mu.Unlock()
		stream := fixture.controller.reader.streams[old.key]
		return stream != old && stream.ready
	})
	fixture.controller.mu.Lock()
	_, retained := fixture.controller.reader.objects[refKey(objectReference(root, root.GroupVersionKind()))]
	guarded := len(fixture.controller.retired[root.GetUID()]) != 0
	fixture.controller.mu.Unlock()
	if !omitted.Load() || !retained || guarded {
		t.Fatalf("initial cached omission retired a lifetime before fresh verification: omitted=%v retained=%v guarded=%v", omitted.Load(), retained, guarded)
	}
	if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: root.GroupVersionKind(), Namespace: "team"}); err != nil {
		t.Fatal(err)
	}
	root.SetResourceVersion("2")
	alias.SetResourceVersion("2")
	for _, object := range []*unstructured.Unstructured{root, alias} {
		resource := invariantJobs
		resource.Version = object.GroupVersionKind().Version
		if _, err := fixture.client.Resource(resource).Namespace("team").Update(t.Context(), object, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	catchup.Modify(root.DeepCopy())
	awaitRuntime(t, func() bool {
		fixture.controller.mu.Lock()
		defer fixture.controller.mu.Unlock()
		for _, object := range []*unstructured.Unstructured{root, alias} {
			ref := objectReference(object, object.GroupVersionKind())
			fact := fixture.controller.reader.objects[refKey(ref)]
			if fact.observation.Ref != ref || fact.observation.Run != fixture.controller.reader.streams[fact.observation.WatchKey].run {
				return false
			}
		}
		return len(fixture.controller.retired[root.GetUID()]) == 0
	})
	for _, gvk := range []schema.GroupVersionKind{root.GroupVersionKind(), alias.GroupVersionKind(), runtimePodGVK} {
		if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: gvk, Namespace: "team"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.controller.verifyMembership(t.Context(), "team"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); err != nil {
		t.Fatal(err)
	}
	view, record := invariantPublication(fixture, "root")
	if record == nil {
		t.Fatal("initial omission permanently retired a live workload")
	}
	for _, uid := range []string{"first", "second"} {
		row, exists := record.Pod(types.UID(uid))
		if valid, _ := view.Valid(row.Mapping); !exists || !valid {
			t.Fatalf("live %s representation did not recover: %+v", uid, row)
		}
	}
}
