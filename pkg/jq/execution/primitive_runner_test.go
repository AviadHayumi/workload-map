// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package execution

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPrimitiveRunnerKeepsIndependentMutationState(t *testing.T) {
	for _, value := range []float64{1, 2} {
		source := map[string]any{"items": []any{map[string]any{"value": value, "canary": "keep"}}}
		runner := NewPrimitiveRunner(source)
		got, err := runner.Evaluate(t.Context(), ".items[].value")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]any{value}, got); diff != "" {
			t.Fatalf("Evaluate() mismatch (-want +got):\n%s", diff)
		}
		if err := runner.AssignZip(t.Context(), ".items[].value", []any{value + 10}); err != nil {
			t.Fatal(err)
		}
		updated, err := runner.GetObject()
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"items": []any{map[string]any{"value": value + 10, "canary": "keep"}}}
		if diff := cmp.Diff(want, updated); diff != "" {
			t.Fatalf("GetObject() mismatch (-want +got):\n%s", diff)
		}
		if err := runner.AssignZip(t.Context(), ".items[].value", nil); err == nil {
			t.Fatal("AssignZip(nil) succeeded, want length mismatch")
		}
		after, err := runner.GetObject()
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, after); diff != "" {
			t.Fatalf("failed write changed object (-want +got):\n%s", diff)
		}
	}
}
