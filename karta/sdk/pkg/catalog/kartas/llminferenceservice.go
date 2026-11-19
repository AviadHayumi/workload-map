// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// LLMInferenceService returns the built-in Karta for LLMInferenceService.
func LLMInferenceService() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "serving-kserve-io-llminferenceservice-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "llminferenceservice",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "serving.kserve.io",
					Version: "v1alpha1",
					Kind:    "LLMInferenceService",
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
									Expression: `object.?status.?conditions.orValue([]).exists(c, c.?type.orValue("") == "Ready" && c.?status.orValue("") == "Unknown") && !(object.?status.?conditions.orValue([]).exists(c, c.?type.orValue("") == "PresetsCombined" && c.?status.orValue("") == "False"))`,
								},
							},
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object.?status.?conditions.orValue([]).exists(c, c.?type.orValue("") == "Ready" && c.?status.orValue("") == "False" && (c.?reason.orValue("") in ["Progressing", "MinimumReplicasUnavailable", "WaitingForGateway", "GatewaysNotReady", "HTTPRoutesNotReady", "InferencePoolNotReady", "HPAProgressing", "ScaledObjectProgressing", "AllGroupsReady"]))`,
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
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object.?status.?conditions.orValue([]).exists(c, c.?type.orValue("") == "Ready" && c.?status.orValue("") == "False" && !(c.?reason.orValue("") in ["Progressing", "MinimumReplicasUnavailable", "WaitingForGateway", "GatewaysNotReady", "HTTPRoutesNotReady", "InferencePoolNotReady", "HPAProgressing", "ScaledObjectProgressing", "AllGroupsReady", "Stopped"]))`,
								},
							},
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object.?status.?conditions.orValue([]).exists(c, c.?type.orValue("") == "Ready" && c.?status.orValue("") == "Unknown") && (object.?status.?conditions.orValue([]).exists(c, c.?type.orValue("") == "PresetsCombined" && c.?status.orValue("") == "False" && c.?reason.orValue("") != "Stopped"))`,
								},
							},
						},
						Suspended: []v1alpha2.StatusMatcher{
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "Ready",
										Status: ptr.To("False"),
										Reason: ptr.To("Stopped"),
									},
								},
							},
							{
								ByConditions: []v1alpha2.ExpectedCondition{
									{
										Type:   "PresetsCombined",
										Status: ptr.To("False"),
										Reason: ptr.To("Stopped"),
									},
								},
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "main",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("llminferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/replicas"),
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"scaling"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/scaling/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"scaling"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/scaling/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("llminferenceservice-workload"),
						},
					},
				},
				{
					Name: "prefill",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("llminferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"prefill"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/prefill/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"prefill"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/prefill/replicas"),
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"prefill"][?"scaling"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/prefill/scaling/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Expression:      `object[?"spec"][?"prefill"][?"scaling"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) == null`,
								Path:            ptr.To("/spec/prefill/scaling/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("llminferenceservice-workload-prefill"),
						},
					},
				},
				{
					Name: "worker",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "leaderworkerset.x-k8s.io",
						Version: "v1",
						Kind:    "LeaderWorkerSet",
					},
					OwnerRef: ptr.To("llminferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"worker"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/worker"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/replicas"),
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"scaling"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/scaling/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"scaling"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/scaling/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("llminferenceservice-workload-worker"),
						},
					},
				},
				{
					Name: "leader",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "leaderworkerset.x-k8s.io",
						Version: "v1",
						Kind:    "LeaderWorkerSet",
					},
					OwnerRef: ptr.To("llminferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/replicas"),
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"scaling"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/scaling/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"scaling"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/scaling/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("llminferenceservice-workload-leader"),
						},
					},
				},
				{
					Name: "prefill-leader",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "leaderworkerset.x-k8s.io",
						Version: "v1",
						Kind:    "LeaderWorkerSet",
					},
					OwnerRef: ptr.To("llminferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"prefill"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/prefill/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"prefill"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/prefill/replicas"),
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"prefill"][?"scaling"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/prefill/scaling/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"prefill"][?"scaling"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/prefill/scaling/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("llminferenceservice-workload-leader-prefill"),
						},
					},
				},
				{
					Name: "prefill-worker",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "leaderworkerset.x-k8s.io",
						Version: "v1",
						Kind:    "LeaderWorkerSet",
					},
					OwnerRef: ptr.To("llminferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/prefill/worker"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"prefill"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/prefill/replicas"),
							},
						},
						MinReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"prefill"][?"scaling"][?"minReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/prefill/scaling/minReplicas"),
							},
						},
						MaxReplicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Expression:      `object[?"spec"][?"prefill"][?"scaling"][?"maxReplicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								MatchExpression: `object[?"spec"][?"prefill"][?"worker"].orValue(dyn(null)) != null`,
								Path:            ptr.To("/spec/prefill/scaling/maxReplicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("llminferenceservice-workload-worker-prefill"),
						},
					},
				},
				{
					Name: "router-scheduler",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("llminferenceservice"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						PodSpec: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"router"][?"scheduler"][?"template"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/router/scheduler/template"),
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"router"][?"scheduler"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/router/scheduler/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("llminferenceservice-router-scheduler"),
						},
					},
				},
			},
			AdditionalChildKinds: []v1alpha2.GroupVersionKind{
				{
					Group:   "leaderworkerset.x-k8s.io",
					Version: "v1",
					Kind:    "LeaderWorkerSet",
				},
				{
					Group:   "inference.networking.k8s.io",
					Version: "v1",
					Kind:    "InferencePool",
				},
				{
					Group:   "gateway.networking.k8s.io",
					Version: "v1",
					Kind:    "HTTPRoute",
				},
				{
					Group:   "",
					Version: "v1",
					Kind:    "Service",
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
								ComponentName: "main",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "prefill",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "worker",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
									`object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue("0")`,
								},
							},
							{
								ComponentName: "leader",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
									`object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue("0")`,
								},
							},
							{
								ComponentName: "prefill-leader",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
									`object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue("0")`,
								},
							},
							{
								ComponentName: "prefill-worker",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
									`object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue("0")`,
								},
							},
							{
								ComponentName: "router-scheduler",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/name"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}

// LLMInferenceServiceV1alpha2 returns the definition for the storage API version.
func LLMInferenceServiceV1alpha2() *v1alpha2.Karta {
	k := LLMInferenceService()
	k.Name = "serving-kserve-io-llminferenceservice-v1alpha2"
	k.Spec.StructureDefinition.RootComponent.Kind.Version = "v1alpha2"
	return k
}
