// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestParseKinds(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []schema.GroupVersionKind
	}{
		{},
		{value: " \t"},
		{value: "v1/Pod", want: []schema.GroupVersionKind{{Version: "v1", Kind: "Pod"}}},
		{value: "batch/v1/Job, example.com/v1/Wrapper,batch/v1/Job", want: []schema.GroupVersionKind{
			{Group: "batch", Version: "v1", Kind: "Job"}, {Group: "example.com", Version: "v1", Kind: "Wrapper"},
		}},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := parseKinds(tc.value)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseKinds(%q) = %v, %v; want %v", tc.value, got, err, tc.want)
			}
		})
	}
	for _, value := range []string{"Job", "/v1/Job", "batch//Job", "batch/v1/", "batch/v1/Job/extra", "batch/v1/*", "batch/v 1/Job", "v1/Pod,", ",v1/Pod"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseKinds(value); err == nil {
				t.Fatalf("accepted invalid exact GVK %q", value)
			}
		})
	}
}

func renderExporterChart(t *testing.T, values string, apis ...string) (map[string]unstructured.Unstructured, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for exporter chart contract tests")
	}
	args := []string{"template", "contract", "../reference/chart", "--namespace", "installation", "--set", "operator.enabled=false", "--set", "crdUpgrader.enabled=false", "--values", "-"}
	for _, api := range apis {
		args = append(args, "--api-versions", api)
	}
	command := exec.Command("helm", args...)
	command.Stdin = strings.NewReader(values)
	out, err := command.CombinedOutput()
	if err != nil {
		return nil, errors.New(string(out))
	}
	docs := make(map[string]unstructured.Unstructured)
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	for {
		var doc unstructured.Unstructured
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if doc.GetLabels()["app.kubernetes.io/component"] == "exporter" ||
			doc.GetKind() == "ConfigMap" && doc.GetName() == "karta-exporter-identity" {
			docs[doc.GetKind()+"/"+doc.GetName()] = doc
		}
	}
	return docs, nil
}

