// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// KnativeServing returns the built-in Karta for Service.
func KnativeServing() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "serving-knative-dev-service-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "minScale",
				Expression: `([dyn(object[?"spec"][?"template"][?"metadata"][?"annotations"][?"autoscaling.knative.dev/min-scale"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "knativeservice",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "serving.knative.dev",
					Version: "v1",
					Kind:    "Service",
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
										Type:   "Ready",
										Status: ptr.To("Unknown"),
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
									},
								},
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "revision",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "serving.knative.dev",
						Version: "v1",
						Kind:    "Revision",
					},
					OwnerRef: ptr.To("knativeservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"template"][?"metadata"][?"annotations"][?"autoscaling.knative.dev/min-scale"].orValue(dyn(null)) == null ? 1 : int(object[?"spec"][?"template"][?"metadata"][?"annotations"][?"autoscaling.knative.dev/min-scale"].orValue(dyn(null)))`,
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"template"][?"metadata"][?"annotations"][?"autoscaling.knative.dev/max-scale"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"template"][?"metadata"][?"annotations"][?"autoscaling.knative.dev/max-scale"].orValue(dyn(null)) == null ? dyn(null) : int(object[?"spec"][?"template"][?"metadata"][?"annotations"][?"autoscaling.knative.dev/max-scale"].orValue(dyn(null)))`,
							},
						},
					},
				},
			},
			AdditionalChildKinds: []v1alpha2.GroupVersionKind{
				{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "revision",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "revision",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"serving.knative.dev/revision"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
