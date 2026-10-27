// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Command scale-exporter wraps the production application with loopback test diagnostics.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/app"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/config"
)

type queueMetric struct {
	mu    sync.Mutex
	Value float64 `json:"value"`
	Count uint64  `json:"count"`
	Sum   float64 `json:"sum"`
	Max   float64 `json:"max"`
}

type queueProvider struct {
	mu      sync.Mutex
	metrics map[string]*queueMetric
}

type diagnostic struct {
	application  *app.Application
	queues       *queueProvider
	baselineHeap uint64
}

type stats struct {
	BaselineHeap uint64                        `json:"baseline_heap_bytes"`
	HeapAlloc    uint64                        `json:"heap_alloc_bytes"`
	TotalAlloc   uint64                        `json:"total_alloc_bytes"`
	HeapObjects  uint64                        `json:"heap_objects"`
	NumGC        uint32                        `json:"gc_count"`
	Goroutines   int                           `json:"goroutines"`
	Queues       map[string]map[string]float64 `json:"queues"`
}

type gcSnapshot struct {
	stats
	GCStartedAt        time.Time `json:"gc_started_at"`
	GCCompletedAt      time.Time `json:"gc_completed_at"`
	SnapshotAt         time.Time `json:"snapshot_at"`
	ProfileStartedAt   time.Time `json:"profile_started_at,omitempty"`
	ProfileCompletedAt time.Time `json:"profile_completed_at,omitempty"`
	HeapProfile        []byte    `json:"heap_profile_base64,omitempty"`
}

func (m *queueMetric) Inc()          { m.mu.Lock(); m.Value++; m.mu.Unlock() }
func (m *queueMetric) Dec()          { m.mu.Lock(); m.Value--; m.mu.Unlock() }
func (m *queueMetric) Set(v float64) { m.mu.Lock(); m.Value = v; m.mu.Unlock() }
func (m *queueMetric) Observe(v float64) {
	m.mu.Lock()
	m.Count++
	m.Sum += v
	m.Max = max(m.Max, v)
	m.mu.Unlock()
}
func (p *queueProvider) metric(name, kind string) *queueMetric {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := name + "." + kind
	if p.metrics[key] == nil {
		p.metrics[key] = &queueMetric{}
	}
	return p.metrics[key]
}
func (p *queueProvider) NewDepthMetric(n string) workqueue.GaugeMetric  { return p.metric(n, "depth") }
func (p *queueProvider) NewAddsMetric(n string) workqueue.CounterMetric { return p.metric(n, "adds") }
func (p *queueProvider) NewLatencyMetric(n string) workqueue.HistogramMetric {
	return p.metric(n, "queue_latency_seconds")
}
func (p *queueProvider) NewWorkDurationMetric(n string) workqueue.HistogramMetric {
	return p.metric(n, "work_duration_seconds")
}
func (p *queueProvider) NewUnfinishedWorkSecondsMetric(n string) workqueue.SettableGaugeMetric {
	return p.metric(n, "unfinished_work_seconds")
}
func (p *queueProvider) NewLongestRunningProcessorSecondsMetric(n string) workqueue.SettableGaugeMetric {
	return p.metric(n, "longest_running_seconds")
}
func (p *queueProvider) NewRetriesMetric(n string) workqueue.CounterMetric {
	return p.metric(n, "retries")
}

func (d diagnostic) stats() stats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	result := stats{BaselineHeap: d.baselineHeap, HeapAlloc: m.HeapAlloc, TotalAlloc: m.TotalAlloc, HeapObjects: m.HeapObjects, NumGC: m.NumGC, Goroutines: runtime.NumGoroutine(), Queues: map[string]map[string]float64{}}
	d.queues.mu.Lock()
	defer d.queues.mu.Unlock()
	for name, metric := range d.queues.metrics {
		metric.mu.Lock()
		result.Queues[name] = map[string]float64{"value": metric.Value, "count": float64(metric.Count), "sum": metric.Sum, "max": metric.Max}
		metric.mu.Unlock()
	}
	return result
}

func (d diagnostic) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, d.stats()) })
	mux.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		result := gcSnapshot{GCStartedAt: time.Now()}
		runtime.GC()
		result.GCCompletedAt = time.Now()
		result.stats = d.stats()
		result.SnapshotAt = time.Now()
		if r.URL.Query().Get("heap") == "1" {
			result.ProfileStartedAt = time.Now()
			var profile bytes.Buffer
			if err := pprof.Lookup("heap").WriteTo(&profile, 0); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			result.HeapProfile = profile.Bytes()
			result.ProfileCompletedAt = time.Now()
		}
		writeJSON(w, result)
	})
	mux.HandleFunc("/publications", func(w http.ResponseWriter, r *http.Request) {
		uids := r.URL.Query()["uid"]
		if len(uids) > 100 {
			http.Error(w, "at most 100 UIDs", http.StatusBadRequest)
			return
		}
		requested := make(map[types.UID]bool, len(uids))
		for _, uid := range uids {
			requested[types.UID(uid)] = true
		}
		view := d.application.Store.Capture()
		result := map[string]any{}
		for _, p := range view.Records {
			if !requested[p.Workload.UID] {
				continue
			}
			creationValid, _ := view.Valid(p.Creation)
			statusValid := true
			if p.Workload.HasStatus {
				statusValid, _ = view.Valid(p.Status)
			}
			valid := creationValid && statusValid && len(p.FailureReasons) == 0
			pods := map[string]any{}
			for _, page := range p.PodPages {
				for _, pod := range page.Rows() {
					mappingValid, reason := view.Valid(pod.Mapping)
					_, duplicate := pods[string(pod.Record.UID)]
					valid = valid && mappingValid && !duplicate
					pods[string(pod.Record.UID)] = map[string]any{"valid": mappingValid, "reason": reason, "record": pod.Record}
				}
			}
			result[string(p.Workload.UID)] = map[string]any{"resource_version": p.Workload.Ref.ResourceVersion, "valid": valid, "creation_valid": creationValid, "status_valid": statusValid, "workload": p.Workload, "pods": pods, "captured_at": view.At}
		}
		writeJSON(w, result)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func run() error {
	fs := flag.NewFlagSet("scale-exporter", flag.ContinueOnError)
	kube := fs.String("kubeconfig", "", "Kubeconfig for loopback fixture")
	address := fs.String("metrics-bind-address", "127.0.0.1:8080", "Production metrics listener")
	diagnostics := fs.String("diagnostics-bind-address", "127.0.0.1:8081", "Loopback test diagnostics listener")
	loader := config.NewLoader(fs, os.LookupEnv)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*diagnostics)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("diagnostics must bind a loopback IP")
	}
	cfg, err := loader.Load()
	if err != nil {
		return err
	}
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", *kube)
	if err != nil {
		return err
	}
	provider := &queueProvider{metrics: map[string]*queueMetric{}}
	workqueue.SetProvider(provider)
	application, err := app.New(cfg, kubeConfig)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *diagnostics)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	server := &http.Server{Handler: diagnostic{application: application, queues: provider, baselineHeap: baseline.HeapAlloc}.handler(), ReadHeaderTimeout: 5 * time.Second}
	failures := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if !errors.Is(err, http.ErrServerClosed) {
			failures <- err
			cancel()
		}
	}()
	err = application.Serve(ctx, *address)
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	err = errors.Join(err, server.Shutdown(shutdownCtx))
	select {
	case failure := <-failures:
		err = errors.Join(err, failure)
	default:
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
