// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

type compactionCancelContext struct {
	context.Context
	cancel   context.CancelFunc
	pods     []*corev1.Pod
	original []*corev1.Pod
	after    int
}

func (c *compactionCancelContext) Err() error {
	compacted := 0
	for i, pod := range c.pods {
		if pod != c.original[i] {
			compacted++
		}
	}
	if compacted >= c.after {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCompactPodArrayCancellation(t *testing.T) {
	for _, test := range []struct {
		name  string
		after int
	}{
		{name: "before_copy", after: 0},
		{name: "during_copy", after: 1},
		{name: "after_last_copy", after: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			pods := []*corev1.Pod{indexTestPod("first", "team", nil), indexTestPod("second", "team", nil), indexTestPod("third", "team", nil)}
			original := append([]*corev1.Pod(nil), pods...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			controlled := &compactionCancelContext{Context: ctx, cancel: cancel, pods: pods, original: original, after: test.after}
			if err := compactPodArray(controlled, pods); !errors.Is(err, context.Canceled) {
				t.Fatalf("compactPodArray error = %v, want context.Canceled", err)
			}
			for i, pod := range pods {
				if copied := pod != original[i]; copied != (i < test.after) {
					t.Fatalf("Pod %d compacted = %v, cancellation after %d", i, copied, test.after)
				}
			}
		})
	}
}

func TestCompactPodArrayPreservesEmptyShapes(t *testing.T) {
	for _, test := range []struct {
		name string
		pods []*corev1.Pod
	}{
		{name: "nil"},
		{name: "empty", pods: []*corev1.Pod{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pods := test.pods
			if err := compactPodArray(context.Background(), pods); err != nil {
				t.Fatal(err)
			}
			if len(pods) != 0 || (pods == nil) != (test.pods == nil) {
				t.Fatal("compaction changed the empty slice shape")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := compactPodArray(ctx, pods); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled empty compaction error = %v, want context.Canceled", err)
			}
		})
	}
}
