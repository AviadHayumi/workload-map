// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

type Capturer interface {
	Capture() store.CapturedView
}

// Collector formats one immutable publication view per collection.
type Collector struct {
	store            Capturer
	options          Options
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

type workloadKey struct {
	ref store.WorkloadRef
	uid types.UID
}

// New registers the KEP gauges. Unregister before shutting down the provider.
func New(meter metric.Meter, s Capturer, options Options) (metric.Registration, error) {
	if options.WorkloadPhases == nil {
		options.WorkloadPhases = slices.Clone(AllPhases)
	} else {
		options.WorkloadPhases = slices.Clone(options.WorkloadPhases)
	}
	c := &Collector{
		store:   s,
		options: options,
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
	if options.Identity {
		c.podInfo.labels = append(c.podInfo.labels, LabelSubmittedBy)
		c.workloadStatus.labels = append(c.workloadStatus.labels, LabelSubmittedBy)
	}
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
	failures := make(map[workloadKey]string)
	for _, h := range view.Health {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, reason := h.Status(view.At)
		if h.Scope == "workload" {
			if value == 0 {
				key := workloadKey{ref: h.Ref, uid: h.UID}
				failures[key] = failureReason(failures[key], reason)
			}
			continue
		}
		if h.Scope != "resource_type" {
			continue
		}
		if value == 0 {
			reason = failureReason("", reason)
		}
		c.observationValid.observe(observer, value, h.Scope, h.Ref.Namespace, h.Ref.Group, h.Ref.Version, h.Ref.Kind, "", "", reason)
	}
	for _, publication := range view.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		workload := publication.Workload
		ref := workload.Ref
		key := workloadKey{ref: ref, uid: workload.UID}
		for _, reason := range publication.FailureReasons {
			failures[key] = failureReason(failures[key], reason)
		}
		if valid, _ := view.Valid(publication.Creation); valid && workload.CreatedTimestamp != 0 {
			c.workloadCreated.observe(observer, float64(workload.CreatedTimestamp), ref.Namespace, ref.Name, string(workload.UID))
		}
		submittedBy := ""
		if c.options.Identity {
			if valid, _ := view.Valid(publication.Membership); valid {
				submittedBy = publication.SubmittedBy
			}
		}
		if workload.HasStatus {
			valid, reason := view.Valid(publication.Status)
			if !valid {
				failures[key] = failureReason(failures[key], reason)
			}
			for _, phase := range c.options.WorkloadPhases {
				value := 0.0
				if slices.Contains(workload.Phases, phase) {
					value = 1
				}
				values := []string{ref.Namespace, ref.Name, ref.Group, ref.Version, ref.Kind, string(workload.UID), string(phase), workload.OwnerResolution, workload.Karta, workload.KartaSource, strconv.FormatBool(valid)}
				if c.options.Identity {
					identity := ""
					if valid {
						identity = submittedBy
					}
					values = append(values, identity)
				}
				c.workloadStatus.observe(observer, value, values...)
			}
		}
		for _, page := range publication.PodPages {
			for _, row := range page.Rows {
				if err := ctx.Err(); err != nil {
					return err
				}
				pod := row.Record
				valid, reason := view.Valid(row.Mapping)
				if !valid {
					failures[key] = failureReason(failures[key], reason)
				}
				podRoot := pod.Workload
				values := []string{podRoot.Namespace, podRoot.Name, podRoot.Group, podRoot.Version, podRoot.Kind, string(workload.UID), pod.OwnerResolution, pod.Name, string(pod.UID), pod.Component, pod.Instance, pod.Replica, strconv.FormatBool(valid)}
				if c.options.Identity {
					identity := ""
					if valid {
						identity = submittedBy
					}
					values = append(values, identity)
				}
				c.podInfo.observe(observer, 1, values...)
			}
		}
	}
	for key, reason := range failures {
		if err := ctx.Err(); err != nil {
			return err
		}
		if reason != "" {
			c.observationValid.observe(observer, 0, "workload", key.ref.Namespace, key.ref.Group, key.ref.Version, key.ref.Kind, key.ref.Name, string(key.uid), reason)
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

func (g gauge) observe(observer metric.Observer, value float64, values ...string) {
	var attributes [14]attribute.KeyValue
	for i, name := range g.labels {
		attributes[i] = attribute.String(name, values[i])
	}
	observer.ObserveFloat64(g.instrument, value, metric.WithAttributeSet(attribute.NewSet(attributes[:len(g.labels)]...)))
}
