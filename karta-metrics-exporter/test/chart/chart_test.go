// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package chart_test

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestChart(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Exporter Helm Chart") }

func render(args ...string) []unstructured.Unstructured {
	command := []string{"template", "test", "../../../charts/karta", "--set", "crdUpgrader.enabled=false"}
	output, err := exec.Command("helm", append(command, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(output))
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	var objects []unstructured.Unstructured
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if errors.Is(err, io.EOF) {
			break
		}
		Expect(err).NotTo(HaveOccurred())
		if object != nil {
			objects = append(objects, unstructured.Unstructured{Object: object})
		}
	}
	return objects
}

func find(objects []unstructured.Unstructured, kind string) unstructured.Unstructured {
	for _, object := range objects {
		if object.GetKind() == kind {
			return object
		}
	}
	Fail("missing " + kind)
	return unstructured.Unstructured{}
}

var _ = Describe("exporter chart", func() {
	BeforeEach(func() {
		if _, err := exec.LookPath("helm"); err != nil {
			Skip("helm is required for chart render tests")
		}
	})
	It("adds no resources when both components are disabled", func() { Expect(render("--set", "operator.enabled=false")).To(BeEmpty()) })
	It("runs standalone with read-only RBAC, probes and a scrapeable service", func() {
		objects := render("--set", "operator.enabled=false", "--set", "exporter.enabled=true")
		deployments := 0
		for _, object := range objects {
			switch object.GetKind() {
			case "Deployment":
				deployments++
			case "ClusterRole":
				if rules, ok := object.Object["rules"].([]any); ok {
					for _, raw := range rules {
						rule := raw.(map[string]any)
						Expect(rule["verbs"]).To(ConsistOf("get", "list", "watch"))
						Expect(rule["resources"]).NotTo(ContainElement("*"))
						Expect(rule["apiGroups"]).NotTo(ContainElement("*"))
					}
				}
			case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
				Fail("standalone exporter must not install operator webhooks")
			}
		}
		Expect(deployments).To(Equal(1))
		deployment := find(objects, "Deployment")
		spec := deployment.Object["spec"].(map[string]any)
		template := spec["template"].(map[string]any)
		pod := template["spec"].(map[string]any)
		serviceAccount := find(objects, "ServiceAccount")
		Expect(pod["serviceAccountName"]).To(Equal(serviceAccount.GetName()))
		Expect(pod["automountServiceAccountToken"]).To(BeTrue())
		container := pod["containers"].([]any)[0].(map[string]any)
		Expect(container["args"]).To(ContainElement("--metrics-bind-address=:8080"))
		for _, probe := range []string{"startupProbe", "livenessProbe", "readinessProbe"} {
			http := container[probe].(map[string]any)["httpGet"].(map[string]any)
			Expect(http["port"]).To(Equal("metrics"))
			if probe == "readinessProbe" {
				Expect(http["path"]).To(Equal("/readyz"))
			} else {
				Expect(http["path"]).To(Equal("/healthz"))
			}
		}
		service := find(objects, "Service").Object["spec"].(map[string]any)
		Expect(service["publishNotReadyAddresses"]).To(BeTrue())
		Expect(service["selector"]).To(Equal(spec["selector"].(map[string]any)["matchLabels"]))
	})
	It("supports an external service account and replaces workload permissions", func() {
		objects := render("--set", "operator.enabled=false", "--set", "exporter.enabled=true", "--set", "exporter.serviceAccount.create=false", "--set", "exporter.serviceAccount.name=external", "--set-json", `exporter.rbac.workloadRules=[{"apiGroups":["example.com"],"resources":["workloads"]}]`)
		custom := false
		for _, object := range objects {
			Expect(object.GetKind()).NotTo(Equal("ServiceAccount"))
			if object.GetKind() == "ClusterRole" {
				if rules, ok := object.Object["rules"].([]any); ok {
					for _, raw := range rules {
						rule := raw.(map[string]any)
						Expect(rule["apiGroups"]).NotTo(ContainElement("apps"))
						if groups := rule["apiGroups"].([]any); len(groups) == 1 && groups[0] == "example.com" {
							custom = true
							Expect(rule["resources"]).To(ConsistOf("workloads"))
						}
					}
				}
			}
		}
		Expect(custom).To(BeTrue())
		binding := find(objects, "ClusterRoleBinding")
		Expect(binding.Object["subjects"].([]any)[0].(map[string]any)["name"]).To(Equal("external"))
	})
	It("requires the external service account name", func() {
		output, err := exec.Command("helm", "template", "test", "../../../charts/karta", "--set", "exporter.enabled=true", "--set", "exporter.serviceAccount.create=false").CombinedOutput()
		Expect(err).To(HaveOccurred())
		Expect(string(output)).To(ContainSubstring("exporter.serviceAccount.name is required"))
	})
	It("renders monitoring and retains selectors across upgrades", func() {
		args := []string{"--set", "operator.enabled=false", "--set", "exporter.enabled=true", "--set", "exporter.serviceMonitor.enabled=true", "--set", "exporter.serviceMonitor.labels.release=monitoring", "--set", "exporter.http.port=9090"}
		before := render(args...)
		after := render(append(args, "--is-upgrade", "--set", "exporter.image.tag=new-version", "--set-json", `exporter.podSelector={"matchLabels":{"team":"changed"}}`)...)
		a := find(before, "Deployment").Object["spec"].(map[string]any)
		b := find(after, "Deployment").Object["spec"].(map[string]any)
		Expect(a["selector"]).To(Equal(b["selector"]))
		annotation := func(spec map[string]any) any {
			return spec["template"].(map[string]any)["metadata"].(map[string]any)["annotations"]
		}
		Expect(annotation(a)).NotTo(Equal(annotation(b)))
		monitor := find(after, "ServiceMonitor")
		Expect(monitor.GetLabels()).To(HaveKeyWithValue("release", "monitoring"))
		endpoint := monitor.Object["spec"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
		Expect(endpoint["path"]).To(Equal("/metrics"))
		Expect(endpoint["port"]).To(Equal("metrics"))
		Expect(endpoint["honorLabels"]).To(BeTrue())
		config := find(after, "ConfigMap").Object["data"].(map[string]any)["config.yaml"].(string)
		Expect(config).To(ContainSubstring("team: changed"))
		Expect(config).NotTo(ContainSubstring("metricsExporter"))
		Expect(config).NotTo(ContainSubstring("submittedBy"))
	})
})
