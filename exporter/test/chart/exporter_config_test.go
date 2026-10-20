// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package chart_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func renderExporter(t *testing.T, exporter map[string]any) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	exporter["enabled"] = true
	input, err := json.Marshal(map[string]any{"operator": map[string]any{"enabled": false}, "exporter": exporter})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "helm", "template", "exporter", "../../../charts/karta", "--api-versions", "monitoring.coreos.com/v1/ServiceMonitor", "-f", "-")
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err == nil {
		for _, raw := range strings.Split(string(output), "\n---") {
			var metadata struct {
				APIVersion string
				Kind       string
			}
			if err := yaml.Unmarshal([]byte(raw), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Kind != "" && metadata.APIVersion == "" {
				t.Fatalf("rendered %s has no apiVersion", metadata.Kind)
			}
		}
	}
	return string(output), err
}

func TestExporterSelectionConfiguration(t *testing.T) {
	selector := map[string]any{"matchExpressions": []any{map[string]any{"key": "team", "operator": "In", "values": []any{"a", "b"}}}}
	rule := map[string]any{"apiGroups": []any{"batch"}, "apiVersions": []any{"v1"}, "resources": []any{"jobs"}}
	selection := map[string]any{
		"kartaDefinitionSource": map[string]any{"catalog": map[string]any{"enabled": true, "include": []any{"batch-job-v1"}, "exclude": []any{"core-pod-v1"}}, "cluster": map[string]any{"enabled": false, "include": []any{}, "exclude": []any{}, "labelSelector": selector}},
		"namespaceSelector":     selector, "objectSelector": selector, "podSelector": selector,
		"resourceRules": []any{rule}, "excludeResourceRules": []any{rule},
		"ignoredOwnerKinds": []any{"example.com/v1/Wrapper"}, "requiredKinds": []any{"batch/v1/Job"},
	}
	exporter := map[string]any{"http": map[string]any{"port": 8181}, "metrics": map[string]any{"workloadPhases": []any{"Running", "Failed"}}, "serviceMonitor": map[string]any{"enabled": true}}
	for key, value := range selection {
		exporter[key] = value
	}
	output, err := renderExporter(t, exporter)
	if err != nil {
		t.Fatalf("helm: %v\n%s", err, output)
	}
	var config string
	var deployment appsv1.Deployment
	var service corev1.Service
	var base rbacv1.ClusterRole
	monitor := false
	for _, raw := range strings.Split(output, "\n---") {
		var doc document
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		switch doc.Kind {
		case "ConfigMap":
			if strings.HasSuffix(doc.Metadata.Name, "-config") {
				config = doc.Data["config.json"]
			}
		case "Deployment":
			if err := yaml.Unmarshal([]byte(raw), &deployment); err != nil {
				t.Fatal(err)
			}
		case "Service":
			if err := yaml.Unmarshal([]byte(raw), &service); err != nil {
				t.Fatal(err)
			}
		case "ClusterRole":
			if strings.HasSuffix(doc.Metadata.Name, "-base") {
				if err := yaml.Unmarshal([]byte(raw), &base); err != nil {
					t.Fatal(err)
				}
			}
		case "ServiceMonitor":
			monitor = true
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(config), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, selection) {
		t.Fatalf("selection config mismatch:\n%#v\nwant %#v", got, selection)
	}
	if deployment.Spec.Template.Annotations["checksum/config"] != fmt.Sprintf("%x", sha256.Sum256([]byte(config))) {
		t.Fatal("selection changes must trigger rollout")
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	for _, arg := range []string{"--config=/etc/karta/exporter/config.json", "--metrics-bind-address=:8181", "--metrics-exporter=prometheus", "--workload-phases=Running,Failed"} {
		if !slices.Contains(container.Args, arg) {
			t.Errorf("missing argument %s", arg)
		}
	}
	if len(container.Env) != 0 || !monitor {
		t.Fatal("Prometheus mode must have no OTLP environment and retain ServiceMonitor")
	}
	if service.Spec.Ports[0].Port != 8181 || container.Ports[0].ContainerPort != 8181 {
		t.Fatal("http.port must configure Service and container")
	}
	if container.ReadinessProbe.HTTPGet.Path != "/readyz" || container.LivenessProbe.HTTPGet.Path != "/healthz" || container.ReadinessProbe.HTTPGet.Port.StrVal != container.Ports[0].Name {
		t.Fatal("health probes must reach the configured HTTP port")
	}
	foundNamespace, foundPod := false, false
	for _, r := range base.Rules {
		if slices.Contains(r.Resources, "kartas") || slices.Contains(r.Resources, "secrets") {
			t.Fatal("disabled cluster source and Secrets need no RBAC")
		}
		foundNamespace = foundNamespace || slices.Contains(r.Resources, "namespaces")
		foundPod = foundPod || slices.Contains(r.Resources, "pods")
	}
	if !foundNamespace || !foundPod {
		t.Fatal("namespace scope and full Pod observation require read RBAC")
	}
	if len(container.VolumeMounts) != 1 || container.VolumeMounts[0].MountPath != "/etc/karta/exporter" || !container.VolumeMounts[0].ReadOnly || deployment.Spec.Template.Spec.Volumes[0].ConfigMap.Name != "karta-exporter-config" {
		t.Fatal("config must be mounted read-only at the configured path")
	}
}

func TestExporterOTLPSecrets(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			endpoint := "https://metrics.example.com:4318/v1/metrics"
			if protocol == "grpc" {
				endpoint = "https://metrics.example.com:4317"
			}
			ref := func(name, key string) map[string]any { return map[string]any{"name": name, "key": key} }
			output, err := renderExporter(t, map[string]any{"metricsExporter": "otlp", "otlp": map[string]any{"protocol": protocol, "endpoint": endpoint, "interval": "45s", "timeout": "5s", "headersSecretRef": ref("auth", "headers"), "tls": map[string]any{"caSecretRef": ref("ca", "ca.pem"), "clientCertSecretRef": ref("client", "cert.pem"), "clientKeySecretRef": ref("client", "key.pem")}}})
			if err != nil {
				t.Fatalf("helm: %v\n%s", err, output)
			}
			var deployment appsv1.Deployment
			for _, raw := range strings.Split(output, "\n---") {
				var doc document
				if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
					t.Fatal(err)
				}
				if doc.Kind == "ServiceMonitor" {
					t.Fatal("OTLP has no ServiceMonitor")
				}
				if doc.Kind == "Deployment" {
					if err := yaml.Unmarshal([]byte(raw), &deployment); err != nil {
						t.Fatal(err)
					}
				}
			}
			c := deployment.Spec.Template.Spec.Containers[0]
			for _, arg := range []string{"--metrics-exporter=otlp", "--otlp-interval=45s", "--otlp-timeout=5s"} {
				if !slices.Contains(c.Args, arg) {
					t.Errorf("missing %s", arg)
				}
			}
			env := map[string]corev1.EnvVar{}
			for _, e := range c.Env {
				env[e.Name] = e
			}
			expected := map[string]string{"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": protocol, "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": endpoint, "OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE": "/etc/karta/otlp/ca.crt", "OTEL_EXPORTER_OTLP_METRICS_CLIENT_CERTIFICATE": "/etc/karta/otlp/client.crt", "OTEL_EXPORTER_OTLP_METRICS_CLIENT_KEY": "/etc/karta/otlp/client.key"}
			for key, value := range expected {
				if env[key].Value != value {
					t.Errorf("%s = %q", key, env[key].Value)
				}
			}
			headers := env["OTEL_EXPORTER_OTLP_METRICS_HEADERS"]
			if headers.Value != "" || headers.ValueFrom == nil || headers.ValueFrom.SecretKeyRef.Name != "auth" || headers.ValueFrom.SecretKeyRef.Key != "headers" {
				t.Fatal("headers must come from the selected Secret key")
			}
			want := map[string]string{"ca.crt": "ca/ca.pem", "client.crt": "client/cert.pem", "client.key": "client/key.pem"}
			got := map[string]string{}
			for _, v := range deployment.Spec.Template.Spec.Volumes {
				if v.Projected != nil {
					for _, source := range v.Projected.Sources {
						for _, item := range source.Secret.Items {
							got[item.Path] = source.Secret.Name + "/" + item.Key
						}
					}
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("TLS projection mismatch: %#v", got)
			}
			for _, mount := range c.VolumeMounts {
				if !mount.ReadOnly {
					t.Fatal("all configuration mounts must be read-only")
				}
			}
			if c.ReadinessProbe.HTTPGet.Path != "/readyz" {
				t.Fatal("OTLP still needs health endpoints")
			}
		})
	}
}

