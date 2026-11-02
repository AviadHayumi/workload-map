// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type timedCollector struct {
	*observer
	now time.Time
}

func (c timedCollector) Collect(ch chan<- prometheus.Metric) { c.collect(ch, c.now) }

func newTestObserver() *observer {
	return newObserver(config{node: "gpu-node", source: "synthetic", poll: 10 * time.Second, window: time.Minute, maxGap: 25 * time.Second, threshold: 0.05})
}

func testSample(at time.Time, value float64) sample {
	return sample{key: deviceKey{uuid: "GPU-example"}, gpu: "0", value: value, at: at}
}

func metricsAt(t *testing.T, o *observer, now time.Time) map[string][]*dto.Metric {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(timedCollector{o, now})
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]*dto.Metric)
	for _, f := range families {
		result[f.GetName()] = f.Metric
	}
	return result
}

func metricLabel(m *dto.Metric, name string) string {
	for _, l := range m.Label {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func TestWindowBusySamplesAndSourceGap(t *testing.T) {
	o := newTestObserver()
	start := time.Unix(1000, 0)
	for step := 0; step <= 6; step++ {
		now := start.Add(time.Duration(step) * 10 * time.Second)
		o.update([]sample{testSample(now, 5)}, true, now)
		m := metricsAt(t, o, now)
		if step < 6 && len(m["gpu_observer_low_activity"]) != 0 {
			t.Fatal("partial window emitted low activity")
		}
	}
	m := metricsAt(t, o, start.Add(time.Minute))
	if m["gpu_observer_low_activity"][0].GetGauge().GetValue() != 1 {
		t.Fatal("full low window not classified")
	}
	if m["gpu_observer_kernel_inactive_ratio"][0].GetGauge().GetValue() != .95 {
		t.Fatal("incorrect complement")
	}
	now := start.Add(70 * time.Second)
	o.update([]sample{testSample(now, 20)}, true, now)
	if metricsAt(t, o, now)["gpu_observer_low_activity"][0].GetGauge().GetValue() != 0 {
		t.Fatal("busy sample classified low")
	}
	now = start.Add(80 * time.Second)
	o.update(nil, false, now)
	m = metricsAt(t, o, now)
	if len(m["gpu_observer_low_activity"]) != 0 || len(m["gpu_observer_kernel_inactive_ratio"]) != 0 || m["gpu_observer_observation_healthy"][0].GetGauge().GetValue() != 0 {
		t.Fatal("failed source retained inferred activity")
	}
	for step := 9; step <= 14; step++ {
		now = start.Add(time.Duration(step) * 10 * time.Second)
		o.update([]sample{testSample(now, 0)}, true, now)
	}
	if len(metricsAt(t, o, now)["gpu_observer_low_activity"]) != 0 {
		t.Fatal("source gap did not reset window")
	}
	now = start.Add(150 * time.Second)
	o.update([]sample{testSample(now, 0)}, true, now)
	if metricsAt(t, o, now)["gpu_observer_low_activity"][0].GetGauge().GetValue() != 1 {
		t.Fatal("fresh full window did not recover")
	}
}

func TestInvalidConflictingAndPartitionSamples(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, value := range []float64{-1, 101, 9223372036854775794, math.NaN(), math.Inf(1)} {
		o := newTestObserver()
		o.update([]sample{testSample(now, value)}, true, now)
		m := metricsAt(t, o, now)
		if len(m["gpu_observer_kernel_inactive_ratio"]) != 0 || metricLabel(m["gpu_observer_observation_healthy"][0], "reason") != "invalid_sample" {
			t.Fatalf("accepted invalid value %v", value)
		}
	}
	o := newTestObserver()
	a := testSample(now, 0)
	b := a
	o.update([]sample{a, b}, true, now)
	if len(metricsAt(t, o, now)["gpu_observer_kernel_inactive_ratio"]) != 1 {
		t.Fatal("identical duplicate not deduplicated")
	}
	b.pod, b.namespace = "different-pod", "demo"
	o = newTestObserver()
	o.update([]sample{a, b}, true, now)
	m := metricsAt(t, o, now)
	if len(m["gpu_observer_kernel_inactive_ratio"]) != 0 || metricLabel(m["gpu_observer_observation_healthy"][0], "allocation") != "ambiguous" {
		t.Fatal("conflicting assignment accepted")
	}
	a.key.gi, b.key.gi = "0", "1"
	o = newTestObserver()
	o.update([]sample{a, b}, true, now)
	m = metricsAt(t, o, now)
	if len(m["gpu_observer_observation_healthy"]) != 2 || len(m["gpu_observer_kernel_inactive_ratio"]) != 0 {
		t.Fatal("partition identities collapsed or unsupported utilization inferred")
	}
}

func TestFreshnessAssignmentResetAndBoundedRetention(t *testing.T) {
	o := newTestObserver()
	start := time.Unix(1000, 0)
	for step := 0; step <= 6; step++ {
		now := start.Add(time.Duration(step) * 10 * time.Second)
		o.update([]sample{testSample(now, 0)}, true, now)
	}
	now := start.Add(70 * time.Second)
	s := testSample(now, 0)
	s.namespace, s.pod = "demo", "reused-name"
	o.update([]sample{s}, true, now)
	m := metricsAt(t, o, now)
	if len(m["gpu_observer_low_activity"]) != 0 {
		t.Fatal("reported assignment change inherited low window")
	}
	if metricLabel(m["gpu_observer_kernel_inactive_ratio"][0], "pod_uid") != "" || metricLabel(m["gpu_observer_kernel_inactive_ratio"][0], "reported_pod") != "reused-name" {
		t.Fatal("unverified assignment presented as UID identity")
	}
	o.update([]sample{s}, true, now.Add(10*time.Second))
	m = metricsAt(t, o, now.Add(10*time.Second))
	if len(m["gpu_observer_kernel_inactive_ratio"]) != 0 || metricLabel(m["gpu_observer_observation_healthy"][0], "reason") != "repeated_sample" {
		t.Fatal("repeated source timestamp refreshed data")
	}
	m = metricsAt(t, o, now.Add(40*time.Second))
	if len(m["gpu_observer_kernel_inactive_ratio"]) != 0 {
		t.Fatal("stale data remained valid")
	}
	o.update(nil, false, now.Add(6*time.Minute))
	m = metricsAt(t, o, now.Add(6*time.Minute))
	if len(o.devices) != 0 || len(m["gpu_observer_observation_healthy"]) != 0 || m["gpu_observer_source_healthy"][0].GetGauge().GetValue() != 0 {
		t.Fatal("failed source identities did not expire")
	}
}

func TestMissingSampleAndLongPollingGapResetWindow(t *testing.T) {
	o := newTestObserver()
	start := time.Unix(1000, 0)
	a := testSample(start, 0)
	b := a
	b.key.uuid = "GPU-other"
	o.update([]sample{a, b}, true, start)
	b.at = start.Add(10 * time.Second)
	o.update([]sample{b}, true, b.at)
	m := metricsAt(t, o, b.at)
	if len(m["gpu_observer_kernel_inactive_ratio"]) != 1 || len(m["gpu_observer_observation_healthy"]) != 2 {
		t.Fatal("missing device was interpreted as zero or forgotten immediately")
	}
	a.at = start.Add(time.Minute)
	o.update([]sample{a}, true, a.at)
	if len(metricsAt(t, o, a.at)["gpu_observer_low_activity"]) != 0 {
		t.Fatal("long polling gap advanced low window")
	}
}

func TestFetchAndParse(t *testing.T) {
	valid := "# TYPE DCGM_FI_DEV_GPU_UTIL gauge\nDCGM_FI_DEV_GPU_UTIL{UUID=\"GPU-example\",gpu=\"0\",Hostname=\"exporter-pod\",namespace=\"demo\",pod=\"training\"} 25\n"
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"valid", valid, 200, false}, {"missing", "other_metric 0\n", 200, true}, {"malformed", "bad{", 200, true}, {"http", valid, 500, true}, {"oversized", strings.Repeat("#", (4<<20)+1), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			samples, err := fetch(context.Background(), server.Client(), server.URL)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected fetch error: %v", err)
			}
			if !tc.wantError && (len(samples) != 1 || samples[0].value != 25 || samples[0].pod != "training") {
				t.Fatal("source labels/value lost")
			}
		})
	}
	now := time.Unix(1000, 0)
	samples, err := parse(strings.TrimSpace(valid)+" 1000000\n", now)
	if err != nil || !samples[0].at.Equal(now) {
		t.Fatalf("explicit timestamp not preserved: %v", err)
	}
}

