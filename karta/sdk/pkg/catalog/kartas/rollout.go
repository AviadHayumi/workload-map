// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Rollout maps the Argo Rollouts progressive delivery controller
// (argoproj.io/v1alpha1).
//
// Controller check order (CalculateRolloutPhase): InvalidSpec condition,
// RolloutAborted reason on Progressing condition, ProgressDeadlineExceeded
// reason on Progressing condition, spec.paused, pauseConditions, then
// replica and strategy convergence. Final fallback is "Healthy".
//
// Status mapping:
//
//	Running ← "Healthy" AND observedGeneration == generation AND spec.paused
//	  false: all replicas updated and the stable RS matches the pod template
//	  hash. The generation guard prevents reporting Running on stale status
//	  after a spec update. observedGeneration is string-typed in this API
//	  (strconv of generation).
//	Degraded ← "Degraded" AND observedGeneration == generation: covers
//	  InvalidSpec, RolloutAborted, and ProgressDeadlineExceeded. Not Failed
//	  because the controller clears the state when the cause resolves. The
//	  controller checks Degraded before spec.paused, so a degraded Rollout
//	  stays Degraded even if paused. The generation guard clears stale
//	  Degraded immediately after a fix.
//	Suspended ← spec.paused true AND phase is not Degraded: the user
//	  suspended the Rollout via spec.paused (same field kubectl argo rollouts
//	  pause sets). Pausing does not delete pods, so no capacity is freed.
//	Initializing ← fallback: not Running, not Degraded, not Suspended.
//	  Covers Progressing, canary/blue-green Paused waits (spec.paused false),
//	  the brief window before the first status write, and stale-generation
//	  Healthy or Degraded right after a spec change.
//
// Exclusions: workloadRef, analysis resources, pauseConditions (only
// spec.paused is exposed as suspend; canary/blue-green pause steps are
// mapped as Initializing).
//
// Recorded: deadline recovery (ProgressDeadlineExceeded → image fix →
// Healthy), canary timed pause step, suspend/resume via spec.paused.
// Unrecorded: InvalidSpec and RolloutAborted degraded causes, abort
// (status.abort is a status patch the recorder cannot apply), promote
// (also a status patch); live writes through the SDK (only direct patches
// demonstrated).

func Rollout() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "argoproj-io-rollout-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "rollout",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "argoproj.io",
					Version: "v1alpha1",
					Kind:    "Rollout",
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
							Expression: `object[?"spec"][?"replicas"].orValue(dyn(null))`,
						},
						Write: &v1alpha2.WriteAccessor{
							Path: ptr.To("/spec/replicas"),
						},
					},
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					PhaseDefinition: &v1alpha2.PhaseDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"phase"].orValue(dyn(null))`,
						},
					},
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
									Expression: `(object[?"status"][?"phase"].orValue("") != "Healthy" || object[?"status"][?"observedGeneration"].orValue(dyn(null)) != string(object.?metadata.?generation.orValue(0)) || object[?"spec"][?"paused"].orValue(false)) && (object[?"status"][?"phase"].orValue("") != "Degraded" || object[?"status"][?"observedGeneration"].orValue(dyn(null)) != string(object.?metadata.?generation.orValue(0))) && (!(object[?"spec"][?"paused"].orValue(false)) || object[?"status"][?"phase"].orValue("") == "Degraded")`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Healthy",
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"observedGeneration"].orValue(dyn(null)) == string(object.?metadata.?generation.orValue(0)) && !(object[?"spec"][?"paused"].orValue(false))`,
								},
							},
						},
						Degraded: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Degraded",
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"observedGeneration"].orValue(dyn(null)) == string(object.?metadata.?generation.orValue(0))`,
								},
							},
						},
						Suspended: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"paused"].orValue(false) && object[?"status"][?"phase"].orValue("") != "Degraded"`,
								},
							},
						},
					},
				},
				SuspendDefinition: &v1alpha2.SuspendDefinition{
					SuspendActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/paused"),
							},
							Value: "true",
						},
					},
					ResumeActions: []v1alpha2.SuspendAction{
						{
							Write: v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/paused"),
							},
							Value: "false",
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
					OwnerRef: ptr.To("rollout"),
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{},
	}}
}
