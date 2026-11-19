// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package catalog_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

// The TrainJob definition reads its effective values through the trainingRuntime reference:
// the TrainJob's override wins, the runtime's base fills the gaps.
var _ = Describe("TrainJob karta with a resolved runtime", func() {
	ctx := context.Background()

	runtime := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "trainer.kubeflow.org/v1alpha1",
		"kind":       "ClusterTrainingRuntime",
		"metadata":   map[string]any{"name": "torch-distributed"},
		"spec": map[string]any{
			"mlPolicy": map[string]any{"numNodes": int64(1), "torch": map[string]any{}},
			"template": map[string]any{"spec": map[string]any{"replicatedJobs": []any{
				map[string]any{
					"name": "dataset-initializer",
					"template": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
						"containers": []any{map[string]any{
							"name":  "dataset-initializer",
							"image": "initializer:1",
						}},
					}}}},
				},
				map[string]any{
					"name": "node",
					"template": map[string]any{
						"metadata": map[string]any{"labels": map[string]any{
							"trainer.kubeflow.org/trainjob-ancestor-step": "trainer",
						}},
						"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
							"containers": []any{
								map[string]any{"name": "sidecar", "image": "sidecar:1"},
								map[string]any{
									"name":  "node",
									"image": "pytorch/pytorch:2.13.0-cuda13.0-cudnn9-runtime",
								},
							},
						}}},
					},
				},
			}}},
		},
	}}

	trainJob := func(trainer map[string]any) *unstructured.Unstructured {
		spec := map[string]any{"runtimeRef": map[string]any{"name": "torch-distributed"}}
		if trainer != nil {
			spec["trainer"] = trainer
		}

		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "trainer.kubeflow.org/v1alpha1",
			"kind":       "TrainJob",
			"metadata":   map[string]any{"name": "fine-tune", "namespace": "team-a"},
			"spec":       spec,
		}}
	}

	factoryFor := func(workload *unstructured.Unstructured) *resource.ComponentFactory {
		resolved := references.ResolvedReferences{"trainingRuntime": {Object: runtime}}

		return resource.NewComponentFactoryFromObject(kartas.TrainJob(), workload, resource.WithReferences(resolved))
	}

	It("falls back to the runtime's image and node count when the TrainJob overrides nothing", func() {
		root, err := factoryFor(trainJob(nil)).GetRootComponent()
		Expect(err).NotTo(HaveOccurred())

		fragments, err := root.GetExtractedInstances(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(fragments).To(HaveLen(1))
		for _, summary := range fragments {
			Expect(summary.FragmentedPodSpec.Image).To(Equal("pytorch/pytorch:2.13.0-cuda13.0-cudnn9-runtime"))
			Expect(summary.Scale.Replicas).To(HaveValue(Equal(int32(1))))
		}
	})

	It("prefers the TrainJob's overrides over the runtime's base", func() {
		root, err := factoryFor(trainJob(map[string]any{
			"image":    "registry.example.com/custom:1",
			"numNodes": int64(4),
		})).GetRootComponent()
		Expect(err).NotTo(HaveOccurred())

		fragments, err := root.GetExtractedInstances(ctx)
		Expect(err).NotTo(HaveOccurred())
		for _, summary := range fragments {
			Expect(summary.FragmentedPodSpec.Image).To(Equal("registry.example.com/custom:1"))
			Expect(summary.Scale.Replicas).To(HaveValue(Equal(int32(4))))
		}
	})
})
