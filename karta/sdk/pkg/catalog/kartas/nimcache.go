// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// NIMCache returns the built-in Karta for NIMCache.
func NIMCache() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "apps-nvidia-com-nimcache-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "nimcache",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "apps.nvidia.com",
					Version: "v1alpha1",
					Kind:    "NIMCache",
				},
				SpecDefinition: &v1alpha2.SpecDefinition{
					FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
						Resources: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `{"requests": ([dyn(object[?"spec"][?"resources"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0]}`,
							},
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
							Expression: `object[?"status"][?"state"].orValue(dyn(null))`,
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
							{
								ByPhase: "NotReady",
							},
							{
								ByPhase: "PVC-Created",
							},
							{
								ByPhase: "Started",
							},
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "NIM_CACHE_JOB_CREATED",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "InProgress",
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Ready",
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "NIM_CACHE_JOB_COMPLETED",
										Status: ptr.To("True"),
									},
								},
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
