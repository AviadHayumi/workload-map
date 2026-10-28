// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package config

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
			"workers: 2\nworkers: 3", "labelLimits:\n  costLimit: 2\n  costLimit: 3",
			"namespaceSelector: {unknown: value}",
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

var _ = Describe("runtime configuration", func() {
	DescribeTable("rejects retired freshness settings from every input", func(key, flagName, envName string) {
		for _, value := range []string{"2m", "''", "null", "0"} {
			_, err := loadConfig(key+": "+value, nil)
			Expect(err).To(MatchError(errRetiredFreshness))
		}
		for _, value := range []string{"2m", ""} {
			_, err := loadConfig("", nil, "--"+flagName+"="+value)
			Expect(err).To(MatchError(ContainSubstring(errRetiredFreshness.Error())))
			_, err = loadConfig("", map[string]string{envName: value})
			Expect(err).To(MatchError(ContainSubstring(errRetiredFreshness.Error())))
		}
	},
		Entry("refresh interval", "refreshInterval", "refresh-interval", "KARTA_EXPORTER_REFRESH_INTERVAL"),
		Entry("max age", "maxAge", "max-age", "KARTA_EXPORTER_MAX_AGE"),
	)
	It("keeps the shared worker default when queue overrides are unset or zero", func() {
		cfg, err := loadConfig("", nil)
		Expect(err).To(Succeed())
		Expect(cfg.Workers).To(Equal(2))
		Expect(cfg.LeafWorkers).To(BeZero())
		Expect(cfg.RootWorkers).To(BeZero())

		cfg, err = loadConfig("workers: 4\nleafWorkers: 7\nrootWorkers: 8", map[string]string{
			"KARTA_EXPORTER_LEAF_WORKERS": "0",
		}, "--root-workers=0")
		Expect(err).To(Succeed())
		Expect(cfg.Workers).To(Equal(4))
		Expect(cfg.LeafWorkers).To(BeZero())
		Expect(cfg.RootWorkers).To(BeZero())
	})
	It("loads queue worker overrides with flag, environment and file precedence", func() {
		cfg, err := loadConfig("workers: 3\nleafWorkers: 4\nrootWorkers: 5", nil)
		Expect(err).To(Succeed())
		Expect(cfg.Workers).To(Equal(3))
		Expect(cfg.LeafWorkers).To(Equal(4))
		Expect(cfg.RootWorkers).To(Equal(5))

		cfg, err = loadConfig("workers: 3\nleafWorkers: 4\nrootWorkers: 5", map[string]string{
			"KARTA_EXPORTER_LEAF_WORKERS": "6",
			"KARTA_EXPORTER_ROOT_WORKERS": "7",
		})
		Expect(err).To(Succeed())
		Expect(cfg.Workers).To(Equal(3))
		Expect(cfg.LeafWorkers).To(Equal(6))
		Expect(cfg.RootWorkers).To(Equal(7))

		cfg, err = loadConfig("workers: 3\nleafWorkers: 4\nrootWorkers: 5", map[string]string{
			"KARTA_EXPORTER_LEAF_WORKERS": "6",
			"KARTA_EXPORTER_ROOT_WORKERS": "7",
		}, "--leaf-workers=8", "--root-workers=9")
		Expect(err).To(Succeed())
		Expect(cfg.Workers).To(Equal(3))
		Expect(cfg.LeafWorkers).To(Equal(8))
		Expect(cfg.RootWorkers).To(Equal(9))
	})
	DescribeTable("rejects invalid queue worker overrides", func(key, flagName, envName string) {
		_, err := loadConfig(key+": -1", nil)
		Expect(err).To(MatchError("leafWorkers and rootWorkers must not be negative"))
		for _, value := range []string{"-1", "many", ""} {
			_, err = loadConfig("", nil, "--"+flagName+"="+value)
			Expect(err).To(HaveOccurred())
			_, err = loadConfig("", map[string]string{envName: value})
			Expect(err).To(HaveOccurred())
		}
	},
		Entry("leaf", "leafWorkers", "leaf-workers", "KARTA_EXPORTER_LEAF_WORKERS"),
		Entry("root", "rootWorkers", "root-workers", "KARTA_EXPORTER_ROOT_WORKERS"),
	)
	It("loads resource filters and bounded worker/API settings with flag precedence", func() {
		cfg, err := loadConfig("resourceRules:\n- apiGroups: [batch]\n  apiVersions: [v1]\n  resources: [jobs]\napiQPS: 30\nworkers: 3\n", map[string]string{"KARTA_EXPORTER_API_QPS": "40"}, "--api-qps=50", "--api-burst=100")
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.APIQPS).To(Equal(float32(50)))
		Expect(cfg.APIBurst).To(Equal(100))
		Expect(cfg.Workers).To(Equal(3))
		Expect(cfg.ResourceRules).To(HaveLen(1))
	})
	It("loads Kubernetes selectors through their JSON field names", func() {
		cfg, err := loadConfig("namespaceSelector:\n  matchLabels: {team: blue}\npodSelector:\n  matchExpressions:\n  - key: app\n    operator: In\n    values: [worker]", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.NamespaceSelector.MatchLabels).To(HaveKeyWithValue("team", "blue"))
		Expect(cfg.PodSelector.MatchExpressions).To(HaveLen(1))
		Expect(cfg.PodSelector.MatchExpressions[0].Values).To(Equal([]string{"worker"}))
	})
	DescribeTable("rejects non-finite API rates before creating clients", func(value string) {
		_, err := loadConfig("", nil, "--api-qps="+value)
		Expect(err).To(MatchError("apiQPS must be finite and positive"))
		_, err = loadConfig("", map[string]string{"KARTA_EXPORTER_API_QPS": value})
		Expect(err).To(MatchError("apiQPS must be finite and positive"))
	},
		Entry("NaN", "NaN"),
		Entry("positive infinity", "+Inf"),
		Entry("negative infinity", "-Inf"),
	)
	It("rejects malformed resources and unbounded runtime settings", func() {
		for _, body := range []string{"apiQPS: 0", "apiBurst: 0", "workers: 0", "maxAge: 1s", "resourceRules: [{apiGroups: [batch], apiVersions: [v1], resources: [jobs/status]}]", "requiredKinds: [{kind: Job}]"} {
			_, err := loadConfig(body, nil)
			Expect(err).To(HaveOccurred(), body)
		}
	})
})

var _ = Describe("public kind strings", func() {
	It("parses core and grouped slash strings and preserves their JSON representation", func() {
		cfg, err := loadConfig("ignoredOwnerKinds: [example.com/v1/Wrapper, v1/Node]\nrequiredKinds: [apps/v1/Deployment, v1/Node]", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.IgnoredOwnerKinds).To(Equal(Kinds{{Group: "example.com", Version: "v1", Kind: "Wrapper"}, {Version: "v1", Kind: "Node"}}))
		Expect(cfg.RequiredKinds[0]).To(Equal(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}))
		encoded, err := json.Marshal(cfg.IgnoredOwnerKinds)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(Equal(`["example.com/v1/Wrapper","v1/Node"]`))
	})
	It("rejects missing segments and wildcard kinds", func() {
		for _, value := range []string{"Node", "/v1/Node", "apps//Deployment", "apps/v1/", "apps/v1/Deployment/extra", "*/v1/Deployment", "apps/*/Deployment", "v1/*", "apps/v 1/Deployment"} {
			_, err := loadConfig("requiredKinds: ['"+value+"']", nil)
			Expect(err).To(HaveOccurred(), value)
		}
	})
})
