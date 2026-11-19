// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// NIMService returns the built-in Karta for NIMService.
func NIMService() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "apps-nvidia-com-nimservice-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "specReplicas",
				Expression: `([dyn(object[?"spec"][?"replicas"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
			{
				Name:       "statusState",
				Expression: `([dyn(object.?status.?state.orValue(dyn(null)))].filter(v, v != null && v != false) + [""])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "nimservice",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "apps.nvidia.com",
					Version: "v1alpha1",
					Kind:    "NIMService",
				},
				SpecDefinition: &v1alpha2.SpecDefinition{
					FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
						SchedulerName: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"schedulerName"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/schedulerName"),
							},
						},
						Labels: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"labels"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/labels"),
							},
						},
						Annotations: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"annotations"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/annotations"),
							},
						},
						Resources: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"resources"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/resources"),
							},
						},
						PodAffinity: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/affinity/podAffinity"),
							},
						},
						NodeAffinity: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/affinity/nodeAffinity"),
							},
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
					},
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByPhase: "NotReady",
							},
							{
								ByPhase: "Pending",
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Ready",
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
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "service",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "nimservice",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
