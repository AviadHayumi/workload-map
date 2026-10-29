// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func BenchmarkPodMutations(b *testing.B) {
	for _, count := range []int{1000, 100000} {
		for _, operation := range []string{"lookup_last", "replace_last_publish", "insert_delete_publish", "replace_all", "capture"} {
			b.Run(fmt.Sprintf("%d/%s", count, operation), func(b *testing.B) {
				s := New(Options{})
				rows := make([]PodResult, count)
				source := SourceKey{Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}}
				for i := range rows {
					rows[i] = PodResult{Record: PodRecord{UID: types.UID(fmt.Sprintf("pod-%08d", i)), Labels: map[string]string{"team": "training"}}, HasValue: true, Sources: []SourceKey{source}}
					s.UpsertPod(&rows[i])
				}
				last := rows[count-1]
				inserted := last
				inserted.Record.UID = "zz-inserted"
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					switch operation {
					case "lookup_last":
						if s.Pod(last.Record.UID) == nil {
							b.Fatal("missing Pod")
						}
					case "replace_last_publish":
						last.Record.Component = []string{"a", "b"}[i%2]
						s.UpsertPod(&last)
					case "insert_delete_publish":
						s.UpsertPod(&inserted)
						s.RemovePod(inserted.Record.UID)
					case "replace_all":
						for j := range rows {
							s.UpsertPod(&rows[j])
						}
					case "capture":
						if len(s.Capture().Pods) != count {
							b.Fatal("capture lost Pods")
						}
					}
				}
			})
		}
	}
}

func BenchmarkPublishOnePodChange(b *testing.B) {
	for _, count := range []int{1000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := New(Options{})
			key := SourceKey{Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}}
			for i := 0; i < count; i++ {
				s.UpsertPod(&PodResult{Record: PodRecord{UID: types.UID(fmt.Sprint(i))}, HasValue: true, Sources: []SourceKey{key}})
			}
			original := *s.Pod("0")
			changed := original
			changed.Record.Labels = map[string]string{"team": "new"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if i%2 == 0 {
					s.UpsertPod(&changed)
				} else {
					s.UpsertPod(&original)
				}
			}
		})
	}
}
