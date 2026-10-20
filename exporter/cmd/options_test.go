// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadOptionsRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, config string
	}{
		{"null", `null`},
		{"unknown_field", `{"unknown":true}`},
		{"unknown_source_field", `{"kartaDefinitionSource":{"cluster":{"unknown":true}}}`},
		{"multiple_objects", `{} {}`},
		{"trailing_input", `{} trailing`},
		{"invalid_selector", `{"podSelector":{"matchExpressions":[{"key":"team","operator":"invalid"}]}}`},
		{"incomplete_resource_rule", `{"resourceRules":[{"apiGroups":["batch"],"resources":["jobs"]}]}`},
		{"invalid_owner_kind", `{"ignoredOwnerKinds":["Job"]}`},
		{"invalid_required_kind", `{"requiredKinds":["batch/v1/*"]}`},
		{"invalid_source_selector", `{"kartaDefinitionSource":{"cluster":{"labelSelector":{"matchExpressions":[{"key":"team","operator":"invalid"}]}}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOptions(path, "", "prometheus", "all"); err == nil {
				t.Fatal("invalid configuration accepted before client creation")
			}
		})
	}
	for _, expression := range []string{`{"submittedByExpression":"("}`, `{"rules":[]}`} {
		path := filepath.Join(t.TempDir(), "identity.json")
		if err := os.WriteFile(path, []byte(expression), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOptions("", path, "prometheus", "all"); err == nil {
			t.Fatalf("invalid submitter configuration accepted: %s", expression)
		}
	}
}

func TestLoadOptionsSelectsConfiguredDelivery(t *testing.T) {
	for _, name := range []string{"CERTIFICATE", "CLIENT_CERTIFICATE", "CLIENT_KEY", "HEADERS_FILE"} {
		t.Setenv("OTEL_EXPORTER_OTLP_"+name, "")
		t.Setenv("OTEL_EXPORTER_OTLP_METRICS_"+name, "")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "https://metrics.example.com/v1/metrics")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "x-test=placeholder")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "45000")
	t.Setenv("OTEL_METRIC_EXPORT_TIMEOUT", "5000")
	options, err := loadOptions("", "", "otlp", "none")
	if err != nil {
		t.Fatal(err)
	}
	if options.mode != "otlp" || options.otlp.Endpoint != "https://metrics.example.com/v1/metrics" ||
		options.otlp.Headers["x-test"] != "placeholder" || options.otlp.Interval != 45*time.Second || options.otlp.Timeout != 5*time.Second {
		t.Fatalf("configured OTLP delivery changed: %#v", options.otlp)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "invalid")
	if _, err := loadOptions("", "", "otlp", "all"); err == nil {
		t.Fatal("invalid OTLP configuration accepted")
	}
	if _, err := loadOptions("", "", "prometheus", "all"); err != nil {
		t.Fatalf("Prometheus unexpectedly read OTLP configuration: %v", err)
	}
	if _, err := loadOptions("", "", "both", "all"); err == nil {
		t.Fatal("multiple delivery modes accepted")
	}
}
