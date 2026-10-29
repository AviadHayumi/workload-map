// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package otel

import (
	"context"
	"fmt"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

func BenchmarkCollectIndependentResults(b *testing.B) {
	for _, shape := range []struct{ roots, pods int }{{10, 1}, {1000, 1}, {1, 100000}} {
		b.Run(fmt.Sprintf("roots_%d_pods_%d", shape.roots, shape.pods), func(b *testing.B) {
			s := store.New(store.Options{})
			key := store.SourceKey{Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}}
			s.SetSourceHealth(key, store.SourceHealth{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, Synced: true})
			for i := 0; i < shape.roots; i++ {
				root := store.ObjectRef{UID: types.UID(fmt.Sprintf("root-%d", i)), Namespace: "ns", Name: fmt.Sprintf("job-%d", i), GVK: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}}
				s.UpsertWorkload(&store.WorkloadResult{Record: store.WorkloadRecord{UID: root.UID, Ref: root, HasStatus: true, Phases: []string{"Running"}, CreatedTimestamp: 50}, HasValue: true, Sources: []store.SourceKey{key}, CreationSources: []store.SourceKey{key}})
				for j := 0; j < shape.pods; j++ {
					s.UpsertPod(&store.PodResult{Record: store.PodRecord{UID: types.UID(fmt.Sprintf("pod-%d-%d", i, j)), Name: fmt.Sprintf("worker-%d-%d", i, j), Namespace: "ns", Workload: root}, HasValue: true, Sources: []store.SourceKey{key}})
				}
			}
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithCardinalityLimit(0))
			b.Cleanup(func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					b.Fatal(err)
				}
			})
			registration, err := Register(provider.Meter("benchmark"), s)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := registration.Unregister(); err != nil {
					b.Fatal(err)
				}
			})
			var metrics metricdata.ResourceMetrics
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := reader.Collect(context.Background(), &metrics); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
