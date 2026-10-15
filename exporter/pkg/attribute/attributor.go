// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package attribute

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
)

// Result is the pod-to-component attribution outcome. A failed step degrades its
// field to the existing sentinel, so workload aggregation retains the pod.
type Result struct {
	Component string
	Instance  string
	Replica   string
	Reason    string
}

// Attribute projects shared attribution semantics using the exporter's legacy
// first-match selector policy and field degradation contract.
func Attribute(ctx context.Context, pod *corev1.Pod, entry *registry.Entry, instanceIDs map[string][]string) Result {
	return Result(attribution.AttributeLegacy(ctx, pod, entry.Summary, entry.Definitions, instanceIDs))
}

// InstanceIDs lists declared instance IDs for pod-producing components.
func InstanceIDs(ctx context.Context, entry *registry.Entry, workload resource.KubernetesObject) (map[string][]string, error) {
	return attribution.InstanceIDs(ctx, entry.Karta, workload, false)
}
