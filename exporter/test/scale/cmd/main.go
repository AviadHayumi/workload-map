// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/telemetry"
	"github.com/dsx-ai-factory/workload-map/exporter/test/scale"
)

type report struct {
	Scenario                scale.Case   `json:"scenario"`
	GoVersion               string       `json:"go_version"`
	Platform                string       `json:"platform"`
	GOMAXPROCS              int          `json:"gomaxprocs"`
	Samples                 scale.Counts `json:"samples"`
	ExpositionBytes         int64        `json:"exposition_bytes"`
	FixtureBuildMS          float64      `json:"fixture_build_ms"`
	VerificationMS          float64      `json:"verification_ms"`
	ScrapeMS                []float64    `json:"scrape_ms"`
	AllocatedBytesPerScrape uint64       `json:"allocated_bytes_per_scrape"`
	AllocationsPerScrape    uint64       `json:"allocations_per_scrape"`
	GCCyclesDuringScrapes   uint32       `json:"gc_cycles_during_scrapes"`
	RetainedHeapBytes       uint64       `json:"retained_heap_bytes"`
	ProcessPeakRSSBytes     uint64       `json:"process_peak_rss_bytes"`
	PeakRSSAvailable        bool         `json:"peak_rss_available"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	workloads := flag.Int("workloads", 1000, "synthetic roots: 1000, 10000, or 100000")
	pods := flag.Int("pods", 1, "Pods per root: 0, 1, or 10")
	scrapes := flag.Int("scrapes", 3, "timed uncompressed HTTP handler scrapes after full oracle verification")
	profileDir := flag.String("profile-dir", "", "optional directory for CPU and before/after allocation profiles")
	flag.Parse()
	cases, err := scale.Cases(fmt.Sprint(*workloads), fmt.Sprint(*pods))
	if err != nil {
		return err
	}
	if *scrapes < 1 {
		return fmt.Errorf("scrapes must be positive")
	}
	r := report{Scenario: cases[0], GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, GOMAXPROCS: runtime.GOMAXPROCS(0)}
	started := time.Now()
	fixture := scale.New(cases[0])
	r.FixtureBuildMS = float64(time.Since(started)) / float64(time.Millisecond)
	registry := prometheus.NewRegistry()
	provider, err := telemetry.NewPrometheusProvider(registry)
	if err != nil {
		return err
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()
	registration, err := collector.New(provider.Meter("scale"), fixture.Store, collector.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = registration.Unregister() }()
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	started = time.Now()
	r.Samples, r.ExpositionBytes, err = fixture.VerifyHandler(handler, false)
	if err != nil {
		return fmt.Errorf("independent exposition verification: %w", err)
	}
	r.VerificationMS = float64(time.Since(started)) / float64(time.Millisecond)
	runtime.GC()
	if *profileDir != "" {
		if err := os.MkdirAll(*profileDir, 0o755); err != nil {
			return err
		}
		if err := writeHeapProfile(filepath.Join(*profileDir, "before.pb.gz")); err != nil {
			return err
		}
		cpu, err := os.Create(filepath.Join(*profileDir, "cpu.pb.gz"))
		if err != nil {
			return err
		}
		defer func() { _ = cpu.Close() }()
		if err := pprof.StartCPUProfile(cpu); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range *scrapes {
		response := &scale.Response{}
		started = time.Now()
		handler.ServeHTTP(response, scale.Request())
		r.ScrapeMS = append(r.ScrapeMS, float64(time.Since(started))/float64(time.Millisecond))
		if response.Code != 200 || response.Bytes != r.ExpositionBytes {
			return fmt.Errorf("scrape returned HTTP %d, bytes=%d, want %d", response.Code, response.Bytes, r.ExpositionBytes)
		}
	}
	if *profileDir != "" {
		pprof.StopCPUProfile()
	}
	runtime.ReadMemStats(&after)
	r.AllocatedBytesPerScrape = (after.TotalAlloc - before.TotalAlloc) / uint64(*scrapes)
	r.AllocationsPerScrape = (after.Mallocs - before.Mallocs) / uint64(*scrapes)
	r.GCCyclesDuringScrapes = after.NumGC - before.NumGC
	runtime.GC()
	runtime.ReadMemStats(&after)
	if *profileDir != "" {
		if err := writeHeapProfile(filepath.Join(*profileDir, "after.pb.gz")); err != nil {
			return err
		}
	}
	r.RetainedHeapBytes = after.HeapAlloc
	r.ProcessPeakRSSBytes, r.PeakRSSAvailable = peakRSS()
	runtime.KeepAlive(fixture)
	runtime.KeepAlive(handler)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(r)
}

func writeHeapProfile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := pprof.WriteHeapProfile(file); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
