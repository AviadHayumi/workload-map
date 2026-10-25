// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadConfig(t *testing.T, content string, env map[string]string, args ...string) (Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	loader := NewLoader(fs, func(key string) (string, bool) { v, ok := env[key]; return v, ok })
	if content != "" {
		path := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0600))
		args = append([]string{"--config", path}, args...)
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return loader.Load()
}

func TestConfigDefaultsAndRules(t *testing.T) {
	cfg, err := loadConfig(t, "", nil)
	require.NoError(t, err)
	require.Equal(t, defaultLabelLimits, cfg.LabelLimits)
	require.Empty(t, cfg.LabelRules)
	for _, contents := range []string{"\n", "labelRules: []\n---\n"} {
		cfg, err = loadConfig(t, contents, nil)
		require.NoError(t, err)
		require.Equal(t, defaultLabelLimits, cfg.LabelLimits)
	}
	cfg, err = loadConfig(t, `labelRules:
- name: team
  metrics: [karta_pod_workload_info]
  sources: [workload, pod]
  expression: |
    has(object.metadata.labels) ? object.metadata.labels["team"] : ""
labelLimits:
  maxValueBytes: 32
`, nil)
	require.NoError(t, err)
	require.Len(t, cfg.LabelRules, 1)
	require.Equal(t, "team", cfg.LabelRules[0].Name)
	require.Contains(t, cfg.LabelRules[0].Expression, "object.metadata.labels")
	require.Equal(t, 32, cfg.LabelLimits.MaxValueBytes)
	require.Equal(t, defaultLabelLimits.CostLimit, cfg.LabelLimits.CostLimit)
}

func TestConfigPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want uint64
	}{
		{name: "file", want: 5},
		{name: "environment", env: map[string]string{"KARTA_EXPORTER_LABEL_COST_LIMIT": "7"}, want: 7},
		{name: "flag", env: map[string]string{"KARTA_EXPORTER_LABEL_COST_LIMIT": "7"}, args: []string{"--label-cost-limit", "9"}, want: 9},
		{name: "explicit zero", args: []string{"--label-cost-limit", "0"}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfig(t, "labelLimits:\n  costLimit: 5\n", tc.env, tc.args...)
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.LabelLimits.CostLimit)
		})
	}
}

func TestConfigPathAndEmptyOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("labelLimits:\n  maxValueBytes: 32\n"), 0600))
	cfg, err := loadConfig(t, "", map[string]string{"KARTA_EXPORTER_CONFIG": path})
	require.NoError(t, err)
	require.Equal(t, 32, cfg.LabelLimits.MaxValueBytes)
	cfg, err = loadConfig(t, "", map[string]string{"KARTA_EXPORTER_CONFIG": path}, "--config=")
	require.NoError(t, err)
	require.Equal(t, defaultLabelLimits, cfg.LabelLimits)
	_, err = loadConfig(t, "", map[string]string{"KARTA_EXPORTER_LABEL_COST_LIMIT": "7"}, "--label-cost-limit=")
	require.Error(t, err)
	_, err = loadConfig(t, "", map[string]string{"KARTA_EXPORTER_LABEL_COST_LIMIT": ""})
	require.Error(t, err)
}

func TestConfigRejectsInvalidInput(t *testing.T) {
	for _, contents := range []string{
		"attribution: {}", "labelLimits:\n  maxIdentityBytes: 1", "labelLimits:\n  costLimit: lots",
		"labelLimits:\n  maxValueBytes: -1", "labelLimits:\n  maxExpressionBytes: -1",
		"labelLimits:\n  interruptCheckFrequency: 0", "labelLimits:\n  interruptCheckFrequency: -1",
		"labelRules: [", "labelRules: []\n---\nlabelRules: []", "labelRules: []\n---\n---\nlabelRules: []",
		"labelRules: []\n---\nlabelRules: [", "unknown: true",
		"labelRules:\n- name: team\n  metrics: [karta_workload_status]\n  sources: [pod]\n  expression: object.kind",
	} {
		t.Run(contents, func(t *testing.T) { _, err := loadConfig(t, contents, nil); require.Error(t, err) })
	}
	for _, args := range [][]string{
		{"--submitted-by-expression", `"alice"`}, {"--submitter-cost-limit", "1"},
		{"--config", "/does-not-exist/config.yaml"}, {"--label-cost-limit", "lots"},
		{"--label-interrupt-check-frequency", "0"}, {"--label-interrupt-check-frequency", "-1"},
		{"--label-max-value-bytes", "-1"},
	} {
		_, err := loadConfig(t, "", nil, args...)
		require.Error(t, err)
	}
	for _, value := range []string{"lots", "0", "-1"} {
		_, err := loadConfig(t, "", map[string]string{"KARTA_EXPORTER_LABEL_INTERRUPT_CHECK_FREQUENCY": value})
		require.Error(t, err)
	}
}

func TestRejectsRemovedSubmitterEnvironment(t *testing.T) {
	_, err := loadConfig(t, "", map[string]string{"KARTA_EXPORTER_SUBMITTED_BY_EXPRESSION": "old"})
	require.ErrorContains(t, err, "was removed")
}
