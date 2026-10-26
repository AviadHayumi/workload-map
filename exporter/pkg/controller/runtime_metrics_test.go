// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/telemetry"
)

func TestGenericOwnerVersionsReachMetrics(t *testing.T) {
	firstRoot := runtimeJob("work", "root")
	secondRoot := firstRoot.DeepCopy()
	secondRoot.SetAPIVersion("batch/v2")
	fixture := startConnected(t, []*unstructured.Unstructured{
		firstRoot, secondRoot,
		runtimePod("first", "first", firstRoot),
		runtimePod("second", "second", secondRoot),
	}, func(options *RuntimeOptions) {
		options.Definitions.Catalog.Enabled = false
	}, "")
	registry := prometheus.NewRegistry()
	provider, err := telemetry.NewPrometheusProvider(registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	registration, err := collector.New(provider.Meter("karta-exporter"), fixture.store, collector.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registration.Unregister(); err != nil {
			t.Error(err)
		}
	})
	awaitRuntime(t, func() bool {
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		versions := map[string]string{}
		for _, family := range families {
			if family.GetName() != collector.MetricPodWorkloadInfo {
				continue
			}
			for _, row := range family.Metric {
				labels := map[string]string{}
				for _, pair := range row.Label {
					labels[pair.GetName()] = pair.GetValue()
				}
				if labels["observation_valid"] != "true" || labels["workload_uid"] != "root" {
					return false
				}
				versions[labels["pod_uid"]] = labels["workload_version"]
			}
		}
		return len(versions) == 2 && versions["first"] == "v1" && versions["second"] == "v2"
	})
}
