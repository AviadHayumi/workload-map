// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package chart_test

import (
	"encoding/json"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("exporter deployment overrides", func() {
	BeforeEach(func() {
		if _, err := exec.LookPath("helm"); err != nil {
			Skip("helm is required for chart render tests")
		}
	})
	It("preserves image, resource and placement settings together", func() {
		overrides := map[string]string{
			"resources":    `{"requests":{"cpu":"250m","memory":"256Mi"},"limits":{"cpu":"2","memory":"2Gi"}}`,
			"nodeSelector": `{"kubernetes.io/arch":"arm64","example.com/pool":"monitoring"}`,
			"tolerations":  `[{"key":"example.com/monitoring","operator":"Equal","value":"true","effect":"NoSchedule"}]`,
			"affinity":     `{"podAntiAffinity":{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":100,"podAffinityTerm":{"topologyKey":"kubernetes.io/hostname","labelSelector":{"matchLabels":{"app.kubernetes.io/component":"exporter"}}}}]}}`,
		}
		args := []string{"--set", "operator.enabled=false", "--set", "exporter.enabled=true", "--set", "exporter.image.repository=registry.example.com/metrics/exporter", "--set", "exporter.image.tag=test-version", "--set", "exporter.image.pullPolicy=Always"}
		for key, value := range overrides {
			args = append(args, "--set-json", "exporter."+key+"="+value)
		}
		deployment := find(render(args...), "Deployment")
		pod := deployment.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		container := pod["containers"].([]any)[0].(map[string]any)
		Expect(container["image"]).To(Equal("registry.example.com/metrics/exporter:test-version"))
		Expect(container["imagePullPolicy"]).To(Equal("Always"))
		for key, value := range overrides {
			var expected any
			Expect(json.Unmarshal([]byte(value), &expected)).To(Succeed())
			actual := pod[key]
			if key == "resources" {
				actual = container[key]
			}
			// JSON normalization makes Helm's integer decoding comparable to JSON values.
			encoded, err := json.Marshal(actual)
			Expect(err).NotTo(HaveOccurred())
			var normalized any
			Expect(json.Unmarshal(encoded, &normalized)).To(Succeed())
			Expect(normalized).To(Equal(expected), key)
		}
	})
})
