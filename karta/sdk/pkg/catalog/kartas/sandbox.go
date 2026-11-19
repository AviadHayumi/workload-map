// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Sandbox returns the built-in Karta for Sandbox.
func Sandbox() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "agents-x-k8s-io-sandbox-v1beta1"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "sandbox",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "agents.x-k8s.io",
					Version: "v1beta1",
					Kind:    "Sandbox",
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
									},
									{
										Type:   "Suspended",
										Status: ptr.To("False"),
									},
								},
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"operatingMode"].orValue("Running") != "Suspended"`,
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
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"operatingMode"].orValue("Running") != "Suspended" && !object.?status.?conditions.orValue([]).exists(c, c.type == "Suspended" && c.status == "True")`,
								},
							},
						},
						Suspended: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Suspended",
										Status: ptr.To("True"),
									},
								},
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"operatingMode"].orValue(dyn(null)) == "Suspended"`,
								},
							},
						},
						Suspending: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"operatingMode"].orValue(dyn(null)) == "Suspended" && !object.?status.?conditions.orValue([]).exists(c, c.type == "Suspended" && c.status == "True")`,
								},
							},
						},
						Resuming: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Suspended",
										Status: ptr.To("True"),
									},
								},
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"operatingMode"].orValue("Running") != "Suspended"`,
								},
							},
						},
					},
				},
				SuspendDefinition: &v1alpha2.SuspendDefinition{
					SuspendActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/operatingMode"),
							},
							Value: `"Suspended"`,
						},
					},
					ResumeActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/operatingMode"),
							},
							Value: `"Running"`,
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "agent",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("sandbox"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"podTemplate"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/podTemplate"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"agents.x-k8s.io/sandbox-name-hash"].orValue(dyn(null))`,
							},
						},
					},
				},
				{
					Name: "companion",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("sandbox"),
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object.?metadata.?labels[?"agents.x-k8s.io/sandbox-name-hash"].orValue(dyn(null)) == null ? (object.?metadata.?ownerReferences.orValue([]).filter(r, r.kind == "Sandbox" && r.apiVersion.startsWith("agents.x-k8s.io/")).map(r, r.uid) + [null])[0] : null`,
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
						Name: "sandbox",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "agent",
								GroupByExpressions: []string{
									`(object.?metadata.?ownerReferences.orValue([]).filter(r, r.kind == "Sandbox" && r.apiVersion.startsWith("agents.x-k8s.io/")).map(r, r.uid) + [null])[0]`,
								},
							},
							{
								ComponentName: "companion",
								GroupByExpressions: []string{
									`(object.?metadata.?ownerReferences.orValue([]).filter(r, r.kind == "Sandbox" && r.apiVersion.startsWith("agents.x-k8s.io/")).map(r, r.uid) + [null])[0]`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
