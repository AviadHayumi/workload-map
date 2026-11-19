// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cataloginstall

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func renderChart(t *testing.T, values ...string) ([]*unstructured.Unstructured, error) {
	t.Helper()
	args := []string{"template", "test", "../../../charts/karta", "--namespace", "test"}
	for _, v := range values {
		args = append(args, "--set", v)
	}
	output, err := exec.CommandContext(t.Context(), "helm", args...).CombinedOutput()
	if err != nil {
		return nil, err
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(output), 4096)
	var objects []*unstructured.Unstructured
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(obj.Object) > 0 {
			objects = append(objects, &obj)
		}
	}
	return objects, nil
}
func TestCatalogHookIsOptInAndRequiresAdmission(t *testing.T) {
	objects, err := renderChart(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects {
		if strings.Contains(obj.GetAnnotations()["helm.sh/hook"], "post-") {
			t.Fatal("catalog hook enabled by default")
		}
	}
	for _, setting := range []string{"operator.enabled=false", "webhook.enabled=false"} {
		if _, err := renderChart(t, "catalogInstaller.enabled=true", setting); err == nil {
			t.Fatalf("accepted missing prerequisite %s", setting)
		}
	}
	objects, err = renderChart(t, "catalogInstaller.enabled=true", "global.fipsMode=only")
	if err != nil {
		t.Fatal(err)
	}
	hooks := map[string]*unstructured.Unstructured{}
	for _, obj := range objects {
		if strings.Contains(obj.GetAnnotations()["helm.sh/hook"], "post-") {
			hooks[obj.GetKind()] = obj
		}
	}
	for _, kind := range []string{"Job", "ServiceAccount", "ClusterRole", "ClusterRoleBinding"} {
		if hooks[kind] == nil {
			t.Fatalf("missing %s hook", kind)
		}
	}
	job := hooks["Job"]
	if job.GetAnnotations()["helm.sh/hook-weight"] != "2" {
		t.Fatal("installer must follow hook RBAC")
	}
	containers, _, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	if err != nil || len(containers) != 1 {
		t.Fatal("invalid installer container")
	}
	container := containers[0].(map[string]any)
	args := container["args"].([]any)
	if !strings.Contains(strings.TrimSpace(strings.Join([]string{args[0].(string), args[1].(string)}, " ")), "--install-catalog --catalog-owner=test/test") {
		t.Fatalf("installer args %v", args)
	}
	env := container["env"].([]any)
	if env[0].(map[string]any)["value"] != "fips140=only,tlsmlkem=0" {
		t.Fatal("installer lost FIPS setting")
	}
}
