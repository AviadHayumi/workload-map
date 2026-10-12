// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

var legacyWorkloadLabels = []string{LabelNamespace, LabelWorkload, LabelWorkloadKind, LabelWorkloadGroup, LabelWorkloadUID}

// Collector renders a store snapshot as const metrics on every scrape.
// Nothing is computed per object at scrape time beyond label assembly, and
// deleted objects simply stop being rendered.
type legacyCollector struct {
	store   *store.Store
	options Options

	podInfoDesc           *prometheus.Desc
	workloadInfoDesc      *prometheus.Desc
	workloadStatusDesc    *prometheus.Desc
	componentReplicasDesc *prometheus.Desc
	componentPodsDesc     *prometheus.Desc
	componentInfoDesc     *prometheus.Desc
	generationDesc        *prometheus.Desc
	createdDesc           *prometheus.Desc
	workloadsDesc         *prometheus.Desc
	unattributedDesc      *prometheus.Desc
	kartasDesc            *prometheus.Desc
}

func newLegacy(s *store.Store, options Options) *legacyCollector {
	if options.PodMetricsMode == "" {
		options.PodMetricsMode = PodMetricsCounts
	}
	if options.WorkloadPhases == nil {
		options.WorkloadPhases = AllPhases
	}
	if options.PodPhases == nil {
		options.PodPhases = AllPodPhases
	}
	podLabels := []string{LabelNamespace, LabelPod, LabelUID, LabelWorkload, LabelWorkloadKind, LabelWorkloadGroup, LabelWorkloadUID, LabelComponent, LabelComponentInstance, LabelReplica}
	if options.PodMetricsMode != PodMetricsCounts {
		podLabels = append(podLabels, LabelPodPhase)
	}
	return &legacyCollector{
		store:   s,
		options: options,
		podInfoDesc: prometheus.NewDesc(MetricPodWorkloadInfo,
			"Pod to workload, component, and component instance attribution derived from the Karta definition. Value is always 1.",
			podLabels, nil),
		workloadInfoDesc: prometheus.NewDesc(MetricWorkloadInfo,
			"Identity and provenance of a Karta-described workload. Value is always 1.",
			append(append([]string{}, legacyWorkloadLabels...), LabelWorkloadVersion, LabelKarta), nil),
		workloadStatusDesc: prometheus.NewDesc(MetricWorkloadStatus,
			"Normalized workload status. One series per phase; several phases can be 1 at the same time.",
			append(append([]string{}, legacyWorkloadLabels...), LabelPhase), nil),
		componentReplicasDesc: prometheus.NewDesc(MetricComponentReplicas,
			"Desired replicas per component instance, from the workload spec.",
			append(append([]string{}, legacyWorkloadLabels...), LabelComponent, LabelComponentInstance), nil),
		componentPodsDesc: prometheus.NewDesc(MetricComponentPods,
			"Observed pod count per component instance, broken down by pod phase.",
			append(append([]string{}, legacyWorkloadLabels...), LabelComponent, LabelComponentInstance, LabelPhase), nil),
		componentInfoDesc: prometheus.NewDesc(MetricComponentInfo,
			"Declared or observed component instance inventory. Value is always 1.",
			append(append([]string{}, legacyWorkloadLabels...), LabelComponent, LabelComponentInstance), nil),
		generationDesc: prometheus.NewDesc(MetricWorkloadGeneration,
			"The workload's metadata.generation. A step change marks a spec change.",
			legacyWorkloadLabels, nil),
		createdDesc: prometheus.NewDesc(MetricWorkloadCreated,
			"The workload's metadata.creationTimestamp as unix seconds. Steps forward when a workload is deleted and recreated under the same name.",
			legacyWorkloadLabels, nil),
		workloadsDesc: prometheus.NewDesc(MetricExporterWorkloads,
			"Number of Karta-described workloads currently tracked.", nil, nil),
		unattributedDesc: prometheus.NewDesc(MetricExporterUnattributedPods,
			"Pods the exporter could not fully attribute, by reason.", []string{LabelReason}, nil),
		kartasDesc: prometheus.NewDesc(MetricExporterKartas,
			"Karta definitions known to the exporter, by validity and reason.", []string{LabelValid, LabelReason}, nil),
	}
}

func (c *legacyCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.podInfoDesc
	ch <- c.workloadInfoDesc
	ch <- c.workloadStatusDesc
	ch <- c.componentReplicasDesc
	ch <- c.componentPodsDesc
	ch <- c.componentInfoDesc
	ch <- c.generationDesc
	ch <- c.createdDesc
	ch <- c.workloadsDesc
	ch <- c.unattributedDesc
	ch <- c.kartasDesc
}

