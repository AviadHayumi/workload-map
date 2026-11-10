// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

func TestCompile(t *testing.T) {
	for _, source := range []string{"", " \n\t", strings.Repeat("x", maxExpressionBytes+1), "unknownVariable", "{'incomplete':}"} {
		if _, err := Compile(source); err == nil {
			t.Fatalf("invalid source accepted: %.80q", source)
		}
	}
	if _, err := Compile("{}" + strings.Repeat(" ", maxExpressionBytes-2)); err != nil {
		t.Fatalf("exact source bound rejected: %v", err)
	}
}

func TestEvaluateJSON(t *testing.T) {
	input, err := NewInput(context.Background(), workload("Deployment"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := Compile(`{'integer': 9007199254740993, 'double': 1.5, 'boolean': true, 'list': [null, {'text': 'hello'}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.Evaluate(context.Background(), input)
	want := map[string]any{"integer": int64(9007199254740993), "double": 1.5, "boolean": true, "list": []any{nil, map[string]any{"text": "hello"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v, %v; want %#v", got, err, want)
	}
}

func TestEvaluateAnnotationConversion(t *testing.T) {
	object := workload("Deployment")
	object.SetAnnotations(map[string]string{"plugins.karta.dev/service-port": "80"})
	input, err := NewInput(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	program, err := Compile(`'plugins.karta.dev/service-port' in object.metadata.annotations
? {'port': int(object.metadata.annotations['plugins.karta.dev/service-port']),
   'targetPort': template.spec.containers[0].ports[0].containerPort}
: {}`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := program.Evaluate(context.Background(), input)
	if err != nil || result["port"] != int64(80) || result["targetPort"] != int64(8080) {
		t.Fatalf("annotation conversion = %#v, %v", result, err)
	}
}

func TestEvaluateRejectsNonJSON(t *testing.T) {
	input, err := NewInput(context.Background(), workload("Deployment"))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"42", "[]", "null", "{1: 'key'}", "{'nested': {false: 1}}",
		"{'uint': 1u}", "{'bytes': b'data'}", "{'timestamp': timestamp('2026-01-01T00:00:00Z')}",
		"{'duration': duration('1s')}", "{'type': type(1)}", "{'nan': double('NaN')}",
		"{'infinite': double('Infinity')}", "{'error': 1 / 0}", "{'missing': object.noSuchField}",
	} {
		t.Run(source, func(t *testing.T) {
			program, err := Compile(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := program.Evaluate(context.Background(), input); err == nil {
				t.Fatal("non-JSON result accepted")
			}
		})
	}
}

func TestEvaluateLimits(t *testing.T) {
	object := workload("Deployment")
	object.Object["large"] = strings.Repeat("x", maxResultBytes)
	object.Object["items"] = make([]any, maxResultNodes)
	object.Object["costItems"] = make([]any, 200)
	for index := range object.Object["costItems"].([]any) {
		object.Object["costItems"].([]any)[index] = int64(index)
	}
	var deep any = true
	for range maxResultDepth {
		deep = []any{deep}
	}
	object.Object["deep"] = deep
	input, err := NewInput(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"bytes": "{'value': object.large}",
		"nodes": "{'value': object.items}",
		"depth": "{'value': object.deep}",
		"cost":  "{'value': object.costItems.map(x, object.costItems.map(y, x + y))}",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			program, err := Compile(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := program.Evaluate(context.Background(), input); err == nil || !strings.Contains(err.Error(), map[string]string{"bytes": "byte", "nodes": "node", "depth": "depth", "cost": "cost limit"}[name]) {
				t.Fatalf("expected %s limit, got %v", name, err)
			}
		})
	}
}

func TestEvaluateCancellationAndNil(t *testing.T) {
	input, err := NewInput(context.Background(), workload("Deployment"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := Compile("{}")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := program.Evaluate(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := program.Evaluate(expired, input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline = %v", err)
	}
	if _, err := program.Evaluate(context.Background(), nil); err == nil {
		t.Fatal("nil input accepted")
	}
	if _, err := (*Program)(nil).Evaluate(context.Background(), input); err == nil {
		t.Fatal("nil program accepted")
	}
	if _, err := (&Program{}).Evaluate(context.Background(), input); err == nil {
		t.Fatal("zero program accepted")
	}
	if _, err := program.Evaluate(context.Background(), &Input{}); err == nil {
		t.Fatal("zero input accepted")
	}
}

func TestConvertCELRejectsUnknownAndError(t *testing.T) {
	for _, value := range []ref.Val{
		types.NewUnknown(1, types.NewAttributeTrail("object")), types.NewErr("failure"),
	} {
		budget := &jsonBudget{maxBytes: maxResultBytes, maxNodes: maxResultNodes, maxDepth: maxResultDepth}
		if _, err := convertCEL(context.Background(), value, 1, budget); err == nil {
			t.Fatalf("CEL value %T accepted", value)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	budget := &jsonBudget{maxBytes: maxResultBytes, maxNodes: maxResultNodes, maxDepth: maxResultDepth}
	if _, err := convertCEL(ctx, types.Int(1), 1, budget); !errors.Is(err, context.Canceled) {
		t.Fatalf("conversion cancellation = %v", err)
	}
}

func TestEvaluateResultByteBoundary(t *testing.T) {
	object := workload("Deployment")
	object.Object["value"] = strings.Repeat("x", maxResultBytes-len(`{"value":""}`))
	input, err := NewInput(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	program, err := Compile("{'value': object.value}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := program.Evaluate(context.Background(), input); err != nil {
		t.Fatalf("exact result bound rejected: %v", err)
	}
}

func TestConcurrentProgramAndInput(t *testing.T) {
	input, err := NewInput(context.Background(), workload("CronJob"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := Compile("{'template': template, 'name': object.metadata.name}")
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			for range 20 {
				result, err := program.Evaluate(context.Background(), input)
				if err != nil {
					t.Error(err)
					return
				}
				template := result["template"].(map[string]any)
				if template["spec"].(map[string]any)["futureField"].(map[string]any)["largeInteger"] != int64(9007199254740993) {
					t.Error("shared snapshot mutated")
				}
				template["spec"] = nil
				input.TemplatePath()[0] = "changed"
			}
		})
	}
	group.Wait()
}
