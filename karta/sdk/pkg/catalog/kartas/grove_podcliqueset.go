// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// GrovePodCliqueSet returns the built-in Karta for PodCliqueSet.
func GrovePodCliqueSet() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "grove-io-podcliqueset-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "cliques",
				Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0]`,
			},
			{
				Name:       "scalingGroups",
				Expression: `([dyn(object[?"spec"][?"template"][?"podCliqueScalingGroups"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0]`,
			},
			{
				Name:       "cliqueReplicas",
				Expression: "variables.cliques.map(c, c.?spec.?replicas.orValue(dyn(null)) == null ? 1 : dyn(c.spec.replicas))",
			},
			{
				Name:       "scalingGroupReplicas",
				Expression: "variables.scalingGroups.map(g, g.?replicas.orValue(dyn(null)) == null ? 1 : dyn(g.replicas))",
			},
			{
				Name:       "statusAvailableReplicas",
				Expression: "([dyn(object.?status.?availableReplicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "specReplicas",
				Expression: "([dyn(object.?spec.?replicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "specReplicasForWorkers",
				Expression: "([dyn(object.?spec.?replicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]",
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "podcliqueset",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "grove.io",
					Version: "v1alpha1",
					Kind:    "PodCliqueSet",
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
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"replicas"].orValue(0) > 0 && object[?"status"][?"availableReplicas"].orValue(0) < object[?"spec"][?"replicas"].orValue(0)`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"availableReplicas"].orValue(0) >= object[?"spec"][?"replicas"].orValue(0)`,
								},
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "TopologyLevelsUnavailable",
										Status: ptr.To("True"),
									},
								},
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "clique",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "grove.io",
						Version: "v1alpha1",
						Kind:    "PodClique",
					},
					OwnerRef: ptr.To("podcliqueset"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"podSpec"][?"schedulerName"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/podSpec/schedulerName"`,
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"labels"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/labels"`,
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"annotations"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/annotations"`,
								},
							},
							ResourceClaims: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"podSpec"][?"resourceClaims"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/podSpec/resourceClaims"`,
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"podSpec"][?"affinity"][?"podAffinity"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/podSpec/affinity/podAffinity"`,
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"podSpec"][?"affinity"][?"nodeAffinity"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/podSpec/affinity/nodeAffinity"`,
								},
							},
							Containers: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"podSpec"][?"containers"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/podSpec/containers"`,
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"podSpec"][?"priorityClassName"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/podSpec/priorityClassName"`,
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.cliqueReplicas.map(v, variables.specReplicasForWorkers * v)",
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"autoScalingConfig"][?"minReplicas"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/autoScalingConfig/minReplicas"`,
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"spec"][?"autoScalingConfig"][?"maxReplicas"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/template/cliques/" + string(index) + "/spec/autoScalingConfig/maxReplicas"`,
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `([dyn(object[?"spec"][?"template"][?"cliques"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"name"].orValue(dyn(null)))`,
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentInstanceSelector: &v1alpha2.ComponentInstanceSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"grove.io/podclique"].orValue(dyn(null))`,
							},
						},
						ReplicaSelector: &v1alpha2.ReplicaSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"grove.io/podcliqueset-replica-index"].orValue(dyn(null))`,
							},
						},
					},
				},
				{
					Name: "scalinggroup",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "grove.io",
						Version: "v1alpha1",
						Kind:    "PodCliqueScalingGroup",
					},
					OwnerRef: ptr.To("podcliqueset"),
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.scalingGroupReplicas.map(v, variables.specReplicasForWorkers * v)",
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"template"][?"podCliqueScalingGroups"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"scaleConfig"][?"minReplicas"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/template/podCliqueScalingGroups/" + string(index) + "/scaleConfig/minReplicas"`,
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"template"][?"podCliqueScalingGroups"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"scaleConfig"][?"maxReplicas"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/template/podCliqueScalingGroups/" + string(index) + "/scaleConfig/maxReplicas"`,
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `([dyn(object[?"spec"][?"template"][?"podCliqueScalingGroups"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"name"].orValue(dyn(null)))`,
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentInstanceSelector: &v1alpha2.ComponentInstanceSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"grove.io/podcliquescalinggroup"].orValue(dyn(null))`,
							},
						},
						ReplicaSelector: &v1alpha2.ReplicaSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"grove.io/podcliquescalinggroup-replica-index"].orValue(dyn(null))`,
							},
						},
					},
				},
			},
			AdditionalChildKinds: []v1alpha2.GroupVersionKind{
				{
					Group:   "grove.io",
					Version: "v1alpha1",
					Kind:    "PodClique",
				},
				{
					Group:   "grove.io",
					Version: "v1alpha1",
					Kind:    "PodCliqueScalingGroup",
				},
				{
					Group:   "scheduler.grove.io",
					Version: "v1alpha1",
					Kind:    "PodGang",
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "podcliqueset-replica",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "clique",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/part-of"].orValue(dyn(null))`,
									`object[?"metadata"][?"labels"][?"grove.io/podcliqueset-replica-index"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "scalinggroup",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/part-of"].orValue(dyn(null))`,
									`object[?"metadata"][?"labels"][?"grove.io/podcliqueset-replica-index"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