func (c *legacyCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.store.Snapshot()

	for _, pod := range snapshot.Pods {
		labels := []string{pod.Namespace, pod.Name, string(pod.UID),
			pod.Workload.Name, pod.Workload.Kind, pod.Workload.Group, string(pod.WorkloadUID),
			pod.Component, pod.Instance, pod.Replica}
		if c.options.PodMetricsMode != PodMetricsCounts {
			labels = append(labels, string(pod.Phase))
		}
		ch <- prometheus.MustNewConstMetric(c.podInfoDesc, prometheus.GaugeValue, 1, labels...)
	}

	for _, workload := range snapshot.Workloads {
		ref := workload.Ref
		ch <- prometheus.MustNewConstMetric(c.workloadInfoDesc, prometheus.GaugeValue, 1,
			ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID), ref.Version, workload.Karta)
		ch <- prometheus.MustNewConstMetric(c.generationDesc, prometheus.GaugeValue, float64(workload.Generation),
			ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID))
		if workload.CreatedTimestamp != 0 {
			ch <- prometheus.MustNewConstMetric(c.createdDesc, prometheus.GaugeValue, float64(workload.CreatedTimestamp),
				ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID))
		}

		if workload.HasStatus {
			matched := make(map[string]struct{}, len(workload.Phases))
			for _, phase := range workload.Phases {
				matched[string(phase)] = struct{}{}
			}
			for _, phase := range c.options.WorkloadPhases {
				value := 0.0
				if _, ok := matched[string(phase)]; ok {
					value = 1.0
				}
				ch <- prometheus.MustNewConstMetric(c.workloadStatusDesc, prometheus.GaugeValue, value,
					ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID), string(phase))
			}
		}

		for _, component := range workload.Components {
			if component.Replicas == nil {
				continue
			}
			ch <- prometheus.MustNewConstMetric(c.componentReplicasDesc, prometheus.GaugeValue, float64(*component.Replicas),
				ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID), component.Component, component.Instance)
		}
	}

	c.collectComponents(ch, snapshot)
	c.collectSelf(ch, snapshot)
}

type legacyComponentKey struct {
	workloadUID types.UID
	component   string
	instance    string
}

// collectComponents preserves empty declared instances and observed attribution keys.
func (c *legacyCollector) collectComponents(ch chan<- prometheus.Metric, snapshot store.Snapshot) {
	refs := make(map[types.UID]store.WorkloadRef, len(snapshot.Workloads))
	keys := make(map[legacyComponentKey]struct{})
	counts := make(map[legacyComponentKey]map[string]int)

	for _, workload := range snapshot.Workloads {
		refs[workload.UID] = workload.Ref
		for _, component := range workload.Components {
			keys[legacyComponentKey{workload.UID, component.Component, component.Instance}] = struct{}{}
		}
	}

	for _, pod := range snapshot.Pods {
		if _, ok := refs[pod.WorkloadUID]; !ok {
			refs[pod.WorkloadUID] = pod.Workload
		}
		key := legacyComponentKey{pod.WorkloadUID, pod.Component, pod.Instance}
		keys[key] = struct{}{}
		if counts[key] == nil {
			counts[key] = make(map[string]int)
		}
		counts[key][string(pod.Phase)]++
	}

	for key := range keys {
		ref := refs[key.workloadUID]
		if c.options.PodMetricsMode != PodMetricsCounts {
			ch <- prometheus.MustNewConstMetric(c.componentInfoDesc, prometheus.GaugeValue, 1,
				ref.Namespace, ref.Name, ref.Kind, ref.Group, string(key.workloadUID), key.component, key.instance)
		}
		if c.options.PodMetricsMode == PodMetricsInventory {
			continue
		}
		for _, phase := range c.options.PodPhases {
			ch <- prometheus.MustNewConstMetric(c.componentPodsDesc, prometheus.GaugeValue,
				float64(counts[key][string(phase)]),
				ref.Namespace, ref.Name, ref.Kind, ref.Group, string(key.workloadUID), key.component, key.instance, string(phase))
		}
	}
}

func (c *legacyCollector) collectSelf(ch chan<- prometheus.Metric, snapshot store.Snapshot) {
	ch <- prometheus.MustNewConstMetric(c.workloadsDesc, prometheus.GaugeValue, float64(len(snapshot.Workloads)))

	unknownInstance, jqErrors := 0, 0
	for _, pod := range snapshot.Pods {
		switch pod.Reason {
		case ReasonUnknownInstance:
			unknownInstance++
		case ReasonJQError:
			jqErrors++
		}
	}
	pending := 0
	if c.options.PendingPods != nil {
		pending = c.options.PendingPods()
	}
	ch <- prometheus.MustNewConstMetric(c.unattributedDesc, prometheus.GaugeValue, float64(pending), ReasonNoOwner)
	ch <- prometheus.MustNewConstMetric(c.unattributedDesc, prometheus.GaugeValue, float64(unknownInstance), ReasonUnknownInstance)
	ch <- prometheus.MustNewConstMetric(c.unattributedDesc, prometheus.GaugeValue, float64(jqErrors), ReasonJQError)

	if c.options.KartaCounts != nil {
		valid, invalid, shadowed := c.options.KartaCounts()
		ch <- prometheus.MustNewConstMetric(c.kartasDesc, prometheus.GaugeValue, float64(valid), "true", "")
		ch <- prometheus.MustNewConstMetric(c.kartasDesc, prometheus.GaugeValue, float64(invalid), "false", ReasonInvalid)
		ch <- prometheus.MustNewConstMetric(c.kartasDesc, prometheus.GaugeValue, float64(shadowed), "false", ReasonShadowed)
	}
}