func TestExporterInvalidDeliveryConfiguration(t *testing.T) {
	for _, values := range []map[string]any{
		{"metricsExporter": "both"},
		{"metricsExporter": "otlp"},
		{"metricsExporter": "otlp", "otlp": map[string]any{"endpoint": "https://metrics.example.com", "protocol": "invalid"}},
		{"metricsExporter": "otlp", "otlp": map[string]any{"endpoint": "https://metrics.example.com"}, "serviceMonitor": map[string]any{"enabled": true}},
		{"metricsExporter": "otlp", "otlp": map[string]any{"endpoint": "https://metrics.example.com", "headersSecretRef": map[string]any{"name": "missing-key"}}},
		{"metricsExporter": "otlp", "otlp": map[string]any{"endpoint": "https://metrics.example.com", "tls": map[string]any{"clientCertSecretRef": map[string]any{"name": "cert", "key": "cert.pem"}}}},
		{"namespace": "legacy"}, {"useCatalog": true}, {"ports": map[string]any{"metrics": 9090}},
	} {
		if output, err := renderExporter(t, values); err == nil {
			t.Fatalf("accepted invalid values %#v\n%s", values, output)
		}
	}
}

func TestExporterOTLPWithoutSecrets(t *testing.T) {
	output, err := renderExporter(t, map[string]any{"metricsExporter": "otlp", "otlp": map[string]any{"endpoint": "https://metrics.example.com:4318/v1/metrics"}})
	if err != nil {
		t.Fatalf("helm: %v\n%s", err, output)
	}
	for _, raw := range strings.Split(output, "\n---") {
		var doc document
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Kind != "Deployment" {
			continue
		}
		var deployment appsv1.Deployment
		if err := yaml.Unmarshal([]byte(raw), &deployment); err != nil {
			t.Fatal(err)
		}
		container := deployment.Spec.Template.Spec.Containers[0]
		if len(container.Env) != 2 || len(container.VolumeMounts) != 1 || len(deployment.Spec.Template.Spec.Volumes) != 1 {
			t.Fatal("unauthenticated OTLP with system CA must not need Secrets")
		}
		return
	}
	t.Fatal("exporter Deployment missing")
}
