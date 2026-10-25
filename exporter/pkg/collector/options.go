// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

type Options struct {
	Identity bool
	// Nil selects all phases; an empty non-nil slice disables status output.
	WorkloadPhases []v1alpha1.ResourceStatus
}

// ParseOptions validates phase selection before cluster access.
func ParseOptions(value string) (Options, error) {
	if value == "all" {
		return Options{WorkloadPhases: slices.Clone(AllPhases)}, nil
	}
	options := Options{WorkloadPhases: []v1alpha1.ResourceStatus{}}
	if value == "none" {
		return options, nil
	}
	for _, name := range strings.Split(value, ",") {
		phase := v1alpha1.ResourceStatus(name)
		if !slices.Contains(AllPhases, phase) {
			return Options{}, fmt.Errorf("unknown workload phase %q", name)
		}
		if slices.Contains(options.WorkloadPhases, phase) {
			return Options{}, fmt.Errorf("duplicate workload phase %q", name)
		}
		options.WorkloadPhases = append(options.WorkloadPhases, phase)
	}
	return options, nil
}
