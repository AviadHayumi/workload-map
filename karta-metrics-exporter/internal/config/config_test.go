// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("config defaults and rules", func() {
	It("loads label rules while retaining unspecified defaults", func() {
		cfg, err := loadConfig("", nil)
		Expect(err).To(Succeed())
		Expect(cfg.LabelLimits).To(Equal(defaultLabelLimits))
		Expect(cfg.LabelRules).To(BeEmpty())
		for _, contents := range []string{"\n", "labelRules: []\n---\n"} {
			cfg, err = loadConfig(contents, nil)
			Expect(err).To(Succeed())
			Expect(cfg.LabelLimits).To(Equal(defaultLabelLimits))
		}
		cfg, err = loadConfig(`labelRules:
- name: team
  metrics: [karta_pod_workload_info]
  sources: [workload, pod]
  expression: |
    has(object.metadata.labels) ? object.metadata.labels["team"] : ""
labelLimits:
  maxValueBytes: 32
`, nil)
		Expect(err).To(Succeed())
		Expect(cfg.LabelRules).To(HaveLen(1))
		Expect(cfg.LabelRules[0].Name).To(Equal("team"))
		Expect(cfg.LabelRules[0].Expression).To(ContainSubstring("object.metadata.labels"))
		Expect(cfg.LabelLimits.MaxValueBytes).To(Equal(32))
		Expect(cfg.LabelLimits.CostLimit).To(Equal(defaultLabelLimits.CostLimit))
	})
})

var _ = Describe("config precedence", func() {
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
		It(tc.name, func() {
			cfg, err := loadConfig("labelLimits:\n  costLimit: 5\n", tc.env, tc.args...)
			Expect(err).To(Succeed())
			Expect(cfg.LabelLimits.CostLimit).To(Equal(tc.want))
		})
	}
})

var _ = Describe("config path and empty override", func() {
	It("lets explicit empty flags override environment values", func() {
		path := filepath.Join(GinkgoT().TempDir(), "config.yaml")
		Expect(os.WriteFile(path, []byte("labelLimits:\n  maxValueBytes: 32\n"), 0o600)).To(Succeed())
		cfg, err := loadConfig("", map[string]string{"KARTA_EXPORTER_CONFIG": path})
		Expect(err).To(Succeed())
		Expect(cfg.LabelLimits.MaxValueBytes).To(Equal(32))
		cfg, err = loadConfig("", map[string]string{"KARTA_EXPORTER_CONFIG": path}, "--config=")
		Expect(err).To(Succeed())
		Expect(cfg.LabelLimits).To(Equal(defaultLabelLimits))
		_, err = loadConfig("", map[string]string{"KARTA_EXPORTER_LABEL_COST_LIMIT": "7"}, "--label-cost-limit=")
		Expect(err).To(HaveOccurred())
		_, err = loadConfig("", map[string]string{"KARTA_EXPORTER_LABEL_COST_LIMIT": ""})
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("config rejects invalid input", func() {
	It("rejects invalid files, flags and environment values", func() {
		for _, contents := range []string{
			"labelLimits:\n  costLimit: lots",
			"labelLimits:\n  maxValueBytes: -1", "labelLimits:\n  maxExpressionBytes: -1",
			"labelLimits:\n  interruptCheckFrequency: 0", "labelLimits:\n  interruptCheckFrequency: -1",
			"labelRules: [", "labelRules: []\n---\nlabelRules: []", "labelRules: []\n---\n---\nlabelRules: []",
			"labelRules: []\n---\nlabelRules: [", "unknown: true",
			"labelRules:\n- name: team\n  metrics: [karta_workload_status]\n  sources: [pod]\n  expression: object.kind",
		} {
			By(contents, func() {
				_, err := loadConfig(contents, nil)
				Expect(err).To(HaveOccurred())
			})
		}
		for _, args := range [][]string{
			{"--unknown-option", "1"},
			{"--config", "/does-not-exist/config.yaml"}, {"--label-cost-limit", "lots"},
			{"--label-interrupt-check-frequency", "0"}, {"--label-interrupt-check-frequency", "-1"},
			{"--label-max-value-bytes", "-1"},
		} {
			_, err := loadConfig("", nil, args...)
			Expect(err).To(HaveOccurred())
		}
		for _, value := range []string{"lots", "0", "-1"} {
			_, err := loadConfig("", map[string]string{"KARTA_EXPORTER_LABEL_INTERRUPT_CHECK_FREQUENCY": value})
			Expect(err).To(HaveOccurred())
		}
	})
})

func loadConfig(content string, env map[string]string, args ...string) (Config, error) {
	GinkgoHelper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	loader := NewLoader(fs, func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	})
	if content != "" {
		path := filepath.Join(GinkgoT().TempDir(), "config.yaml")
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		args = append([]string{"--config", path}, args...)
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return loader.Load()
}
