// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRuntimeHiddenPodCannotReturnFromFailureHistory(t *testing.T) {
	for _, failure := range []string{"inventory", "owner"} {
		for _, stage := range []string{"accepted", "reconciled"} {
			t.Run(failure+"/"+stage, func(t *testing.T) {
				root := runtimeJob("work", "root")
				pod := runtimePod("worker", "pod", root)
				pod.SetLabels(map[string]string{"visible": "yes"})
				sibling := runtimePod("sibling", "sibling", root)
				sibling.SetLabels(map[string]string{"visible": "yes"})
				var inventory *failingSelectionInventory
				fixture := startConnectedWithHooks(t, []*unstructured.Unstructured{root, pod, sibling}, func(options *RuntimeOptions) {
					options.PodSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"visible": "yes"}}
				}, "", connectedHooks{WrapInventory: func(inner kartadefinitioninventory.Interface) kartadefinitioninventory.Interface {
					inventory = &failingSelectionInventory{Interface: inner}
					return inventory
				}})
				awaitInvariantMapping(t, fixture, root.GetUID(), pod.GetUID(), "job")
				awaitInvariantMapping(t, fixture, root.GetUID(), sibling.GetUID(), "job")
				fixture.controller.queue.ShutDownWithDrain()
				pod.SetLabels(nil)
				pod.SetResourceVersion("2")
				if _, err := fixture.client.Resource(invariantPods).Namespace("team").Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					return fixture.controller.reader.objects[refKey(objectReference(pod, runtimePodGVK))].observation.Ref.ResourceVersion == "2"
				})
				if stage == "reconciled" {
					if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: runtimePodGVK, Namespace: "team"}); err != nil {
						t.Fatal(err)
					}
					if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); err != nil {
						t.Fatal(err)
					}
				}
				if failure == "inventory" {
					inventory.failed.Store(true)
				} else {
					fixture.controller.failStream(invariantStream(t, fixture, invariantJobs),
						apierrors.NewForbidden(invariantJobs.GroupResource(), "", errors.New("test owner denial")))
				}
				if err := fixture.controller.reconcileObject(t.Context(), queuedWork{GVK: runtimePodGVK, Namespace: "team", Name: pod.GetName(), UID: pod.GetUID()}); !errors.Is(err, discovery.ErrPermissionDenied) {
					t.Fatalf("Pod resolution = %v", err)
				}
				if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); !errors.Is(err, discovery.ErrPermissionDenied) {
					t.Fatalf("root resolution = %v", err)
				}
				_, current := invariantPublication(fixture, root.GetUID())
				if current == nil {
					t.Fatal("failure removed unrelated workload history")
				}
				if _, exists := current.Pod(pod.GetUID()); exists {
					t.Fatal("failure resurrected a Pod excluded by an accepted selector change")
				}
				if _, exists := current.Pod(sibling.GetUID()); !exists {
					t.Fatal("removing hidden history also removed the visible sibling")
				}
				fixture.controller.mu.Lock()
				_, retained := fixture.controller.history[root.GetUID()].record.Pod(pod.GetUID())
				_, observed := fixture.controller.reader.objects[refKey(objectReference(pod, runtimePodGVK))]
				association := fixture.controller.associations[pod.GetUID()]
				fixture.controller.mu.Unlock()
				if retained || !observed || association != root.GetUID() {
					t.Fatal("hidden Pod must leave output history while remaining observed and associated")
				}
			})
		}
	}
}
