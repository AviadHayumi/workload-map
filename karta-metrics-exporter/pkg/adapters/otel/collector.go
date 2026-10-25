// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package otel

import (
	"go.opentelemetry.io/otel/metric"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Capturer supplies one immutable publication snapshot per collection.
type Capturer interface {
	Capture() store.CapturedView
}

type Options struct {
	WorkloadPhases []string // Empty selects all phases.
}

// Registrar registers a collection callback. The caller must unregister it.
type Registrar interface {
	Register(metric.Meter, Capturer, Options) (metric.Registration, error)
}
