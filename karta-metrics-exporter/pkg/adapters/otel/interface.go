// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package otel

import (
	"go.opentelemetry.io/otel/metric"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type Capturer interface {
	Capture() store.CapturedView
}

type Options struct {
	WorkloadPhases []string
}

// RegisterFunc registers a collection callback. The caller must unregister it.
type RegisterFunc func(metric.Meter, Capturer, Options) (metric.Registration, error)
