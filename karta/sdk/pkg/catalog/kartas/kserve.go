// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// KServe returns the built-in Karta for InferenceService.
func KServe() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "serving-kserve-io-inferenceservice-v1beta1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "containerKeys",
				Expression: `([dyn(object[?"spec"][?"predictor"].orValue(dyn(null)))].filter(v, type(v) == map) + [{}])[0].map(k, string(k)).sort().filter(k, type(object.spec.predictor[k]) == map && "storageUri" in object.spec.predictor[k] && object.spec.predictor[k]["storageUri"] != null && object.spec.predictor[k]["storageUri"] != false)`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "inferenceservice",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "serving.kserve.io",
					Version: "v1beta1",
					Kind:    "InferenceService",
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
						Running: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "PredictorReady",
										Status: ptr.To("True"),
									},
									{
										Type:   "RoutesReady",
										Status: ptr.To("True"),
									},
									{
										Type:   "LatestDeploymentReady",
										Status: ptr.To("True"),
									},
								},
							},
						},
						Failed: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "PredictorReady",
										Status: ptr.To("False"),
									},
									{
										Type:   "PredictorConfigurationReady",
										Status: ptr.To("False"),
									},
									{
										Type:   "RoutesReady",
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
					Name: "predictor",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("inferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"predictor"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/predictor/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"predictor"][?"labels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/predictor/labels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"predictor"][?"annotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/predictor/annotations"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"predictor"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/predictor/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"predictor"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/predictor/affinity/nodeAffinity"),
								},
							},
							Containers: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"predictor"][?"containers"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/predictor/containers"),
								},
							},
							Container: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									MatchExpression: "variables.containerKeys.size() > 0",
									Expression:      "variables.containerKeys.size() == 1 ? object.spec.predictor[variables.containerKeys[0]] : variables.containerKeys.map(k, object.spec.predictor[k])",
								},
								Write: &v1alpha2.WriteAccessor{
									MatchExpression: "variables.containerKeys.size() == 1",
									PathExpression:  `"/spec/predictor/" + variables.containerKeys[0].replace("~", "~0").replace("/", "~1")`,
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"predictor"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/predictor/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"predictor"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/predictor/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"predictor"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/predictor/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"component"].orValue(dyn(null))`,
							},
							Value: ptr.To("predictor"),
						},
					},
				},
				{
					Name: "transformer",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("inferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"transformer"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/transformer"),
							},
						},
						Metadata: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"transformer"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/transformer"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"transformer"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/transformer/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"transformer"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/transformer/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"component"].orValue(dyn(null))`,
							},
							Value: ptr.To("transformer"),
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
								ComponentName: "predictor",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"serving.kserve.io/inferenceservice"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "transformer",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"serving.kserve.io/inferenceservice"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
