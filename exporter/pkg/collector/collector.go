// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

var workloadLabels = []string{LabelNamespace, LabelWorkload, LabelWorkloadKind, LabelWorkloadGroup, LabelWorkloadUID}

// Collector observes one current store snapshot per collection.
type Collector struct {
	store   *store.Store
	options Options

	podInfo           gauge
	workloadInfo      gauge
	workloadStatus    gauge
	componentReplicas gauge
	componentPods     gauge
	componentInfo     gauge
	generation        gauge
	created           gauge
	workloads         gauge
	unattributed      gauge
	kartas            gauge
	resourceHealthy   gauge
}

type gauge struct {
	name        string
	description string
	labels      []string
	instrument  metric.Float64ObservableGauge
}

// New registers current-state observations. Unregister the returned registration before releasing the store.
func New(meter metric.Meter, s *store.Store, options Options) (metric.Registration, error) {
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
	c := &Collector{
		store:             s,
		resourceHealthy:   gauge{name: MetricExporterResourceHealthy, description: "Fresh verified Karta observation: 1 healthy, 0 failed, incomplete, or stale. Not application readiness or GPU telemetry coverage.", labels: []string{"scope", LabelNamespace, "group", "version", "kind", LabelWorkload, LabelWorkloadUID}},
		options:           options,
		podInfo:           gauge{name: MetricPodWorkloadInfo, description: "Pod to workload, component, and component instance attribution derived from the Karta definition. Value is always 1.", labels: podLabels},
		workloadInfo:      gauge{name: MetricWorkloadInfo, description: "Identity and provenance of a Karta-described workload. Value is always 1.", labels: append(append([]string{}, workloadLabels...), LabelWorkloadVersion, LabelKarta)},
		workloadStatus:    gauge{name: MetricWorkloadStatus, description: "Normalized workload status. One series per phase; several phases can be 1 at the same time.", labels: append(append([]string{}, workloadLabels...), LabelPhase)},
		componentReplicas: gauge{name: MetricComponentReplicas, description: "Desired replicas per component instance, from the workload spec.", labels: append(append([]string{}, workloadLabels...), LabelComponent, LabelComponentInstance)},
		componentPods:     gauge{name: MetricComponentPods, description: "Observed pod count per component instance, broken down by pod phase.", labels: append(append([]string{}, workloadLabels...), LabelComponent, LabelComponentInstance, LabelPhase)},
		componentInfo:     gauge{name: MetricComponentInfo, description: "Declared or observed component instance inventory. Value is always 1.", labels: append(append([]string{}, workloadLabels...), LabelComponent, LabelComponentInstance)},
		generation:        gauge{name: MetricWorkloadGeneration, description: "The workload's metadata.generation. A step change marks a spec change.", labels: workloadLabels},
		created:           gauge{name: MetricWorkloadCreated, description: "The workload's metadata.creationTimestamp as unix seconds. Steps forward when a workload is deleted and recreated under the same name.", labels: workloadLabels},
		workloads:         gauge{name: MetricExporterWorkloads, description: "Number of Karta-described workloads currently tracked.", labels: nil},
		unattributed:      gauge{name: MetricExporterUnattributedPods, description: "Pods the exporter could not fully attribute, by reason.", labels: []string{LabelReason}},
		kartas:            gauge{name: MetricExporterKartas, description: "Karta definitions known to the exporter, by validity and reason.", labels: []string{LabelValid, LabelReason}},
	}
	gauges := []*gauge{&c.podInfo, &c.workloadInfo, &c.workloadStatus,
		&c.componentReplicas, &c.componentPods, &c.componentInfo,
		&c.generation, &c.created, &c.workloads, &c.unattributed, &c.kartas, &c.resourceHealthy}
	instruments := make([]metric.Observable, 0, len(gauges))
	for _, g := range gauges {
		instrument, err := meter.Float64ObservableGauge(g.name, metric.WithDescription(g.description))
		if err != nil {
			return nil, fmt.Errorf("create instrument %s: %w", g.name, err)
		}
		g.instrument = instrument
		instruments = append(instruments, instrument)
	}
	return meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		c.collect(observer)
		return nil
	}, instruments...)
}

