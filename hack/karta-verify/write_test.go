// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

func TestDiffObjectsReportsLeavesAndEmptyContainers(t *testing.T) {
	before := map[string]any{
		"spec": map[string]any{
			"replicas": float64(2),
			"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "a"}}}},
		},
	}
	after := map[string]any{
		"spec": map[string]any{
			"replicas": 2,
			"template": map[string]any{
				"metadata": map[string]any{},
				"spec": map[string]any{
					"containers":   []any{map[string]any{"name": "a"}},
					"nodeSelector": map[string]any{"karta-verify/probe": probeValue},
				},
			},
		},
	}
	got := diffObjects(before, after)
	want := []string{`.spec.template.metadata`, `.spec.template.spec.nodeSelector["karta-verify/probe"]`}
	if len(got) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(got), len(want), got)
	}
	for i, c := range got {
		if c.path != want[i] {
			t.Errorf("change %d path = %q, want %q", i, c.path, want[i])
		}
	}
	if got[0].after == nil || got[1].after != probeValue {
		t.Errorf("unexpected values: %+v", got)
	}
}

func TestUnexpectedChanges(t *testing.T) {
	probe := change{path: ".a", after: probeValue}
	for _, tc := range []struct {
		name      string
		changes   []change
		instances int
		want      string
	}{
		{"exact", []change{probe, probe}, 2, ""},
		{"extra path", []change{probe, {path: ".b", after: map[string]any{}}}, 1, "besides the probe field: .b"},
		{"missed an instance", []change{probe}, 2, "landed in 1 place(s) for 2 instance(s)"},
	} {
		if got := unexpectedChanges(tc.changes, tc.instances); !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to contain %q", tc.name, got, tc.want)
		}
	}
}

func TestActionPaths(t *testing.T) {
	def := &v1alpha1.SuspendDefinition{
		SuspendActions: []v1alpha1.SuspendAction{{Path: `.spec.suspend`, Value: "true"}, {Path: `.metadata.annotations["cnpg.io/hibernation"]`, Value: `"on"`}},
		ResumeActions:  []v1alpha1.SuspendAction{{Path: `.spec.suspend`, Value: "false"}},
	}
	got := actionPaths(def)
	for _, p := range []string{`.spec.suspend`, `.metadata.annotations["cnpg.io/hibernation"]`} {
		if !got[p] {
			t.Errorf("missing %q in %v", p, got)
		}
	}
	if actionPaths(&v1alpha1.SuspendDefinition{SuspendActions: []v1alpha1.SuspendAction{{Path: `.spec.tasks[] | .suspend`, Value: "true"}}}) != nil {
		t.Error("a path with a filter must not be compared")
	}
}

// The object carries every field the typed pod template serializes without
// omitempty, so the test exercises the probes and not the write engine.
func TestProbeWritesOnAPodTemplate(t *testing.T) {
	var obj unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: d}
spec:
  replicas: 1
  suspend: false
  template:
    metadata:
      labels: {app: d}
    spec:
      containers:
      - name: app
        image: busybox:1.36
        resources:
          limits: {cpu: 100m}
`), &obj.Object); err != nil {
		t.Fatal(err)
	}
	karta := &v1alpha1.Karta{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{
		RootComponent: v1alpha1.ComponentDefinition{
			Name:              "deployment",
			SpecDefinition:    &v1alpha1.SpecDefinition{PodTemplateSpecPath: ptr.To(".spec.template")},
			SuspendDefinition: &v1alpha1.SuspendDefinition{SuspendActions: []v1alpha1.SuspendAction{{Path: ".spec.suspend", Value: "true"}}, ResumeActions: []v1alpha1.SuspendAction{{Path: ".spec.suspend", Value: "false"}}},
		},
	}}}
	probes := probeWrites(context.Background(), karta, &obj)
	byName := map[string]writeProbe{}
	for _, p := range probes {
		byName[p.name] = p
	}
	if p := byName["identity write"]; p.warning != "" || len(p.changes) != 0 {
		t.Errorf("identity write: %+v", p)
	}
	if p := byName["change probe"]; p.warning != "" || len(p.changes) != 1 || !strings.HasSuffix(p.changes[0].path, `nodeSelector["karta-verify/probe"]`) {
		t.Errorf("change probe: %+v", p)
	}
	if p := byName["suspend"]; p.warning != "" || len(p.changes) != 1 || p.changes[0].path != ".spec.suspend" {
		t.Errorf("suspend: %+v", p)
	}
	if p := byName["resume"]; p.warning != "" || len(p.changes) != 0 {
		t.Errorf("resume: %+v", p)
	}
}
