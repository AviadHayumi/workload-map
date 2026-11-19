// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  Scope
		source string
		valid  bool
	}{
		{"root", ReadScope, "object.spec.replicas", true},
		{"read instance", ReadScope, "instance", false},
		{"write instance", WriteScope, "instance + string(index)", true},
		{"write value", WriteScope, "value", false},
		{"pod variable", ObjectScope, "variables.x", false},
		{"pod reference", ObjectScope, "references.x", false},
		{"target predicate", TargetScope, "object.metadata.name == target.metadata.name", true},
		{"target variable", TargetScope, "variables.x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateScope(tc.scope, tc.source)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}
func TestLazyVariablesAndReferenceRetry(t *testing.T) {
	ctx := context.Background()
	calls := 0
	r, err := NewRunnerWithVariables(map[string]any{"count": int64(9007199254740993)}, []NamedExpression{
		{Name: "broken", Expression: "object.missing"},
		{Name: "second", Expression: "variables.first + 1"},
		{Name: "first", Expression: "object.count"},
		{Name: "remote", Expression: "references.runtime.image"},
	}, WithReferenceProvider(func(context.Context) (map[string]any, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary")
		}
		return map[string]any{"runtime": map[string]any{"image": "example:v1"}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Evaluate(ctx, "variables.second")
	if err != nil || !reflect.DeepEqual(got, []any{int64(9007199254740994)}) {
		t.Fatalf("precision/lazy result %v, %v", got, err)
	}
	if calls != 0 {
		t.Fatal("unused reference fetched")
	}
	if _, err = r.Evaluate(ctx, "variables.remote"); err == nil {
		t.Fatal("expected reader error")
	}
	for range 2 {
		got, err = r.Evaluate(ctx, "variables.remote")
		if err != nil || got[0] != "example:v1" {
			t.Fatalf("retry %v %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("expected successful result cached, got %d calls", calls)
	}
}
func TestDependenciesUseAST(t *testing.T) {
	for _, tc := range []struct {
		source string
		vars   []string
		refs   bool
	}{
		{`"variables.fake references"`, nil, false},
		{`object.spec.references`, nil, false},
		{`[1].map(references, references + 1)`, nil, false},
		{`variables.real + variables["other"]`, []string{"other", "real"}, false},
		{`false ? references.runtime : object`, nil, true},
	} {
		deps, err := Analyze(tc.source)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(deps.Variables, tc.vars) || deps.UsesReferences != tc.refs {
			t.Fatalf("%s: %+v", tc.source, deps)
		}
	}
}
func TestVariableValidation(t *testing.T) {
	for _, vars := range [][]NamedExpression{
		{{Name: "a", Expression: "1"}, {Name: "a", Expression: "2"}},
		{{Name: "a", Expression: "variables.missing"}},
		{{Name: "a", Expression: "variables.b"}, {Name: "b", Expression: "variables.a"}},
		{{Name: "a", Expression: "instance"}},
		{{Name: "a", Expression: "variables[object.key]"}},
	} {
		if err := ValidateVariables(vars); err == nil {
			t.Fatalf("accepted %+v", vars)
		}
	}
}
func TestJSONAndExtensions(t *testing.T) {
	r, err := NewRunner(map[string]any{"value": int64(9007199254740993), "items": []any{int64(3), int64(1)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		source string
		want   any
	}{
		{`object.value`, int64(9007199254740993)},
		{`object.?missing.orValue(null)`, nil},
		{`object.items.sort()`, []any{int64(1), int64(3)}},
		{`"a/b~c".replace("~", "~0").replace("/", "~1")`, "a~1b~0c"},
	} {
		got, err := r.EvaluateWithVariables(context.Background(), tc.source, nil)
		if err != nil || !reflect.DeepEqual(got, []any{tc.want}) {
			t.Fatalf("%s => %v err=%v", tc.source, got, err)
		}
	}
	if _, err = r.Evaluate(context.Background(), "object.?missing"); !errors.Is(err, ErrAbsent) {
		t.Fatalf("optional absence: %v", err)
	}
	obj, _ := r.GetObject()
	obj.(map[string]any)["value"] = int64(0)
	got, _ := r.Evaluate(context.Background(), "object.value")
	if got[0] != int64(9007199254740993) {
		t.Fatal("object mutation leaked")
	}
}
func TestLimitsAndSafeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Evaluate(ctx, ObjectScope, "true", map[string]any{"object": map[string]any{}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	values := make([]any, 1000)
	for i := range values {
		values[i] = int64(i)
	}
	bindings := map[string]any{"object": map[string]any{"items": values, "secret": "DO_NOT_LEAK"}}
	if _, err := Evaluate(context.Background(), ObjectScope, "object.items.map(x, x + 1)", bindings, WithCostLimit(5)); !errors.Is(err, ErrCostLimit) {
		t.Fatalf("cost: %v", err)
	}
	if _, err := Evaluate(context.Background(), ObjectScope, "object.items.map(x, x + 1)", bindings, WithTimeout(time.Nanosecond)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if _, err := Evaluate(context.Background(), ObjectScope, "int(object.secret)", bindings); err == nil || strings.Contains(err.Error(), "DO_NOT_LEAK") {
		t.Fatalf("unsafe error: %v", err)
	}
	if _, err := EvaluateBool(context.Background(), ObjectScope, "1", bindings); err == nil {
		t.Fatal("numeric predicate accepted")
	}
}
