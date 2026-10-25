// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package execution

import (
	"context"
	"runtime"
	"testing"
)

func BenchmarkFreshRunnerStatus(b *testing.B) {
	object := map[string]any{
		"metadata": map[string]any{"name": "job", "namespace": "scale"},
		"spec":     map[string]any{"parallelism": 1, "suspend": false},
		"status":   map[string]any{"active": 1, "succeeded": 0, "failed": 0},
	}
	expressions := []string{
		"(.spec.suspend // false) | tostring",
		"((.status.active // 0) > 0) | tostring",
		"((.status.succeeded // 0) > 0) | tostring",
		"((.status.failed // 0) > 0) | tostring",
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		r := NewDefaultRunner(object)
		for _, expression := range expressions {
			if _, err := r.Evaluate(ctx, expression); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkFreshRunnerStatusTwoWorkers(b *testing.B) {
	previous := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(previous)
	object := map[string]any{
		"metadata": map[string]any{"name": "job", "namespace": "scale"},
		"spec":     map[string]any{"parallelism": 1, "suspend": false},
		"status":   map[string]any{"active": 1, "succeeded": 0, "failed": 0},
	}
	expressions := []string{
		"(.spec.suspend // false) | tostring",
		"((.status.active // 0) > 0) | tostring",
		"((.status.succeeded // 0) > 0) | tostring",
		"((.status.failed // 0) > 0) | tostring",
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r := NewDefaultRunner(object)
			for _, expression := range expressions {
				if _, err := r.Evaluate(ctx, expression); err != nil {
					b.Error(err)
					return
				}
			}
		}
	})
}
