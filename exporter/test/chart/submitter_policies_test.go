// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package chart_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func policyAPIs(version string) []string {
	var args []string
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "MutatingAdmissionPolicy", "MutatingAdmissionPolicyBinding"} {
		v := "v1"
		if strings.HasPrefix(kind, "Mutating") {
			v = version
		}
		if v != "" {
			args = append(args, "--api-versions", "admissionregistration.k8s.io/"+v+"/"+kind)
		}
	}
	return args
}

func renderPolicies(t *testing.T, values map[string]any, apis []string) (map[string]map[string]any, string, error) {
	t.Helper()
	input, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"template", "admission", "../../reference/chart", "-f", "-"}, apis...)
	cmd := exec.CommandContext(t.Context(), "helm", args...)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	docs := map[string]map[string]any{}
	if err == nil {
		for _, raw := range strings.Split(string(output), "\n---") {
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			kind, _ := doc["kind"].(string)
			if strings.Contains(kind, "AdmissionPolicy") {
				metadata := doc["metadata"].(map[string]any)
				docs[kind+"/"+metadata["name"].(string)] = doc
			}
		}
	}
	return docs, string(output), err
}

func TestNativePolicyCapabilitiesAndProtection(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	for _, tc := range []struct {
		name, version        string
		enabled, unsupported bool
		count                int
	}{
		{name: "stable", version: "v1", enabled: true, count: 6},
		{name: "beta", version: "v1beta1", enabled: true, count: 6},
		{name: "capture disabled", version: "v1", count: 2},
		{name: "MAP unavailable capture disabled", count: 2},
		{name: "MAP unavailable capture enabled", enabled: true, unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]any{"operator": map[string]any{"enabled": false}, "webhook": map[string]any{"enabled": false}, "submitterAdmission": map[string]any{"enabled": tc.enabled}}
			docs, output, err := renderPolicies(t, values, policyAPIs(tc.version))
			if tc.unsupported {
				if err == nil || !strings.Contains(output, "requires served MutatingAdmissionPolicy") {
					t.Fatalf("expected capability error: %v\n%s", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("helm: %v\n%s", err, output)
			}
			if len(docs) != tc.count {
				t.Fatalf("got %d policies, want %d", len(docs), tc.count)
			}
			for key, doc := range docs {
				spec := doc["spec"].(map[string]any)
				if strings.HasPrefix(key, "Mutating") && doc["apiVersion"] != "admissionregistration.k8s.io/"+tc.version {
					t.Fatal("wrong MAP API")
				}
				if strings.HasSuffix(key, "-protect") {
					if _, exists := spec["matchResources"]; exists {
						t.Fatal("protection binding must have no filters")
					}
					if doc["metadata"].(map[string]any)["annotations"].(map[string]any)["helm.sh/resource-policy"] != "keep" {
						t.Fatal("protection must survive uninstall")
					}
					if constraints, exists := spec["matchConstraints"]; exists {
						want := map[string]any{"resourceRules": []any{map[string]any{"operations": []any{"UPDATE"}, "apiGroups": []any{"*"}, "apiVersions": []any{"*"}, "resources": []any{"*/*"}, "scope": "Namespaced"}}}
						if !reflect.DeepEqual(constraints, want) {
							t.Fatalf("protection scope is escapable: %#v", constraints)
						}
					}
				}
				if strings.Contains(key, "ValidatingAdmissionPolicyBinding/") && !reflect.DeepEqual(spec["validationActions"], []any{"Deny"}) {
					t.Fatal("validation must deny")
				}
			}
		})
	}
	docs, output, err := renderPolicies(t, map[string]any{}, nil)
	if err != nil || len(docs) != 0 {
		t.Fatalf("disabled, unsupported cluster: %v %s", err, output)
	}
	for _, missing := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "MutatingAdmissionPolicy", "MutatingAdmissionPolicyBinding"} {
		args := policyAPIs("v1")
		filtered := []string{}
		for i := 0; i < len(args); i += 2 {
			if !strings.HasSuffix(args[i+1], "/"+missing) {
				filtered = append(filtered, args[i:i+2]...)
			}
		}
		_, _, err := renderPolicies(t, map[string]any{"submitterAdmission": map[string]any{"enabled": true}}, filtered)
		if err == nil {
			t.Errorf("missing %s should fail", missing)
		}
	}
}

func TestNativeCaptureScope(t *testing.T) {
	conditions := []any{map[string]any{"name": "caller", "expression": "request.userInfo.username != 'excluded'"}}
	rule := map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{"batch"}, "apiVersions": []any{"v1"}, "resources": []any{"jobs"}, "scope": "Namespaced"}
	constraints := map[string]any{"namespaceSelector": map[string]any{}, "objectSelector": map[string]any{"matchLabels": map[string]any{"capture": "true"}}, "resourceRules": []any{rule}, "excludeResourceRules": []any{rule}}
	docs, output, err := renderPolicies(t, map[string]any{"submitterAdmission": map[string]any{"enabled": true, "matchConstraints": constraints, "matchConditions": conditions}}, policyAPIs("v1"))
	if err != nil {
		t.Fatalf("helm: %v\n%s", err, output)
	}
	for key, doc := range docs {
		if strings.HasPrefix(key, "MutatingAdmissionPolicy/") || strings.HasPrefix(key, "ValidatingAdmissionPolicy/") && strings.HasSuffix(key, "-create") {
			spec := doc["spec"].(map[string]any)
			if !reflect.DeepEqual(spec["matchConstraints"], constraints) || !reflect.DeepEqual(spec["matchConditions"], conditions) {
				t.Fatalf("capture conditions not preserved: %#v", spec)
			}
		}
	}
	for _, invalid := range []map[string]any{
		{"matchConstraints": map[string]any{"resourceRules": []any{}}},
		{"matchConstraints": map[string]any{"resourceRules": []any{map[string]any{"operations": []any{"UPDATE"}, "scope": "Namespaced"}}}},
		{"matchConditions": map[string]any{}},
		{"matchConditions": []any{map[string]any{"name": "x", "expression": "true"}, map[string]any{"name": "x", "expression": "false"}}},
		{"matchConditions": []any{map[string]any{"name": "bad name", "expression": "true"}}},
		{"matchConditions": []any{map[string]any{"name": "x", "expression": " "}}},
	} {
		invalid["enabled"] = true
		if _, _, err := renderPolicies(t, map[string]any{"submitterAdmission": invalid}, policyAPIs("v1")); err == nil {
			t.Fatalf("accepted invalid config %#v", invalid)
		}
	}
}
