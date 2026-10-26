// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store_test

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func TestPublicationChurnPreservesCurrentLifetimes(t *testing.T) {
	s := store.New()
	random := rand.New(rand.NewPCG(17, 29))
	const roots = 64
	generations := make([]int, roots)
	active := make(map[int]bool)
	for step := range 1000 {
		i := random.IntN(roots)
		uid := types.UID(fmt.Sprintf("root-%d-%d", i, generations[i]))
		if random.IntN(3) == 0 {
			s.RemovePublication(uid)
			delete(active, i)
			generations[i]++
		} else {
			ref := store.WorkloadRef{Namespace: "test", Name: fmt.Sprintf("root-%d", i), Group: "batch", Version: "v1", Kind: "Job"}
			rows := make([]store.PublishedPod, 3)
			for p := range rows {
				rows[p].Record = store.PodRecord{UID: types.UID(fmt.Sprintf("pod-%d-%d-%d", i, p, generations[i])), WorkloadUID: uid, Workload: ref}
			}
			s.Publish(&store.Publication{Workload: store.WorkloadRecord{UID: uid, Ref: ref}, PodPages: store.BuildPodPages(rows, nil)})
			active[i] = true
		}
		view := s.Capture()
		if len(view.Records) != len(active) {
			t.Fatalf("step %d: %d roots, want %d", step, len(view.Records), len(active))
		}
		expected := make(map[types.UID]types.UID, len(active)*3)
		for i := range active {
			for p := range 3 {
				expected[types.UID(fmt.Sprintf("pod-%d-%d-%d", i, p, generations[i]))] = types.UID(fmt.Sprintf("root-%d-%d", i, generations[i]))
			}
		}
		for _, record := range view.Records {
			for _, page := range record.PodPages {
				for _, pod := range page.Rows {
					root, exists := expected[pod.Record.UID]
					if !exists || root != record.Workload.UID || root != pod.Record.WorkloadUID {
						t.Fatalf("step %d: stale or duplicate Pod %s under %s", step, pod.Record.UID, record.Workload.UID)
					}
					delete(expected, pod.Record.UID)
				}
			}
		}
		if len(expected) != 0 {
			t.Fatalf("step %d: %d missing Pods", step, len(expected))
		}
	}
}

func TestPublicationConcurrentCaptureKeepsRecordAtomic(t *testing.T) {
	s := store.New()
	const updates = 1000
	var workers sync.WaitGroup
	workers.Go(func() {
		for i := range updates {
			text := fmt.Sprint(i)
			s.Publish(&store.Publication{
				Workload:    store.WorkloadRecord{UID: "root", Ref: store.WorkloadRef{Name: text}, CreatedTimestamp: int64(i)},
				SubmittedBy: text, Creation: store.Evidence{ExpiresAt: time.Unix(int64(i), 0)},
			})
		}
	})
	for range 4 {
		workers.Go(func() {
			for range updates {
				for _, record := range s.Capture().Records {
					if record.Workload.Ref.Name != fmt.Sprint(record.Workload.CreatedTimestamp) ||
						record.SubmittedBy != record.Workload.Ref.Name ||
						record.Creation.ExpiresAt.Unix() != record.Workload.CreatedTimestamp {
						t.Error("capture mixed fields from different publications")
						return
					}
				}
			}
		})
	}
	workers.Wait()
}
