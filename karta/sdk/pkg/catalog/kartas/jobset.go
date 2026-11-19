// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Jobset returns the built-in Karta for the JobSet workload
// (jobset.x-k8s.io/v1alpha2).

func Jobset() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "jobset-x-k8s-io-jobset-v1alpha2"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "jobset",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "jobset.x-k8s.io",
					Version: "v1alpha2",
					Kind:    "JobSet",
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
									Expression: "object.?status.?replicatedJobsStatus.orValue([]).exists(j, j.?active.orValue(0) > 0 && j.?ready.orValue(0) == 0) && object.?status.?replicatedJobsStatus.orValue([]).all(j, j.?failed.orValue(0) == 0)",
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: "object.?status.?replicatedJobsStatus.orValue([]).exists(j, j.?active.orValue(0) > 0 || j.?ready.orValue(0) > 0) && object.?status.?replicatedJobsStatus.orValue([]).all(j, j.?failed.orValue(0) == 0)",
								},
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Completed",
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
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "replicatedjob",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "batch",
						Version: "v1",
						Kind:    "Job",
					},
					OwnerRef: ptr.To("jobset"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"replicatedJobs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"template"][?"spec"][?"template"].orValue(dyn(null)))`,
							},
							Write: &v1alpha2.WriteAccessor{
								PathExpression: `"/spec/replicatedJobs/" + string(index) + "/template/spec/template"`,
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `([dyn(object[?"spec"][?"replicatedJobs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(j, j.replicas * j.template.spec.parallelism)`,
							},
						},
					},
					InstanceIds: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `([dyn(object[?"spec"][?"replicatedJobs"].orValue(dyn(null)))].filter(v, type(v) == list) + [[]])[0].map(x, x[?"name"].orValue(dyn(null)))`,
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentInstanceSelector: &v1alpha2.ComponentInstanceSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"jobset.sigs.k8s.io/replicatedjob-name"].orValue(dyn(null))`,
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
						Name: "job",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "replicatedjob",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"jobset.sigs.k8s.io/replicatedjob-name"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
