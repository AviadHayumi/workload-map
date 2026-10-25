// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelresolver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateConfig(t *testing.T) {
	base := LabelRule{Name: "team", Metrics: []string{"karta_pod_workload_info"}, Sources: []Source{SourceWorkload, SourcePod}, Expression: `object.metadata.labels["team"]`}
	require.NoError(t, ValidateConfig(Config{}))
	require.NoError(t, ValidateConfig(Config{Rules: []LabelRule{base}}))
	status := base
	status.Metrics = []string{"karta_workload_status"}
	status.Sources = []Source{SourceWorkload}
	require.NoError(t, ValidateConfig(Config{Rules: []LabelRule{base, status}}))
	for _, tc := range []struct {
		name   string
		change func(*LabelRule)
	}{
		{"empty name", func(r *LabelRule) { r.Name = "" }},
		{"invalid name", func(r *LabelRule) { r.Name = "my-team" }},
		{"internal name", func(r *LabelRule) { r.Name = "__name__" }},
		{"built in name", func(r *LabelRule) { r.Name = "workload_uid" }},
		{"target name", func(r *LabelRule) { r.Name = "instance" }},
		{"no metrics", func(r *LabelRule) { r.Metrics = nil }},
		{"unknown metric", func(r *LabelRule) { r.Metrics = []string{"other"} }},
		{"duplicate metric", func(r *LabelRule) { r.Metrics = []string{"karta_pod_workload_info", "karta_pod_workload_info"} }},
		{"no sources", func(r *LabelRule) { r.Sources = nil }},
		{"unknown source", func(r *LabelRule) { r.Sources = []Source{"other"} }},
		{"duplicate source", func(r *LabelRule) { r.Sources = []Source{SourcePod, SourcePod} }},
		{"pod for status", func(r *LabelRule) { r.Metrics = []string{"karta_workload_status"} }},
		{"empty expression", func(r *LabelRule) { r.Expression = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := base
			tc.change(&rule)
			require.Error(t, ValidateConfig(Config{Rules: []LabelRule{rule}}))
		})
	}
	require.Error(t, ValidateConfig(Config{Rules: []LabelRule{base, base}}))
}
