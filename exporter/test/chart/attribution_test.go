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
	Webhooks []struct{ Name string }
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
		{name: "legacy rules", values: `{"exporter":{"enabled":true,"attribution":{"enabled":true,"rules":` + legacy + `}}}`, wantError: true},
		{name: "native admission", values: `{"operator":{"enabled":false},"webhook":{"enabled":false},"submitterAdmission":{"enabled":true}}`, admission: true},
		{name: "obsolete nested admission", values: `{"exporter":{"attribution":{"admission":{"enabled":true}}}}`, wantError: true},
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
			cmd := exec.CommandContext(t.Context(), "helm", append([]string{"template", "attribution-review", "../../reference/chart", "-f", "-"}, policyAPIs("v1")...)...)
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
			admissionPolicies := 0
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
				if doc.Kind == "MutatingAdmissionPolicy" {
					admissionPolicies++
				}
				for _, hook := range doc.Webhooks {
					if strings.HasPrefix(hook.Name, "submitter.") {
						t.Fatal("native admission must not install submitter webhooks")
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
				if _, expr := cfg["submittedByExpression"]; !expr || len(cfg) != 1 {
					t.Fatal("configuration must contain only submittedByExpression")
				}
			}
			wantPolicies := 0
			if tc.admission {
				wantPolicies = 1
			}
			if admissionPolicies != wantPolicies || admissionArgument {
				t.Fatalf("native admission policies/legacy argument mismatch: %d, %v", admissionPolicies, admissionArgument)
			}
		})
	}
}
