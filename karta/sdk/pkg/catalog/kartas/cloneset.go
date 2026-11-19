// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// CloneSet returns the built-in Karta for the OpenKruise apps.kruise.io/v1alpha1
// CloneSet workload.
//
// CloneSet manages pods directly (no intermediate ReplicaSet) and reports
// counter-based status: replicas, readyReplicas, availableReplicas,
// updatedReplicas, currentRevision, and updateRevision. The controller reconciles in
// clonesetcontroller.syncCloneSet; it updates counters after every pod
// change and bumps observedGeneration on each reconcile.
//
// Status mapping (every rule guarded by observedGeneration > 0):
//
//	Running ← generation current, all counters (including availableReplicas)
//	  match spec.replicas, and either paused or updatedReplicas >=
//	  expectedUpdatedReplicas.
//	  A zero-replica CloneSet reads Running (all counters are 0 == desired 0).
//	  A partition canary reads Running: the controller computes
//	  expectedUpdatedReplicas = replicas - partition, so updatedReplicas
//	  reaching that target is enough. A paused rollout reads Running:
//	  the user chose to pause, and scaling still works while paused.
//	Initializing ← not Running: observedGeneration > 0 and any Running
//	  condition fails (generation stale, a counter mismatch such as a drain
//	  with status.replicas > spec.replicas, availableReplicas mismatch, or an
//	  update in progress while not paused).
//	Degraded ← not mapped. FailedScale and FailedUpdate conditions
//	  (clonesetutils) are set once and never cleared by the controller;
//	  a consumer wanting the fault reads the condition directly.
//	  ProgressDeadlineExceeded fires during healthy progress; unmapped.
//
// spec.replicas defaults to 1 (API defaulting webhook). Writes through
// the exposed paths are unproven (no write round-trip check exists yet).
//
// Unrecorded branches: InPlaceIfPossible and InPlaceOnly update strategies,
// lifecycle hooks, and PVC templates. OnDelete update strategy is rejected
// by the webhook.

func CloneSet() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "apps-kruise-io-cloneset-v1alpha1"}, Spec: v1alpha2.KartaSpec{
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "cloneset",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "apps.kruise.io",
					Version: "v1alpha1",
					Kind:    "CloneSet",
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
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"observedGeneration"].orValue(0) > 0 && (object[?"status"][?"observedGeneration"].orValue(0) != object[?"metadata"][?"generation"].orValue(0) || object[?"status"][?"replicas"].orValue(0) != object[?"spec"][?"replicas"].orValue(1) || object[?"status"][?"readyReplicas"].orValue(0) != object[?"spec"][?"replicas"].orValue(1) || object[?"status"][?"availableReplicas"].orValue(0) != object[?"spec"][?"replicas"].orValue(1) || (!(object[?"spec"][?"updateStrategy"][?"paused"].orValue(false)) && object[?"status"][?"updatedReplicas"].orValue(0) < object[?"status"][?"expectedUpdatedReplicas"].orValue(0)))`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"observedGeneration"].orValue(0) > 0 && object[?"status"][?"observedGeneration"].orValue(0) == object[?"metadata"][?"generation"].orValue(0) && object[?"status"][?"replicas"].orValue(0) == object[?"spec"][?"replicas"].orValue(1) && object[?"status"][?"readyReplicas"].orValue(0) == object[?"spec"][?"replicas"].orValue(1) && object[?"status"][?"availableReplicas"].orValue(0) == object[?"spec"][?"replicas"].orValue(1) && (object[?"spec"][?"updateStrategy"][?"paused"].orValue(false) || object[?"status"][?"updatedReplicas"].orValue(0) >= object[?"status"][?"expectedUpdatedReplicas"].orValue(0))`,
								},
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "pod",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "",
						Version: "v1",
						Kind:    "Pod",
					},
					OwnerRef: ptr.To("cloneset"),
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{},
	}}
}
