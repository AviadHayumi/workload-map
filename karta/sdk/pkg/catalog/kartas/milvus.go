// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha2 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

// Milvus returns the built-in Karta for Milvus.
func Milvus() *v1alpha2.Karta {
	return &v1alpha2.Karta{TypeMeta: metav1.TypeMeta{APIVersion: "run.ai/v1alpha2", Kind: "Karta"}, ObjectMeta: metav1.ObjectMeta{Name: "milvus-io-milvus-v1beta1"}, Spec: v1alpha2.KartaSpec{
		Variables: []v1alpha2.Variable{
			{
				Name:       "statusStatus",
				Expression: `([dyn(object.?status.?status.orValue(dyn(null)))].filter(v, v != null && v != false) + [""])[0]`,
			},
		},
		StructureDefinition: v1alpha2.StructureDefinition{
			References: []v1alpha2.Reference{
				{
					Name:            "etcdStatefulSets",
					APIVersion:      "apps/v1",
					Kind:            "StatefulSet",
					MatchExpression: "!object.?spec.?dependencies.?etcd.?external.orValue(false)",
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "etcdCronJobs",
					APIVersion:      "batch/v1",
					Kind:            "CronJob",
					MatchExpression: "!object.?spec.?dependencies.?etcd.?external.orValue(false)",
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "minioDeployments",
					APIVersion:      "apps/v1",
					Kind:            "Deployment",
					MatchExpression: "!object.?spec.?dependencies.?storage.?external.orValue(false)",
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "minioStatefulSets",
					APIVersion:      "apps/v1",
					Kind:            "StatefulSet",
					MatchExpression: "!object.?spec.?dependencies.?storage.?external.orValue(false)",
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "pulsarStatefulSets",
					APIVersion:      "apps/v1",
					Kind:            "StatefulSet",
					MatchExpression: `object.?spec.?dependencies.?msgStreamType.orValue("") == "pulsar" && !object.?spec.?dependencies.?pulsar.?external.orValue(false)`,
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "pulsarDeployments",
					APIVersion:      "apps/v1",
					Kind:            "Deployment",
					MatchExpression: `object.?spec.?dependencies.?msgStreamType.orValue("") == "pulsar" && !object.?spec.?dependencies.?pulsar.?external.orValue(false)`,
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "pulsarJobs",
					APIVersion:      "batch/v1",
					Kind:            "Job",
					MatchExpression: `object.?spec.?dependencies.?msgStreamType.orValue("") == "pulsar" && !object.?spec.?dependencies.?pulsar.?external.orValue(false)`,
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "pulsarDaemonSets",
					APIVersion:      "apps/v1",
					Kind:            "DaemonSet",
					MatchExpression: `object.?spec.?dependencies.?msgStreamType.orValue("") == "pulsar" && !object.?spec.?dependencies.?pulsar.?external.orValue(false)`,
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "kafkaStatefulSets",
					APIVersion:      "apps/v1",
					Kind:            "StatefulSet",
					MatchExpression: `object.?spec.?dependencies.?msgStreamType.orValue("") == "kafka" && !object.?spec.?dependencies.?kafka.?external.orValue(false)`,
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "kafkaDeployments",
					APIVersion:      "apps/v1",
					Kind:            "Deployment",
					MatchExpression: `object.?spec.?dependencies.?msgStreamType.orValue("") == "kafka" && !object.?spec.?dependencies.?kafka.?external.orValue(false)`,
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
				{
					Name:            "teiStatefulSets",
					APIVersion:      "apps/v1",
					Kind:            "StatefulSet",
					MatchExpression: "object.?spec.?dependencies.?tei.?enabled.orValue(false)",
					List: &v1alpha2.ListReference{
						MatchLabels: map[string]v1alpha2.LabelValue{
							"app.kubernetes.io/managed-by": {
								Value: ptr.To("Helm"),
							},
						},
					},
				},
			},
			RootComponent: v1alpha2.ComponentDefinition{
				Name: "milvus",
				Kind: &v1alpha2.GroupVersionKind{
					Group:   "milvus.io",
					Version: "v1beta1",
					Kind:    "Milvus",
				},
				StatusDefinition: &v1alpha2.StatusDefinition{
					PhaseDefinition: &v1alpha2.PhaseDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"status"].orValue(dyn(null))`,
						},
					},
					ConditionsDefinition: &v1alpha2.ConditionsDefinition{
						Read: v1alpha2.ReadAccessor{
							Expression: `object[?"status"][?"conditions"].orValue(dyn(null))`,
						},
						TypeFieldName:   "type",
						StatusFieldName: "status",
					},
					StatusMappings: v1alpha2.StatusMappings{
						Initializing: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Pending",
							},
						},
						Running: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Healthy",
							},
						},
						Degraded: []v1alpha2.StatusMatcher{
							{
								ByPhase: "Unhealthy",
							},
						},
					},
				},
			},
			ChildComponents: []v1alpha2.ComponentDefinition{
				{
					Name: "standalone",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"standalone"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/standalone/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"standalone"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/standalone/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"standalone"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/standalone/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"standalone"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/standalone/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"standalone"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/standalone/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"standalone"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/standalone/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"standalone"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/standalone/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"standalone"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/standalone/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("standalone"),
						},
					},
				},
				{
					Name: "proxy",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"proxy"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/proxy/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"proxy"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/proxy/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"proxy"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/proxy/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"proxy"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/proxy/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"proxy"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/proxy/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"proxy"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/proxy/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"proxy"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/proxy/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"proxy"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/proxy/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("proxy"),
						},
					},
				},
				{
					Name: "mixcoord",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"mixCoord"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/mixCoord/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"mixCoord"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/mixCoord/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"mixCoord"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/mixCoord/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"mixCoord"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/mixCoord/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"mixCoord"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/mixCoord/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"mixCoord"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/mixCoord/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"mixCoord"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/mixCoord/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"mixCoord"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/mixCoord/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("mixcoord"),
						},
					},
				},
				{
					Name: "datanode",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataNode"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataNode/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataNode"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataNode/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataNode"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataNode/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataNode"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataNode/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataNode"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataNode/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataNode"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataNode/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataNode"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataNode/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"dataNode"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/dataNode/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("datanode"),
						},
					},
				},
				{
					Name: "querynode",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryNode"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryNode/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryNode"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryNode/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryNode"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryNode/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryNode"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryNode/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryNode"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryNode/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryNode"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryNode/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryNode"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryNode/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"queryNode"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/queryNode/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("querynode"),
						},
					},
				},
				{
					Name: "streamingnode",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"streamingNode"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/streamingNode/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"streamingNode"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/streamingNode/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"streamingNode"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/streamingNode/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"streamingNode"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/streamingNode/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"streamingNode"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/streamingNode/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"streamingNode"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/streamingNode/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"streamingNode"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/streamingNode/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"streamingNode"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/streamingNode/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("streamingnode"),
						},
					},
				},
				{
					Name: "indexnode",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexNode"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexNode/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexNode"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexNode/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexNode"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexNode/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexNode"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexNode/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexNode"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexNode/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexNode"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexNode/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexNode"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexNode/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"indexNode"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/indexNode/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("indexnode"),
						},
					},
				},
				{
					Name: "rootcoord",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"rootCoord"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/rootCoord/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"rootCoord"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/rootCoord/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"rootCoord"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/rootCoord/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"rootCoord"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/rootCoord/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"rootCoord"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/rootCoord/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"rootCoord"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/rootCoord/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"rootCoord"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/rootCoord/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"rootCoord"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/rootCoord/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("rootcoord"),
						},
					},
				},
				{
					Name: "datacoord",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataCoord"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataCoord/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataCoord"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataCoord/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataCoord"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataCoord/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataCoord"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataCoord/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataCoord"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataCoord/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataCoord"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataCoord/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"dataCoord"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/dataCoord/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"dataCoord"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/dataCoord/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("datacoord"),
						},
					},
				},
				{
					Name: "querycoord",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryCoord"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryCoord/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryCoord"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryCoord/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryCoord"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryCoord/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryCoord"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryCoord/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryCoord"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryCoord/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryCoord"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryCoord/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"queryCoord"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/queryCoord/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"queryCoord"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/queryCoord/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("querycoord"),
						},
					},
				},
				{
					Name: "indexcoord",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexCoord"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexCoord/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexCoord"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexCoord/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexCoord"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexCoord/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexCoord"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexCoord/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexCoord"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexCoord/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexCoord"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexCoord/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"indexCoord"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/indexCoord/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"indexCoord"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/indexCoord/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("indexcoord"),
						},
					},
				},
				{
					Name: "cdc",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "Deployment",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							SchedulerName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"cdc"][?"schedulerName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/cdc/schedulerName"),
								},
							},
							Labels: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"cdc"][?"podLabels"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/cdc/podLabels"),
								},
							},
							Annotations: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"cdc"][?"podAnnotations"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/cdc/podAnnotations"),
								},
							},
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"cdc"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/cdc/resources"),
								},
							},
							PodAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"cdc"][?"affinity"][?"podAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/cdc/affinity/podAffinity"),
								},
							},
							NodeAffinity: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"cdc"][?"affinity"][?"nodeAffinity"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/cdc/affinity/nodeAffinity"),
								},
							},
							PriorityClassName: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"components"][?"cdc"][?"priorityClassName"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/components/cdc/priorityClassName"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"components"][?"cdc"][?"replicas"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/components/cdc/replicas"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `object[?"metadata"][?"labels"][?"app.kubernetes.io/component"].orValue(dyn(null))`,
							},
							Value: ptr.To("cdc"),
						},
					},
				},
				{
					Name: "etcd",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"dependencies"][?"etcd"][?"inCluster"][?"values"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/dependencies/etcd/inCluster/values/resources"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"dependencies"][?"etcd"][?"inCluster"][?"values"][?"replicaCount"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/dependencies/etcd/inCluster/values/replicaCount"),
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						FromReferences: []v1alpha2.PodReference{
							{
								Name:            "etcdStatefulSets",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-etcd" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
							{
								Name:            "etcdCronJobs",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-etcd" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
						},
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `"etcd"`,
							},
							Value: ptr.To("etcd"),
						},
					},
				},
				{
					Name: "minio",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"dependencies"][?"storage"][?"inCluster"][?"values"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/dependencies/storage/inCluster/values/resources"),
								},
							},
						},
					},
					PodSelector: &v1alpha2.PodSelector{
						FromReferences: []v1alpha2.PodReference{
							{
								Name:            "minioDeployments",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-minio" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
							{
								Name:            "minioStatefulSets",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-minio" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
						},
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `"minio"`,
							},
							Value: ptr.To("minio"),
						},
					},
				},
				{
					Name: "pulsar-zookeeper",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"zookeeper"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/zookeeper/resources"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"zookeeper"][?"replicaCount"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/zookeeper/replicaCount"),
							},
						},
					},
				},
				{
					Name: "pulsar-bookkeeper",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"bookkeeper"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/bookkeeper/resources"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"bookkeeper"][?"replicaCount"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/bookkeeper/replicaCount"),
							},
						},
					},
				},
				{
					Name: "pulsar-broker",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"broker"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/broker/resources"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"broker"][?"replicaCount"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/broker/replicaCount"),
							},
						},
					},
				},
				{
					Name: "pulsar-proxy",
					Kind: &v1alpha2.GroupVersionKind{
						Group:   "apps",
						Version: "v1",
						Kind:    "StatefulSet",
					},
					OwnerRef: ptr.To("milvus"),
					SpecDefinition: &v1alpha2.SpecDefinition{
						FragmentedPodSpecDefinition: &v1alpha2.FragmentedPodSpecDefinition{
							Resources: &v1alpha2.ValueAccessor{
								Read: &v1alpha2.ReadAccessor{
									Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"proxy"][?"resources"].orValue(dyn(null))`,
								},
								Write: &v1alpha2.WriteAccessor{
									Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/proxy/resources"),
								},
							},
						},
					},
					ScaleDefinition: &v1alpha2.ScaleDefinition{
						Replicas: &v1alpha2.ValueAccessor{
							Read: &v1alpha2.ReadAccessor{
								Expression: `object[?"spec"][?"dependencies"][?"pulsar"][?"inCluster"][?"values"][?"proxy"][?"replicaCount"].orValue(dyn(null))`,
							},
							Write: &v1alpha2.WriteAccessor{
								Path: ptr.To("/spec/dependencies/pulsar/inCluster/values/proxy/replicaCount"),
							},
						},
					},
				},
				{
					Name:     "pulsar",
					OwnerRef: ptr.To("milvus"),
					PodSelector: &v1alpha2.PodSelector{
						FromReferences: []v1alpha2.PodReference{
							{
								Name:            "pulsarStatefulSets",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-pulsar" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
							{
								Name:            "pulsarDeployments",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-pulsar" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
							{
								Name:            "pulsarJobs",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-pulsar" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
							{
								Name:            "pulsarDaemonSets",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-pulsar" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
						},
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `"pulsar"`,
							},
							Value: ptr.To("pulsar"),
						},
					},
				},
				{
					Name:     "kafka",
					OwnerRef: ptr.To("milvus"),
					PodSelector: &v1alpha2.PodSelector{
						FromReferences: []v1alpha2.PodReference{
							{
								Name:            "kafkaStatefulSets",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-kafka" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
							{
								Name:            "kafkaDeployments",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-kafka" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
						},
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `"kafka"`,
							},
							Value: ptr.To("kafka"),
						},
					},
				},
				{
					Name:     "tei",
					OwnerRef: ptr.To("milvus"),
					PodSelector: &v1alpha2.PodSelector{
						FromReferences: []v1alpha2.PodReference{
							{
								Name:            "teiStatefulSets",
								MatchExpression: `target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("") == object.metadata.name + "-tei" && target.?metadata.?annotations[?"meta.helm.sh/release-namespace"].orValue("") == object.metadata.namespace && target.?metadata.?annotations[?"helm.sh/hook"].orValue("") == ""`,
							},
						},
						ComponentTypeSelector: &v1alpha2.ComponentTypeSelector{
							Read: v1alpha2.ReadAccessor{
								Expression: `"tei"`,
							},
							Value: ptr.To("tei"),
						},
					},
				},
			},
		},
		Instructions: v1alpha2.OptimizationInstructions{
			GangScheduling: &v1alpha2.GangSchedulingInstruction{
				PodGroups: []v1alpha2.PodGroupDefinition{
					{
						Name: "cluster",
						Members: []v1alpha2.PodGroupMemberDefinition{
							{
								ComponentName: "querynode",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/instance"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "datanode",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/instance"].orValue(dyn(null))`,
								},
							},
							{
								ComponentName: "streamingnode",
								GroupByExpressions: []string{
									`object[?"metadata"][?"labels"][?"app.kubernetes.io/instance"].orValue(dyn(null))`,
								},
							},
						},
					},
				},
			},
		},
	}}
}
