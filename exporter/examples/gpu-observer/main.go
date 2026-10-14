// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type observer struct {
	mu        sync.RWMutex
	cfg       config
	devices   map[deviceKey]observation
	lastPoll  time.Time
	sourceOK  bool
	inactive  *prometheus.Desc
	low       *prometheus.Desc
	health    *prometheus.Desc
	source    *prometheus.Desc
	window    *prometheus.Desc
	threshold *prometheus.Desc
}

type config struct {
	url, node, source, listen string
	poll, window, maxGap      time.Duration
	threshold                 float64
}

type deviceKey struct{ uuid, gi, ci, missingGPU string }

type sample struct {
	partition                      bool
	key                            deviceKey
	gpu, namespace, pod, container string
	value                          float64
	at                             time.Time
}

type observation struct {
	sample
	first, lastBusy, receivedAt time.Time
	reason                      string
}

var deviceLabels = []string{"node", "UUID", "gpu", "GPU_I_ID", "GPU_CI_ID", "source", "allocation", "reported_namespace", "reported_pod", "reported_container"}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config{}
	flag.StringVar(&cfg.url, "dcgm-url", "", "One node's direct DCGM /metrics URL; do not use a service balancing multiple nodes")
	flag.StringVar(&cfg.node, "node", "", "Verified Kubernetes node served by this DCGM endpoint")
	flag.StringVar(&cfg.source, "source", "dcgm", "Telemetry provenance: dcgm or synthetic")
	flag.StringVar(&cfg.listen, "listen-address", ":9091", "HTTP listen address")
	flag.DurationVar(&cfg.poll, "poll-interval", 10*time.Second, "Direct source polling interval")
	flag.DurationVar(&cfg.window, "window", 5*time.Minute, "Full uninterrupted observation window required for low activity")
	flag.DurationVar(&cfg.maxGap, "max-gap", 25*time.Second, "Maximum source sample age or gap")
	flag.Float64Var(&cfg.threshold, "threshold-ratio", 0.05, "Maximum GPU kernel utilization ratio considered low")
	flag.Parse()
	u, err := url.Parse(cfg.url)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || cfg.node == "" {
		return errors.New("dcgm-url must be HTTP(S) without credentials, and node is required")
	}
	if cfg.poll < time.Second || cfg.window < 2*cfg.poll || cfg.maxGap < 2*cfg.poll || math.IsNaN(cfg.threshold) || cfg.threshold < 0 || cfg.threshold > 1 || (cfg.source != "dcgm" && cfg.source != "synthetic") {
		return errors.New("invalid polling, window, freshness, threshold or provenance configuration")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := newObserver(cfg)
	registry := prometheus.NewRegistry()
	registry.MustRegister(o)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: cfg.listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	go func() {
		ticker := time.NewTicker(cfg.poll)
		defer ticker.Stop()
		for {
			samples, fetchErr := fetch(ctx, client, cfg.url)
			o.update(samples, fetchErr == nil, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	select {
	case err = <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve observer: %w", err)
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

func newObserver(cfg config) *observer {
	return &observer{
		cfg: cfg, devices: make(map[deviceKey]observation),
		inactive:  prometheus.NewDesc("gpu_observer_kernel_inactive_ratio", "Fraction of the source GPU-utilization sample without executing kernels; not scheduler availability or application idleness.", deviceLabels, nil),
		low:       prometheus.NewDesc("gpu_observer_low_activity", "One when every valid device GPU-utilization sample in the configured full window was at or below the threshold; omitted for incomplete windows or unknown observations.", deviceLabels, nil),
		health:    prometheus.NewDesc("gpu_observer_observation_healthy", "One for a fresh valid device observation; zero with a bounded reason when unknown. This does not certify workload attribution or hardware collector freshness.", append(append([]string{}, deviceLabels...), "reason"), nil),
		source:    prometheus.NewDesc("gpu_observer_source_healthy", "One when the latest direct source request succeeded with GPU samples and is within max-gap; not proof of hardware collector freshness.", []string{"node", "source"}, nil),
		window:    prometheus.NewDesc("gpu_observer_window_seconds", "Configured full observation window required for sustained device low activity.", []string{"node", "source"}, nil),
		threshold: prometheus.NewDesc("gpu_observer_threshold_ratio", "Configured maximum GPU kernel utilization ratio for low activity.", []string{"node", "source"}, nil),
	}
}

func fetch(ctx context.Context, client *http.Client, endpoint string) ([]sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source status %d", response.StatusCode)
	}
	const maxBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, errors.New("source response exceeds limit")
	}
	return parse(string(body), time.Now())
}

func parse(body string, now time.Time) ([]sample, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse source: %w", err)
	}
	family := families["DCGM_FI_DEV_GPU_UTIL"]
	if family == nil || len(family.Metric) == 0 {
		return nil, errors.New("source contains no GPU utilization samples")
	}
	samples := make([]sample, 0, len(family.Metric))
	for _, metric := range family.Metric {
		labels := make(map[string]string)
		for _, label := range metric.Label {
			labels[label.GetName()] = label.GetValue()
		}
		value := math.NaN()
		if metric.Gauge != nil {
			value = metric.Gauge.GetValue()
		}
		at := now
		if metric.TimestampMs != nil {
			at = time.UnixMilli(metric.GetTimestampMs())
		}
		samples = append(samples, sample{partition: labels["GPU_I_PROFILE"] != "", key: deviceKey{uuid: labels["UUID"], gi: labels["GPU_I_ID"], ci: labels["GPU_CI_ID"]}, gpu: labels["gpu"], namespace: labels["namespace"], pod: labels["pod"], container: labels["container"], value: value, at: at})
	}
	return samples, nil
}

func (o *observer) update(samples []sample, ok bool, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	const maxDevices = 1024
	keys := make(map[deviceKey]bool)
	for key, current := range o.devices {
		if now.Sub(current.receivedAt) > o.retention() {
			delete(o.devices, key)
		} else {
			keys[key] = true
		}
	}
	for _, s := range samples {
		key := s.key
		if key.uuid == "" {
			key.missingGPU = s.gpu
		}
		keys[key] = true
	}
	if len(keys) > maxDevices {
		ok = false
		samples = nil
	}
	o.lastPoll, o.sourceOK = now, ok && len(samples) > 0
	seen := make(map[deviceKey]bool)
	for _, s := range samples {
		key := s.key
		// Missing UUIDs remain unhealthy and cannot alias a valid physical device.
		if key.uuid == "" {
			key.missingGPU = s.gpu
		}
		old, exists := o.devices[key]
		if seen[key] {
			if old.sample != s {
				old.reason, old.first = "conflicting_sample", time.Time{}
				o.devices[key] = old
			}
			continue
		}
		seen[key] = true
		reason := "none"
		switch {
		case s.key.uuid == "" || math.IsNaN(s.value) || math.IsInf(s.value, 0) || s.value < 0 || s.value > 100:
			reason = "invalid_sample"
		case s.key.gi != "" || s.key.ci != "" || s.partition || strings.HasPrefix(s.key.uuid, "MIG-"):
			reason = "unsupported_partition"
		case now.Sub(s.at) > o.cfg.maxGap || s.at.After(now.Add(time.Second)):
			reason = "stale_sample"
		case exists && !s.at.After(old.at):
			reason = "repeated_sample"
		}
		current := observation{sample: s, receivedAt: now, first: old.first, lastBusy: old.lastBusy, reason: reason}
		if !exists || old.reason != "none" || s.at.Sub(old.at) > o.cfg.maxGap || old.gpu != s.gpu || old.namespace != s.namespace || old.pod != s.pod || old.container != s.container {
			current.first, current.lastBusy = s.at, time.Time{}
		}
		if reason != "none" {
			current.first = time.Time{}
		} else if s.value/100 > o.cfg.threshold {
			current.lastBusy = s.at
		}
		o.devices[key] = current
	}
	for key, current := range o.devices {
		if now.Sub(current.receivedAt) > o.retention() {
			delete(o.devices, key)
			continue
		}
		if !seen[key] || !o.sourceOK {
			current.reason, current.first = "missing_sample", time.Time{}
			if !o.sourceOK {
				current.reason = "source_unavailable"
			}
			o.devices[key] = current
		}
	}
}

func (o *observer) retention() time.Duration {
	return max(2*o.cfg.window, 5*time.Minute)
}

func (o *observer) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{o.inactive, o.low, o.health, o.source, o.window, o.threshold} {
		ch <- desc
	}
}

func (o *observer) Collect(ch chan<- prometheus.Metric) {
	o.collect(ch, time.Now())
}

func (o *observer) collect(ch chan<- prometheus.Metric, now time.Time) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	sourceOK := o.sourceOK && now.Sub(o.lastPoll) <= o.cfg.maxGap
	sourceValue := 0.0
	if sourceOK {
		sourceValue = 1
	}
	ch <- prometheus.MustNewConstMetric(o.source, prometheus.GaugeValue, sourceValue, o.cfg.node, o.cfg.source)
	ch <- prometheus.MustNewConstMetric(o.window, prometheus.GaugeValue, o.cfg.window.Seconds(), o.cfg.node, o.cfg.source)
	ch <- prometheus.MustNewConstMetric(o.threshold, prometheus.GaugeValue, o.cfg.threshold, o.cfg.node, o.cfg.source)
	for _, current := range o.devices {
		if now.Sub(current.receivedAt) > o.retention() {
			continue
		}
		reason := current.reason
		if !sourceOK {
			reason = "source_unavailable"
		} else if now.Sub(current.at) > o.cfg.maxGap {
			reason = "stale_sample"
		}
		allocation := "none_reported"
		if current.namespace != "" || current.pod != "" || current.container != "" {
			allocation = "reported"
		}
		if current.reason == "conflicting_sample" {
			allocation = "ambiguous"
		}
		labels := []string{o.cfg.node, current.key.uuid, current.gpu, current.key.gi, current.key.ci, o.cfg.source, allocation, current.namespace, current.pod, current.container}
		health := 0.0
		if reason == "none" {
			health = 1
		}
		ch <- prometheus.MustNewConstMetric(o.health, prometheus.GaugeValue, health, append(append([]string{}, labels...), reason)...)
		if health == 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(o.inactive, prometheus.GaugeValue, 1-current.value/100, labels...)
		if current.first.IsZero() || current.at.Sub(current.first) < o.cfg.window {
			continue
		}
		low := 0.0
		if current.lastBusy.IsZero() || current.at.Sub(current.lastBusy) >= o.cfg.window {
			low = 1
		}
		ch <- prometheus.MustNewConstMetric(o.low, prometheus.GaugeValue, low, labels...)
	}
}
