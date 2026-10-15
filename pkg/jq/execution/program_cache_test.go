// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package execution

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/itchyny/gojq"
)

func TestProgramCacheEligibility(t *testing.T) {
	for _, expression := range []string{
		`.`, `.metadata.name`, `.spec.containers[0].name`, `.items[]?.name`,
		`.metadata.labels["example.io/key"]`, `.items | .[] | .name`,
		`(.first, .second)`, `"literal", null, true, false, 123`, `$first, $second`,
	} {
		t.Run(expression, func(t *testing.T) {
			cache := newProgramCache(2, 1024)
			first, err := cache.compile(expression, []string{"$first", "$second"})
			if err != nil {
				t.Fatal(err)
			}
			second, err := cache.compile(expression, []string{"$first", "$second"})
			if err != nil || first != second || len(cache.entries) != 1 {
				t.Fatalf("selector was not reused: %v", err)
			}
		})
	}
	for _, expression := range []string{
		`{a:[1]}`, `[1]`, `999999999999999999999999999999`,
		`1 + 2`, `-1`, `1.5`, `.items[0:1]`, `.a = 1`, `.a |= .b`,
		`env`, `$ENV`, `from_entries`, `@json`, `"value: \(.name)"`,
		`test(.pattern)`, `.pattern as $p | .text | test($p)`,
		`def field: .a; field`, `..`, `if .a then .b else .c end`,
		`try .a catch .`, `reduce .[] as $x (0; . + $x)`,
		`error({a:[1]})`, `({a:[1]} | halt_error)`,
	} {
		t.Run(expression, func(t *testing.T) {
			cache := newProgramCache(2, 1024)
			first, err := cache.compile(expression, nil)
			if err != nil {
				t.Fatal(err)
			}
			second, err := cache.compile(expression, nil)
			if err != nil || first == second || len(cache.entries) != 0 {
				t.Fatalf("complex expression entered cache: %v", err)
			}
		})
	}
	// A value just beyond the native int range must also bypass the cache on 32-bit targets.
	tooLarge := new(big.Int).Add(big.NewInt(int64(^uint(0)>>1)), big.NewInt(1)).String()
	query, err := gojq.Parse(tooLarge)
	if err != nil || isReusableQuery(query, nil) {
		t.Fatalf("out-of-range number is reusable: %s, %v", tooLarge, err)
	}
}

func TestProgramCacheRejectsMetadataAndUndeclaredVariables(t *testing.T) {
	for _, expression := range []string{
		`module {}; .`, `import "missing" as m; .`, `include "missing"; .`,
		`$__loc__`, `$unknown`, `$ENV`,
	} {
		query, err := gojq.Parse(expression)
		if err != nil || isReusableQuery(query, nil) {
			t.Fatalf("unsupported query admitted: %q, %v", expression, err)
		}
	}
	query, err := gojq.Parse(`$ENV`)
	if err != nil || isReusableQuery(query, []string{"$ENV"}) {
		t.Fatalf("environment intrinsic admitted: %v", err)
	}
}

func TestProgramCacheVariablesAndIdentity(t *testing.T) {
	cache := newProgramCache(8, 1024)
	variables := []string{"$first", "$second"}
	code, err := cache.compile(`$first, $second`, variables)
	if err != nil {
		t.Fatal(err)
	}
	variables[0] = "$changed"
	again, err := cache.compile(`$first, $second`, []string{"$first", "$second"})
	if err != nil || code != again {
		t.Fatalf("variable declaration mutation changed cached program: %v", err)
	}
	reversed, err := cache.compile(`$first, $second`, []string{"$second", "$first"})
	if err != nil || code == reversed {
		t.Fatalf("variable order not part of identity: %v", err)
	}
	for _, test := range []struct {
		code *gojq.Code
		want []any
	}{{code, []any{"one", "two"}}, {reversed, []any{"two", "one"}}} {
		iter := test.code.Run(nil, "one", "two")
		var got []any
		for value, ok := iter.Next(); ok; value, ok = iter.Next() {
			got = append(got, value)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("variable values = %v, want %v", got, test.want)
		}
	}
	keys := make(map[programKey]bool)
	for _, vars := range [][]string{{"$a", "$bc"}, {"$ab", "$c"}, {"$a:$b"}, {"$a", "$b"}} {
		key, ok := compilationKey(".", vars, 1024)
		if !ok || keys[key] {
			t.Fatalf("variable key collision: %q", vars)
		}
		keys[key] = true
	}
	plain, _ := cache.compile(".", nil)
	spaced, _ := cache.compile(". ", nil)
	if plain == spaced {
		t.Fatal("expression identity was normalized")
	}
}

