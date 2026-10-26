// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func TestRuntimeBarePodRequiresSelectedDefinition(t *testing.T) {
	for _, tc := range []struct {
		name             string
		include, exclude []string
		publish          bool
	}{
		{name: "default exclusion", include: []string{"batch-job-v1"}, exclude: []string{"core-pod-v1"}},
		{name: "exclusion wins", include: []string{"core-pod-v1"}, exclude: []string{"core-pod-v1"}},
		{name: "explicit Pod definition", include: []string{"core-pod-v1"}, publish: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := runtimePod("standalone", "pod-root", runtimeJob("unused", "unused"))
			pod.SetOwnerReferences(nil)
			fixture := startConnected(t, []*unstructured.Unstructured{pod}, func(options *RuntimeOptions) {
				options.Definitions.Catalog.Include = tc.include
				options.Definitions.Catalog.Exclude = tc.exclude
			}, "")
			awaitRuntime(t, func() bool {
				if !fixture.controller.Ready() {
					return false
				}
				fixture.controller.mu.Lock()
				_, verified := fixture.controller.proofLocked(objectReference(pod, runtimePodGVK))
				fixture.controller.mu.Unlock()
				return verified
			})
			if !tc.publish {
				if err := fixture.controller.reconcileRoot(context.Background(), pod.GetUID()); err != nil {
					t.Fatal(err)
				}
				if rows := fixture.store.Capture().Records; len(rows) != 0 {
					t.Fatalf("bare Pod published without selected definition: %v", rows)
				}
				return
			}
			awaitRuntime(t, func() bool {
				view := fixture.store.Capture()
				if len(view.Records) != 1 {
					return false
				}
				record := view.Records[0]
				row, exists := record.Pod(pod.GetUID())
				mapping, _ := view.Valid(row.Mapping)
				status, _ := view.Valid(record.Status)
				creation, _ := view.Valid(record.Creation)
				return exists && mapping && status && creation && record.Workload.UID == pod.GetUID() &&
					record.Workload.HasStatus && row.Record.Component == "pod" && row.Record.Instance == "" && row.Record.Replica == ""
			})
		})
	}
}

func TestRuntimeRequiredWatchDenialAndRecovery(t *testing.T) {
	var denied atomic.Bool
	denied.Store(true)
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	fixture := startConnected(t, []*unstructured.Unstructured{runtimeJob("required", "root")}, func(options *RuntimeOptions) {
		options.RequiredKinds = []schema.GroupVersionKind{gvk}
	}, "", func(client dynamic.Interface) dynamic.Interface {
		client.(*fake.FakeDynamicClient).PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			if denied.Load() {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "", nil)
			}
			return false, nil, nil
		})
		return client
	})
	awaitRuntime(t, func() bool {
		for _, row := range fixture.store.Capture().Health {
			if row.Ref.Kind == "Job" && row.Reason == store.ReasonPermissionDenied {
				return true
			}
		}
		return false
	})
	if fixture.controller.Ready() {
		t.Fatal("required API denial reported ready")
	}
	denied.Store(false)
	awaitRuntime(t, func() bool {
		if !fixture.controller.Ready() {
			return false
		}
		view := fixture.store.Capture()
		if len(view.Records) != 1 {
			return false
		}
		valid, _ := view.Valid(view.Records[0].Status)
		return valid
	})
}
