// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"errors"
	"maps"
	"testing"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// These owner-chain fixtures preserve the scenarios from the former registry
// tests. They exercise discovery without installing the named operators.
func TestCatalogOwnerChainFixturesUseOutermostRoot(t *testing.T) {
	knative := []schema.GroupVersionKind{
		{Group: "serving.knative.dev", Version: "v1", Kind: "Service"},
		{Group: "serving.knative.dev", Version: "v1", Kind: "Configuration"},
		{Group: "serving.knative.dev", Version: "v1", Kind: "Revision"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "apps", Version: "v1", Kind: "ReplicaSet"},
	}
	for _, tc := range []struct {
		name               string
		root, intermediate *v1alpha1.Karta
		chain              []schema.GroupVersionKind
	}{
		{"knative", kartas.KnativeServing(), kartas.Deployment(), knative},
		{"kserve serverless", kartas.KServe(), kartas.KnativeServing(), append([]schema.GroupVersionKind{
			{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		}, knative...)},
		{"rayjob", kartas.Rayjob(), kartas.Raycluster(), []schema.GroupVersionKind{
			{Group: "ray.io", Version: "v1", Kind: "RayJob"},
			{Group: "ray.io", Version: "v1", Kind: "RayCluster"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, withRootDefinition := range []bool{true, false} {
				name := "generic root"
				if withRootDefinition {
					name = "selected root"
				}
				t.Run(name, func(t *testing.T) {
					inventory := testDefinition(tc.intermediate)
					if withRootDefinition {
						maps.Copy(inventory, testDefinition(tc.root))
					}
					d := New(nil, inventory)
					root := testRef(tc.chain[0], tc.chain[0].Kind)
					observe(d, root)
					parent := root
					for _, kind := range tc.chain[1:] {
						child := testRef(kind, kind.Kind)
						observe(d, child, parent)
						parent = child
					}
					pod := testRef(podKind, "pod")
					observe(d, pod, parent)
					workload := resolveTest(t, d, pod)
					if workload.Root() != root || len(workload.Path) != len(tc.chain)+1 || !d.IsCurrent(workload.State()) {
						t.Fatalf("owner chain stopped before outermost root: %+v", workload)
					}
					selected := workload.KartaWorkload()
					if !withRootDefinition {
						if selected != nil {
							t.Fatal("generic root inherited an intermediate Karta")
						}
					} else if selected == nil || selected.Selection().Definition.Name != tc.root.Name {
						t.Fatal("outermost root did not select its own Karta")
					}
				})
			}
		})
	}
}

func TestLWSUnmatchedPodReturnsEvaluationError(t *testing.T) {
	d, workload, pod, _ := evaluationFixture(t, kartas.LWS(), map[string]any{}, nil, nil)
	mapping, err := workload.PodMapping(t.Context(), pod, d.NewEvaluationBatch())
	if !errors.Is(err, ErrEvaluation) {
		t.Fatalf("Pod without a matching LWS selector: mapping=%+v, err=%v", mapping, err)
	}
	if mapping.Component != "" || mapping.Instance != "" || mapping.Group != "" || d.IsCurrent(mapping.State()) {
		t.Fatalf("failed mapping fabricated valid coordinates: %+v", mapping)
	}
}
