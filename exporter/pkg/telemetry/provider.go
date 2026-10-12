// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package telemetry

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// ParseExporter validates the configured delivery mode before cluster access.
func ParseExporter(value string) (string, error) {
	switch value {
	case "", "prometheus":
		return "prometheus", nil
	default:
		return "", fmt.Errorf("unsupported metrics exporter %q: this release supports prometheus", value)
	}
}

// NewPrometheusProvider creates OTel instruments with the existing Prometheus wire contract.
// The caller must shut down the provider after observation and HTTP serving stop.
func NewPrometheusProvider(registerer prometheus.Registerer) (*sdkmetric.MeterProvider, error) {
	reader, err := otelprom.New(
		otelprom.WithRegisterer(registerer),
		otelprom.WithTranslationStrategy(otlptranslator.NoTranslation),
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("create Prometheus reader: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		// Overflow would discard the pod identities needed for joins. Current-state
		// callbacks release deleted observations; capacity follows the watched scope.
		sdkmetric.WithCardinalityLimit(0),
	), nil
}