func (c *Collector) collect(observer metric.Observer) {
	snapshot := c.store.Snapshot()
	now := time.Now()
	seenHealth := make(map[types.UID]bool)
	activeWorkloads := make(map[types.UID]bool, len(snapshot.Workloads))
	for _, w := range snapshot.Workloads {
		activeWorkloads[w.UID] = true
	}
	for _, h := range snapshot.Health {
		if h.Scope == "workload" {
			if !activeWorkloads[h.UID] {
				continue
			}
			seenHealth[h.UID] = true
		}
		c.resourceHealthy.observe(observer, h.Value(now), h.Scope, h.Ref.Namespace, h.Ref.Group, h.Ref.Version, h.Ref.Kind, h.Ref.Name, string(h.UID))
	}
	for _, w := range snapshot.Workloads {
		if !seenHealth[w.UID] {
			c.resourceHealthy.observe(observer, 0, "workload", w.Ref.Namespace, w.Ref.Group, w.Ref.Version, w.Ref.Kind, w.Ref.Name, string(w.UID))
		}
	}

	for _, pod := range snapshot.Pods {
		labels := []string{pod.Namespace, pod.Name, string(pod.UID),
			pod.Workload.Name, pod.Workload.Kind, pod.Workload.Group, string(pod.WorkloadUID),
			pod.Component, pod.Instance, pod.Replica}
		if c.options.PodMetricsMode != PodMetricsCounts {
			labels = append(labels, string(pod.Phase))
		}
		c.podInfo.observe(observer, 1, labels...)
	}

	for _, workload := range snapshot.Workloads {
		ref := workload.Ref
		c.workloadInfo.observe(observer, 1,
			ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID), ref.Version, workload.Karta)
		c.generation.observe(observer, float64(workload.Generation),
			ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID))
		if workload.CreatedTimestamp != 0 {
			c.created.observe(observer, float64(workload.CreatedTimestamp),
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
				c.workloadStatus.observe(observer, value,
					ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID), string(phase))
			}
		}

		for _, component := range workload.Components {
			if component.Replicas == nil {
				continue
			}
			c.componentReplicas.observe(observer, float64(*component.Replicas),
				ref.Namespace, ref.Name, ref.Kind, ref.Group, string(workload.UID), component.Component, component.Instance)
		}
	}

	c.collectComponents(observer, snapshot)
	c.collectSelf(observer, snapshot)
}

type componentKey struct {
	workloadUID types.UID
	component   string
	instance    string
}

// collectComponents preserves empty declared instances and observed attribution keys.
func (c *Collector) collectComponents(observer metric.Observer, snapshot store.Snapshot) {
	refs := make(map[types.UID]store.WorkloadRef, len(snapshot.Workloads))
	keys := make(map[componentKey]struct{})
	counts := make(map[componentKey]map[string]int)

	for _, workload := range snapshot.Workloads {
		refs[workload.UID] = workload.Ref
		for _, component := range workload.Components {
			keys[componentKey{workload.UID, component.Component, component.Instance}] = struct{}{}
		}
	}

	for _, pod := range snapshot.Pods {
		if _, ok := refs[pod.WorkloadUID]; !ok {
			refs[pod.WorkloadUID] = pod.Workload
		}
		key := componentKey{pod.WorkloadUID, pod.Component, pod.Instance}
		keys[key] = struct{}{}
		if counts[key] == nil {
			counts[key] = make(map[string]int)
		}
		counts[key][string(pod.Phase)]++
	}

	for key := range keys {
		ref := refs[key.workloadUID]
		if c.options.PodMetricsMode != PodMetricsCounts {
			c.componentInfo.observe(observer, 1,
				ref.Namespace, ref.Name, ref.Kind, ref.Group, string(key.workloadUID), key.component, key.instance)
		}
		if c.options.PodMetricsMode == PodMetricsInventory {
			continue
		}
		for _, phase := range c.options.PodPhases {
			c.componentPods.observe(observer,
				float64(counts[key][string(phase)]),
				ref.Namespace, ref.Name, ref.Kind, ref.Group, string(key.workloadUID), key.component, key.instance, string(phase))
		}
	}
}

func (c *Collector) collectSelf(observer metric.Observer, snapshot store.Snapshot) {
	c.workloads.observe(observer, float64(len(snapshot.Workloads)))

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
	c.unattributed.observe(observer, float64(pending), ReasonNoOwner)
	c.unattributed.observe(observer, float64(unknownInstance), ReasonUnknownInstance)
	c.unattributed.observe(observer, float64(jqErrors), ReasonJQError)

	if c.options.KartaCounts != nil {
		valid, invalid, shadowed := c.options.KartaCounts()
		c.kartas.observe(observer, float64(valid), "true", "")
		c.kartas.observe(observer, float64(invalid), "false", ReasonInvalid)
		c.kartas.observe(observer, float64(shadowed), "false", ReasonShadowed)
	}
}

func (g gauge) observe(observer metric.Observer, value float64, values ...string) {
	attributes := make([]attribute.KeyValue, len(g.labels))
	for i, name := range g.labels {
		attributes[i] = attribute.String(name, values[i])
	}
	observer.ObserveFloat64(g.instrument, value, metric.WithAttributes(attributes...))
}
