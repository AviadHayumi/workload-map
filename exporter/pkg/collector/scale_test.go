// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dsx-ai-factory/workload-map/exporter/test/scale"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
)

func TestScalePrometheusExposition(t *testing.T) {
	if os.Getenv("KARTA_SCALE_WORKLOADS") == "" {
		t.Skip("set KARTA_SCALE_WORKLOADS=1000,10000,100000 to enable synthetic scale validation")
	}
	cases, err := scale.Cases(os.Getenv("KARTA_SCALE_WORKLOADS"), os.Getenv("KARTA_SCALE_PODS_PER_WORKLOAD"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range cases {
		t.Run(scenario.Name(), func(t *testing.T) {
			fixture := scale.New(scenario)
			handler := promhttp.HandlerFor(testGatherer(t, fixture.Store, Options{}), promhttp.HandlerOpts{})
			started := time.Now()
			counts, bytes, err := fixture.VerifyHandler(handler, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("initial exposition: status=%d health=%d pods=%d bytes=%d verification_wall=%s", counts.Status, counts.Health, counts.Pods, bytes, time.Since(started))
			for i := range scenario.Workloads {
				switch i % 10 {
				case 0:
					fixture.Delete(i)
				case 1:
					fixture.Replace(i)
				}
			}
			fixture.PublishHealth()
			counts, bytes, err = fixture.VerifyHandler(handler, false)
			if err != nil {
				t.Fatalf("deletion/replacement exposition: %v", err)
			}
			t.Logf("after 10%% deletion and 10%% UID replacement: status=%d health=%d pods=%d bytes=%d", counts.Status, counts.Health, counts.Pods, bytes)
		})
	}
}

func BenchmarkScalePrometheusHTTP(b *testing.B) {
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
			handler := promhttp.HandlerFor(testGatherer(b, fixture.Store, Options{}), promhttp.HandlerOpts{})
			counts, bytes, err := fixture.VerifyHandler(handler, false)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(bytes)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				response := &scale.Response{}
				handler.ServeHTTP(response, scale.Request())
				if response.Code != 200 || response.Bytes != bytes {
					b.Fatalf("HTTP %d, bytes=%d; want 200, bytes=%d", response.Code, response.Bytes, bytes)
				}
			}
			b.ReportMetric(float64(counts.Status+counts.Health+counts.Pods), "samples/op")
			b.ReportMetric(float64(bytes), "exposition-B/op")
		})
	}
}

func TestScaleConcurrentHTTPOracle(t *testing.T) {
	fixture := scale.New(scale.Case{Workloads: 32, Pods: 2})
	handler := promhttp.HandlerFor(testGatherer(t, fixture.Store, Options{}), promhttp.HandlerOpts{})
	var workers sync.WaitGroup
	stop := make(chan struct{})
	workers.Go(func() {
		for iteration := 0; ; iteration++ {
			select {
			case <-stop:
				return
			default:
			}
			i := iteration % fixture.Case.Workloads
			record := fixture.Workload(i)
			if iteration%2 == 0 {
				record.Phases = []v1alpha1.ResourceStatus{v1alpha1.FailedStatus}
			} else {
				record.Phases = []v1alpha1.ResourceStatus{v1alpha1.RunningStatus}
			}
			fixture.Store.UpsertWorkload(record)
		}
	})
	var scrapers sync.WaitGroup
	errors := make(chan error, 4)
	for worker := range 4 {
		scrapers.Go(func() {
			for pass := range 4 {
				if _, _, err := fixture.VerifyHandler(handler, true); err != nil {
					errors <- fmt.Errorf("worker %d pass %d: %w", worker, pass, err)
					return
				}
			}
		})
	}
	scrapers.Wait()
	close(stop)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
