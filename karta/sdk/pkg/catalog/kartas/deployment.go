// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Deployment returns the built-in Karta for the apps/v1 Deployment workload.

func Deployment() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "apps-deployment-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "specReplicas",
				Expression: `([dyn(object[?"spec"][?"replicas"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "deployment",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "Deployment",
				},
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
										Type:   "Progressing",
										Status: ptr.To("True"),
										Reason: ptr.To("NewReplicaSetCreated"),
									},
								},
							},
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Progressing",
										Status: ptr.To("True"),
									},
									{
										Type:   "Available",
										Status: ptr.To("False"),
									},
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Progressing",
										Status: ptr.To("True"),
										Reason: ptr.To("NewReplicaSetAvailable"),
									},
								},
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Progressing",
										Status: ptr.To("False"),
										Reason: ptr.To("ProgressDeadlineExceeded"),
									},
								},
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "replicaset",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "ReplicaSet",
					},
					OwnerRef: ptr.To("deployment"),
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{},
	}}
}
