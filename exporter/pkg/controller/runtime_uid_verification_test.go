// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
)

func TestRuntimeFreshListDistinguishesUIDFromResourceVersion(t *testing.T) {
	for _, test := range []struct {
		name        string
		listedUID   types.UID
		acceptedUID types.UID
	}{
		{name: "replaced UID", listedUID: "replacement"},
		{name: "same UID newer resourceVersion", listedUID: "root"},
		{name: "same lifetime callback during LIST", listedUID: "replacement", acceptedUID: "root"},
		{name: "new lifetime callback during LIST", listedUID: "replacement", acceptedUID: "newer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := runtimeJob("work", "root")
			peer := runtimeJob("peer", "peer")
			fixture := startConnected(t, []*unstructured.Unstructured{
				root, runtimePod("worker", "pod", root), peer, runtimePod("peer-worker", "peer-pod", peer),
			}, nil, "")
			awaitInvariantMapping(t, fixture, peer.GetUID(), "peer-pod", "job")
			awaitRuntime(t, func() bool {
				view, record := invariantPublication(fixture, root.GetUID())
				if record == nil {
					return false
				}
				row, exists := record.Pod("pod")
				mapping, _ := view.Valid(row.Mapping)
				status, _ := view.Valid(record.Status)
				creation, _ := view.Valid(record.Creation)
				return exists && mapping && status && creation
			})
			fixture.controller.queue.ShutDownWithDrain()
			stream := invariantStream(t, fixture, invariantJobs)
			before, previous := invariantPublication(fixture, root.GetUID())
			key := refKey(objectReference(root, root.GroupVersionKind()))
			fixture.now.Add(int64(time.Minute))

			listed := root.DeepCopy()
			listed.SetUID(test.listedUID)
			listed.SetResourceVersion("2")
			expected := root.DeepCopy()
			if test.acceptedUID != "" {
				expected.SetUID(test.acceptedUID)
				expected.SetResourceVersion("3")
			}
			fixture.client.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
				if options.ResourceVersion != "" {
					t.Errorf("fresh LIST used resourceVersion=%q", options.ResourceVersion)
				}
				if test.acceptedUID != "" {
					// The callback is accepted after verification captured the old
					// local lifetime but before the complete API result returns.
					fixture.controller.acceptObject(stream, expected, false)
				}
				page := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*listed, *peer}}
				page.SetResourceVersion("2")
				return true, page, nil
			})
			if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: root.GroupVersionKind(), Namespace: "team"}); err != nil {
				t.Fatal(err)
			}

			fixture.controller.mu.Lock()
			fact, hasFact := fixture.controller.reader.objects[key]
			_, hasOldUID := fixture.controller.reader.byUID[root.GetUID()]
			_, hasProof := fixture.controller.proofs[key]
			_, hasHistory := fixture.controller.history[root.GetUID()]
			_, hasRoot := fixture.controller.rootsByNamespace["team"][root.GetUID()]
			_, hasMembers := fixture.controller.podsByRoot[root.GetUID()]
			association := fixture.controller.associations["pod"]
			fixture.controller.mu.Unlock()
			after, current := invariantPublication(fixture, root.GetUID())
			if test.acceptedUID == "" && test.listedUID != root.GetUID() {
				if hasFact || hasOldUID || hasProof || hasHistory || hasRoot || hasMembers || association != "" || current != nil {
					t.Fatalf("fresh LIST replacement retained old UID: fact=%v uid=%v proof=%v history=%v root=%v members=%v association=%s publication=%v",
						hasFact, hasOldUID, hasProof, hasHistory, hasRoot, hasMembers, association, current != nil)
				}
			} else {
				if want := objectReference(expected, expected.GroupVersionKind()); !hasFact || fact.observation.Ref != want {
					t.Fatalf("fresh LIST replaced newer local state: fact=%+v, want=%+v", fact.observation.Ref, want)
				}
				if test.acceptedUID != "" {
					if hasProof {
						t.Fatal("LIST certified a callback accepted after the read started")
					}
					if test.acceptedUID == root.GetUID() && (!hasHistory || current == nil) {
						t.Fatal("LIST retired the locally changed lifetime")
					}
				} else {
					if hasProof || !hasHistory || !hasOldUID || association != root.GetUID() || current == nil {
						t.Fatal("resourceVersion mismatch must revoke the proof and retain the lifetime, association, and history")
					}
					if valid, _ := after.Valid(current.Creation); valid || current.Workload.HasStatus || len(current.PodPages) != 0 || len(current.FailureReasons) != 0 {
						t.Fatal("resourceVersion mismatch must suppress pending fields without reporting a source outage")
					}
				}
			}
			peerView, peerRecord := invariantPublication(fixture, peer.GetUID())
			if peerRecord == nil {
				t.Fatal("verification removed an unchanged peer")
			}
			peerRow, exists := peerRecord.Pod("peer-pod")
			peerMapping, _ := peerView.Valid(peerRow.Mapping)
			peerStatus, _ := peerView.Valid(peerRecord.Status)
			if !exists || !peerMapping || !peerStatus || !peerRecord.Workload.HasStatus {
				t.Fatal("verification invalidated unchanged peer fields")
			}
			if valid, _ := before.Valid(previous.Status); !valid {
				t.Fatal("verification mutated an earlier capture")
			}
			if test.acceptedUID == "" {
				if test.listedUID != root.GetUID() {
					if err := fixture.client.Resource(invariantJobs).Namespace("team").Delete(t.Context(), root.GetName(), metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
					if _, err := fixture.client.Resource(invariantJobs).Namespace("team").Create(t.Context(), listed, metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				} else if _, err := fixture.client.Resource(invariantJobs).Namespace("team").Update(t.Context(), listed, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				pod := runtimePod("worker", "pod", listed)
				pod.SetResourceVersion("2")
				if _, err := fixture.client.Resource(invariantPods).Namespace("team").Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					return fixture.controller.reader.objects[key].observation.Ref == objectReference(listed, listed.GroupVersionKind()) &&
						fixture.controller.reader.objects[refKey(objectReference(pod, runtimePodGVK))].observation.Ref.ResourceVersion == "2"
				})
				for _, gvk := range []schema.GroupVersionKind{root.GroupVersionKind(), runtimePodGVK} {
					if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: gvk, Namespace: "team"}); err != nil {
						t.Fatal(err)
					}
				}
				if err := fixture.controller.reconcileRoot(t.Context(), listed.GetUID()); err != nil {
					t.Fatal(err)
				}
				view, recovered := invariantPublication(fixture, listed.GetUID())
				if recovered == nil {
					t.Fatal("matching informer input and verification did not recover the workload")
				}
				row, exists := recovered.Pod("pod")
				mapping, _ := view.Valid(row.Mapping)
				status, _ := view.Valid(recovered.Status)
				if !exists || !mapping || !status || !recovered.Workload.HasStatus {
					t.Fatal("recovered workload did not publish current fields")
				}
				if test.listedUID != root.GetUID() {
					if _, old := invariantPublication(fixture, root.GetUID()); old != nil {
						t.Fatal("replacement recovery resurrected the old UID")
					}
				}
			}
		})
	}
}