func TestConcurrentCollection(t *testing.T) {
	o := newTestObserver()
	start := time.Now()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 100; i++ {
			now := start.Add(time.Duration(i) * time.Second)
			o.update([]sample{testSample(now, 10)}, true, now)
		}
	})
	for i := 0; i < 100; i++ {
		metricsAt(t, o, start)
	}
	wg.Wait()
}

func TestFutureTimestampRetentionAndIdentityCap(t *testing.T) {
	start := time.Unix(1000, 0)
	t.Run("future timestamp expires by local receipt", func(t *testing.T) {
		o := newTestObserver()
		s := testSample(start.Add(24*time.Hour), 0)
		o.update([]sample{s}, true, start)
		o.update(nil, false, start.Add(6*time.Minute))
		if len(o.devices) != 0 {
			t.Fatal("future source timestamp bypassed retention")
		}
	})
	t.Run("identity churn fails closed at bound", func(t *testing.T) {
		o := newTestObserver()
		for i := 0; i < 1025; i++ {
			s := testSample(start, 0)
			s.key.uuid = fmt.Sprintf("GPU-%d", i)
			o.update([]sample{s}, true, start)
		}
		if len(o.devices) > 1024 || o.sourceOK {
			t.Fatal("identity churn was unbounded or treated as healthy")
		}
	})
}

func TestMIGWithoutInstanceIdentifiersIsUnknown(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, input := range []sample{{key: deviceKey{uuid: "MIG-example"}, value: 0, at: now}, {key: deviceKey{uuid: "GPU-example"}, partition: true, value: 0, at: now}} {
		o := newTestObserver()
		o.update([]sample{input}, true, now)
		m := metricsAt(t, o, now)
		if len(m["gpu_observer_kernel_inactive_ratio"]) != 0 || metricLabel(m["gpu_observer_observation_healthy"][0], "reason") != "unsupported_partition" {
			t.Fatal("unidentified MIG partition accepted")
		}
	}
}
