// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelevaluator

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func labelObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": "train", "namespace": "team-a",
			"labels":      map[string]any{"example.com/team": "research"},
			"annotations": map[string]any{"example.com/owner": "alice"}},
		"spec":   map[string]any{"schedulerName": "example-scheduler", "parallelism": int64(2)},
		"status": map[string]any{"phase": "Running"},
	}}
}

func TestEvaluateObject(t *testing.T) {
	for _, tc := range []struct{ name, expression, want string }{
		{"annotation", `object.metadata.annotations["example.com/owner"]`, "alice"},
		{"label", `object.metadata.labels["example.com/team"]`, "research"},
		{"spec", `object.spec.schedulerName`, "example-scheduler"},
		{"status", `object.status.phase`, "Running"},
		{"identity", `object.apiVersion + "/" + object.kind + "/" + object.metadata.namespace + "/" + object.metadata.name`, "batch/v1/Job/team-a/train"},
		{"kind selection", `object.kind == "Job" ? object.metadata.annotations["example.com/owner"] : ""`, "alice"},
		{"fallback", `"absent" in object.metadata.annotations ? object.metadata.annotations["absent"] : object.metadata.labels["example.com/team"]`, "research"},
		{"empty result", `""`, ""},
		{"disabled", "", ""},
		{"whitespace", " \n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evaluator, err := NewCompiler().Compile(Config{Expression: tc.expression})
			require.NoError(t, err)
			obj := labelObject()
			before := obj.DeepCopy()
			value, err := evaluator.Evaluate(context.Background(), obj)
			require.NoError(t, err)
			require.Equal(t, tc.want, value)
			require.Equal(t, before, obj)
		})
	}
}

func TestEvaluationFailures(t *testing.T) {
	for _, tc := range []struct {
		name, expression string
		obj              *unstructured.Unstructured
		want             error
	}{
		{"dynamic number", `object.spec.parallelism`, labelObject(), ErrNotString},
		{"dynamic map", `object.metadata`, labelObject(), ErrNotString},
		{"missing object", `object.kind`, nil, ErrMissingObject},
		{"missing key", `object.metadata.labels["absent"]`, labelObject(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evaluator, err := NewCompiler().Compile(Config{Expression: tc.expression})
			require.NoError(t, err)
			value, err := evaluator.Evaluate(context.Background(), tc.obj)
			require.Empty(t, value)
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
	evaluator, err := NewCompiler().Compile(Config{Expression: `has(object.metadata.labels) && "team" in object.metadata.labels ? object.metadata.labels["team"] : ""`})
	require.NoError(t, err)
	value, err := evaluator.Evaluate(context.Background(), &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{}}})
	require.NoError(t, err)
	require.Empty(t, value)
}

func TestCompileFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want error
	}{
		{"syntax", Config{Expression: `object[`}, nil},
		{"old variable", Config{Expression: `labels["team"]`}, nil},
		{"static number", Config{Expression: `1 + 1`}, ErrNotString},
		{"static map", Config{Expression: `{"team": "a"}`}, ErrNotString},
		{"expression default bound", Config{Expression: `"` + strings.Repeat("a", DefaultMaxExpressionBytes) + `"`}, ErrExpressionTooLong},
		{"configured expression bound", Config{Expression: `"hello"`, Limits: Limits{MaxExpressionBytes: 4}}, ErrExpressionTooLong},
		{"negative expression bound", Config{Limits: Limits{MaxExpressionBytes: -1}}, ErrNegativeLimit},
		{"negative value bound", Config{Limits: Limits{MaxValueBytes: -1}}, ErrNegativeLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evaluator, err := NewCompiler().Compile(tc.cfg)
			require.Error(t, err)
			require.Nil(t, evaluator)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestValueLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size, limit int
		failed      bool
	}{
		{"default", DefaultMaxValueBytes + 1, 0, true},
		{"configured", 5, 4, true},
		{"raised", DefaultMaxValueBytes + 1, 2 * DefaultMaxValueBytes, false},
		{"boundary", 4, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evaluator, err := NewCompiler().Compile(Config{Expression: `object.value`, Limits: Limits{MaxValueBytes: tc.limit}})
			require.NoError(t, err)
			obj := &unstructured.Unstructured{Object: map[string]any{"value": strings.Repeat("a", tc.size)}}
			value, err := evaluator.Evaluate(context.Background(), obj)
			if tc.failed {
				require.ErrorIs(t, err, ErrValueTooLong)
				require.Empty(t, value)
			} else {
				require.NoError(t, err)
				require.Len(t, value, tc.size)
			}
		})
	}
}

const list = "[1,2,3,4,5,6,7,8,9,10]"

var expensiveExpression = fmt.Sprintf("string(size(%[1]s.map(a, %[1]s.map(b, %[1]s.map(c, %[1]s.map(d, %[1]s.map(e, %[1]s.map(f, a+b+c+d+e+f))))))))", list)

func TestCostLimits(t *testing.T) {
	for _, limit := range []uint64{0, 1} {
		evaluator, err := NewCompiler().Compile(Config{Expression: expensiveExpression, Limits: Limits{CostLimit: limit}})
		require.NoError(t, err)
		_, err = evaluator.Evaluate(context.Background(), labelObject())
		require.ErrorContains(t, err, "cost limit")
	}
}

func TestCancellation(t *testing.T) {
	for _, expression := range []string{"", `object.kind`} {
		evaluator, err := NewCompiler().Compile(Config{Expression: expression})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = evaluator.Evaluate(ctx, labelObject())
		require.ErrorIs(t, err, context.Canceled)
		ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err = evaluator.Evaluate(ctx, labelObject())
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	for _, frequency := range []uint{0, 1, 1000} {
		evaluator, err := NewCompiler().Compile(Config{Expression: expensiveExpression, Limits: Limits{CostLimit: math.MaxUint64, InterruptCheckFrequency: frequency}})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(5*time.Millisecond, cancel)
		defer timer.Stop()
		start := time.Now()
		_, err = evaluator.Evaluate(ctx, labelObject())
		require.ErrorIs(t, err, context.Canceled)
		require.Less(t, time.Since(start), time.Second)
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	evaluator, err := NewCompiler().Compile(Config{Expression: `object.metadata.name`})
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			obj := labelObject()
			obj.SetName(fmt.Sprint(i))
			value, err := evaluator.Evaluate(context.Background(), obj)
			require.NoError(t, err)
			require.Equal(t, fmt.Sprint(i), value)
		})
	}
}
