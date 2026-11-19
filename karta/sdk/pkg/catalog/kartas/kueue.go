// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// KueueWorkload returns the admission, readiness and activity view of a Kueue Workload.
// Running requires Kueue to report PodsReady, through waitForPodsReady or its
// workload-level readiness feature. Admission alone does not mean that Pods
// have started. Missing readiness remains Undefined.
//
// Completed follows Kueue's Finished/Succeeded condition. For Pod groups this
// means the group finished, not that every Pod or application task succeeded.
// Failed maps only explicit execution or permanent start failures.
// Workload podSets describe admission requests. They are not mutable templates
// for the Pods, and the Workload does not own those Pods.
// Suspend deactivates and evicts the Workload. Resume allows admission again;
// recreating deleted Pods requires their controller. Finished work is not restarted.

func KueueWorkload() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "kueue-x-k8s-io-workload-v1beta2"}, Spec: v1alpha2.KartaSpec{
		Experimental: true,
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "workload",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "kueue.x-k8s.io",
					Version: "v1beta2",
					Kind:    "Workload",
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					ConditionsDefinition: &v1alpha2.ConditionsDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"conditions"].orValue([])`,
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
										Type:   "Admitted",
										Status: ptr.To("False"),
									},
								},
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object.?spec.?active.orValue(dyn(null)) != false && !object.?status.?conditions.orValue([]).exists(c, c.type == "Finished" && c.status == "True")`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Admitted",
										Status: ptr.To("True"),
									},
									{
										Type:   "PodsReady",
										Status: ptr.To("True"),
									},
								},
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object.?spec.?active.orValue(dyn(null)) != false && !object.?status.?conditions.orValue([]).exists(c, c.type in ["Finished", "Evicted", "WaitingForReplacementPods"] && c.status == "True")`,
								},
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Finished",
										Status: ptr.To("True"),
										Reason: ptr.To("Succeeded"),
									},
								},
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Finished",
										Status: ptr.To("True"),
										Reason: ptr.To("Failed"),
									},
								},
							},
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Finished",
										Status: ptr.To("True"),
										Reason: ptr.To("FailedToStart"),
									},
								},
							},
						},
						Suspended: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object.?spec.?active.orValue(dyn(null)) == false && !object.?status.?conditions.orValue([]).exists(c, c.type == "Finished" && c.status == "True") && !object.?status.?conditions.orValue([]).exists(c, c.type == "QuotaReserved" && c.status == "True")`,
								},
							},
						},
					},
				},
				SuspendDefinition: &v1alpha2.SuspendDefinition{
					SuspendActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/active"),
							},
							Value: "false",
						},
					},
					ResumeActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/active"),
							},
							Value: "true",
						},
					},
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{},
	}}
}
