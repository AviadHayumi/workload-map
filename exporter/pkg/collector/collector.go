// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

// Collector observes one current store snapshot per collection.
type Collector struct {
	store              *store.Store
	options            Options
	podInfo            gauge
	workloadStatus     gauge
	observationHealthy gauge
}

type gauge struct {
	name        string
	description string
	labels      []string
	instrument  metric.Float64ObservableGauge
}

// New registers the KEP gauges. Unregister before shutting down the provider.
func New(meter metric.Meter, s *store.Store, options Options) (metric.Registration, error) {
	if options.WorkloadPhases == nil {
		options.WorkloadPhases = slices.Clone(AllPhases)
	}
	c := &Collector{
		store:   s,
		options: options,
		podInfo: gauge{
			name:        MetricPodWorkloadInfo,
			description: "Pod attribution to a confirmed workload root. Value is always 1.",
			labels:      []string{LabelNamespace, LabelWorkload, LabelWorkloadGroup, LabelWorkloadVersion, LabelWorkloadKind, LabelWorkloadUID, LabelOwnerResolution, LabelPod, LabelPodUID, LabelComponent, LabelComponentInstance, LabelReplicaGroupID},
		},
		workloadStatus: gauge{
			name:        MetricWorkloadStatus,
			description: "Normalized root workload status. Each phase is 0 or 1; several phases can match.",
			labels:      []string{LabelNamespace, LabelWorkload, LabelWorkloadGroup, LabelWorkloadVersion, LabelWorkloadKind, LabelWorkloadUID, LabelPhase, LabelOwnerResolution, LabelKarta, LabelKartaSource},
		},
		observationHealthy: gauge{
			name:        MetricExporterObservationHealthy,
			description: "Fresh verified Karta observation: 1 healthy, otherwise 0. Not application readiness or GPU coverage.",
			labels:      []string{"scope", LabelNamespace, "group", "version", "kind", LabelWorkload, LabelWorkloadUID, LabelReason},
		},
	}
	gauges := []*gauge{&c.podInfo, &c.workloadStatus, &c.observationHealthy}
	instruments := make([]metric.Observable, 0, len(gauges))
	for _, g := range gauges {
		instrument, err := meter.Float64ObservableGauge(g.name, metric.WithDescription(g.description))
		if err != nil {
			return nil, fmt.Errorf("create instrument %s: %w", g.name, err)
		}
		g.instrument = instrument
		instruments = append(instruments, instrument)
	}
	return meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		return c.collect(ctx, observer)
	}, instruments...)
}

func (c *Collector) collect(ctx context.Context, observer metric.Observer) error {
	snapshot := c.store.Snapshot()
	now := time.Now()
	active := make(map[types.UID]*store.WorkloadRecord, len(snapshot.Workloads))
	seenHealth := make(map[types.UID]bool, len(snapshot.Workloads))
	for i := range snapshot.Workloads {
		workload := &snapshot.Workloads[i]
		if workload.OwnerResolution != "" || workload.OwnerFailure != "" {
			active[workload.UID] = workload
		}
	}
	for _, h := range snapshot.Health {
		if h.Scope == "workload" {
			if active[h.UID] == nil {
				continue
			}
			seenHealth[h.UID] = true
		}
		value, reason := h.Status(now)
		c.observationHealthy.observe(observer, value, h.Scope, h.Ref.Namespace, h.Ref.Group, h.Ref.Version, h.Ref.Kind, h.Ref.Name, string(h.UID), reason)
	}
	for _, workload := range snapshot.Workloads {
		if err := ctx.Err(); err != nil {
			return err
		}
		ref := workload.Ref
		if active[workload.UID] != nil && !seenHealth[workload.UID] {
			reason := workload.OwnerFailure
			if reason == "" {
				reason = store.ReasonAPIUnavailable
			}
			c.observationHealthy.observe(observer, 0, "workload", ref.Namespace, ref.Group, ref.Version, ref.Kind, ref.Name, string(workload.UID), reason)
		}
		if workload.OwnerResolution == "" || !workload.HasStatus {
			continue
		}
		for _, phase := range c.options.WorkloadPhases {
			value := 0.0
			if slices.Contains(workload.Phases, phase) {
				value = 1
			}
			c.workloadStatus.observe(observer, value, ref.Namespace, ref.Name, ref.Group, ref.Version, ref.Kind, string(workload.UID), string(phase), workload.OwnerResolution, workload.Karta, workload.KartaSource)
		}
	}
	for _, pod := range snapshot.Pods {
		if err := ctx.Err(); err != nil {
			return err
		}
		root := active[pod.WorkloadUID]
		if pod.OwnerResolution == "" || root == nil || root.OwnerFailure != "" || root.OwnerResolution != pod.OwnerResolution || root.Ref != pod.Workload {
			continue
		}
		ref := pod.Workload
		c.podInfo.observe(observer, 1, ref.Namespace, ref.Name, ref.Group, ref.Version, ref.Kind, string(pod.WorkloadUID), pod.OwnerResolution, pod.Name, string(pod.UID), pod.Component, pod.Instance, pod.Replica)
	}
	return nil
}

func (g gauge) observe(observer metric.Observer, value float64, values ...string) {
	var attributes [12]attribute.KeyValue
	for i, name := range g.labels {
		attributes[i] = attribute.String(name, values[i])
	}
	observer.ObserveFloat64(g.instrument, value, metric.WithAttributeSet(attribute.NewSet(attributes[:len(g.labels)]...)))
}
