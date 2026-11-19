// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Mpijob returns the built-in Karta for MPIJob.
func Mpijob() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "kubeflow-org-mpijob-v2beta1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "specMpiReplicaSpecsLauncherReplicas",
				Expression: `([dyn(object[?"spec"][?"mpiReplicaSpecs"][?"Launcher"][?"replicas"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
			{
				Name:       "specMpiReplicaSpecsWorkerReplicas",
				Expression: `([dyn(object[?"spec"][?"mpiReplicaSpecs"][?"Worker"][?"replicas"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "mpijob",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "kubeflow.org",
					Version: "v2beta1",
					Kind:    "MPIJob",
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
										Type:   "Created",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Running",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Completed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Succeeded",
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
								Path: ptr.To("/spec/runPolicy/suspend"),
							},
							Value: "true",
						},
					},
					ResumeActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/runPolicy/suspend"),
							},
							Value: "false",
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "launcher",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("mpijob"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"mpiReplicaSpecs"][?"Launcher"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/mpiReplicaSpecs/Launcher/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.specMpiReplicaSpecsLauncherReplicas",
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/mpiReplicaSpecs/Launcher/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"training.kubeflow.org/job-role"].orValue(dyn(null))`,
							},
							Value: ptr.To("launcher"),
						},
					},
				},
				{
					Name: "worker",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("mpijob"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodTemplateSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"mpiReplicaSpecs"][?"Worker"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/mpiReplicaSpecs/Worker/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: "variables.specMpiReplicaSpecsWorkerReplicas",
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/mpiReplicaSpecs/Worker/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"training.kubeflow.org/job-role"].orValue(dyn(null))`,
							},
							Value: ptr.To("worker"),
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
								ComponentName: "launcher",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"training.kubeflow.org/job-name"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "worker",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"training.kubeflow.org/job-name"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
