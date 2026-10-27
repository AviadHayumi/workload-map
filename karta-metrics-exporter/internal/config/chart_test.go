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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
)

var _ = Describe("chart label configuration", func() {
	It("loads and compiles the configuration rendered by Helm", func() {
		helm, err := exec.LookPath("helm")
		if err != nil {
			Skip("helm is needed for chart rendering")
		}
		chart := filepath.Join("..", "..", "..", "charts", "karta")
		rules := []labelresolver.LabelRule{{
			Name:       "team",
			Metrics:    []string{"karta_pod_workload_info"},
			Sources:    []labelresolver.Source{labelresolver.SourceWorkload, labelresolver.SourcePod},
			Expression: `has(object.metadata.labels) && "team" in object.metadata.labels ? object.metadata.labels["team"] : ""`,
		}}
		raw, err := json.Marshal(rules)
		Expect(err).To(Succeed())
		out, err := exec.Command(helm, "template", "labels", chart,
			"--set", "exporter.enabled=true",
			"--set-json", "exporter.labelRules="+string(raw),
			"--set", "exporter.labelLimits.maxValueBytes=32",
			"--set", "exporter.workers=3",
			"--set", "exporter.leafWorkers=4",
			"--set", "exporter.rootWorkers=5",
		).CombinedOutput()
		Expect(err).To(Succeed(), string(out))
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
			Expect(err).To(Succeed())
			if doc.Kind != "ConfigMap" || doc.Data["config.yaml"] == "" {
				continue
			}
			found = true
			cfg, err := loadConfig(doc.Data["config.yaml"], nil)
			Expect(err).To(Succeed())
			Expect(cfg.LabelRules).To(Equal(rules))
			Expect(cfg.LabelLimits.MaxValueBytes).To(Equal(32))
			Expect(cfg.Workers).To(Equal(3))
			Expect(cfg.LeafWorkers).To(Equal(4))
			Expect(cfg.RootWorkers).To(Equal(5))
			for _, rule := range cfg.LabelRules {
				_, err = labelevaluator.Compile(labelevaluator.Config{Expression: rule.Expression, Limits: cfg.LabelLimits})
				Expect(err).To(Succeed())
			}
		}
		Expect(found).To(BeTrue(), "rendered exporter config was not found")
	})
})