func TestProgramCacheEvictionAndFailures(t *testing.T) {
	cache := newProgramCache(2, 32)
	first, _ := cache.compile(".first", nil)
	second, _ := cache.compile(".second", nil)
	cache.compile(".first", nil)
	cache.compile(".third", nil)
	if len(cache.entries) != 2 || cache.recency.Len() != 2 {
		t.Fatal("cache exceeded capacity")
	}
	if cached, _ := cache.compile(".first", nil); cached != first {
		t.Fatal("recently used entry was evicted")
	}
	if compiled, _ := cache.compile(".second", nil); compiled == second {
		t.Fatal("least recently used entry was retained")
	}
	for _, expression := range []string{".", "[", "$missing"} {
		disabled := newProgramCache(0, 32)
		a, errA := disabled.compile(expression, nil)
		b, errB := disabled.compile(expression, nil)
		if len(disabled.entries) != 0 || (errA == nil && a == b) || (errA != nil && errA == errB) {
			t.Fatal("disabled cache retained a program or error")
		}
	}
	for _, expression := range []string{"[", "$missing"} {
		fresh := newProgramCache(2, 32)
		_, firstErr := fresh.compile(expression, nil)
		_, secondErr := fresh.compile(expression, nil)
		if firstErr == nil || secondErr == nil || firstErr == secondErr || len(fresh.entries) != 0 {
			t.Fatalf("failure was cached: %q", expression)
		}
	}
	for _, test := range []struct {
		expression string
		variables  []string
	}{{".field" + strings.Repeat(" ", 32), nil}, {".", []string{"$" + strings.Repeat("v", 32)}}} {
		a, errA := cache.compile(test.expression, test.variables)
		b, errB := cache.compile(test.expression, test.variables)
		if errA != nil || errB != nil || a == b {
			t.Fatalf("oversized key was cached: %v, %v", errA, errB)
		}
	}
}

func TestProgramCacheMutableConstantsStayPrivate(t *testing.T) {
	for _, expression := range []string{`{a:[1]}`, `[1]`, `999999999999999999999999999999`, `env`, `$ENV`} {
		t.Run(expression, func(t *testing.T) {
			first, err := NewDefaultRunner(nil).Evaluate(context.Background(), expression)
			if err != nil || len(first) != 1 {
				t.Fatalf("first evaluation: %v, %v", first, err)
			}
			original := fmt.Sprint(first)
			switch value := first[0].(type) {
			case map[string]any:
				value["poison"] = true
				if nested, ok := value["a"].([]any); ok {
					nested[0] = "poison"
				}
			case []any:
				value[0] = "poison"
			case *big.Int:
				value.SetInt64(0)
			default:
				t.Fatalf("expected mutable constant, got %T", value)
			}
			second, err := NewDefaultRunner(nil).Evaluate(context.Background(), expression)
			if err != nil || fmt.Sprint(second) != original {
				t.Fatalf("constant leaked between runners: %v, %v", second, err)
			}
		})
	}
	for _, expression := range []string{`error({a:[1]})`, `({a:[1]} | halt_error)`} {
		_, firstErr := NewDefaultRunner(nil).Evaluate(context.Background(), expression)
		var valueErr gojq.ValueError
		if !errors.As(firstErr, &valueErr) {
			t.Fatalf("expected ValueError: %v", firstErr)
		}
		valueErr.Value().(map[string]any)["a"].([]any)[0] = "poison"
		_, secondErr := NewDefaultRunner(nil).Evaluate(context.Background(), expression)
		if !errors.As(secondErr, &valueErr) || valueErr.Value().(map[string]any)["a"].([]any)[0] != 1 {
			t.Fatalf("error value leaked between runners: %v", secondErr)
		}
	}
}

func TestProgramCacheFreshInputsContextAndLimits(t *testing.T) {
	ctx := context.Background()
	first := NewDefaultRunner(map[string]any{"item": map[string]any{"name": "first"}})
	values, err := first.Evaluate(ctx, ".item")
	if err != nil {
		t.Fatal(err)
	}
	values[0].(map[string]any)["name"] = "mutated"
	object, _ := first.GetObject()
	if object.(map[string]any)["item"].(map[string]any)["name"] != "mutated" {
		t.Fatal("existing same-runner result ownership changed")
	}
	second := NewDefaultRunner(map[string]any{"item": map[string]any{"name": "second"}})
	values, err = second.Evaluate(ctx, ".item")
	if err != nil || values[0].(map[string]any)["name"] != "second" {
		t.Fatalf("cached input or result: %v, %v", values, err)
	}
	if err := second.Assign(ctx, ".item.name", "assigned"); err != nil {
		t.Fatal(err)
	}
	values, err = second.Evaluate(ctx, ".item")
	if err != nil || values[0].(map[string]any)["name"] != "assigned" {
		t.Fatalf("cached result survived assignment: %v, %v", values, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := second.Evaluate(canceled, ".item"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached code ignored current context: %v", err)
	}
	if _, err := second.Evaluate(ctx, ".item"); err != nil {
		t.Fatalf("cancellation leaked into fresh execution: %v", err)
	}
	input := map[string]any{"items": []any{1, 2, 3}}
	low, high := 2, 4
	if _, err := NewRunner(input, &low, nil).Evaluate(ctx, ".items[]"); err == nil {
		t.Fatal("result limit was ignored")
	}
	if got, err := NewRunner(input, &high, nil).Evaluate(ctx, ".items[]"); err != nil || len(got) != 3 {
		t.Fatalf("previous result limit leaked: %v, %v", got, err)
	}
}

func TestProgramCacheConcurrentReuse(t *testing.T) {
	cache := newProgramCache(8, 1024)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			for range 20 {
				code, err := cache.compile(".name", nil)
				if err != nil {
					t.Error(err)
					return
				}
				want := strconv.Itoa(i)
				value, ok := code.Run(map[string]any{"name": want}).Next()
				if !ok || value != want {
					t.Errorf("shared runtime/input: %v", value)
				}
				got, err := NewDefaultRunner(map[string]any{"name": want}).Evaluate(context.Background(), ".name")
				if err != nil || len(got) != 1 || got[0] != want {
					t.Errorf("shared runner result: %v, %v", got, err)
				}
			}
		})
	}
	wg.Wait()
	if len(cache.entries) != 1 || cache.recency.Len() != 1 {
		t.Fatal("concurrent misses inserted duplicate entries")
	}
}
