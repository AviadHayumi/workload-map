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
		NamespaceSelector struct{ MatchLabels map[string]string }
		ClientConfig      struct{ Service struct{ Port int } }
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
					if hook.FailurePolicy != "Fail" || hook.ClientConfig.Service.Port != 443 || hook.NamespaceSelector.MatchLabels["karta.run.ai/capture-submitter"] != "true" {
						t.Fatalf("unscoped or insecure submitter webhook: %+v", hook)
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
