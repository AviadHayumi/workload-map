//go:build e2e

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

var (
	client          dynamic.Interface
	mapper          meta.RESTMapper
	metricsURL      string
	outputDir       string
	repository      string
	clusterContext  string
	includeBarePods bool
)

const systemNamespace = "exporter-e2e-system"
const release = "exporter-e2e"
const teamLabel = "example.com/exporter-e2e-team"

func TestExporterE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Exporter Real Operator Matrix")
}

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "helm" {
		args = append([]string{"--kubeconfig", os.Getenv("KUBECONFIG"), "--kube-context", clusterContext}, args...)
	}
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %v: %w: %s", name, args, err, output)
	}
	return output, nil
}

var _ = BeforeSuite(func(ctx SpecContext) {
	Expect(apiProfile).To(BeElementOf("default", "alternate"), "EXPORTER_E2E_API_PROFILE must be default or alternate")
	AddReportEntry("api-profile", apiProfile)
	Expect(os.Getenv("KUBECONFIG")).NotTo(BeEmpty(), "set the isolated kind kubeconfig explicitly")
	config, err := clientcmd.LoadFromFile(os.Getenv("KUBECONFIG"))
	Expect(err).NotTo(HaveOccurred())
	expected := os.Getenv("EXPORTER_E2E_CONTEXT")
	if expected == "" {
		expected = "kind-exporter-e2e"
	}
	clusterContext = expected
	Expect(config.CurrentContext).To(Equal(expected), "suite creates and deletes resources only in its isolated kind context")
	restConfig, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	Expect(err).NotTo(HaveOccurred())
	client, err = dynamic.NewForConfig(restConfig)
	Expect(err).NotTo(HaveOccurred())
	disco, err := discovery.NewDiscoveryClientForConfig(restConfig)
	Expect(err).NotTo(HaveOccurred())
	mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disco))
	repository, err = filepath.Abs("../../..")
	Expect(err).NotTo(HaveOccurred())
	outputDir = os.Getenv("EXPORTER_E2E_OUTPUT")
	if outputDir == "" {
		outputDir, err = os.MkdirTemp("", "exporter-operator-e2e-")
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(os.MkdirAll(outputDir, 0755)).To(Succeed())
	image := os.Getenv("EXPORTER_E2E_IMAGE")
	Expect(image).NotTo(BeEmpty(), "build and load the new exporter image into kind, then set EXPORTER_E2E_IMAGE=repository:tag")
	split := strings.LastIndex(image, ":")
	Expect(split).To(BeNumerically(">", 0))
	includeBarePods = os.Getenv("EXPORTER_E2E_INCLUDE_BARE_PODS") == "true"
	excluded := []string{"core-pod-v1"}
	if includeBarePods {
		excluded = []string{}
	}
	values := map[string]any{"operator": map[string]any{"enabled": false}, "crdUpgrader": map[string]any{"enabled": false}, "exporter": map[string]any{
		"enabled": true, "image": map[string]any{"repository": image[:split], "tag": image[split+1:], "pullPolicy": "IfNotPresent"},
		"namespaceSelector":     map[string]any{"matchLabels": map[string]string{"exporter-e2e": "true"}},
		"kartaDefinitionSource": map[string]any{"catalog": map[string]any{"enabled": true, "include": catalogNames(), "exclude": excluded}, "cluster": map[string]any{"enabled": false}},
		"resources":             map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "256Mi"}, "limits": map[string]string{"memory": "2Gi"}},
		"labelRules":            []any{map[string]any{"name": "team", "metrics": []string{"karta_pod_workload_info", "karta_workload_status"}, "sources": []string{"workload"}, "expression": `has(object.metadata.labels) && "example.com/exporter-e2e-team" in object.metadata.labels ? object.metadata.labels["example.com/exporter-e2e-team"] : ""`}},
		"rbac":                  map[string]any{"workloadRules": workloadRules()},
	}}
	raw, err := json.MarshalIndent(values, "", "  ")
	Expect(err).NotTo(HaveOccurred())
	valuesPath := filepath.Join(outputDir, "values.json")
	Expect(os.WriteFile(valuesPath, raw, 0600)).To(Succeed())
	_, err = command(ctx, "helm", "upgrade", "--install", "--skip-crds", release, filepath.Join(repository, "charts/karta"), "--namespace", systemNamespace, "--create-namespace", "--values", valuesPath, "--wait", "--timeout", "5m")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func(ctx SpecContext) {
		_, err := command(ctx, "helm", "uninstall", release, "--namespace", systemNamespace, "--wait", "--timeout", "2m")
		Expect(err).NotTo(HaveOccurred())
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	port := listener.Addr().(*net.TCPAddr).Port
	Expect(listener.Close()).To(Succeed())
	log, err := os.Create(filepath.Join(outputDir, "port-forward.log"))
	Expect(err).NotTo(HaveOccurred())
	forward := exec.Command("kubectl", "--kubeconfig", os.Getenv("KUBECONFIG"), "--context", clusterContext, "--namespace", systemNamespace, "port-forward", "service/karta-metrics-exporter", fmt.Sprintf("%d:8080", port), "--address=127.0.0.1")
	forward.Stdout = log
	forward.Stderr = log
	Expect(forward.Start()).To(Succeed())
	DeferCleanup(func() { _ = forward.Process.Kill(); _ = forward.Wait(); _ = log.Close() })
	metricsURL = fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	Eventually(ctx, func() error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
		if err != nil {
			return err
		}
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("metrics HTTP %d", response.StatusCode)
		}
		return nil
	}, time.Minute, time.Second).Should(Succeed())
	pods, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(systemNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + release})
	Expect(err).NotTo(HaveOccurred())
	podEvidence, err := json.MarshalIndent(pods, "", "  ")
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(filepath.Join(outputDir, "exporter-pods.json"), podEvidence, 0600)).To(Succeed())
	AddReportEntry("exporter-image", image)
	AddReportEntry("evidence-directory", outputDir)
})

