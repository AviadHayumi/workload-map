// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store_test

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/test/scale"
)

func BenchmarkScaleStoreSnapshot(b *testing.B) {
	if os.Getenv("KARTA_SCALE_WORKLOADS") == "" {
		b.Skip("set KARTA_SCALE_WORKLOADS to enable explicit scale scenarios")
	}
	cases, err := scale.Cases(os.Getenv("KARTA_SCALE_WORKLOADS"), os.Getenv("KARTA_SCALE_PODS_PER_WORKLOAD"))
	if err != nil {
		b.Fatal(err)
	}
	for _, scenario := range cases {
		b.Run(scenario.Name(), func(b *testing.B) {
			fixture := scale.New(scenario)
			b.ReportAllocs()
			for b.Loop() {
				snapshot := fixture.Store.Snapshot()
				if len(snapshot.Workloads) != scenario.Workloads || len(snapshot.Pods) != scenario.Workloads*scenario.Pods || len(snapshot.Health) != scenario.Workloads {
					b.Fatal("snapshot lost records")
				}
			}
		})
	}
}

func TestScaleStoreChurnOracle(t *testing.T) {
	fixture := scale.New(scale.Case{Workloads: 64, Pods: 3})
	expected := make(map[types.UID]types.UID)
	for i := range fixture.Case.Workloads {
		for p := range fixture.Case.Pods {
			expected[types.UID(fmt.Sprintf("pod-%d-%d-v0", i, p))] = types.UID(fmt.Sprintf("workload-%d-v0", i))
		}
	}
	for i := range fixture.Case.Workloads {
		switch i % 3 {
		case 0:
			fixture.Delete(i)
			for p := range fixture.Case.Pods {
				delete(expected, types.UID(fmt.Sprintf("pod-%d-%d-v0", i, p)))
			}
		case 1:
			fixture.Replace(i)
			for p := range fixture.Case.Pods {
				delete(expected, types.UID(fmt.Sprintf("pod-%d-%d-v0", i, p)))
				expected[types.UID(fmt.Sprintf("pod-%d-%d-v1", i, p))] = types.UID(fmt.Sprintf("workload-%d-v1", i))
			}
		}
	}
	snapshot := fixture.Store.Snapshot()
	if len(snapshot.Pods) != len(expected) {
		t.Fatalf("Pods=%d, want %d", len(snapshot.Pods), len(expected))
	}
	for _, pod := range snapshot.Pods {
		if root, ok := expected[pod.UID]; !ok || root != pod.WorkloadUID {
			t.Fatalf("unexpected attribution %s -> %s", pod.UID, pod.WorkloadUID)
		}
	}
	for _, workload := range snapshot.Workloads {
		pods := fixture.Store.PodsOfWorkload(workload.UID)
		if len(pods) != fixture.Case.Pods {
			t.Fatalf("root %s has %d indexed Pods", workload.UID, len(pods))
		}
		for _, pod := range pods {
			if expected[pod.UID] != workload.UID {
				t.Fatalf("reverse index retained stale attribution for %s", pod.UID)
			}
		}
	}
}

func TestScaleStoreConcurrentSnapshots(t *testing.T) {
	s := store.New()
	const records = 64
	for i := range records {
		s.UpsertWorkload(store.WorkloadRecord{UID: types.UID(fmt.Sprintf("w-%d", i)), Generation: 0, CreatedTimestamp: 0})
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		for iteration := range 2000 {
			s.UpsertWorkload(store.WorkloadRecord{UID: types.UID(fmt.Sprintf("w-%d", iteration%records)), Generation: int64(iteration), CreatedTimestamp: -int64(iteration)})
		}
	})
	for range 4 {
		workers.Go(func() {
			for range 100 {
				snapshot := s.Snapshot()
				if len(snapshot.Workloads) != records {
					t.Errorf("snapshot has %d records, want %d", len(snapshot.Workloads), records)
					return
				}
				for _, workload := range snapshot.Workloads {
					if workload.Generation != -workload.CreatedTimestamp {
						t.Errorf("torn immutable record for %s", workload.UID)
						return
					}
				}
			}
		})
	}
	workers.Wait()
}
