// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// StatefulSet returns the built-in Karta for the apps/v1 StatefulSet workload.

func StatefulSet() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "apps-statefulset-v1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "specReplicas",
				Expression: `([dyn(object[?"spec"][?"replicas"].orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]`,
			},
			{
				Name:       "statusObservedGeneration",
				Expression: "([dyn(object.?status.?observedGeneration.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "metadataGeneration",
				Expression: "([dyn(object.?metadata.?generation.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "statusReadyReplicas",
				Expression: "([dyn(object.?status.?readyReplicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "specReplicasOne",
				Expression: "([dyn(object.?spec.?replicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [1])[0]",
			},
			{
				Name:       "statusUpdatedReplicas",
				Expression: "([dyn(object.?status.?updatedReplicas.orValue(dyn(null)))].filter(v, v != null && v != false) + [0])[0]",
			},
			{
				Name:       "statusCurrentRevision",
				Expression: `([dyn(object.?status.?currentRevision.orValue(dyn(null)))].filter(v, v != null && v != false) + [""])[0]`,
			},
			{
				Name:       "statusUpdateRevision",
				Expression: `([dyn(object.?status.?updateRevision.orValue(dyn(null)))].filter(v, v != null && v != false) + [""])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "statefulset",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "apps",
					Version: "v1",
					Kind:    "StatefulSet",
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
							Expression: "variables.specReplicas",
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
									Expression: `object[?"status"][?"observedGeneration"].orValue(dyn(null)) == object[?"metadata"][?"generation"].orValue(dyn(null)) && object[?"status"][?"replicas"].orValue(0) > object[?"spec"][?"replicas"].orValue(1)`,
								},
							},
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"spec"][?"replicas"].orValue(1) > 0 && (object[?"status"][?"observedGeneration"].orValue(0) != object[?"metadata"][?"generation"].orValue(0) || object[?"status"][?"readyReplicas"].orValue(0) == 0 || object[?"status"][?"updatedReplicas"].orValue(0) != object[?"spec"][?"replicas"].orValue(1) || (object[?"status"][?"currentRevision"].orValue(dyn(null)) != object[?"status"][?"updateRevision"].orValue(dyn(null))))`,
								},
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"observedGeneration"].orValue(0) == object[?"metadata"][?"generation"].orValue(0) && object[?"status"][?"replicas"].orValue(0) == object[?"spec"][?"replicas"].orValue(1) && object[?"status"][?"readyReplicas"].orValue(0) == object[?"spec"][?"replicas"].orValue(1) && object[?"status"][?"updatedReplicas"].orValue(0) == object[?"spec"][?"replicas"].orValue(1) && (object[?"status"][?"currentRevision"].orValue(dyn(null)) == object[?"status"][?"updateRevision"].orValue(dyn(null)))`,
								},
							},
						},
						Degraded: []v1alpha2.StatusMatcher{
							{
								ByExpression: &v1alpha2.ExpressionMatcher{
									Expression: `object[?"status"][?"readyReplicas"].orValue(0) > 0 && object[?"status"][?"readyReplicas"].orValue(0) < object[?"spec"][?"replicas"].orValue(1) && object[?"status"][?"updatedReplicas"].orValue(0) == object[?"spec"][?"replicas"].orValue(1) && (object[?"status"][?"currentRevision"].orValue(dyn(null)) == object[?"status"][?"updateRevision"].orValue(dyn(null))) && object[?"status"][?"observedGeneration"].orValue(0) == object[?"metadata"][?"generation"].orValue(0)`,
								},
							},
						},
					},
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{},
	}}
}
