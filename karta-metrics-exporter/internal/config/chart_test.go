// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
)

func TestChartLabelConfiguration(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is needed for chart rendering")
	}
	chart := filepath.Join("..", "..", "..", "charts", "karta")
	rules := []labelresolver.LabelRule{{Name: "team", Metrics: []string{"karta_pod_workload_info"}, Sources: []labelresolver.Source{labelresolver.SourceWorkload, labelresolver.SourcePod}, Expression: `has(object.metadata.labels) && "team" in object.metadata.labels ? object.metadata.labels["team"] : ""`}}
	raw, err := json.Marshal(rules)
	require.NoError(t, err)
	out, err := exec.Command(helm, "template", "labels", chart, "--set", "exporter.enabled=true", "--set-json", "exporter.labelRules="+string(raw), "--set", "exporter.labelLimits.maxValueBytes=32").CombinedOutput()
	require.NoError(t, err, string(out))
	decoder := yaml.NewDecoder(bytes.NewReader(out))
	found := false
	for {
		var doc struct {
			Kind string            `yaml:"kind"`
			Data map[string]string `yaml:"data"`
		}
		err = decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if doc.Kind != "ConfigMap" || doc.Data["config.yaml"] == "" {
			continue
		}
		found = true
		cfg, err := loadConfig(t, doc.Data["config.yaml"], nil)
		require.NoError(t, err)
		require.Equal(t, rules, cfg.LabelRules)
		require.Equal(t, 32, cfg.LabelLimits.MaxValueBytes)
		for _, rule := range cfg.LabelRules {
			_, err = labelevaluator.NewCompiler().Compile(labelevaluator.Config{Expression: rule.Expression, Limits: cfg.LabelLimits})
			require.NoError(t, err)
		}
	}
	require.True(t, found, "rendered exporter config was not found")
	for _, enabled := range []string{"true", "false"} {
		out, err = exec.Command(helm, "template", "labels", chart, "--set", "exporter.enabled="+enabled, "--set", "exporter.attribution.submittedByExpression=old").CombinedOutput()
		require.Error(t, err)
		require.Contains(t, string(out), "exporter.attribution was removed")
	}
}
