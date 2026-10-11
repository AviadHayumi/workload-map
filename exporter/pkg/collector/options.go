// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

// Options configures metric selection and exporter diagnostics.
type Options struct {
	PendingPods func() int
	KartaCounts func() (valid, invalid, shadowed int)
	// Nil selects all phases; an empty non-nil slice disables the family.
	WorkloadPhases []v1alpha1.ResourceStatus
	PodPhases      []corev1.PodPhase
	// Empty selects counts. Inventory and both add pod_phase to attribution.
	PodMetricsMode string
}

const (
	PodMetricsCounts    = "counts"
	PodMetricsInventory = "inventory"
	PodMetricsBoth      = "both"
)

// ParseOptions validates CLI metric selection before any cluster access.
func ParseOptions(workloadPhases, podPhases, mode string) (Options, error) {
	switch mode {
	case PodMetricsCounts, PodMetricsInventory, PodMetricsBoth:
	default:
		return Options{}, fmt.Errorf("invalid pod metrics mode %q: choose counts, inventory, or both", mode)
	}
	options := Options{PodMetricsMode: mode}
	allowed := make([]string, 0, len(AllPhases))
	for _, phase := range AllPhases {
		allowed = append(allowed, string(phase))
	}
	selected, err := parsePhaseNames(workloadPhases, allowed)
	if err != nil {
		return Options{}, fmt.Errorf("workload phases: %w", err)
	}
	options.WorkloadPhases = make([]v1alpha1.ResourceStatus, 0, len(selected))
	for _, phase := range selected {
		options.WorkloadPhases = append(options.WorkloadPhases, v1alpha1.ResourceStatus(phase))
	}
	allowed = allowed[:0]
	for _, phase := range AllPodPhases {
		allowed = append(allowed, string(phase))
	}
	selected, err = parsePhaseNames(podPhases, allowed)
	if err != nil {
		return Options{}, fmt.Errorf("pod phases: %w", err)
	}
	options.PodPhases = make([]corev1.PodPhase, 0, len(selected))
	for _, phase := range selected {
		options.PodPhases = append(options.PodPhases, corev1.PodPhase(phase))
	}
	return options, nil
}

func parsePhaseNames(value string, allowed []string) ([]string, error) {
	switch value {
	case "all":
		return slices.Clone(allowed), nil
	case "none":
		return []string{}, nil
	}
	selected := strings.Split(value, ",")
	for i, phase := range selected {
		if !slices.Contains(allowed, phase) {
			return nil, fmt.Errorf("unknown phase %q (expected %s, all, or none)", phase, strings.Join(allowed, ","))
		}
		if slices.Contains(selected[:i], phase) {
			return nil, fmt.Errorf("duplicate phase %q", phase)
		}
	}
	return selected, nil
}
