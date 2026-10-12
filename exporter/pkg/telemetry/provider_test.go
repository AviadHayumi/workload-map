// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package telemetry

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func TestParseExporter(t *testing.T) {
	for _, input := range []string{"", "prometheus", "otlp", "none", "prometheus,otlp", "invalid"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseExporter(input)
			if input == "" || input == "prometheus" {
				if err != nil || got != "prometheus" {
					t.Fatalf("ParseExporter(%q) = %q, %v; want prometheus, nil", input, got, err)
				}
			} else if err == nil {
				t.Fatalf("ParseExporter(%q) accepted unsupported output", input)
			}
		})
	}
}

func TestDiagnosticInstrumentsRetainNamesAndValues(t *testing.T) {
	registry := prometheus.NewRegistry()
	provider, err := NewPrometheusProvider(registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	meter := provider.Meter("karta-exporter")
	counter, err := meter.Int64Counter("karta_exporter_attribution_errors_total")
	if err != nil {
		t.Fatal(err)
	}
	gauge, err := meter.Float64Gauge("karta_exporter_last_event_timestamp_seconds")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(t.Context(), 2, metric.WithAttributes(attribute.String("reason", "jq_error")))
	gauge.Record(t.Context(), 1755600000)
	for range 2 {
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		if len(families) != 2 {
			t.Fatalf("families = %d, want 2 without target/scope extras", len(families))
		}
		for _, family := range families {
			if len(family.Metric) != 1 {
				t.Fatalf("%s has %d samples, want 1", family.GetName(), len(family.Metric))
			}
			sample := family.Metric[0]
			switch family.GetName() {
			case "karta_exporter_attribution_errors_total":
				if sample.GetCounter().GetValue() != 2 || len(sample.Label) != 1 || sample.Label[0].GetName() != "reason" || sample.Label[0].GetValue() != "jq_error" {
					t.Fatalf("counter contract changed: %v", sample)
				}
			case "karta_exporter_last_event_timestamp_seconds":
				if sample.GetGauge().GetValue() != 1755600000 || len(sample.Label) != 0 {
					t.Fatalf("gauge contract changed: %v", sample)
				}
			default:
				t.Fatalf("unexpected family %q", family.GetName())
			}
		}
	}
}
