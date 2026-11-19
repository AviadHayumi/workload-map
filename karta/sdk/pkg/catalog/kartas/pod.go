// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Pod returns the built-in Karta for Pod.
func Pod() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "core-pod-v1"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "pod",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    "Pod",
				},
				SpecDefinition: &v1alpha2.SpecDefinition{
					PodTemplateSpec: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: "object",
						},
						Write: &v1alpha2.WriteAccessor{
							Path: ptr.To(""),
						},
					},
				},
				ScaleDefinition: &v1alpha2.ScaleDefinition{
					Replicas: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: "1",
						},
					},
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					PhaseDefinition: &v1alpha2.PhaseDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"phase"].orValue(dyn(null))`,
						},
					},
					ConditionsDefinition: &v1alpha2.ConditionsDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"conditions"].orValue(dyn(null))`,
						},
						TypeFieldName:    "type",
						StatusFieldName:  "status",
						MessageFieldName: ptr.To("message"),
						ReasonFieldName:  ptr.To("reason"),
					},
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Pending",
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Running",
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Succeeded",
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Failed",
							},
						},
					},
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{},
	}}
}
