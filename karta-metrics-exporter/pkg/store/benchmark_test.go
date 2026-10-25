// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

func BenchmarkPodMutations(b *testing.B) {
	for _, count := range []int{1000, 100000} {
		for _, operation := range []string{"lookup_last", "replace_last_publish", "insert_delete_publish", "refresh_all_publish", "build_all"} {
			b.Run(fmt.Sprintf("%d/%s", count, operation), func(b *testing.B) {
				s := New(Options{})
				rows := make([]PublishedPod, count)
				expiry := time.Now().Add(time.Hour)
				for i := range rows {
					rows[i] = PublishedPod{Record: PodRecord{UID: types.UID(fmt.Sprintf("pod-%08d", i))}, Mapping: Evidence{ExpiresAt: expiry, Dependencies: []string{"stream"}}}
				}
				p := &Publication{Workload: WorkloadRecord{UID: "root"}, PodPages: BuildPodPages(rows, nil)}
				s.Publish(p)
				last := rows[count-1]
				inserted := PublishedPod{Record: PodRecord{UID: "zz-inserted"}, Mapping: last.Mapping}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					switch operation {
					case "lookup_last":
						if _, ok := p.Pod(last.Record.UID); !ok {
							b.Fatal("missing Pod")
						}
					case "replace_last_publish":
						last.Record.Component = fmt.Sprint(i % 2)
						p = p.WithPod(last.Record.UID, &last)
						s.Publish(p)
					case "insert_delete_publish":
						p = p.WithPod(inserted.Record.UID, &inserted)
						s.Publish(p)
						p = p.WithPod(inserted.Record.UID, nil)
						s.Publish(p)
					case "refresh_all_publish":
						expiry = expiry.Add(time.Second)
						for j := range rows {
							rows[j].Mapping.ExpiresAt = expiry
						}
						next := *p
						next.PodPages = BuildPodPages(rows, p)
						p = &next
						s.Publish(p)
					case "build_all":
						next := *p
						next.PodPages = BuildPodPages(rows, nil)
						p = &next
					}
				}
			})
		}
	}
}
