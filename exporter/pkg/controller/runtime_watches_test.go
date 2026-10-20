// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestRuntimeWatchConstructionFailureRevokesEvidence(t *testing.T) {
	for _, resource := range []schema.GroupVersionResource{invariantJobs, {Version: "v1", Resource: "namespaces"}, kartaGVR} {
		for _, status := range []int{429, 500} {
			t.Run(resource.Resource+"/"+strconv.Itoa(status), func(t *testing.T) {
				watchErr := apierrors.NewGenericServerResponse(status, "watch", resource.GroupResource(), "", "test watch failure", 0, false)
				source := watch.NewRaceFreeFake()
				var calls atomic.Int64
				retried := make(chan struct{}, 1)
				root := runtimeJob("work", "root")
				fixture := startConnectedWithHooks(t, []*unstructured.Unstructured{root}, func(options *RuntimeOptions) {
					options.Definitions.Cluster.Enabled = true
				}, "", connectedHooks{BeforeRun: func(c *Runtime) {
					c.dynamic.(*fake.FakeDynamicClient).PrependWatchReactor(resource.Resource, func(action ktesting.Action) (bool, watch.Interface, error) {
						options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
						if options.TimeoutSeconds == nil {
							// The independent inventory watch is not the failing consumer.
							return false, nil, nil
						}
						if calls.Add(1) == 1 {
							return true, source, nil
						}
						select {
						case retried <- struct{}{}:
						default:
						}
						return true, nil, watchErr
					})
				}})
				awaitRuntime(t, func() bool {
					view, record := invariantPublication(fixture, root.GetUID())
					if record == nil {
						return false
					}
					valid, _ := view.Valid(record.Status)
					return valid
				})
				fixture.controller.queue.ShutDownWithDrain()
				before, record := invariantPublication(fixture, root.GetUID())
				fixture.controller.mu.Lock()
				proof := fixture.controller.proofs[refKey(objectReference(root, root.GroupVersionKind()))]
				fixture.controller.mu.Unlock()

				// A bookmark makes this a normal watch closure. The subsequent
				// constructor failure must revoke evidence even if Reflector retries.
				source.Action(watch.Bookmark, &unstructured.Unstructured{Object: map[string]any{
					"metadata": map[string]any{"resourceVersion": "2"},
				}})
				source.Stop()
				select {
				case <-retried:
				case <-time.After(5 * time.Second):
					t.Fatal("watch did not reconnect")
				}
				awaitRuntime(t, func() bool {
					view := fixture.store.Capture()
					valid, reason := view.Valid(record.Status)
					return !valid && reason == store.ReasonAPIUnavailable
				})
				fixture.controller.mu.Lock()
				afterProof := fixture.controller.proofs[refKey(objectReference(root, root.GroupVersionKind()))]
				fixture.controller.mu.Unlock()
				if proof != afterProof {
					t.Fatal("failed watch construction renewed or replaced object evidence")
				}
				if valid, _ := before.Valid(record.Status); !valid {
					t.Fatal("watch failure mutated the earlier capture")
				}
			})
		}
	}
}

func TestRuntimeCanceledWatchConstructionPreservesEvidence(t *testing.T) {
	root := runtimeJob("work", "root")
	fixture := startConnected(t, []*unstructured.Unstructured{root}, nil, "")
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, root.GetUID())
		if record == nil {
			return false
		}
		valid, _ := view.Valid(record.Status)
		return valid
	})
	fixture.controller.queue.ShutDownWithDrain()
	_, record := invariantPublication(fixture, root.GetUID())
	stream := invariantStream(t, fixture, invariantJobs)
	requestErr := apierrors.NewInternalError(errors.New("request ended during shutdown"))
	fixture.client.PrependWatchReactor("jobs", func(ktesting.Action) (bool, watch.Interface, error) {
		return true, nil, requestErr
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := fixture.controller.listWatch(ctx, stream).WatchWithContext(ctx, metav1.ListOptions{})
	if !errors.Is(err, requestErr) {
		t.Fatalf("watch returned %v, want original request error", err)
	}
	view := fixture.store.Capture()
	if valid, reason := view.Valid(record.Status); !valid {
		t.Fatalf("shutdown revoked accepted evidence: %s", reason)
	}
}

func TestRuntimeMetadataWatchAcceptsTypedUpdates(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"}
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "trainings"}
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(gvk, &metav1.PartialObjectMetadata{})
	object := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "example.com/v1", Kind: "Training"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team", Name: "root", UID: "root", ResourceVersion: "1",
		},
	}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	metadataClient := metadatafake.NewSimpleMetadataClient(scheme, object)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
	mapper.Add(gvk, meta.RESTScopeNamespace)
	options := RuntimeOptions{Definitions: kartadefinitioninventory.Options{}}
	inventory, err := kartadefinitioninventory.New(client, options.Definitions)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewInformerReader()
	d := discovery.New(reader, inventory)
	controller, err := NewRuntime(client, metadataClient, mapper, reader, d, inventory, nil, store.New(), options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); controller.wg.Wait(); controller.queue.ShutDown() })
	controller.namespaces["team"] = true
	if err := controller.ensureCoverage(ctx, discovery.CoverageRequest{GVK: gvk, Namespace: "team"}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		body, err := reader.Get(ctx, objectReference(object, gvk))
		return err == nil && body.GetResourceVersion() == "1"
	})
	updated := object.DeepCopy()
	updated.ResourceVersion = "2"
	updated.Annotations = map[string]string{"changed": "yes"}
	if err := metadataClient.Tracker().Update(gvr, updated, "team"); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		body, err := reader.Get(ctx, objectReference(updated, gvk))
		return err == nil && body.GetAnnotations()["changed"] == "yes" && body.GetKind() == "Training"
	})
}
