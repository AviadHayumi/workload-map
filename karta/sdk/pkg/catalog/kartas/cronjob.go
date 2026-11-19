// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// CronJob returns the built-in Karta for CronJob.
func CronJob() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "batch-cronjob-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "specJobTemplateSpecParallelism",
				Expression: `([dyn(object[?"spec"][?"jobTemplate"][?"spec"][?"parallelism"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "cronjob",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "batch",
					Version: "v1",
					Kind:    "CronJob",
				},
				SpecDefinition: &v1alpha2.SpecDefinition{
					PodTemplateSpec: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: `object[?"spec"][?"jobTemplate"][?"spec"][?"template"].orValue(dyn(null))`,
						},
						Write: &v1alpha2.WriteAccessor{
							Path: ptr.To("/spec/jobTemplate/spec/template"),
						},
					},
				},
				ScaleDefinition: &v1alpha2.ScaleDefinition{
					Replicas: &v1alpha2.ValueAccessor{
						Read: &v1alpha2.ReadAccessor{
							Expression: "variables.specJobTemplateSpecParallelism",
						},
						Write: &v1alpha2.WriteAccessor{
							Path: ptr.To("/spec/jobTemplate/spec/parallelism"),
						},
					},
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"suspend"].orValue(false) != true && (object[?"status"][?"lastScheduleTime"].orValue(dyn(null)) == null)`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"suspend"].orValue(false) != true && (object[?"status"][?"lastScheduleTime"].orValue(dyn(null)) != null)`,
								},
							},
						},
						Suspended: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"suspend"].orValue(false) == true`,
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
					Name: "job",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "batch",
						Version: "v1",
						Kind:    "Job",
					},
					OwnerRef: ptr.To("cronjob"),
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{},
	}}
}
