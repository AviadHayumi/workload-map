// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/watch"
	ktesting "k8s.io/client-go/testing"
)

func TestDeletingWinnerPromotesShadowedClusterDefinition(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "valid winner"
		if invalid {
			name = "invalid winner"
		}
		t.Run(name, func(t *testing.T) {
			c := client()
			stream := watch.NewRaceFreeFake()
			c.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) { return true, stream, nil })
			inventory, err := New(c, testOptions())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- inventory.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			eventually(t, func() bool { return inventory.Ready() == nil })
			winner, shadow := job(t, "oldest", 1), job(t, "shadow", 2)
			shadow.ResourceVersion = "7"
			stream.Add(object(t, shadow))
			eventually(t, func() bool {
				s, err := inventory.Lookup(jobGVK)
				return err == nil && s.Origin.Name == shadow.Name
			})
			if invalid {
				winner.Spec.StructureDefinition.RootComponent.Name = ""
			}
			stream.Add(object(t, winner))
			eventually(t, func() bool {
				s, err := inventory.Lookup(jobGVK)
				if invalid {
					return errors.Is(err, ErrInvalidDefinition)
				}
				return err == nil && s.Origin.UID == winner.UID
			})
			stream.Delete(object(t, winner))
			eventually(t, func() bool {
				s, err := inventory.Lookup(jobGVK)
				return err == nil && s.Origin.Source == SourceCluster && s.Origin.UID == shadow.UID &&
					s.Origin.ResourceVersion == "7" && s.Definition.Name == shadow.Name
			})
		})
	}
}
