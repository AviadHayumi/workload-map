// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/workqueue"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

type observedWorkQueue struct {
	workqueue.TypedRateLimitingInterface[queuedWork]
	objects atomic.Int64
}

func (q *observedWorkQueue) Add(key queuedWork) {
	if key.Operation == "object" {
		q.objects.Add(1)
	}
	q.TypedRateLimitingInterface.Add(key)
}

func TestRuntimeUnchangedSelectionRefreshAvoidsObjectReevaluation(t *testing.T) {
	root := runtimeJob("work", "root")
	var queue *observedWorkQueue
	fixture := startConnectedWithHooks(t, []*unstructured.Unstructured{root, runtimePod("worker", "pod", root)}, nil, "",
		connectedHooks{BeforeRun: func(controller *Runtime) {
			queue = &observedWorkQueue{TypedRateLimitingInterface: controller.queue}
			controller.queue = queue
		}})
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		membership, _ := view.Valid(record.Membership)
		status, _ := view.Valid(record.Status)
		return membership && status && record.Workload.HasStatus
	})
	queue.ShutDownWithDrain()
	_, before := invariantPublication(fixture, "root")
	objectWork := queue.objects.Load()
	fixture.now.Add(int64(time.Minute))
	if err := fixture.controller.refreshSelections(t.Context()); err != nil {
		t.Fatal(err)
	}
	if added := queue.objects.Load() - objectWork; added != 0 {
		t.Fatalf("unchanged selection refresh queued %d objects", added)
	}

	for _, failedSource := range []bool{false, true} {
		if failedSource {
			fixture.controller.failConfiguration(false, apierrors.NewForbidden(kartaGVR.GroupResource(), "", errors.New("test source unavailable")))
			view, record := invariantPublication(fixture, "root")
			if valid, reason := view.Valid(record.Status); valid || reason != store.ReasonPermissionDenied {
				t.Fatalf("source failure left status valid=%v (%s)", valid, reason)
			}
			if err := fixture.controller.refreshSelections(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		for _, gvk := range []queuedWork{{GVK: root.GroupVersionKind(), Namespace: "team"}, {GVK: runtimePodGVK, Namespace: "team"}} {
			if err := fixture.controller.verifyStream(t.Context(), gvk); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.controller.verifyMembership(t.Context(), "team"); err != nil {
			t.Fatal(err)
		}
		if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); err != nil {
			t.Fatal(err)
		}
		view, renewed := invariantPublication(fixture, "root")
		row, exists := renewed.Pod("pod")
		status, _ := view.Valid(renewed.Status)
		creation, _ := view.Valid(renewed.Creation)
		mapping, _ := view.Valid(row.Mapping)
		membership, _ := view.Valid(renewed.Membership)
		if !status || !creation || !exists || !mapping || !membership || !renewed.Status.ExpiresAt.After(before.Status.ExpiresAt) {
			t.Fatalf("resource verification did not renew accepted publication after source failure=%v: %+v", failedSource, renewed)
		}
	}
}
