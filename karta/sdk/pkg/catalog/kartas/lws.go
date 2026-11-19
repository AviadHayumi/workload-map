// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// LWS returns the built-in Karta for LeaderWorkerSet.
func LWS() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "leaderworkerset-x-k8s-io-leaderworkerset-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "statusReplicas",
				Expression: "([dyn(object.?status.?replicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "specReplicas",
				Expression: `([dyn(object[?"spec"][?"replicas"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
			{
				Name:       "specReplicasForWorkers",
				Expression: "([dyn(object.?spec.?replicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]",
			},
			{
				Name:       "specLeaderWorkerTemplateSize",
				Expression: "([dyn(object.?spec.?leaderWorkerTemplate.?size.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]",
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "leaderworkerset",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "leaderworkerset.x-k8s.io",
					Version: "v1",
					Kind:    "LeaderWorkerSet",
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
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
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Progressing",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Available",
										Status: ptr.To("True"),
									},
								},
							},
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"replicas"].orValue(0) > 0 && object[?"status"][?"readyReplicas"].orValue(dyn(null)) == object[?"status"][?"replicas"].orValue(dyn(null)) && object[?"status"][?"updatedReplicas"].orValue(dyn(null)) == object[?"status"][?"replicas"].orValue(dyn(null))`,
								},
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Available",
										Status: ptr.To("False"),
									},
									{
										Type:   "Progressing",
										Status: ptr.To("False"),
									},
									{
										Type:   "UpdateInProgress",
										Status: ptr.To("False"),
									},
								},
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name:     "group",
					OwnerRef: ptr.To("leaderworkerset"),
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"leaderWorkerTemplate"][?"size"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/leaderWorkerTemplate/size"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ReplicaSelector: &v1alpha2.ReplicaSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue(dyn(null))`,
							},
						},
					},
				},
				{
					Name: "leader",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("group"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"leaderWorkerTemplate"][?"leaderTemplate"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/leaderWorkerTemplate/leaderTemplate"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.specReplicas",
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/worker-index"].orValue(dyn(null))`,
							},
							Value: ptr.To("0"),
						},
					},
				},
				{
					Name: "worker",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("group"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"leaderWorkerTemplate"][?"workerTemplate"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/leaderWorkerTemplate/workerTemplate"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.specReplicasForWorkers * (variables.specLeaderWorkerTemplateSize - 1)",
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"annotations"][?"leaderworkerset.sigs.k8s.io/leader-name"].orValue(dyn(null))`,
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
						Name: "group",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "group",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/name"].orValue(dyn(null))`,
									`([dyn(object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue(dyn(null)))].filter(v, v != null && v != false) + ["0"])[0]`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
