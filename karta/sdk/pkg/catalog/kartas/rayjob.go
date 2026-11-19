// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Rayjob returns the built-in Karta for RayJob.
func Rayjob() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "ray-io-rayjob-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "workerGroups",
				Expression: `([dyn(object[?"spec"][?"rayClusterSpec"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0]`,
			},
			{
				Name:       "workerReplicas",
				Expression: `variables.workerGroups.map(x, dyn(x[?"replicas"].orValue(dyn(null)))).filter(v, v != null && v != false)`,
			},
			{
				Name:       "statusJobStatus",
				Expression: `([dyn(object.?status.?jobStatus.orValue(dyn(null)))].filter(v, v != null && v != false) + [""])[0]`,
			},
			{
				Name:       "statusJobDeploymentStatus",
				Expression: `([dyn(object.?status.?jobDeploymentStatus.orValue(dyn(null)))].filter(v, v != null && v != false) + [""])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "rayjob",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "ray.io",
					Version: "v1",
					Kind:    "RayJob",
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					PhaseDefinition: &v1alpha2.PhaseDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"jobStatus"].orValue(dyn(null))`,
						},
					},
					ConditionsDefinition: &v1alpha2.ConditionsDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"conditions"].orValue(dyn(null))`,
						},
						TypeFieldName:    "type",
						StatusFieldName:  "status",
						MessageFieldName: ptr.To("message"),
					},
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByPhase: "PENDING",
							},
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"jobDeploymentStatus"].orValue(dyn(null)) == "Initializing"`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "RUNNING",
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "SUCCEEDED",
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "FAILED",
							},
						},
						Suspended: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"jobDeploymentStatus"].orValue(dyn(null)) == "Suspended"`,
								},
							},
						},
					},
				},
				SuspendDefinition: &v1alpha2.SuspendDefinition{
					SuspendActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/suspend"),
							},
							Value: "true",
						},
					},
					ResumeActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/suspend"),
							},
							Value: "false",
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
					OwnerRef: ptr.To("rayjob"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"rayClusterSpec"][?"headGroupSpec"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/rayClusterSpec/headGroupSpec/template"),
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
					OwnerRef: ptr.To("rayjob"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"rayClusterSpec"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"template"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/rayClusterSpec/workerGroupSpecs/" + string(index) + "/template"`,
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "(variables.workerReplicas.size() > 0 ? variables.workerReplicas : [dyn(1)])",
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/rayClusterSpec/workerGroupSpecs/" + string(index) + "/replicas"`,
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"rayClusterSpec"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"minReplicas"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/rayClusterSpec/workerGroupSpecs/" + string(index) + "/minReplicas"`,
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"rayClusterSpec"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"maxReplicas"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/rayClusterSpec/workerGroupSpecs/" + string(index) + "/maxReplicas"`,
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `([dyn(object[?"spec"][?"rayClusterSpec"][?"workerGroupSpecs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"groupName"].orValue(dyn(null)))`,
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
						Name: "job",
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
