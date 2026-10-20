// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store_test

import (
	"os"
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/test/scale"
)

func BenchmarkScaleStoreCapture(b *testing.B) {
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
				snapshot := fixture.Store.Capture()
				if len(snapshot.Records) != scenario.Workloads || len(snapshot.Health) != 1 {
					b.Fatal("capture lost records")
				}
			}
		})
	}
}
