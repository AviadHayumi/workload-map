// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package telemetry

import (
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"net/url"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc/credentials"
)

// ParseExporter validates the configured delivery mode before cluster access.
func ParseExporter(value string) (string, error) {
	switch value {
	case "", "prometheus":
		return "prometheus", nil
	case "otlp":
		return "otlp", nil
	default:
		return "", fmt.Errorf("unsupported metrics exporter %q: choose prometheus or otlp", value)
	}
}

// NewOTLPProvider exports current observations periodically, even when unchanged.
// The caller must shut down the provider with a bounded context.
func NewOTLPProvider(ctx context.Context, config OTLPConfig) (*sdkmetric.MeterProvider, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(config.Endpoint)
	headers := maps.Clone(config.Headers)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
	}
	var exporter sdkmetric.Exporter
	var err error
	switch config.Protocol {
	case "http/protobuf":
		if endpoint.Path == "" || endpoint.Path == "/" {
			endpoint.Path = "/v1/metrics"
		}
		options := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpointURL(endpoint.String()),
			otlpmetrichttp.WithHeaders(headers),
			otlpmetrichttp.WithTimeout(config.Timeout),
		}
		if endpoint.Scheme == "https" {
			options = append(options, otlpmetrichttp.WithTLSClientConfig(tlsConfig))
		}
		exporter, err = otlpmetrichttp.New(ctx, options...)
	case "grpc":
		options := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpointURL(endpoint.String()),
			otlpmetricgrpc.WithHeaders(headers),
			otlpmetricgrpc.WithTimeout(config.Timeout),
		}
		if endpoint.Scheme == "http" {
			options = append(options, otlpmetricgrpc.WithInsecure())
		} else {
			options = append(options, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(tlsConfig)))
		}
		exporter, err = otlpmetricgrpc.New(ctx, options...)
	}
	if err != nil {
		return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(exporter,
		sdkmetric.WithInterval(config.Interval),
		sdkmetric.WithTimeout(config.Timeout),
	)
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithCardinalityLimit(0),
	), nil
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
