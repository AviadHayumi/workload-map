// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package recorder

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

func TestObserveCapturesClusterScopedRuntime(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "trainer.kubeflow.org", Version: "v1alpha1", Kind: "ClusterTrainingRuntime"}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
	mapper.Add(gvk, meta.RESTScopeRoot)
	runtime := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "metadata": map[string]any{"name": "example-runtime"}, "spec": map[string]any{"image": "example:v1"}}}
	clusterClient := fake.NewClientBuilder().WithRESTMapper(mapper).WithRuntimeObjects(runtime).Build()
	rec := New(Config{OutputDir: t.TempDir(), Cluster: Cluster{Client: clusterClient, Namespace: "workload-ns"}}).AddState(api.CompletedStatus, func(*unstructured.Unstructured) bool { return true })
	workload := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob", "metadata": map[string]any{"name": "job", "namespace": "workload-ns"}}}
	flow := NewFlow(rec, "completed", "").Capturing(CapturedReference{GVK: api.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}, Name: "example-runtime"}).Through(Reaches(api.CompletedStatus))
	recording, err := flow.Observe(context.Background(), workload)
	if err != nil {
		t.Fatal(err)
	}
	if len(recording.Events) != 1 || len(recording.Events[0].References) != 1 {
		t.Fatalf("references were not captured: %#v", recording.Events)
	}
	reader := newReader(*recording)
	if !reader.Next() {
		t.Fatal("no captured state")
	}
	references := reader.References()
	references[0].Object["spec"] = nil
	if reader.References()[0].Object["spec"] == nil {
		t.Fatal("reference result aliases recording")
	}
	if got := reader.References()[0].GetNamespace(); got != "" {
		t.Fatalf("cluster runtime got workload namespace: %q", got)
	}
}
