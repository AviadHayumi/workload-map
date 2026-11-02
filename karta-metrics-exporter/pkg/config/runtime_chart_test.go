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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
)

var _ = Describe("chart runtime configuration", func() {
	DescribeTable("keeps workload observation permissions independent of cluster definitions", func(clusterEnabled string, restricted bool) {
		helm, err := exec.LookPath("helm")
		if err != nil {
			Skip("helm is needed for chart rendering")
		}
		args := []string{"template", "runtime", filepath.Join("..", "..", "..", "charts", "karta"),
			"--set", "exporter.enabled=true",
			"--set", "exporter.kartaDefinitionSource.cluster.enabled=" + clusterEnabled,
		}
		if restricted {
			args = append(args, "--set-json", `exporter.rbac.workloadRules=[{"apiGroups":["apps"],"resources":["deployments","replicasets"]}]`)
		}
		out, err := exec.Command(helm, args...).CombinedOutput()
		Expect(err).To(Succeed(), string(out))
		decoder := yaml.NewDecoder(bytes.NewReader(out))
		watchedResources := map[string][]string{}
		bound := false
		for {
			var doc struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
				Rules []struct {
					APIGroups []string `yaml:"apiGroups"`
					Resources []string `yaml:"resources"`
					Verbs     []string `yaml:"verbs"`
				} `yaml:"rules"`
			}
			err = decoder.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			Expect(err).To(Succeed())
			if doc.Metadata.Name != "karta-metrics-exporter-default" {
				continue
			}
			if doc.Kind == "ClusterRoleBinding" {
				bound = true
			}
			if doc.Kind != "ClusterRole" {
				continue
			}
			for _, rule := range doc.Rules {
				Expect(rule.Verbs).To(ConsistOf("list", "watch"))
				for _, group := range rule.APIGroups {
					watchedResources[group] = append(watchedResources[group], rule.Resources...)
				}
			}
		}
		Expect(bound).To(BeTrue())
		expectedResources := map[string][]string{"": {"pods", "namespaces"}}
		if restricted {
			expectedResources["apps"] = []string{"deployments", "replicasets"}
		} else {
			expectedResources["*"] = []string{"*"}
		}
		if clusterEnabled == "true" {
			expectedResources["run.ai"] = []string{"kartas"}
		}
		Expect(watchedResources).To(Equal(expectedResources))
	},
		Entry("default rules with cluster definitions enabled", "true", false),
		Entry("default rules with cluster definitions disabled", "false", false),
		Entry("restricted rules with cluster definitions enabled", "true", true),
		Entry("restricted rules with cluster definitions disabled", "false", true),
	)

	DescribeTable("loads and compiles the configuration rendered by Helm", func(includeSystemNamespaces, includeConfigMapsAndSecrets bool) {
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
		args := []string{"template", "labels", chart,
			"--set", "exporter.enabled=true",
			"--set-json", "exporter.labelRules=" + string(raw),
			"--set", "exporter.genericLabelEvaluationLimits.maxResultBytes=32",
			"--set", "exporter.workers=3",
			"--set", "exporter.leafWorkers=4",
			"--set", "exporter.rootWorkers=5",
		}
		if includeSystemNamespaces {
			args = append(args, "--set-json", "exporter.namespaceSelector.matchExpressions=[]")
		}
		if includeConfigMapsAndSecrets {
			args = append(args, "--set-json", "exporter.excludeResourceRules=[]")
		}
		out, err := exec.Command(helm, args...).CombinedOutput()
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
			Expect(cfg.Rules).To(Equal(rules))
			Expect(cfg.Limits.MaxResultBytes).To(Equal(32))
			Expect(cfg.Workers).To(Equal(3))
			Expect(cfg.LeafWorkers).To(Equal(4))
			Expect(cfg.RootWorkers).To(Equal(5))
			if includeConfigMapsAndSecrets {
				Expect(cfg.ExcludeResourceRules).To(BeEmpty())
			} else {
				Expect(cfg.ExcludeResourceRules).To(HaveLen(1))
				Expect(cfg.ExcludeResourceRules[0].APIGroups).To(ConsistOf(""))
				Expect(cfg.ExcludeResourceRules[0].APIVersions).To(ConsistOf("*"))
				Expect(cfg.ExcludeResourceRules[0].Resources).To(ConsistOf("configmaps", "secrets"))
			}
			Expect(cfg.NamespaceSelector).NotTo(BeNil())
			selector, err := metav1.LabelSelectorAsSelector(cfg.NamespaceSelector)
			Expect(err).To(Succeed())
			for _, namespace := range []string{"kube-system", "kube-public", "kube-node-lease"} {
				Expect(selector.Matches(labels.Set{"kubernetes.io/metadata.name": namespace})).To(Equal(includeSystemNamespaces), namespace)
			}
			Expect(selector.Matches(labels.Set{"kubernetes.io/metadata.name": "app"})).To(BeTrue())
			_, err = labelresolver.New(cfg.Config, labelresolver.Dependencies{Compiler: labelevaluator.NewCompiler()})
			Expect(err).To(Succeed())
		}
		Expect(found).To(BeTrue(), "rendered exporter config was not found")
	},
		Entry("with the default exclusions", false, false),
		Entry("with the namespace exclusions cleared", true, false),
		Entry("with the resource exclusions cleared", false, true),
		Entry("with all exclusions cleared", true, true),
	)
})