func TestExporterChartConfiguration(t *testing.T) {
	docs, err := renderExporterChart(t, `exporter:
  enabled: true
  replicaCount: 2
  namespaceSelector:
    matchLabels:
      team: observed
  objectSelector:
    matchLabels:
      export: "true"
  podSelector:
    matchLabels:
      visible: "true"
  resourceRules:
  - apiGroups: [batch]
    apiVersions: [v1]
    resources: [jobs, cronjobs]
  excludeResourceRules:
  - apiGroups: [batch]
    apiVersions: [v1]
    resources: [cronjobs]
  metricsExporter: prometheus
  image:
    tag: contract
  http:
    port: 9090
  kartaDefinitionSource:
    catalog:
      enabled: true
      include: [batch-job-v1]
    cluster:
      enabled: true
      include: [team-definition]
      exclude: [disabled-definition]
      labelSelector:
        matchLabels:
          source: observability
  attribution:
    submittedByExpression: '"submitter" in annotations ? annotations["submitter"] : ""'
  ignoredOwnerKinds: [example.com/v1/Wrapper, v1/Node]
  requiredKinds: [jobset.x-k8s.io/v1alpha2/JobSet]
  metrics:
    workloadPhases: [Running, Failed]
  serviceAccount:
    create: false
    name: existing-exporter
  networkPolicy:
    enabled: true
    allowedNamespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: monitoring
  rbac:
    workloadRules:
    - apiGroups: [jobset.x-k8s.io]
      resources: [jobsets]
`)
	if err != nil {
		t.Fatal(err)
	}
	deployment, ok := docs["Deployment/karta-exporter"]
	if !ok || deployment.GetNamespace() != "installation" {
		t.Fatal("observation namespace must not change the installation namespace")
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("exporter containers: %v", containers)
	}
	container := containers[0].(map[string]any)
	args, _, _ := unstructured.NestedStringSlice(container, "args")
	wantArgs := []string{"--config=/etc/karta/exporter/config.json", "--metrics-bind-address=:9090", "--identity-config=/etc/karta/identity/rules.json", "--metrics-exporter=prometheus", "--workload-phases=Running,Failed"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %v, want %v", args, wantArgs)
	}
	directory := t.TempDir()
	configPath, identityPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "rules.json")
	for _, file := range []struct {
		name, key, path string
	}{{"karta-exporter-config", "config.json", configPath}, {"karta-exporter-identity", "rules.json", identityPath}} {
		config := docs["ConfigMap/"+file.name]
		data, present, err := unstructured.NestedString(config.Object, "data", file.key)
		if err != nil || !present {
			t.Fatalf("rendered %s/%s is missing: %v", file.name, file.key, err)
		}
		if err := os.WriteFile(file.path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options, err := loadOptions(configPath, identityPath, "prometheus", "Running,Failed")
	if err != nil {
		t.Fatalf("command rejected rendered configuration: %v", err)
	}
	if options.submitter == nil || !options.metricOptions.Identity || len(options.ignored) != 2 || len(options.required) != 1 ||
		options.config.NamespaceSelector.MatchLabels["team"] != "observed" || !options.config.DefinitionSources.Catalog.Enabled ||
		len(options.config.ResourceRules) != 1 || len(options.config.ExcludeResourceRules) != 1 {
		t.Fatalf("command lost configured scope or attribution: %#v", options)
	}
	if !strings.HasSuffix(container["image"].(string), ":contract") {
		t.Fatalf("image = %v", container["image"])
	}
	if replicas, _, _ := unstructured.NestedInt64(deployment.Object, "spec", "replicas"); replicas != 2 {
		t.Fatalf("replicas = %d", replicas)
	}
	if account, _, _ := unstructured.NestedString(deployment.Object, "spec", "template", "spec", "serviceAccountName"); account != "existing-exporter" {
		t.Fatalf("service account = %q", account)
	}
	if _, exists := docs["ServiceAccount/karta-exporter"]; exists {
		t.Fatal("created a service account despite create=false")
	}
	if _, exists := docs["ClusterRoleBinding/karta-exporter"]; !exists {
		t.Fatal("existing service account still requires its role binding")
	}
	role := docs["ClusterRole/karta-exporter-base"]
	rules, _, _ := unstructured.NestedSlice(role.Object, "rules")
	for _, rule := range rules {
		rule := rule.(map[string]any)
		verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
		if !reflect.DeepEqual(verbs, []string{"get", "list", "watch"}) {
			t.Fatalf("non-read-only rule: %v", rule)
		}
		groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
		if len(groups) != 1 || groups[0] == "batch" || groups[0] == "apps" {
			t.Fatalf("workloadRules override retained default workload permissions: %v", rule)
		}
	}
	service := docs["Service/karta-exporter"]
	ports, _, _ := unstructured.NestedSlice(service.Object, "spec", "ports")
	if len(ports) != 1 || ports[0].(map[string]any)["port"] != int64(9090) {
		t.Fatalf("service ports: %v", ports)
	}
	if publish, _, _ := unstructured.NestedBool(service.Object, "spec", "publishNotReadyAddresses"); !publish {
		t.Fatal("unhealthy startup observations must remain scrapeable")
	}
	if _, exists := docs["NetworkPolicy/karta-exporter"]; !exists {
		t.Fatal("missing requested NetworkPolicy")
	}
}

func TestExporterChartMonitoringKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		apis []string
		want bool
	}{
		{name: "absent"},
		{name: "only-group-version", apis: []string{"monitoring.coreos.com/v1"}},
		{name: "only-rules", apis: []string{"monitoring.coreos.com/v1", "monitoring.coreos.com/v1/PrometheusRule"}},
		{name: "service-monitor", apis: []string{"monitoring.coreos.com/v1/ServiceMonitor"}, want: true},
		{name: "both", apis: []string{"monitoring.coreos.com/v1/ServiceMonitor", "monitoring.coreos.com/v1/PrometheusRule"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := renderExporterChart(t, "exporter:\n  enabled: true\n  serviceMonitor:\n    enabled: true\n", tc.apis...)
			if err != nil {
				t.Fatal(err)
			}
			monitor, exists := docs["ServiceMonitor/karta-exporter"]
			if exists != tc.want {
				t.Fatalf("ServiceMonitor present = %t, want %t", exists, tc.want)
			}
			for _, doc := range docs {
				if doc.GetKind() == "PrometheusRule" {
					t.Fatal("release chart must not install platform-owned recording rules")
				}
			}
			if exists {
				endpoints, _, _ := unstructured.NestedSlice(monitor.Object, "spec", "endpoints")
				endpoint := endpoints[0].(map[string]any)
				if endpoint["honorLabels"] != true || endpoint["interval"] != "30s" || endpoint["scrapeTimeout"] != "25s" || endpoint["port"] != "metrics" {
					t.Fatalf("incorrect scrape configuration: %v", endpoint)
				}
			}
		})
	}
}