func TestRuntimeFreshOwnerMismatchPreservesIndependentFields(t *testing.T) {
	root := runtimeJob("work", "root")
	owner := runtimeObject(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"}, "group", "group")
	owner.SetOwnerReferences(runtimePod("unused", "unused", root).GetOwnerReferences())
	fixture := startConnected(t, []*unstructured.Unstructured{
		root, owner, runtimePod("worker", "pod", owner), runtimePod("sibling", "sibling", root),
	}, nil, "")
	awaitInvariantMapping(t, fixture, root.GetUID(), "pod", "job")
	awaitInvariantMapping(t, fixture, root.GetUID(), "sibling", "job")
	fixture.controller.queue.ShutDownWithDrain()
	_, previous := invariantPublication(fixture, root.GetUID())
	listed := owner.DeepCopy()
	listed.SetResourceVersion("2")
	fixture.client.PrependReactor("list", "trainings", func(ktesting.Action) (bool, runtime.Object, error) {
		page := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*listed}}
		page.SetResourceVersion("2")
		return true, page, nil
	})
	if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: owner.GroupVersionKind(), Namespace: "team"}); err != nil {
		t.Fatal(err)
	}
	view, current := invariantPublication(fixture, root.GetUID())
	if current == nil {
		t.Fatal("owner mismatch removed the independent root")
	}
	status, _ := view.Valid(current.Status)
	creation, _ := view.Valid(current.Creation)
	sibling, exists := current.Pod("sibling")
	mapping, _ := view.Valid(sibling.Mapping)
	if !status || !creation || !current.Workload.HasStatus || !exists || !mapping || current.Status.ExpiresAt != previous.Status.ExpiresAt {
		t.Fatal("owner mismatch changed independent root or sibling evidence")
	}
	if _, exists := current.Pod("pod"); exists {
		t.Fatal("owner mismatch left its dependent Pod mapping published")
	}
	fixture.controller.mu.Lock()
	_, proof := fixture.controller.proofs[refKey(objectReference(owner, owner.GroupVersionKind()))]
	_, retained := fixture.controller.history[root.GetUID()].record.Pod("pod")
	fixture.controller.mu.Unlock()
	if proof || !retained || len(current.FailureReasons) != 0 {
		t.Fatal("owner mismatch must revoke proof, retain history, and avoid an outage reason")
	}
}
