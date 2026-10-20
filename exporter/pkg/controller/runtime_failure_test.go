// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
)

type failingSelectionInventory struct {
	kartadefinitioninventory.Interface
	failed atomic.Bool
}

func (i *failingSelectionInventory) Lookup(gvk schema.GroupVersionKind) (kartadefinitioninventory.Selection, error) {
	if i.failed.Load() {
		return kartadefinitioninventory.Selection{}, discovery.ErrPermissionDenied
	}
	return i.Interface.Lookup(gvk)
}

func TestRuntimeRootSelectionUsesAcceptedResource(t *testing.T) {
	root := runtimeJob("work", "root")
	fixture := startConnected(t, []*unstructured.Unstructured{root, runtimePod("worker", "pod", root)}, nil, "")
	awaitInvariantMapping(t, fixture, "root", "pod", "job")
	fixture.controller.queue.ShutDownWithDrain()
	fixture.controller.mapper = meta.NewDefaultRESTMapper(nil)
	if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); err != nil {
		t.Fatal(err)
	}
	view, record := invariantPublication(fixture, "root")
	if record == nil {
		t.Fatal("transient mapper absence was treated as output exclusion")
	}
	row, exists := record.Pod("pod")
	status, _ := view.Valid(record.Status)
	mapping, _ := view.Valid(row.Mapping)
	if !exists || !status || !mapping {
		t.Fatal("accepted resource lost valid output without a filter change")
	}
}

func TestRuntimeGenericReadErrorDoesNotBecomeExclusion(t *testing.T) {
	root := runtimeJob("work", "root")
	alias := root.DeepCopy()
	alias.SetAPIVersion("batch/v2")
	fixture := startConnected(t, []*unstructured.Unstructured{root, alias, runtimePod("worker", "pod", alias)}, func(options *RuntimeOptions) {
		options.Definitions.Catalog.Enabled = false
		options.RequiredKinds = []schema.GroupVersionKind{root.GroupVersionKind()}
		options.ResourceRules = []ResourceRule{{APIGroups: []string{"batch"}, APIVersions: []string{"v2"}, Resources: []string{"jobs"}}}
	}, "")
	awaitInvariantMapping(t, fixture, "root", "pod", "<unknown>")
	fixture.controller.queue.ShutDownWithDrain()
	_, before := invariantPublication(fixture, "root")
	stream := invariantStream(t, fixture, schema.GroupVersionResource{Group: "batch", Version: "v2", Resource: "jobs"})
	changed := alias.DeepCopy()
	changed.SetResourceVersion("2")
	// The informer store can lead its callback. The accepted reference still
	// names the prior version, so this read must discard obsolete work.
	if err := stream.informer.GetStore().Update(changed); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); !errors.Is(err, discovery.ErrStale) {
		t.Fatalf("generic read returned %v, want ErrStale", err)
	}
	_, after := invariantPublication(fixture, "root")
	if after != before {
		t.Fatal("read error replaced or removed accepted publication")
	}
	fixture.controller.mu.Lock()
	_, history := fixture.controller.history[root.GetUID()]
	fixture.controller.mu.Unlock()
	if !history {
		t.Fatal("read error removed applicable history")
	}
}

func TestRuntimeDefinitionFailurePreservesVerifiedOwnership(t *testing.T) {
	root := runtimeJob("work", "root")
	pod := runtimePod("worker", "pod", root)
	var inventory *failingSelectionInventory
	fixture := startConnectedWithHooks(t, []*unstructured.Unstructured{root, pod}, nil, "", connectedHooks{
		WrapInventory: func(inner kartadefinitioninventory.Interface) kartadefinitioninventory.Interface {
			inventory = &failingSelectionInventory{Interface: inner}
			return inventory
		},
	})
	awaitInvariantMapping(t, fixture, "root", "pod", "job")
	fixture.controller.queue.ShutDownWithDrain()
	inventory.failed.Store(true)
	err := fixture.controller.reconcileObject(t.Context(), queuedWork{
		GVK: runtimePodGVK, Namespace: pod.GetNamespace(), Name: pod.GetName(), UID: pod.GetUID(),
	})
	if !errors.Is(err, discovery.ErrPermissionDenied) {
		t.Fatalf("definition lookup failure = %v", err)
	}
	if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); !errors.Is(err, discovery.ErrPermissionDenied) {
		t.Fatalf("root definition lookup failure = %v", err)
	}
	view, record := invariantPublication(fixture, "root")
	row, exists := record.Pod("pod")
	valid, reason := view.Valid(row.Mapping)
	creation, _ := view.Valid(record.Creation)
	if !exists || valid || reason != store.ReasonPermissionDenied || row.Record.OwnerResolution != "resolved" || record.Workload.OwnerResolution != "resolved" || !creation {
		t.Fatalf("definition failure erased verified ownership or root metadata: row=%+v creation=%v", row, creation)
	}
}

func TestRuntimeOwnerReadFailureRetainsUnknownResolution(t *testing.T) {
	for _, test := range []struct {
		name    string
		request error
		want    error
		reason  string
	}{
		{"denied", apierrors.NewForbidden(invariantJobs.GroupResource(), "", errors.New("test owner denial")), discovery.ErrPermissionDenied, store.ReasonPermissionDenied},
		{"unavailable", apierrors.NewServiceUnavailable("test owner outage"), discovery.ErrUnavailable, store.ReasonAPIUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := runtimeJob("work", "root")
			pod := runtimePod("worker", "pod", root)
			fixture := startConnected(t, []*unstructured.Unstructured{root, pod}, nil, "")
			awaitInvariantMapping(t, fixture, root.GetUID(), pod.GetUID(), "job")
			fixture.controller.queue.ShutDownWithDrain()
			fixture.client.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, test.request
			})
			if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: root.GroupVersionKind(), Namespace: "team"}); !errors.Is(err, test.request) {
				t.Fatalf("owner verification = %v", err)
			}
			for range 2 {
				if err := fixture.controller.reconcileObject(t.Context(), queuedWork{GVK: runtimePodGVK, Namespace: "team", Name: pod.GetName(), UID: pod.GetUID()}); !errors.Is(err, test.want) {
					t.Fatalf("Pod resolution = %v", err)
				}
				if err := fixture.controller.reconcileRoot(t.Context(), root.GetUID()); !errors.Is(err, test.want) {
					t.Fatalf("root resolution = %v", err)
				}
				view, record := invariantPublication(fixture, root.GetUID())
				if record == nil {
					t.Fatal("owner failure removed applicable history")
				}
				row, exists := record.Pod(pod.GetUID())
				valid, reason := view.Valid(row.Mapping)
				status, statusReason := view.Valid(record.Status)
				if !exists || valid || status || reason != test.reason || statusReason != test.reason ||
					row.Record.OwnerResolution != "unknown" || record.Workload.OwnerResolution != "unknown" {
					t.Fatalf("root retry restored resolved labels after failed ownership: workload=%+v pod=%+v", record.Workload, row)
				}
			}
		})
	}
}
