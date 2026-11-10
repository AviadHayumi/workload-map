// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cel

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/jq/execution"
)

func workload(kind string) *unstructured.Unstructured {
	template := map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
		"spec": map[string]any{
			"containers":  []any{map[string]any{"name": "web", "ports": []any{map[string]any{"containerPort": int64(8080)}}}},
			"futureField": map[string]any{"largeInteger": int64(9007199254740993)},
		},
	}
	version := "apps/v1"
	spec := map[string]any{"template": template}
	if kind == "CronJob" {
		version = "batch/v1"
		spec = map[string]any{"jobTemplate": map[string]any{"spec": spec}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": version, "kind": kind,
		"metadata": map[string]any{"name": "web"}, "spec": spec,
	}}
}

func TestNewInputRawTemplate(t *testing.T) {
	for _, kind := range []string{"Deployment", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			object := workload(kind)
			before := object.DeepCopy()
			input, err := NewInput(context.Background(), object)
			if err != nil {
				t.Fatal(err)
			}
			wantPath := []string{"spec", "template"}
			if kind == "CronJob" {
				wantPath = []string{"spec", "jobTemplate", "spec", "template"}
			}
			if !reflect.DeepEqual(input.TemplatePath(), wantPath) {
				t.Fatalf("path = %v, want %v", input.TemplatePath(), wantPath)
			}
			input.TemplatePath()[0] = "changed"
			if !reflect.DeepEqual(input.TemplatePath(), wantPath) {
				t.Fatal("TemplatePath exposed its backing slice")
			}
			program, err := Compile(`{'value': template.spec.futureField.largeInteger, 'port': template.spec.containers[0].ports[0].containerPort, 'annotations': object.metadata.annotations}`)
			if err != nil {
				t.Fatal(err)
			}
			result, err := program.Evaluate(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if result["value"] != int64(9007199254740993) || result["port"] != int64(8080) {
				t.Fatalf("raw values changed: %#v", result)
			}
			if !reflect.DeepEqual(result["annotations"], map[string]any{}) || !reflect.DeepEqual(before, object) {
				t.Fatal("annotations normalization changed the source")
			}
			object.Object["spec"] = nil
			object.Object["metadata"].(map[string]any)["annotations"] = map[string]any{"new": "value"}
			result["annotations"].(map[string]any)["mutated"] = true
			second, err := program.Evaluate(context.Background(), input)
			if err != nil || !reflect.DeepEqual(second["annotations"], map[string]any{}) || second["value"] != int64(9007199254740993) {
				t.Fatalf("snapshot changed: %#v, %v", second, err)
			}
		})
	}
}

func TestNewInputUnsupported(t *testing.T) {
	cases := map[string]*unstructured.Unstructured{
		"nil":                   nil,
		"empty":                 {},
		"unknown kind":          {Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Widget"}},
		"pod empty path":        {Object: map[string]any{"apiVersion": "v1", "kind": "Pod"}},
		"root without template": {Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayJob"}},
		"instances":             {Object: map[string]any{"apiVersion": "kubeflow.org/v1", "kind": "PyTorchJob"}},
		"missing template":      {Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "spec": map[string]any{}}},
		"nonmap template":       {Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "spec": map[string]any{"template": []any{}}}},
	}
	for name, object := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewInput(context.Background(), object); err == nil {
				t.Fatal("unsupported input accepted")
			}
		})
	}
}

func TestNewInputOtherCatalogRoots(t *testing.T) {
	for _, kind := range []string{"StatefulSet", "Job"} {
		object := workload(kind)
		if kind == "Job" {
			object.SetAPIVersion("batch/v1")
		}
		input, err := NewInput(context.Background(), object)
		if err != nil {
			t.Fatalf("catalog root %s rejected: %v", kind, err)
		}
		if !reflect.DeepEqual(input.TemplatePath(), []string{"spec", "template"}) {
			t.Fatalf("catalog root %s path = %v", kind, input.TemplatePath())
		}
	}
}

func TestInputActivationByteBoundary(t *testing.T) {
	object := workload("Deployment")
	object.Object["extra"] = ""
	input, err := NewInput(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{"object": input.object, "template": input.template})
	if err != nil {
		t.Fatal(err)
	}
	object.Object["extra"] = strings.Repeat("x", maxInputBytes-len(encoded))
	if _, err := NewInput(context.Background(), object); err != nil {
		t.Fatalf("exact activation size rejected: %v", err)
	}
	object.Object["extra"] = object.Object["extra"].(string) + "x"
	if _, err := NewInput(context.Background(), object); err == nil {
		t.Fatal("activation above byte limit accepted")
	}
}

func TestResolveTemplatePath(t *testing.T) {
	object := map[string]any{"items": []any{map[string]any{}, map[string]any{}}, "one": map[string]any{}}
	for _, expression := range []string{".", ".items[0]", ".items[]", "empty", strings.Repeat(".x", 65)} {
		t.Run(expression, func(t *testing.T) {
			maxResults, timeout := 2, 100
			runner := execution.NewRunner(object, &maxResults, &timeout)
			if _, err := resolveTemplatePath(context.Background(), runner, expression); err == nil {
				t.Fatal("unsupported path accepted")
			}
		})
	}
}

func TestInputLimitsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewInput(ctx, workload("Deployment")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	cases := map[string]any{
		"bytes":        strings.Repeat("x", maxInputBytes),
		"nodes":        make([]any, maxInputNodes),
		"uint":         uint64(1),
		"integer type": int(1),
		"bytes type":   []byte("data"),
		"nan":          math.NaN(),
		"infinity":     math.Inf(1),
		"invalid UTF8": string([]byte{0xff}),
	}
	var deep any = "leaf"
	for range maxInputDepth {
		deep = []any{deep}
	}
	cases["depth"] = deep
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	cases["cycle"] = cycle
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			object := workload("Deployment")
			object.Object["extra"] = value
			if _, err := NewInput(context.Background(), object); err == nil {
				t.Fatal("unbounded or non-JSON input accepted")
			}
		})
	}
	t.Run("template counts toward activation size", func(t *testing.T) {
		object := workload("Deployment")
		template := object.Object["spec"].(map[string]any)["template"].(map[string]any)
		template["extra"] = strings.Repeat("x", maxInputBytes/2)
		if _, err := NewInput(context.Background(), object); err == nil {
			t.Fatal("combined object and template size was not bounded")
		}
	})
}

func TestCopyJSONNullAndBounds(t *testing.T) {
	ctx := context.Background()
	for _, value := range []any{nil, map[string]any(nil), []any(nil)} {
		budget := &jsonBudget{maxBytes: 4, maxNodes: 1, maxDepth: 1}
		result, err := copyJSON(ctx, value, 1, budget)
		if err != nil || result != nil {
			t.Fatalf("JSON null = %#v, %v", result, err)
		}
	}
	budget := &jsonBudget{maxBytes: 7, maxNodes: 3, maxDepth: 2}
	if _, err := copyJSON(ctx, map[string]any{"a": int64(1)}, 1, budget); err != nil {
		t.Fatalf("exact byte/node/depth boundary failed: %v", err)
	}
}
