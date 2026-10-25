// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package app

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/metric"

	collector "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/adapters/otel"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/controller"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type Components struct {
	Runtime   controller.Runner
	Store     store.Interface
	Registrar collector.Registrar
}

// Run registers collection before starting workers, then unregisters on shutdown.
func Run(ctx context.Context, components Components, meter metric.Meter, options collector.Options) (err error) {
	if components.Runtime == nil || components.Store == nil || components.Registrar == nil || meter == nil {
		return errors.New("incomplete exporter components")
	}
	registration, err := components.Registrar.Register(meter, components.Store, options)
	if err != nil {
		return fmt.Errorf("register collector: %w", err)
	}
	if registration == nil {
		return errors.New("collector returned no registration")
	}
	defer func() { err = errors.Join(err, registration.Unregister()) }()
	return components.Runtime.Run(ctx)
}
