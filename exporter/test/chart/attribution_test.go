// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package chart_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type document struct {
	Kind     string
	Metadata struct{ Name string }
	Data     map[string]string
	Spec     struct {
		Template struct {
			Metadata struct{ Annotations map[string]string }
			Spec     struct{ Containers []struct{ Args []string } }
		}
	}
	Webhooks []struct {
		Name              string
		FailurePolicy     string
		MatchConditions   []map[string]string
		NamespaceSelector struct{ MatchLabels map[string]string }
		Rules             []struct{ Operations []string }
		ClientConfig      struct {
			Service struct {
				Port int
				Path string
			}
		}
	}
}

func TestAttributionChart(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart rendering tests")
	}
	legacy := `[{"name":"legacy","matchExpression":"true","submittedByExpression":"'alice'","identitySourceExpression":"'tenant'","submitterTypeExpression":"'unknown'","partial":true}]`
	for _, tc := range []struct {
		name, values, example          string
		wantError, identity, admission bool
	}{
		{name: "defaults", values: `{}`},
		{name: "exporter only", values: `{"exporter":{"enabled":true}}`},
		{name: "simple example", example: "values.yaml", identity: true, admission: true},
		{name: "complex example", example: "values-complex.yaml", identity: true, admission: true},
		{name: "expression only", values: `{"exporter":{"enabled":true,"attribution":{"submittedByExpression":"'alice'"}}}`, identity: true},
		{name: "legacy rules", values: `{"exporter":{"enabled":true,"attribution":{"enabled":true,"rules":` + legacy + `}}}`, identity: true},
		{name: "legacy admission", values: `{"operator":{"webhookOnly":true},"submitterAdmission":{"enabled":true,"identitySource":"kubernetes:test"}}`, admission: true},
		{name: "nested admission", values: `{"operator":{"webhookOnly":true},"exporter":{"attribution":{"admission":{"enabled":true}}}}`, admission: true},
		{name: "ambiguous expression", values: `{"exporter":{"enabled":true,"attribution":{"submittedByExpression":"'alice'","rules":` + legacy + `}}}`, wantError: true},
		{name: "ambiguous admission", values: `{"submitterAdmission":{"enabled":true,"identitySource":"test"},"exporter":{"attribution":{"admission":{"enabled":true}}}}`, wantError: true},
		{name: "empty namespace selector", values: `{"exporter":{"attribution":{"admission":{"enabled":true,"namespaceSelector":null}}}}`, wantError: true},
		{name: "disabled webhook", values: `{"webhook":{"enabled":false},"exporter":{"attribution":{"admission":{"enabled":true}}}}`, wantError: true},
		{name: "empty identity config", values: `{"exporter":{"enabled":true,"attribution":{"enabled":true}}}`, wantError: true},
		{name: "webhook only without capture", values: `{"operator":{"webhookOnly":true}}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := []byte(tc.values)
			if tc.example != "" {
				var err error
				values, err = os.ReadFile("../../examples/submitter-attribution/" + tc.example)
				if err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "helm", "template", "attribution-review", "../../../charts/karta", "-f", "-")
			cmd.Stdin = bytes.NewReader(values)
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				if err == nil {
					t.Fatal("invalid configuration rendered successfully")
				}
				return
			}
			if err != nil {
				t.Fatalf("helm: %v\n%s", err, output)
			}
			var config, checksum string
			var identityArgument, admissionArgument bool
			admissionHooks := 0
			for _, raw := range strings.Split(string(output), "\n---") {
				var doc document
				if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
					t.Fatal(err)
				}
				if doc.Kind == "ConfigMap" && strings.HasSuffix(doc.Metadata.Name, "-identity") {
					config = doc.Data["rules.json"]
				}
				if doc.Kind == "Deployment" {
					if doc.Metadata.Name == "karta-exporter" {
						checksum = doc.Spec.Template.Metadata.Annotations["checksum/identity"]
					}
					for _, c := range doc.Spec.Template.Spec.Containers {
						for _, arg := range c.Args {
							identityArgument = identityArgument || strings.HasPrefix(arg, "--identity-config=")
							admissionArgument = admissionArgument || strings.HasPrefix(arg, "--submitter-identity-source=")
						}
					}
				}
				for _, hook := range doc.Webhooks {
					if !strings.HasPrefix(hook.Name, "submitter.") {
						continue
					}
					admissionHooks++
					if len(hook.MatchConditions) != 0 {
						t.Fatal("default admission unexpectedly renders matchConditions")
					}
					if hook.FailurePolicy != "Fail" || hook.ClientConfig.Service.Port != 443 || hook.NamespaceSelector.MatchLabels["karta.run.ai/capture-submitter"] != "true" {
						t.Fatalf("unscoped or insecure submitter webhook: %+v", hook)
					}
					operation, path := "CREATE", "/mutate-submitter"
					if hook.Name == "submitter.validate.karta.run.ai" {
						operation, path = "UPDATE", "/validate-submitter"
					}
					if hook.ClientConfig.Service.Path != path || len(hook.Rules) == 0 {
						t.Fatalf("incorrect submitter endpoint or missing rules: %+v", hook)
					}
					for _, rule := range hook.Rules {
						if !reflect.DeepEqual(rule.Operations, []string{operation}) {
							t.Fatalf("%s operations = %v, want %s", hook.Name, rule.Operations, operation)
						}
					}
				}
			}
			if (config != "") != tc.identity || identityArgument != tc.identity {
				t.Fatalf("identity configuration/argument mismatch: %q, %v", config, identityArgument)
			}
			if tc.identity {
				if checksum != fmt.Sprintf("%x", sha256.Sum256([]byte(config))) {
					t.Fatal("rollout checksum does not match actual configuration")
				}
				var cfg map[string]any
				if err := json.Unmarshal([]byte(config), &cfg); err != nil {
					t.Fatal(err)
				}
				if _, expr := cfg["submittedByExpression"]; !expr && cfg["rules"] == nil {
					t.Fatal("configuration contains neither expression nor rules")
				}
			}
			wantHooks := 0
			if tc.admission {
				wantHooks = 2
			}
			if admissionHooks != wantHooks || admissionArgument != tc.admission {
				t.Fatalf("admission hooks/argument mismatch: %d, %v", admissionHooks, admissionArgument)
			}
		})
	}
}

func TestAdmissionMatchConditions(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart rendering tests")
	}
	conditions := []map[string]any{
		{"name": "example.com/Deployment", "expression": `request.kind.group == 'apps' && request.kind.kind == 'Deployment'`},
		{"name": "namespace", "expression": "request.namespace == 'captured'"},
	}
	maxConditions := make([]map[string]any, 64)
	for i := range maxConditions {
		maxConditions[i] = map[string]any{"name": fmt.Sprintf("condition-%d", i), "expression": "true"}
	}
	render := func(conditions any, legacy bool, version string) ([]byte, error) {
		t.Helper()
		admission := map[string]any{"enabled": true, "identitySource": "kubernetes", "matchConditions": conditions}
		values := map[string]any{
			"webhook": map[string]any{"cert": map[string]any{"provisionMode": "manual", "caBundle": "dGVzdC1jYQ=="}},
		}
		if legacy {
			values["submitterAdmission"] = admission
		} else {
			values["exporter"] = map[string]any{"attribution": map[string]any{"admission": admission}}
		}
		input, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), "helm", "template", "attribution-review", "../../../charts/karta", "--kube-version", version, "-f", "-")
		cmd.Stdin = bytes.NewReader(input)
		return cmd.CombinedOutput()
	}
	webhooks := func(output []byte) map[string]map[string]any {
		t.Helper()
		hooks := make(map[string]map[string]any)
		for _, raw := range strings.Split(string(output), "\n---") {
			var doc struct{ Webhooks []map[string]any }
			if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			for _, hook := range doc.Webhooks {
				hooks[hook["name"].(string)] = hook
			}
		}
		return hooks
	}
	baseline, err := render([]any{}, false, "1.30.0")
	if err != nil {
		t.Fatalf("baseline helm: %v\n%s", err, baseline)
	}
	wantHooks := webhooks(baseline)
	if len(wantHooks) != 4 {
		t.Fatalf("baseline webhook count: %d, want 4", len(wantHooks))
	}
	for _, tc := range []struct {
		name, version, wantError string
		conditions               any
		legacy                   bool
	}{
		{name: "configured", conditions: conditions},
		{name: "legacy configured", conditions: conditions, legacy: true},
		{name: "vendor version", conditions: conditions, version: "1.30.0-gke.123"},
		{name: "empty keeps older compatibility", conditions: []any{}, version: "1.29.0"},
		{name: "null omits conditions", conditions: nil, version: "1.29.0"},
		{name: "64 conditions", conditions: maxConditions},
		{name: "CEL preserved without template evaluation", conditions: []map[string]any{{"name": "literal", "expression": "'{{ .Values.operator.enabled }}' == 'false'\n"}}},
		{name: "CEL compilation belongs to API server", conditions: []map[string]any{{"name": "invalid-cel", "expression": "not valid CEL ("}}},
		{name: "older Kubernetes", conditions: conditions, version: "1.29.9", wantError: "requires Kubernetes 1.30 or newer"},
		{name: "object instead of list", conditions: map[string]any{"name": "x", "expression": "true"}, wantError: "matchConditions must be a list"},
		{name: "empty object instead of list", conditions: map[string]any{}, wantError: "matchConditions must be a list"},
		{name: "string instead of list", conditions: "true", wantError: "matchConditions must be a list"},
		{name: "boolean instead of list", conditions: false, wantError: "matchConditions must be a list"},
		{name: "nonobject entry", conditions: []any{"true"}, wantError: "must be an object with name and expression"},
		{name: "null entry", conditions: []any{nil}, wantError: "must be an object with name and expression"},
		{name: "missing name", conditions: []map[string]any{{"expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "empty name", conditions: []map[string]any{{"name": "", "expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "nonstring name", conditions: []map[string]any{{"name": 1, "expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "invalid name", conditions: []map[string]any{{"name": "invalid name", "expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "long name", conditions: []map[string]any{{"name": strings.Repeat("n", 64), "expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "invalid prefix", conditions: []map[string]any{{"name": "Example.com/name", "expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "long prefix", conditions: []map[string]any{{"name": strings.Repeat("a", 254) + "/name", "expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "multiple slashes", conditions: []map[string]any{{"name": "a/b/c", "expression": "true"}}, wantError: ".name must be a nonempty qualified name string"},
		{name: "duplicate name", conditions: []map[string]any{{"name": "x", "expression": "true"}, {"name": "x", "expression": "false"}}, wantError: ".name must be unique"},
		{name: "missing expression", conditions: []map[string]any{{"name": "x"}}, wantError: ".expression must be a nonempty string"},
		{name: "empty expression", conditions: []map[string]any{{"name": "x", "expression": ""}}, wantError: ".expression must be a nonempty string"},
		{name: "blank expression", conditions: []map[string]any{{"name": "x", "expression": " \t\n"}}, wantError: ".expression must be a nonempty string"},
		{name: "nonstring expression", conditions: []map[string]any{{"name": "x", "expression": true}}, wantError: ".expression must be a nonempty string"},
		{name: "65 conditions", conditions: append(maxConditions, map[string]any{"name": "condition-64", "expression": "true"}), wantError: "at most 64 entries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := tc.version
			if version == "" {
				version = "1.30.0"
			}
			output, err := render(tc.conditions, tc.legacy, version)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(string(output), tc.wantError) {
					t.Fatalf("helm error = %v\n%s\nwant %q", err, output, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("helm: %v\n%s", err, output)
			}
			hooks := webhooks(output)
			input, err := json.Marshal(tc.conditions)
			if err != nil {
				t.Fatal(err)
			}
			var want []any
			if err := json.Unmarshal(input, &want); err != nil {
				t.Fatal(err)
			}
			for name, hook := range hooks {
				if strings.HasPrefix(name, "submitter.") && len(want) > 0 {
					if !reflect.DeepEqual(hook["matchConditions"], want) {
						t.Fatalf("%s matchConditions = %#v, want %#v", name, hook["matchConditions"], want)
					}
					delete(hook, "matchConditions")
				} else if _, ok := hook["matchConditions"]; ok {
					t.Fatalf("%s unexpectedly has matchConditions", name)
				}
			}
			if !reflect.DeepEqual(hooks, wantHooks) {
				t.Fatalf("webhook fields changed beyond matchConditions:\ngot %#v\nwant %#v", hooks, wantHooks)
			}
		})
	}
}
