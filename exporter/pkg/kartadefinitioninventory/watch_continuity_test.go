// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	ktesting "k8s.io/client-go/testing"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

func TestInventoryWatchClosurePreservesSourceUntilFailure(t *testing.T) {
	for _, mode := range []string{"normal", "short-empty", "failed-reconnect"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := client()
			definition := object(t, job(t, "cluster", 1))
			if _, err := c.Resource(v1alpha1.GroupVersion.WithResource("kartas")).Create(ctx, definition, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			source := watch.NewRaceFreeFake()
			var calls atomic.Int32
			c.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) {
				if calls.Add(1) == 1 {
					return true, source, nil
				}
				if mode == "failed-reconnect" {
					return true, nil, apierrors.NewForbidden(v1alpha1.GroupVersion.WithResource("kartas").GroupResource(), "", errors.New("test reconnect denied"))
				}
				<-ctx.Done()
				return true, nil, ctx.Err()
			})
			inventory, err := New(c, testOptions())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- inventory.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			eventually(t, func() bool {
				selected, err := inventory.Lookup(jobGVK)
				return err == nil && selected.Origin.Name == "cluster"
			})
			if mode != "short-empty" {
				source.Action(watch.Bookmark, definition.DeepCopy())
			}
			source.Stop()
			switch mode {
			case "normal":
				eventually(t, func() bool { return calls.Load() >= 2 })
				selected, err := inventory.Lookup(jobGVK)
				if err != nil || selected.Origin.Name != "cluster" {
					t.Fatalf("normal close revoked the selected definition: %+v, %v", selected, err)
				}
			case "short-empty":
				eventually(t, func() bool { _, err := inventory.Lookup(jobGVK); return errors.Is(err, ErrUnavailable) })
			case "failed-reconnect":
				eventually(t, func() bool { _, err := inventory.Lookup(jobGVK); return errors.Is(err, ErrPermissionDenied) })
			}
		})
	}
}
