// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// TrainJob returns the built-in Karta for TrainJob.
func TrainJob() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "trainer-kubeflow-org-trainjob-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "conditions",
				Expression: "object.?status.?conditions.orValue([])",
			},
			{
				Name:       "jobsActive",
				Expression: "object.?status.?jobsStatus.orValue([]).exists(j, j.?active.orValue(0) > 0 || j.?ready.orValue(0) > 0)",
			},
			{
				Name:       "isSuspended",
				Expression: `variables.conditions.exists(c, c.type == "Suspended" && c.status == "True") || (object.?spec.?suspend.orValue(false) == true && !variables.jobsActive)`,
			},
			{
				Name:       "isTerminal",
				Expression: `variables.conditions.exists(c, (c.type == "Complete" || c.type == "Failed") && c.status == "True")`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			References: []v1alpha2.Reference{
				{
					Name:       "trainingRuntime",
					APIVersion: "trainer.kubeflow.org/v1alpha1",
					Kind:       "ClusterTrainingRuntime",
					Lookup: &v1alpha2.LookupReference{
						NameExpression: "object.spec.runtimeRef.name",
					},
				},
			},
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "trainjob",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "trainer.kubeflow.org",
					Version: "v1alpha1",
					Kind:    "TrainJob",
				},
				SpecDefinition: &v1alpha2.SpecDefinition{
					FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
						Resources: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object.?spec.?trainer.?resourcesPerNode.orValue(references.trainingRuntime.spec.template.spec.replicatedJobs.filter(j, j.?template.?metadata.?labels["trainer.kubeflow.org/trainjob-ancestor-step"].orValue("") == "trainer")[0].template.spec.template.spec.containers.filter(c, c[?"name"].orValue("") == "node")[0][?"resources"].orValue(dyn(null)))`,
							},
						},
						Image: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object.?spec.?trainer.?image.orValue(references.trainingRuntime.spec.template.spec.replicatedJobs.filter(j, j.?template.?metadata.?labels["trainer.kubeflow.org/trainjob-ancestor-step"].orValue("") == "trainer")[0].template.spec.template.spec.containers.filter(c, c[?"name"].orValue("") == "node")[0][?"image"].orValue(dyn(null)))`,
							},
						},
					},
				},
				ScaleDefinition: &v1alpha2.ScaleDefinition{
					Replicas: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: "object.?spec.?trainer.?numNodes.orValue(references.trainingRuntime.?spec.?mlPolicy.?numNodes.orValue(1))",
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
									Expression: "!variables.isTerminal && !variables.isSuspended && !variables.jobsActive",
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: "!variables.isTerminal && !variables.isSuspended && variables.jobsActive",
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
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: "variables.isSuspended",
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
			AdditionalChildKinds: []v1alpha2.GroupVersionKind{
				{
					Group:   "jobset.x-k8s.io",
					Version: "v1alpha2",
					Kind:    "JobSet",
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "trainjob",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "trainjob",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"jobset.sigs.k8s.io/jobset-name"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
