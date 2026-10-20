// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package scale

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// The HTTP fixture's expected phases and component coordinates come from the
// public catalog and SDK. This test is not a runtime capacity measurement.
func TestRuntimeFixtureCatalogExpectations(t *testing.T) {
	ctx := context.Background()
	gvk := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	karta, err := catalog.Get(gvk)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := instructions.NewStructureSummary(karta)
	if err != nil {
		t.Fatal(err)
	}
	for _, failed := range []bool{false, true} {
		status := map[string]any{"active": int64(10), "ready": int64(10)}
		want := v1alpha1.RunningStatus
		if failed {
			status = map[string]any{"active": int64(0), "ready": int64(0), "conditions": []any{map[string]any{"type": "Failed", "status": "True"}}}
			want = v1alpha1.FailedStatus
		}
		job := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "batch/v1", "kind": "Job",
			"metadata": map[string]any{"name": "job-0", "namespace": "scale", "uid": "job-0-g0"},
			"spec":     map[string]any{"parallelism": int64(10), "template": map[string]any{"spec": map[string]any{}}},
			"status":   status,
		}}
		factory := resource.NewComponentFactoryFromObject(karta, job)
		root, err := factory.GetRootComponent()
		if err != nil {
			t.Fatal(err)
		}
		actual, err := root.GetStatus(ctx)
		if err != nil || actual == nil || len(actual.MatchedStatuses) != 1 || actual.MatchedStatuses[0] != want {
			t.Fatalf("failed=%v status=%+v error=%v, want only %s", failed, actual, err, want)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-0-0", Namespace: "scale", Labels: map[string]string{"batch.kubernetes.io/job-name": "job-0"}}}
		querier := resource.NewPodQuerier(pod)
		component, err := instructions.InferPodComponent(ctx, querier, summary)
		if err != nil || component != "job" {
			t.Fatalf("component=%q error=%v, want job", component, err)
		}
		instance, err := instructions.InferPodComponentInstance(ctx, querier, component, factory)
		if err != nil || instance != nil {
			t.Fatalf("instance=%v error=%v, want unnamed instance", instance, err)
		}
		if selector := root.GetPodSelector(); selector != nil && selector.ReplicaSelector != nil {
			t.Fatal("Job unexpectedly defines a replica group selector")
		}
	}
}
