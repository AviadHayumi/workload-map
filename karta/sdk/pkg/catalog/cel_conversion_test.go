// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package catalog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

func TestKServeModelCardinality(t *testing.T) {
	for _, tc := range []struct {
		name      string
		models    map[string]any
		wantImage string
		wantError bool
	}{
		{name: "no model", models: map[string]any{}},
		{name: "model with escaped key", models: map[string]any{"model/a~b": map[string]any{"storageUri": "s3://example/model", "image": "ghcr.io/example/model:v1"}}, wantImage: "ghcr.io/example/model:v1"},
		{name: "false storage URI is not a model", models: map[string]any{"model": map[string]any{"storageUri": false}}},
		{name: "ambiguous models", models: map[string]any{"a": map[string]any{"storageUri": "s3://example/a"}, "b": map[string]any{"storageUri": "s3://example/b"}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "serving.kserve.io/v1beta1", "kind": "InferenceService", "spec": map[string]any{"predictor": tc.models}}}
			component, err := resource.NewComponentFactoryFromObject(kartas.KServe(), object).GetComponent("predictor")
			if err != nil {
				t.Fatal(err)
			}
			values, err := component.GetFragmentedPodSpec(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("read error = %v, want error %t", err, tc.wantError)
			}
			if err != nil {
				return
			}
			if tc.wantImage != "" {
				if values[""].Container == nil || values[""].Container.Image != tc.wantImage {
					t.Fatalf("container = %#v", values[""].Container)
				}
			} else if values[""].Container != nil {
				t.Fatalf("unexpected container: %#v", values[""].Container)
			}
		})
	}
}

func TestLLMInferenceServiceReadMatch(t *testing.T) {
	for _, worker := range []bool{false, true} {
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "serving.kserve.io/v1alpha2", "kind": "LLMInferenceService", "spec": map[string]any{"template": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "ghcr.io/example/model:v1"}}}}}}
		if worker {
			if err := unstructured.SetNestedMap(object.Object, map[string]any{"containers": []any{map[string]any{"name": "worker", "image": "ghcr.io/example/worker:v1"}}}, "spec", "worker"); err != nil {
				t.Fatal(err)
			}
		}
		factory := resource.NewComponentFactoryFromObject(kartas.LLMInferenceServiceV1alpha2(), object)
		for _, name := range []string{"main", "leader"} {
			component, err := factory.GetComponent(name)
			if err != nil {
				t.Fatal(err)
			}
			values, err := component.GetPodSpec(context.Background())
			wantPresent := (name == "main") != worker
			if !wantPresent {
				if !errors.Is(err, resource.ErrValueNotFound) {
					t.Fatalf("worker=%t component=%s: error=%v", worker, name, err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != 1 {
				t.Fatalf("worker=%t component=%s: got %d values", worker, name, len(values))
			}

		}
	}
}

func TestRolloutCurrentGenerationAndPause(t *testing.T) {
	for _, tc := range []struct {
		name, phase, observed string
		paused                bool
		want                  v1alpha2.ResourceStatus
	}{
		{name: "healthy current", phase: "Healthy", observed: "3", want: v1alpha2.RunningStatus},
		{name: "stale healthy", phase: "Healthy", observed: "2", want: v1alpha2.InitializingStatus},
		{name: "paused healthy", phase: "Healthy", observed: "3", paused: true, want: v1alpha2.SuspendedStatus},
		{name: "degraded paused", phase: "Degraded", observed: "3", paused: true, want: v1alpha2.DegradedStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"generation": int64(3)}, "spec": map[string]any{"paused": tc.paused}, "status": map[string]any{"phase": tc.phase, "observedGeneration": tc.observed}}}
			component, err := resource.NewComponentFactoryFromObject(kartas.Rollout(), object).GetRootComponent()
			if err != nil {
				t.Fatal(err)
			}
			status, err := component.GetStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(status.MatchedStatuses, []v1alpha2.ResourceStatus{tc.want}) {
				t.Fatalf("statuses=%v want=%v", status.MatchedStatuses, tc.want)
			}
		})
	}
}

func TestKServeGroupingUsesPodLabels(t *testing.T) {
	definition := kartas.KServe()
	summary, err := instructions.NewStructureSummary(definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"predictor", "transformer"} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: component, Labels: map[string]string{"component": component, "serving.kserve.io/inferenceservice": "model"}}}
		querier := resource.NewPodQuerier(pod)
		selected, err := instructions.InferPodComponent(context.Background(), querier, summary)
		if err != nil {
			t.Fatal(err)
		}
		if selected != component {
			t.Fatalf("selected=%s", selected)
		}
		grouping, err := instructions.GetPodGroupingEffectiveComponent(context.Background(), querier, component, summary)
		if err != nil {
			t.Fatal(err)
		}
		if grouping == nil || grouping.PodGroupName != "service" {
			t.Fatalf("group=%#v", grouping)
		}
		keys, err := querier.ExtractGroupKeysFor(context.Background(), *grouping.MemberDefinition)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(keys, []string{"model"}) {
			t.Fatalf("keys=%v", keys)
		}
	}
}

func TestMilvusReferenceMembershipNeedsOwnershipProof(t *testing.T) {
	summary, err := instructions.NewStructureSummary(kartas.Milvus())
	if err != nil {
		t.Fatal(err)
	}
	querier := resource.NewPodQuerier(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}})
	if component, err := instructions.InferPodComponent(context.Background(), querier, summary); err == nil {
		t.Fatalf("unrelated Pod inferred as %s", component)
	}
}