func TestExporterChartDisabledAndDeliveryModes(t *testing.T) {
	docs, err := renderExporterChart(t, "{}")
	if err != nil || len(docs) != 0 {
		t.Fatalf("disabled exporter resources: %v, error: %v", docs, err)
	}
	if _, err := renderExporterChart(t, "exporter:\n  enabled: true\n  metricsExporter: unsupported\n"); err == nil || !strings.Contains(err.Error(), "must be prometheus or otlp") {
		t.Fatalf("unsupported delivery must fail clearly: %v", err)
	}
	if _, err := renderExporterChart(t, "exporter:\n  enabled: true\n  metricsExporter: otlp\n  otlp:\n    endpoint: https://metrics.example.com/v1/metrics\n"); err != nil {
		t.Fatalf("supported OTLP delivery rejected: %v", err)
	}
}

func TestExporterChartRejectsLegacyAttribution(t *testing.T) {
	for _, values := range []string{"enabled: true", "enabled: false", "rules: []"} {
		_, err := renderExporterChart(t, "exporter:\n  enabled: true\n  attribution:\n    "+values+"\n")
		if err == nil || !strings.Contains(err.Error(), "submittedByExpression") {
			t.Fatalf("obsolete attribution %q must reject with the supported setting: %v", values, err)
		}
	}
}

func TestQueryGuideExamplesMatchDocument(t *testing.T) {
	doc, err := os.ReadFile("../reference/docs/README.md")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := os.ReadFile("../test/rules/kep-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var documentRule map[string]any
	for _, block := range regexp.MustCompile("(?s)```yaml\\n(.*?)\\n```").FindAllSubmatch(doc, -1) {
		if !bytes.HasPrefix(block[1], []byte("groups:")) {
			continue
		}
		if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(block[1]), 4096).Decode(&documentRule); err != nil {
			t.Fatal(err)
		}
	}
	var testedRule map[string]any
	if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(rules), 4096).Decode(&testedRule); err != nil {
		t.Fatal(err)
	}
	for _, value := range []map[string]any{documentRule, testedRule} {
		groups, ok := value["groups"].([]any)
		if !ok || len(groups) != 1 {
			t.Fatal("expected one attribution rule group")
		}
		rules := groups[0].(map[string]any)["rules"].([]any)
		if len(rules) != 1 {
			t.Fatal("expected one attribution recording rule")
		}
		rule := rules[0].(map[string]any)
		rule["expr"] = strings.Join(strings.Fields(rule["expr"].(string)), " ")
	}
	if !reflect.DeepEqual(testedRule, documentRule) {
		t.Fatal("platform attribution rule differs from the historical query guide")
	}
	var fixtures strings.Builder
	for _, name := range []string{"tests", "duration-tests", "identity-tests", "uid-tests", "replica-tests", "kep-tests"} {
		contents, err := os.ReadFile("../test/rules/" + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		fixtures.Write(contents)
	}
	normalized := strings.Join(strings.Fields(fixtures.String()), " ")
	queries := regexp.MustCompile("(?s)```promql\\n(.*?)\\n```").FindAllSubmatch(doc, -1)
	if len(queries) == 0 {
		t.Fatal("historical query guide contains no query examples")
	}
	for _, query := range queries {
		if !strings.Contains(normalized, strings.Join(strings.Fields(string(query[1])), " ")) {
			t.Fatalf("historical query has no exact expression in the promtool fixtures: %s", query[1])
		}
	}
}
