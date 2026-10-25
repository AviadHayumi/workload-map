// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package otel

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type Capturer interface {
	Capture() store.CapturedView
}

// Collector formats one immutable publication view per collection.
type Collector struct {
	store            Capturer
	podInfo          gauge
	workloadStatus   gauge
	workloadCreated  gauge
	observationValid gauge
}

type gauge struct {
	name        string
	description string
	labels      []string
	instrument  metric.Float64ObservableGauge
}

// Registrar registers a callback. The caller must unregister it.
type Registrar interface {
	Register(metric.Meter, Capturer) (metric.Registration, error)
}

func New() Registrar { return &Collector{} }

// Register registers the KEP gauges. Unregister before shutting down the provider.
func (*Collector) Register(meter metric.Meter, s Capturer) (metric.Registration, error) {
	c := &Collector{
		store: s,
		podInfo: gauge{
			name:        MetricPodWorkloadInfo,
			description: "Pod attribution to a confirmed workload root. Value is always 1.",
			labels:      []string{LabelNamespace, LabelWorkload, LabelWorkloadGroup, LabelWorkloadVersion, LabelWorkloadKind, LabelWorkloadUID, LabelOwnerResolution, LabelPod, LabelPodUID, LabelComponent, LabelComponentInstance, LabelReplicaGroupID, LabelObservationValid},
		},
		workloadStatus: gauge{
			name:        MetricWorkloadStatus,
			description: "Normalized root workload status. Each phase is 0 or 1; several phases can match.",
			labels:      []string{LabelNamespace, LabelWorkload, LabelWorkloadGroup, LabelWorkloadVersion, LabelWorkloadKind, LabelWorkloadUID, LabelPhase, LabelOwnerResolution, LabelKarta, LabelKartaSource, LabelObservationValid},
		},
		workloadCreated: gauge{
			name:        MetricWorkloadCreatedTimestamp,
			description: "Verified root workload creation time in Unix seconds.",
			labels:      []string{LabelNamespace, LabelWorkload, LabelWorkloadUID},
		},
		observationValid: gauge{
			name:        MetricExporterObservationValid,
			description: "Resource-type collection validity and known workload failures. Not application readiness.",
			labels:      []string{"scope", LabelNamespace, LabelResourceGroup, LabelResourceVersion, LabelResourceKind, LabelWorkload, LabelWorkloadUID, LabelReason},
		},
	}
	gauges := []*gauge{&c.podInfo, &c.workloadStatus, &c.workloadCreated, &c.observationValid}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	view := c.store.Capture()
	for _, h := range view.Health {
		valid, reason := view.Valid(h.Evidence)
		value := 0.0
		if valid {
			value = 1
		} else {
			reason = failureReason("", reason)
		}
		c.observationValid.observe(observer, value, nil, "resource_type", h.Namespace, h.GVK.Group, h.GVK.Version, h.GVK.Kind, "", "", reason)
	}
	for _, publication := range view.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		workload := publication.Workload
		ref := workload.Ref
		reason := ""
		for _, failure := range publication.FailureReasons {
			reason = failureReason(reason, failure)
		}
		if valid, failure := view.Valid(publication.Creation); !valid {
			reason = failureReason(reason, failure)
		} else if workload.CreatedTimestamp > 0 {
			c.workloadCreated.observe(observer, float64(workload.CreatedTimestamp), nil, ref.Namespace, ref.Name, string(workload.UID))
		}
		if workload.HasStatus {
			valid, failure := view.Valid(publication.Status)
			if !valid {
				reason = failureReason(reason, failure)
			}
			for _, phase := range allPhases {
				value := 0.0
				if slices.Contains(workload.Phases, string(phase)) {
					value = 1
				}
				values := []string{ref.Namespace, ref.Name, ref.GVK.Group, ref.GVK.Version, ref.GVK.Kind, string(workload.UID), string(phase), string(workload.OwnerResolution), workload.Karta, workload.KartaSource, strconv.FormatBool(valid)}
				c.workloadStatus.observe(observer, value, workload.StatusLabels, values...)
			}
		}
		for _, page := range publication.PodPages {
			for _, row := range page.Rows {
				if err := ctx.Err(); err != nil {
					return err
				}
				pod := row.Record
				valid, failure := view.Valid(row.Mapping)
				if !valid {
					reason = failureReason(reason, failure)
				}
				podRoot := ref
				if pod.WorkloadGVK != (schema.GroupVersionKind{}) {
					podRoot.GVK = pod.WorkloadGVK
				}
				values := []string{pod.Namespace, podRoot.Name, podRoot.GVK.Group, podRoot.GVK.Version, podRoot.GVK.Kind, string(workload.UID), string(pod.OwnerResolution), pod.Name, string(pod.UID), pod.Component, pod.Instance, pod.Group, strconv.FormatBool(valid)}
				c.podInfo.observe(observer, 1, pod.Labels, values...)
			}
		}
		if reason != "" {
			c.observationValid.observe(observer, 0, nil, "workload", ref.Namespace, ref.GVK.Group, ref.GVK.Version, ref.GVK.Kind, ref.Name, string(workload.UID), reason)
		}
	}

	return nil
}

func failureReason(current, next string) string {
	reasons := []string{store.ReasonPermissionDenied, store.ReasonAPIUnavailable, store.ReasonInvalidDefinition, store.ReasonUnresolvedOwner, store.ReasonEvaluationFailed}
	if next == "" || next == store.ReasonNone {
		return current
	}
	if !slices.Contains(reasons, next) {
		next = store.ReasonAPIUnavailable
	}
	for _, reason := range reasons {
		if reason == current || reason == next {
			return reason
		}
	}
	return ""
}

func (g gauge) observe(observer metric.Observer, value float64, custom map[string]string, values ...string) {
	attributes := make([]attribute.KeyValue, 0, len(custom)+len(g.labels))
	for name, value := range custom {
		attributes = append(attributes, attribute.String(name, value))
	}
	for i, name := range g.labels {
		attributes = append(attributes, attribute.String(name, values[i]))
	}
	observer.ObserveFloat64(g.instrument, value, metric.WithAttributeSet(attribute.NewSet(attributes...)))
}
