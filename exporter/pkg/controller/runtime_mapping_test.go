// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type failingOwnerMapper struct {
	meta.RESTMapper
	kind   schema.GroupKind
	err    error
	failed atomic.Bool
}

func (m *failingOwnerMapper) RESTMapping(kind schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if kind == m.kind && m.failed.Load() {
		return nil, m.err
	}
	return m.RESTMapper.RESTMapping(kind, versions...)
}

func TestRuntimeDiscoveredOwnerMappingFailureAndRecovery(t *testing.T) {
	for _, failure := range []struct {
		name      string
		err, want error
		reason    string
	}{
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Group: "example.com", Resource: "trainings"}, "", errors.New("discovery denied")), discovery.ErrPermissionDenied, store.ReasonPermissionDenied},
		{"unavailable", errors.New("discovery unavailable"), discovery.ErrUnavailable, store.ReasonAPIUnavailable},
	} {
		for _, required := range []bool{false, true} {
			name := failure.name + "/optional"
			if required {
				name = failure.name + "/required"
			}
			t.Run(name, func(t *testing.T) {
				owner := runtimeObject(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"}, "training", "training-root")
				pod := runtimePod("dependent", "dependent", owner)
				peer := runtimeJob("peer", "peer-root")
				var mapper *failingOwnerMapper
				fixture := startConnectedWithHooks(t, []*unstructured.Unstructured{owner, pod, peer, runtimePod("peer-pod", "peer-pod", peer)}, func(options *RuntimeOptions) {
					if required {
						options.RequiredKinds = []schema.GroupVersionKind{owner.GroupVersionKind()}
					}
				}, "", connectedHooks{BeforeRun: func(controller *Runtime) {
					mapper = &failingOwnerMapper{RESTMapper: controller.mapper, kind: owner.GroupVersionKind().GroupKind(), err: failure.err}
					mapper.failed.Store(true)
					controller.mapper = mapper
				}})
				awaitRuntime(t, func() bool {
					view, record := invariantPublication(fixture, peer.GetUID())
					if record == nil {
						return false
					}
					row, exists := record.Pod("peer-pod")
					mapping, _ := view.Valid(row.Mapping)
					status, _ := view.Valid(record.Status)
					creation, _ := view.Valid(record.Creation)
					_, err := fixture.controller.discovery.Resolve(t.Context(), objectReference(pod, runtimePodGVK))
					if !exists || !mapping || !status || !creation || !errors.Is(err, failure.want) {
						return false
					}
					for _, health := range view.Health {
						if health.Ref.Kind == "Training" && health.Ref.Namespace == "team" && !health.Healthy && health.Reason == failure.reason {
							return required || fixture.controller.Ready()
						}
					}
					return false
				})
				if ready := fixture.controller.Ready(); ready == required {
					t.Fatalf("readiness = %v with required = %v", ready, required)
				}
				fixture.controller.queue.ShutDownWithDrain()
				request := discovery.CoverageRequest{GVK: owner.GroupVersionKind(), Namespace: "team"}
				if _, err := fixture.controller.reader.Get(t.Context(), objectReference(owner, request.GVK)); !errors.Is(err, failure.want) {
					t.Fatalf("reported mapping failure became %v", err)
				}
				mapper.failed.Store(false)
				if err := fixture.controller.ensureCoverage(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					for _, stream := range fixture.controller.reader.streams {
						if stream.gvk == request.GVK {
							return stream.ready
						}
					}
					return false
				})
				if _, err := fixture.controller.discovery.Resolve(t.Context(), objectReference(pod, runtimePodGVK)); !errors.Is(err, failure.want) {
					t.Fatalf("mapping recovery renewed unverified coverage: %v", err)
				}
				for _, health := range fixture.store.Capture().Health {
					if health.Ref.Kind == "Training" && (health.Healthy || health.Reason != failure.reason) {
						t.Fatalf("unverified recovery changed reported failure: %+v", health)
					}
				}
				if err := fixture.controller.verifyStream(t.Context(), queuedWork{GVK: request.GVK, Namespace: request.Namespace}); err != nil {
					t.Fatal(err)
				}
				workload, err := fixture.controller.discovery.Resolve(t.Context(), objectReference(pod, runtimePodGVK))
				if err != nil || workload.Root().UID != owner.GetUID() {
					t.Fatalf("recovered owner = %+v, err = %v", workload.Root(), err)
				}
				if !fixture.controller.Ready() {
					t.Fatal("verified recovered coverage is not ready")
				}
				view, record := invariantPublication(fixture, peer.GetUID())
				row, exists := record.Pod("peer-pod")
				mapping, _ := view.Valid(row.Mapping)
				status, _ := view.Valid(record.Status)
				creation, _ := view.Valid(record.Creation)
				if !exists || !mapping || !status || !creation {
					t.Fatal("optional owner failure invalidated unrelated root fields")
				}
				count := 0
				for _, health := range view.Health {
					if health.Ref.Kind == "Training" && health.Ref.Namespace == "team" {
						count++
						if !health.Healthy || health.Reason != store.ReasonNone {
							t.Fatalf("recovered health = %+v", health)
						}
					}
				}
				if count != 1 {
					t.Fatalf("recovered resource health rows = %d", count)
				}
				fixture.controller.mu.Lock()
				remaining := len(fixture.controller.reader.mappingErrors)
				fixture.controller.mu.Unlock()
				if remaining != 0 {
					t.Fatalf("recovery retained %d mapping errors", remaining)
				}
				mapper.failed.Store(true)
				if err := fixture.controller.ensureCoverage(t.Context(), request); !errors.Is(err, failure.want) {
					t.Fatalf("established coverage hid mapping failure: %v", err)
				}
				if _, err := fixture.controller.discovery.Resolve(t.Context(), objectReference(pod, runtimePodGVK)); !errors.Is(err, failure.want) {
					t.Fatalf("established owner hid mapping failure: %v", err)
				}
				view, record = invariantPublication(fixture, peer.GetUID())
				if valid, _ := view.Valid(record.Status); !valid {
					t.Fatal("established owner failure invalidated unrelated root status")
				}
			})
		}
	}
}

func TestRuntimeMappingFailureRemovedWithNamespace(t *testing.T) {
	root := runtimeJob("peer", "peer-root")
	fixture := startConnected(t, []*unstructured.Unstructured{root}, nil, "")
	awaitRuntime(t, fixture.controller.Ready)
	fixture.controller.queue.ShutDownWithDrain()
	request := discovery.CoverageRequest{GVK: schema.GroupVersionKind{Group: "absent.example.com", Version: "v1", Kind: "Missing"}, Namespace: "team"}
	if err := fixture.controller.ensureCoverage(t.Context(), request); !errors.Is(err, discovery.ErrUnavailable) {
		t.Fatalf("mapping failure = %v", err)
	}
	fixture.controller.mu.Lock()
	fixture.controller.removeNamespaceLocked("team")
	remaining := len(fixture.controller.reader.mappingErrors)
	fixture.controller.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("excluded namespace retained %d mapping errors", remaining)
	}
	for _, health := range fixture.store.Capture().Health {
		if health.Ref.Namespace == "team" {
			t.Fatalf("excluded namespace retained health: %+v", health)
		}
	}
}
