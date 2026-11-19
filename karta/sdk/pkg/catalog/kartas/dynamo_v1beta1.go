// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// DynamoV1beta1 returns the built-in Karta for DynamoGraphDeployment.
func DynamoV1beta1() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "nvidia-com-dynamographdeployment-v1beta1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "components",
				Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "dynamographdeployment",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "nvidia.com",
					Version: "v1beta1",
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
					Name:     "component",
					OwnerRef: ptr.To("dynamographdeployment"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"podTemplate"][?"spec"][?"schedulerName"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/components/" + string(index) + "/podTemplate/spec/schedulerName"`,
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"podTemplate"][?"metadata"][?"labels"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/components/" + string(index) + "/podTemplate/metadata/labels"`,
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"podTemplate"][?"metadata"][?"annotations"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/components/" + string(index) + "/podTemplate/metadata/annotations"`,
								},
							},
							ResourceClaims: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"podTemplate"][?"spec"][?"resourceClaims"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/components/" + string(index) + "/podTemplate/spec/resourceClaims"`,
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"podTemplate"][?"spec"][?"affinity"][?"podAffinity"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/components/" + string(index) + "/podTemplate/spec/affinity/podAffinity"`,
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"podTemplate"][?"spec"][?"affinity"][?"nodeAffinity"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/components/" + string(index) + "/podTemplate/spec/affinity/nodeAffinity"`,
								},
							},
							Container: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, (([dyn(x[?"podTemplate"][?"spec"][?"containers"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].filter(c, c[?"name"].orValue("") == "main") + [null])[0])`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `[variables.components[index].podTemplate.spec.containers]
  .map(containers,
    lists.range(containers.size())
      .filter(i, containers[i].?name.orValue("") == "main")
  )
  .map(matches,
    matches.size() == 1
      ? "/spec/components/" + string(index)
        + "/podTemplate/spec/containers/" + string(matches[0])
      : dyn(null)
  )[0]`,
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"podTemplate"][?"spec"][?"priorityClassName"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `"/spec/components/" + string(index) + "/podTemplate/spec/priorityClassName"`,
								},
							},
							Image: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, ((([dyn(x[?"podTemplate"][?"spec"][?"containers"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].filter(c, c[?"name"].orValue("") == "main") + [{}])[0])[?"image"].orValue(dyn(null)))`,
								},
								Write: &v1alpha2.WriteAccessor{
									PathExpression: `[variables.components[index].podTemplate.spec.containers]
  .map(containers,
    lists.range(containers.size())
      .filter(i, containers[i].?name.orValue("") == "main")
  )
  .map(matches,
    matches.size() == 1
      ? "/spec/components/" + string(index)
        + "/podTemplate/spec/containers/" + string(matches[0])
        + "/image"
      : dyn(null)
  )[0]`,
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.components.map(x, ([dyn(x.?replicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0] * ([dyn(x.?multinode.?nodeCount.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0])",
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `([dyn(object[?"spec"][?"components"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"name"].orValue(dyn(null)))`,
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
					Version: "v1beta1",
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
						Name: "component",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "component",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"nvidia.com/dynamo-component"].orValue(dyn(null)).lowerAscii()`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
