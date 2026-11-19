// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Dynamo returns the built-in Karta for DynamoGraphDeployment.
func Dynamo() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "nvidia-com-dynamographdeployment-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "services",
				Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0]`,
			},
			{
				Name:       "statusState",
				Expression: `([dyn(object.?status.?state.orValue(dyn(null)))].filter(v, v != null && v != false) + [""])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "dynamographdeployment",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "nvidia.com",
					Version: "v1alpha1",
					Kind:    "DynamoGraphDeployment",
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					PhaseDefinition: &v1alpha2.PhaseDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"state"].orValue(dyn(null))`,
						},
					},
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByPhase: "initializing",
							},
							{
								ByPhase: "pending",
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "successful",
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByPhase: "failed",
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name:     "service",
					OwnerRef: ptr.To("dynamographdeployment"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"extraPodSpec"][?"schedulerName"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/extraPodSpec/schedulerName"`,
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"labels"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/labels"`,
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"annotations"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/annotations"`,
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"resources"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/resources"`,
								},
							},
							ResourceClaims: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"extraPodSpec"][?"resourceClaims"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/extraPodSpec/resourceClaims"`,
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"extraPodSpec"][?"affinity"][?"podAffinity"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/extraPodSpec/affinity/podAffinity"`,
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"extraPodSpec"][?"affinity"][?"nodeAffinity"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/extraPodSpec/affinity/nodeAffinity"`,
								},
							},
							Container: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"extraPodSpec"][?"mainContainer"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/extraPodSpec/mainContainer"`,
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"extraPodSpec"][?"priorityClassName"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/extraPodSpec/priorityClassName"`,
								},
							},
							Image: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"extraPodSpec"][?"mainContainer"][?"image"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/services/" + instance.replace("~", "~0").replace("/", "~1") + "/extraPodSpec/mainContainer/image"`,
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.services.map(k, string(k)).sort().map(k, ([dyn(variables.services[k].?replicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0] * ([dyn(variables.services[k].?multinode.?nodeCount.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0])",
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"autoscaling"][?"minReplicas"].orValue(dyn(null)))`,
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort().map(k, object.spec.services[k][?"autoscaling"][?"maxReplicas"].orValue(dyn(null)))`,
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `([dyn(object[?"spec"][?"services"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, k).sort()`,
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentInstanceSelector: &v1alpha2.ComponentInstanceSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"nvidia.com/dynamo-component"].orValue(dyn(null))`,
							},
						},
						ReplicaSelector: &v1alpha2.ReplicaSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"grove.io/podcliquescalinggroup-replica-index"].orValue(object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue(dyn(null)))`,
							},
						},
					},
				},
			},
			AdditionalChildKinds: []v1alpha2.GroupVersionKind{
				{
					Group:   "nvidia.com",
					Version: "v1alpha1",
					Kind:    "DynamoComponentDeployment",
				},
				{
					Group:   "leaderworkerset.x-k8s.io",
					Version: "v1",
					Kind:    "LeaderWorkerSet",
				},
				{
					Group:   "scheduler.grove.io",
					Version: "v1alpha1",
					Kind:    "PodGang",
				},
				{
					Group:   "grove.io",
					Version: "v1alpha1",
					Kind:    "PodClique",
				},
				{
					Group:   "grove.io",
					Version: "v1alpha1",
					Kind:    "PodCliqueSet",
				},
				{
					Group:   "grove.io",
					Version: "v1alpha1",
					Kind:    "PodCliqueScalingGroup",
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
								ComponentName: "service",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"nvidia.com/dynamo-component"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
