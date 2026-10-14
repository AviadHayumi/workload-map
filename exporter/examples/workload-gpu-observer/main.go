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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type observer struct {
	mu                                                                                         sync.RWMutex
	cfg                                                                                        config
	roots                                                                                      map[string]observation
	lastPoll                                                                                   time.Time
	sourceOK                                                                                   bool
	state, low, utilization, devices, observed, lowObserved, health, source, window, threshold *prometheus.Desc
}
type config struct {
	sourceURL, kartaURL, namespace, node, listen, demoConfigMap string
	poll, window, maxGap                                        time.Duration
	threshold                                                   float64
}
type identity struct{ namespace, workload, group, version, kind, uid string }
type podMapping struct {
	root     identity
	pod, uid string
}
type reading struct {
	root                identity
	fingerprint, reason string
	devices             int
	maximum             float64
	at                  time.Time
}
type observation struct {
	reading
	first, lowSince, receivedAt time.Time
}
type gpuSample struct {
	uuid, node, namespace, pod, uid, container string
	value                                      float64
	at                                         time.Time
}
type gpuRequest struct {
	pod         corev1.Pod
	counts      map[string]int
	unsupported bool
}

var rootLabels = []string{"namespace", "workload", "workload_group", "workload_version", "workload_kind", "workload_uid", "source"}

