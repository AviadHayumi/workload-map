// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// RayService returns the built-in Karta for RayService.
func RayService() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "ray-io-rayservice-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "workerGroups",
				Expression: `([dyn(object[?"spec"][?"rayClusterConfig"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0]`,
			},
			{
				Name:       "workerReplicas",
				Expression: `variables.workerGroups.map(x, dyn(x[?"replicas"].orValue(dyn(null)))).filter(v, v != null && v != false)`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "rayservice",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "ray.io",
					Version: "v1",
					Kind:    "RayService",
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
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
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Ready",
										Status: ptr.To("False"),
										Reason: ptr.To("Initializing"),
									},
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Ready",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Ready",
										Status: ptr.To("False"),
										Reason: ptr.To("InitializingTimeout"),
									},
								},
							},
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Ready",
										Status: ptr.To("False"),
										Reason: ptr.To("ValidationFailed"),
									},
								},
							},
						},
						Degraded: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Ready",
										Status: ptr.To("False"),
										Reason: ptr.To("ZeroServeEndpoints"),
									},
								},
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "head",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("rayservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"rayClusterConfig"][?"headGroupSpec"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/rayClusterConfig/headGroupSpec/template"),
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
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"ray.io/node-type"].orValue(dyn(null))`,
							},
							Value: ptr.To("head"),
						},
					},
				},
				{
					Name: "worker",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("rayservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"rayClusterConfig"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"template"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/rayClusterConfig/workerGroupSpecs/" + string(index) + "/template"`,
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "(variables.workerReplicas.size() > 0 ? variables.workerReplicas : [dyn(1)])",
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/rayClusterConfig/workerGroupSpecs/" + string(index) + "/replicas"`,
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `([dyn(object[?"spec"][?"rayClusterConfig"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"groupName"].orValue(dyn(null)))`,
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"ray.io/node-type"].orValue(dyn(null))`,
							},
							Value: ptr.To("worker"),
						},
						ComponentInstanceSelector: &v1alpha2.ComponentInstanceSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"ray.io/group"].orValue(dyn(null))`,
							},
						},
					},
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "service",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "head",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"ray.io/cluster"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "worker",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"ray.io/cluster"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