func workloadRules() []map[string]any {
	return []map[string]any{
		{"apiGroups": []string{""}, "resources": []string{"replicationcontrollers"}},
		{"apiGroups": []string{"apps"}, "resources": []string{"deployments", "replicasets", "statefulsets", "daemonsets"}},
		{"apiGroups": []string{"batch"}, "resources": []string{"jobs", "cronjobs"}},
		{"apiGroups": []string{"jobset.x-k8s.io"}, "resources": []string{"jobsets"}},
		{"apiGroups": []string{"leaderworkerset.x-k8s.io"}, "resources": []string{"leaderworkersets"}},
		{"apiGroups": []string{"ray.io"}, "resources": []string{"rayclusters", "rayjobs", "rayservices"}},
		{"apiGroups": []string{"kubeflow.org"}, "resources": []string{"pytorchjobs", "mpijobs"}},
		{"apiGroups": []string{"serving.knative.dev"}, "resources": []string{"services", "configurations", "revisions"}},
		{"apiGroups": []string{"serving.kserve.io"}, "resources": []string{"inferenceservices", "llminferenceservices"}},
		{"apiGroups": []string{"milvus.io"}, "resources": []string{"milvuses"}},
		{"apiGroups": []string{"grove.io", "scheduler.grove.io"}, "resources": []string{"podcliquesets", "podcliques", "podcliquescalinggroups", "podgangs"}},
		{"apiGroups": []string{"nvidia.com"}, "resources": []string{"dynamographdeployments", "dynamocomponentdeployments"}},
		{"apiGroups": []string{"apps.nvidia.com"}, "resources": []string{"nimservices", "nimcaches"}},
		{"apiGroups": []string{"agents.x-k8s.io"}, "resources": []string{"sandboxes", "sandboxclaims", "sandboxtemplates"}},
	}
}

func catalogNames() []string {
	definitions := make(map[string]schema.GroupKind)
	for _, definition := range catalog.List() {
		definitions[definition.Name] = catalog.RootKey(definition).GroupKind()
	}
	seenKinds := make(map[schema.GroupKind]string)
	seen := make(map[string]bool)
	var names []string
	for _, scenario := range scenarios {
		if !seen[scenario.definition] {
			kind, exists := definitions[scenario.definition]
			Expect(exists).To(BeTrue(), "profile references an unknown catalog definition: %s", scenario.definition)
			Expect(seenKinds[kind]).To(BeEmpty(), "profile must select only one Karta per GroupKind: %s", kind)
			seenKinds[kind] = scenario.definition
			names = append(names, scenario.definition)
			seen[scenario.definition] = true
		}
	}
	return names
}
