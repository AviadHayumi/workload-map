// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

func TestExtractInstanceIDsRequiresUniqueNonemptyStrings(t *testing.T) {
	for _, test := range []struct {
		name       string
		expression string
		want       []string
		wantError  string
	}{
		{name: "source order", expression: `["gpu", "cpu"]`, want: []string{"gpu", "cpu"}},
		{name: "empty list", expression: `[]`},
		{name: "null result", expression: `null`, wantError: "must return a list"},
		{name: "scalar result", expression: `"gpu"`, wantError: "must return a list"},
		{name: "map result", expression: `{"gpu": 1}`, wantError: "must return a list"},
		{name: "missing group name", expression: `[null, "cpu"]`, wantError: "must be strings"},
		{name: "number group name", expression: `[7, "cpu"]`, wantError: "must be strings"},
		{name: "boolean group name", expression: `[true, "cpu"]`, wantError: "must be strings"},
		{name: "map group name", expression: `[{"name": "gpu"}, "cpu"]`, wantError: "must be strings"},
		{name: "empty group name", expression: `["", "cpu"]`, wantError: "empty string"},
		{name: "duplicate group name", expression: `["gpu", "gpu"]`, wantError: "duplicate instance id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			accessor := sdkTestAccessor(t, map[string]any{})
			definition := v1alpha1.ComponentDefinition{
				InstanceIds: &v1alpha1.ValueAccessor{Read: &v1alpha1.ReadAccessor{Expression: test.expression}},
			}
			got, err := accessor.ExtractInstanceIds(context.Background(), definition)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("ids = %#v, error = %v, want %q", got, err, test.wantError)
				}
				if got != nil {
					t.Fatalf("invalid IDs returned a partial result: %#v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ids = %#v, error = %v, want %#v", got, err, test.want)
			}
		})
	}
}

func TestExtractInstanceIDsRejectsWrongResultCount(t *testing.T) {
	ctx := context.Background()
	for _, results := range [][]any{nil, {[]any{"gpu"}, []any{"cpu"}}} {
		runner := expression.NewMockRunner(gomock.NewController(t))
		runner.EXPECT().EvaluateWithVariables(ctx, "ids", nil).Return(results, nil)
		definition := v1alpha1.ComponentDefinition{InstanceIds: &v1alpha1.ValueAccessor{Read: &v1alpha1.ReadAccessor{Expression: "ids"}}}
		got, err := NewAccessor(runner).ExtractInstanceIds(ctx, definition)
		if got != nil || err == nil || !strings.Contains(err.Error(), "must return exactly one list") {
			t.Fatalf("ids = %#v, error = %v for %d results", got, err, len(results))
		}
	}
}

func sdkTestAccessor(t *testing.T, object any) *Accessor {
	t.Helper()
	return NewAccessor(mustCelRunner(object))
}
