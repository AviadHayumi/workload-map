// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// BatchJob returns the built-in Karta for Job.
func BatchJob() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "batch-job-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "specParallelism",
				Expression: `([dyn(object[?"spec"][?"parallelism"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
			{
				Name:       "statusActive",
				Expression: "([dyn(object.?status.?active.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "statusReady",
				Expression: "([dyn(object.?status.?ready.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "specParallelismZero",
				Expression: "([dyn(object.?spec.?parallelism.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "statusSucceeded",
				Expression: "([dyn(object.?status.?succeeded.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "statusFailed",
				Expression: "([dyn(object.?status.?failed.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "job",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
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
							Expression: "variables.specParallelism",
						},
						Write: &v1alpha2.WriteAccessor{
							Path: ptr.To("/spec/parallelism"),
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
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"active"].orValue(0) > 0 && object[?"status"][?"ready"].orValue(0) == 0`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"active"].orValue(0) > 0 && object[?"status"][?"ready"].orValue(0) > 0`,
								},
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Complete",
										Status: ptr.To("True"),
									},
								},
							},
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "SuccessCriteriaMet",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Failed",
										Status: ptr.To("True"),
									},
								},
							},
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "FailureTarget",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Degraded: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"parallelism"].orValue(0) > 1 && object[?"status"][?"ready"].orValue(0) < object[?"spec"][?"parallelism"].orValue(dyn(null)) && (object[?"status"][?"succeeded"].orValue(0) > 0 || object[?"status"][?"failed"].orValue(0) > 0)`,
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
		},
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "job",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "job",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"batch.kubernetes.io/job-name"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