const maxObjects = 10000

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	cfg := config{}
	flag.StringVar(&cfg.sourceURL, "source-url", "", "UID-bearing synthetic DCGM source /metrics URL")
	flag.StringVar(&cfg.kartaURL, "karta-url", "", "Direct Karta exporter /metrics URL")
	flag.StringVar(&cfg.namespace, "namespace", "", "One explicitly selected demo workload namespace")
	flag.StringVar(&cfg.node, "node", "", "Verified single full-GPU source node")
	flag.StringVar(&cfg.demoConfigMap, "demo-configmap", "", "Optional named scenario metadata ConfigMap; never used for activity decisions")
	flag.StringVar(&cfg.listen, "listen-address", ":9092", "HTTP listen address")
	flag.DurationVar(&cfg.poll, "poll-interval", 10*time.Second, "Input polling interval")
	flag.DurationVar(&cfg.window, "window", 3*time.Minute, "Full sampled low-activity window")
	flag.DurationVar(&cfg.maxGap, "max-gap", 25*time.Second, "Maximum source age and observation gap")
	flag.Float64Var(&cfg.threshold, "threshold-ratio", 0.05, "Maximum utilization ratio for every assigned GPU")
	flag.Parse()
	for _, endpoint := range []string{cfg.sourceURL, cfg.kartaURL} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return errors.New("source-url and karta-url must be HTTP(S) without credentials")
		}
	}
	if cfg.namespace == "" || cfg.node == "" || cfg.poll < time.Second || cfg.window < 2*cfg.poll || cfg.maxGap < 2*cfg.poll || math.IsNaN(cfg.threshold) || cfg.threshold < 0 || cfg.threshold > 1 {
		return errors.New("invalid namespace, node, window, polling or threshold configuration")
	}
	kcfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	kcfg.Timeout = 5 * time.Second
	kube, err := kubernetes.NewForConfig(kcfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := newObserver(cfg)
	registry := prometheus.NewRegistry()
	registry.MustRegister(o)
	metadata := newDemoMetadata(cfg.namespace, cfg.maxGap)
	if cfg.demoConfigMap != "" {
		registry.MustRegister(metadata)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: cfg.listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	go func() {
		ticker := time.NewTicker(cfg.poll)
		defer ticker.Stop()
		for {
			if cfg.demoConfigMap != "" {
				metadata.poll(ctx, kube, cfg.demoConfigMap)
			}
			values, pollErr := poll(ctx, kube, client, cfg)
			o.update(values, pollErr == nil, time.Now())
			if pollErr != nil {
				log.Printf("observation unavailable: %v", pollErr)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	select {
	case err = <-errs:
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
	desc := func(name, help string, extra ...string) *prometheus.Desc {
		return prometheus.NewDesc("workload_gpu_observer_"+name, help, append(slices.Clone(rootLabels), extra...), nil)
	}
	return &observer{cfg: cfg, roots: make(map[string]observation),
		state:       desc("state", "Current sampled GPU activity classification; not application productivity or reclaimable capacity.", "state", "reason"),
		low:         desc("low_activity", "One when all assigned full GPUs remained at or below the threshold throughout a complete sampled window; omitted without complete evidence."),
		utilization: desc("utilization_max_ratio", "Maximum fresh utilization ratio across every verified assigned full GPU."),
		devices:     desc("assigned_devices", "Number of source devices matching the complete current per-container GPU request inventory."),
		observed:    desc("observation_seconds", "Seconds of uninterrupted valid inventory and source observations."),
		lowObserved: desc("low_activity_observation_seconds", "Seconds of uninterrupted samples at or below the threshold for every assigned full GPU."),
		health:      desc("observation_healthy", "One when current workload identity, GPU inventory and source samples are complete and verified.", "reason"),
		source:      prometheus.NewDesc("workload_gpu_observer_source_healthy", "One for a successful fresh input poll.", []string{"source"}, nil),
		window:      prometheus.NewDesc("workload_gpu_observer_window_seconds", "Required complete sampled low-activity window.", []string{"source"}, nil),
		threshold:   prometheus.NewDesc("workload_gpu_observer_threshold_ratio", "Maximum utilization ratio allowed on every assigned full GPU.", []string{"source"}, nil)}
}
func poll(ctx context.Context, kube kubernetes.Interface, client *http.Client, cfg config) ([]reading, error) {
	var pods []corev1.Pod
	continuation := ""
	for {
		list, err := kube.CoreV1().Pods(cfg.namespace).List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil {
			return nil, fmt.Errorf("list Pods: %w", err)
		}
		pods = append(pods, list.Items...)
		if len(pods) > maxObjects {
			return nil, errors.New("pod inventory limit exceeded")
		}
		continuation = list.Continue
		if continuation == "" {
			break
		}
	}
	karta, err := fetch(ctx, client, cfg.kartaURL)
	if err != nil {
		return nil, fmt.Errorf("read Karta: %w", err)
	}
	source, err := fetch(ctx, client, cfg.sourceURL)
	if err != nil {
		return nil, fmt.Errorf("read GPU source: %w", err)
	}
	return evaluate(cfg, pods, karta, source, time.Now())
}
func fetch(ctx context.Context, client *http.Client, endpoint string) (map[string]*dto.MetricFamily, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source status %d", res.StatusCode)
	}
	const maxBytes = 32 << 20
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, errors.New("source response exceeds limit")
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	return parser.TextToMetricFamilies(strings.NewReader(string(body)))
}
func labelsOf(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.Label))
	for _, label := range metric.Label {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
func rootOf(labels map[string]string, health bool) identity {
	group, version, kind := "workload_group", "workload_version", "workload_kind"
	if health {
		group, version, kind = "group", "version", "kind"
	}
	return identity{labels["namespace"], labels["workload"], labels[group], labels[version], labels[kind], labels["workload_uid"]}
}
func (i identity) key() string { return i.namespace + "/" + i.uid }
func (i identity) values() []string {
	return []string{i.namespace, i.workload, i.group, i.version, i.kind, i.uid, "synthetic"}
}
func samplesOf(source map[string]*dto.MetricFamily, cfg config, now time.Time) ([]gpuSample, error) {
	health := source["fake_gpu_source_healthy"]
	if health == nil || len(health.Metric) != 1 || health.Metric[0].GetGauge().GetValue() != 1 || labelsOf(health.Metric[0])["node"] != cfg.node {
		return nil, errors.New("synthetic source unhealthy or wrong node")
	}
	timestamps := make(map[string]float64)
	timestampLabels := make(map[string]map[string]string)
	for _, m := range source["fake_gpu_source_sample_timestamp_seconds"].GetMetric() {
		l := labelsOf(m)
		key := l["node"] + "/" + l["UUID"]
		if _, exists := timestamps[key]; exists {
			return nil, errors.New("conflicting source timestamps")
		}
		timestamps[key] = m.GetGauge().GetValue()
		timestampLabels[key] = l
	}
	var samples []gpuSample
	seen := make(map[string]bool)
	for _, m := range source["DCGM_FI_DEV_GPU_UTIL"].GetMetric() {
		l := labelsOf(m)
		key := l["node"] + "/" + l["UUID"]
		if seen[key] || l["UUID"] == "" || l["node"] != cfg.node || l["source"] != "synthetic" || strings.HasPrefix(l["UUID"], "MIG-") || l["GPU_I_ID"] != "" || l["GPU_CI_ID"] != "" || l["GPU_I_PROFILE"] != "" {
			return nil, errors.New("ambiguous or unsupported source device")
		}
		seen[key] = true
		for _, name := range []string{"node", "UUID", "namespace", "pod", "pod_uid", "container", "source"} {
			if timestampLabels[key][name] != l[name] {
				return nil, errors.New("timestamp allocation does not match utilization")
			}
		}
		value := m.GetGauge().GetValue()
		stamp, exists := timestamps[key]
		if m.Gauge == nil || !exists || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 || math.IsNaN(stamp) || math.IsInf(stamp, 0) {
			return nil, errors.New("invalid source sample")
		}
		at := time.Unix(0, int64(stamp*1e9))
		if now.Sub(at) > cfg.maxGap || at.After(now.Add(time.Second)) {
			return nil, errors.New("stale source sample")
		}
		samples = append(samples, gpuSample{l["UUID"], l["node"], l["namespace"], l["pod"], l["pod_uid"], l["container"], value / 100, at})
	}
	if len(samples) == 0 || len(samples) > 1024 {
		return nil, errors.New("empty or excessive source inventory")
	}
	return samples, nil
}
func requestsFor(pod corev1.Pod) gpuRequest {
	r := gpuRequest{pod: pod, counts: make(map[string]int)}
	for _, c := range append(slices.Clone(pod.Spec.Containers), pod.Spec.InitContainers...) {
		for name, q := range c.Resources.Limits {
			if strings.HasPrefix(string(name), "nvidia.com/") && name != "nvidia.com/gpu" && !q.IsZero() {
				r.unsupported = true
			}
		}
		q := c.Resources.Limits["nvidia.com/gpu"]
		count, exact := q.AsInt64()
		if count > 0 {
			r.counts[c.Name] = int(count)
		}
		requested := c.Resources.Requests["nvidia.com/gpu"]
		if !exact || count < 0 || count > 1024 || q.Cmp(requested) != 0 || (count > 0 && slices.ContainsFunc(pod.Spec.InitContainers, func(init corev1.Container) bool { return init.Name == c.Name })) {
			r.unsupported = true
		}
	}
	if pod.Labels["runai-gpu-group"] != "" || len(pod.Spec.ResourceClaims) > 0 {
		r.unsupported = true
	}
	// Reservation and fractional allocations require an independent sharing model.
	for key := range pod.Annotations {
		if key == "gpu-fraction" || key == "runai-gpu" || key == "run.ai/reserve_for_gpu_index" {
			r.unsupported = true
		}
	}
	return r
}
func evaluate(cfg config, pods []corev1.Pod, karta, source map[string]*dto.MetricFamily, now time.Time) ([]reading, error) {
	roots := make(map[string]reading)
	for _, m := range karta["karta_exporter_observation_healthy"].GetMetric() {
		l := labelsOf(m)
		if l["scope"] != "workload" || l["namespace"] != cfg.namespace {
			continue
		}
		root := rootOf(l, true)
		if root.uid == "" || root.workload == "" || root.kind == "" {
			return nil, errors.New("invalid Karta root identity")
		}
		r := reading{root: root, reason: "none", at: now}
		if m.Gauge == nil || m.GetGauge().GetValue() != 1 || l["reason"] != "none" {
			r.reason = "karta_unhealthy"
		}
		if old, exists := roots[root.key()]; exists && (old.root != root || old.reason != "none") {
			r.reason = "karta_unhealthy"
		}
		roots[root.key()] = r
	}
	if len(roots) == 0 || len(roots) > maxObjects {
		return nil, errors.New("empty or excessive Karta workload inventory")
	}
	mappings := make(map[string]podMapping)
	for _, m := range karta["karta_pod_workload_info"].GetMetric() {
		l := labelsOf(m)
		if l["namespace"] != cfg.namespace {
			continue
		}
		p := podMapping{root: rootOf(l, false), pod: l["pod"], uid: l["pod_uid"]}
		if p.uid == "" || p.pod == "" || m.GetGauge().GetValue() != 1 || l["owner_resolution"] != "resolved" {
			return nil, errors.New("invalid Karta Pod mapping")
		}
		if old, exists := mappings[p.uid]; exists && old != p {
			return nil, errors.New("conflicting Karta Pod mapping")
		}
		mappings[p.uid] = p
	}
	requests := make(map[string]gpuRequest)
	expected := make(map[string]map[string]int)
	fingerprints := make(map[string][]string)
	namespaceReason := ""
	for _, pod := range pods {
		if pod.Namespace != cfg.namespace {
			return nil, errors.New("pod inventory outside selected namespace")
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		r := requestsFor(pod)
		if len(r.counts) == 0 && !r.unsupported {
			continue
		}
		uid := string(pod.UID)
		mapping, exists := mappings[uid]
		root, rootExists := roots[mapping.root.key()]
		if !exists || !rootExists || mapping.pod != pod.Name || root.root != mapping.root {
			namespaceReason = "incomplete_mapping"
			continue
		}
		requests[uid] = r
		if r.unsupported {
			root.reason = "unsupported_allocation"
		} else if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName != cfg.node {
			root.reason = "allocation_pending"
		}
		roots[root.root.key()] = root
		if expected[root.root.key()] == nil {
			expected[root.root.key()] = make(map[string]int)
		}
		for container, count := range r.counts {
			expected[root.root.key()][uid+"/"+container] = count
			fingerprints[root.root.key()] = append(fingerprints[root.root.key()], uid+"/"+container+"/"+strconv.Itoa(count))
		}
	}
	samples, sourceErr := samplesOf(source, cfg, now)
	if sourceErr != nil {
		namespaceReason = "source_unavailable"
	}
	actual := make(map[string]map[string]int)
	for _, s := range samples {
		if s.namespace != cfg.namespace {
			continue
		}
		r, exists := requests[s.uid]
		mapping := mappings[s.uid]
		if !exists || s.uid == "" || s.pod != r.pod.Name || s.node != r.pod.Spec.NodeName || r.counts[s.container] == 0 {
			namespaceReason = "allocation_mismatch"
			continue
		}
		key := mapping.root.key()
		root := roots[key]
		if actual[key] == nil {
			actual[key] = make(map[string]int)
		}
		actual[key][s.uid+"/"+s.container]++
		root.devices++
		root.maximum = math.Max(root.maximum, s.value)
		if s.at.Before(root.at) {
			root.at = s.at
		}
		roots[key] = root
		fingerprints[key] = append(fingerprints[key], s.uid+"/"+s.container+"/"+s.node+"/"+s.uuid)
	}
	var readings []reading
	for key, root := range roots {
		if namespaceReason != "" {
			root.reason = namespaceReason
		}
		if root.reason == "none" {
			if len(expected[key]) == 0 {
				root.reason = "no_gpu"
			} else {
				for container, count := range expected[key] {
					if actual[key][container] != count {
						root.reason = "incomplete_inventory"
					}
				}
			}
		}
		slices.Sort(fingerprints[key])
		root.fingerprint = strings.Join(append(root.root.values(), fingerprints[key]...), "\x00")
		readings = append(readings, root)
	}
	return readings, sourceErr
}
func (o *observer) update(readings []reading, ok bool, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastPoll, o.sourceOK = now, ok
	if !ok && len(readings) == 0 {
		for key, current := range o.roots {
			if now.Sub(current.receivedAt) > 10*time.Minute {
				delete(o.roots, key)
				continue
			}
			current.reason = "input_unavailable"
			current.first, current.lowSince = time.Time{}, time.Time{}
			o.roots[key] = current
		}
		return
	}
	next := make(map[string]observation, len(readings))
	for _, r := range readings {
		old := o.roots[r.root.key()]
		current := observation{reading: r, first: old.first, lowSince: old.lowSince, receivedAt: now}
		if r.reason == "none" && !old.at.IsZero() && !r.at.After(old.at) {
			current.reason = "repeated_sample"
		}
		if current.reason != "none" {
			current.first, current.lowSince = time.Time{}, time.Time{}
		} else {
			if old.reason != "none" || old.fingerprint != r.fingerprint || r.at.Sub(old.at) > o.cfg.maxGap || now.Sub(old.receivedAt) > o.cfg.maxGap || current.first.IsZero() {
				current.first = r.at
				current.lowSince = time.Time{}
			}
			if r.maximum > o.cfg.threshold {
				current.lowSince = time.Time{}
			} else if current.lowSince.IsZero() {
				current.lowSince = r.at
			}
		}
		next[r.root.key()] = current
	}
	o.roots = next
}
func (o *observer) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{o.state, o.low, o.utilization, o.devices, o.observed, o.lowObserved, o.health, o.source, o.window, o.threshold} {
		ch <- d
	}
}
func (o *observer) Collect(ch chan<- prometheus.Metric) {
	o.collectAt(ch, time.Now())
}
func (o *observer) collectAt(ch chan<- prometheus.Metric, now time.Time) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	fresh := o.sourceOK && now.Sub(o.lastPoll) <= o.cfg.maxGap
	sourceHealthy := 0.0
	if fresh {
		sourceHealthy = 1
	}
	ch <- prometheus.MustNewConstMetric(o.source, prometheus.GaugeValue, sourceHealthy, "synthetic")
	ch <- prometheus.MustNewConstMetric(o.window, prometheus.GaugeValue, o.cfg.window.Seconds(), "synthetic")
	ch <- prometheus.MustNewConstMetric(o.threshold, prometheus.GaugeValue, o.cfg.threshold, "synthetic")
	for _, current := range o.roots {
		values := current.root.values()
		reason := current.reason
		if now.Sub(o.lastPoll) > o.cfg.maxGap || now.Sub(current.at) > o.cfg.maxGap {
			reason = "stale_observation"
		}
		state := "unknown"
		healthy := 0.0
		if reason == "no_gpu" {
			state = "no_gpu"
		}
		if reason == "none" {
			healthy = 1
			state = "warming"
			observed := current.at.Sub(current.first).Seconds()
			lowSeconds := 0.0
			if !current.lowSince.IsZero() {
				lowSeconds = current.at.Sub(current.lowSince).Seconds()
			}
			if current.maximum > o.cfg.threshold {
				state = "active"
			} else if lowSeconds >= o.cfg.window.Seconds() {
				state = "low_activity"
			}
			for _, metric := range []struct {
				desc  *prometheus.Desc
				value float64
			}{{o.utilization, current.maximum}, {o.devices, float64(current.devices)}, {o.observed, observed}, {o.lowObserved, lowSeconds}} {
				ch <- prometheus.MustNewConstMetric(metric.desc, prometheus.GaugeValue, metric.value, values...)
			}
			if observed >= o.cfg.window.Seconds() {
				low := 0.0
				if state == "low_activity" {
					low = 1
				}
				ch <- prometheus.MustNewConstMetric(o.low, prometheus.GaugeValue, low, values...)
			}
		}
		ch <- prometheus.MustNewConstMetric(o.state, prometheus.GaugeValue, 1, append(slices.Clone(values), state, reason)...)
		ch <- prometheus.MustNewConstMetric(o.health, prometheus.GaugeValue, healthy, append(slices.Clone(values), reason)...)
	}
}
