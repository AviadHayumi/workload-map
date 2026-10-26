// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testConfig() config {
	return config{namespace: "demo", node: "node", window: 180 * time.Second, poll: 10 * time.Second, maxGap: 25 * time.Second, threshold: .05}
}
func metricFamilies(t *testing.T, body string) map[string]*dto.MetricFamily {
	t.Helper()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return families
}
func fixture(t *testing.T, now time.Time) ([]corev1.Pod, map[string]*dto.MetricFamily, map[string]*dto.MetricFamily) {
	t.Helper()
	pods := []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "demo", Name: "worker", UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}, Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}}
	karta := metricFamilies(t, `# TYPE karta_exporter_observation_healthy gauge
karta_exporter_observation_healthy{scope="workload",namespace="demo",workload="training",group="example.io",version="v1",kind="Example",workload_uid="root-uid",reason="none"} 1
# TYPE karta_pod_workload_info gauge
karta_pod_workload_info{namespace="demo",workload="training",workload_group="example.io",workload_version="v1",workload_kind="Example",workload_uid="root-uid",pod="worker",pod_uid="pod-uid",owner_resolution="resolved"} 1
`)
	source := metricFamilies(t, fmt.Sprintf(`# TYPE fake_gpu_source_healthy gauge
fake_gpu_source_healthy{node="node",source="synthetic"} 1
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{node="node",UUID="gpu-a",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker",source="synthetic"} 3
DCGM_FI_DEV_GPU_UTIL{node="node",UUID="gpu-b",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker",source="synthetic"} 0
# TYPE fake_gpu_source_sample_timestamp_seconds gauge
fake_gpu_source_sample_timestamp_seconds{node="node",UUID="gpu-a",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker",source="synthetic"} %.6f
fake_gpu_source_sample_timestamp_seconds{node="node",UUID="gpu-b",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker",source="synthetic"} %.6f
`, float64(now.Unix()), float64(now.Unix())))
	return pods, karta, source
}
func collectAt(t *testing.T, o *observer, now time.Time) map[string][]*dto.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 10000)
	o.collectAt(ch, now)
	close(ch)
	metrics := make(map[string][]*dto.Metric)
	for m := range ch {
		d := m.Desc().String()
		start := strings.Index(d, `fqName: "`) + len(`fqName: "`)
		end := strings.Index(d[start:], `"`) + start
		parsed := &dto.Metric{}
		if err := m.Write(parsed); err != nil {
			t.Fatal(err)
		}
		metrics[d[start:end]] = append(metrics[d[start:end]], parsed)
	}
	return metrics
}
func stateOf(t *testing.T, o *observer, now time.Time) string {
	t.Helper()
	values := collectAt(t, o, now)["workload_gpu_observer_state"]
	if len(values) != 1 {
		t.Fatalf("state count %d", len(values))
	}
	return labelsOf(values[0])["state"]
}
func TestCompleteWindowAllGPUsAndTransitions(t *testing.T) {
	o := newObserver(testConfig())
	start := time.Unix(100000, 0)
	for second := 0; second <= 180; second += 10 {
		now := start.Add(time.Duration(second) * time.Second)
		pods, karta, source := fixture(t, now)
		readings, err := evaluate(o.cfg, pods, karta, source, now)
		if err != nil {
			t.Fatal(err)
		}
		o.update(readings, true, now)
		want := "warming"
		if second == 180 {
			want = "low_activity"
		}
		if got := stateOf(t, o, now); got != want {
			t.Fatalf("at %d state %s want %s", second, got, want)
		}
		low := collectAt(t, o, now)["workload_gpu_observer_low_activity"]
		if second < 180 && len(low) != 0 {
			t.Fatal("partial window classified idle")
		}
		if second == 180 && (len(low) != 1 || low[0].GetGauge().GetValue() != 1) {
			t.Fatal("full low window missing")
		}
	}
	now := start.Add(190 * time.Second)
	pods, karta, source := fixture(t, now)
	*source["DCGM_FI_DEV_GPU_UTIL"].Metric[1].Gauge.Value = 90
	readings, _ := evaluate(o.cfg, pods, karta, source, now)
	o.update(readings, true, now)
	if got := stateOf(t, o, now); got != "active" {
		t.Fatalf("busy second GPU classified %s", got)
	}
	if collectAt(t, o, now)["workload_gpu_observer_low_activity"][0].GetGauge().GetValue() != 0 {
		t.Fatal("busy GPU failed whole workload rule")
	}
	now = now.Add(10 * time.Second)
	pods, karta, source = fixture(t, now)
	readings, _ = evaluate(o.cfg, pods, karta, source, now)
	o.update(readings, true, now)
	if got := stateOf(t, o, now); got != "warming" {
		t.Fatalf("quiet after busy requires new window, got %s", got)
	}
}
func TestIncompleteOrAmbiguousInventoryNeverIdle(t *testing.T) {
	tests := []struct {
		name, reason string
		mutate       func([]corev1.Pod, map[string]*dto.MetricFamily, map[string]*dto.MetricFamily)
	}{
		{"missing_device", "incomplete_inventory", func(_ []corev1.Pod, _ map[string]*dto.MetricFamily, s map[string]*dto.MetricFamily) {
			s["DCGM_FI_DEV_GPU_UTIL"].Metric = s["DCGM_FI_DEV_GPU_UTIL"].Metric[:1]
		}},
		{"reused_pod_name", "incomplete_mapping", func(p []corev1.Pod, _ map[string]*dto.MetricFamily, _ map[string]*dto.MetricFamily) {
			p[0].UID = "new-uid"
		}},
		{"missing_uid", "allocation_mismatch", func(_ []corev1.Pod, _ map[string]*dto.MetricFamily, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_DEV_GPU_UTIL"].Metric[0], "pod_uid", "")
			setLabel(s["fake_gpu_source_sample_timestamp_seconds"].Metric[0], "pod_uid", "")
		}},
		{"wrong_container", "allocation_mismatch", func(_ []corev1.Pod, _ map[string]*dto.MetricFamily, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_DEV_GPU_UTIL"].Metric[0], "container", "sidecar")
			setLabel(s["fake_gpu_source_sample_timestamp_seconds"].Metric[0], "container", "sidecar")
		}},
		{"duplicate_device", "source_unavailable", func(_ []corev1.Pod, _ map[string]*dto.MetricFamily, s map[string]*dto.MetricFamily) {
			s["DCGM_FI_DEV_GPU_UTIL"].Metric = append(s["DCGM_FI_DEV_GPU_UTIL"].Metric, s["DCGM_FI_DEV_GPU_UTIL"].Metric[0])
		}},
		{"stale_sample", "source_unavailable", func(_ []corev1.Pod, _ map[string]*dto.MetricFamily, s map[string]*dto.MetricFamily) {
			*s["fake_gpu_source_sample_timestamp_seconds"].Metric[0].Gauge.Value -= 30
		}},
		{"bad_value", "source_unavailable", func(_ []corev1.Pod, _ map[string]*dto.MetricFamily, s map[string]*dto.MetricFamily) {
			*s["DCGM_FI_DEV_GPU_UTIL"].Metric[0].Gauge.Value = 9223372036854775794
		}},
		{"unhealthy_root", "karta_unhealthy", func(_ []corev1.Pod, k map[string]*dto.MetricFamily, _ map[string]*dto.MetricFamily) {
			*k["karta_exporter_observation_healthy"].Metric[0].Gauge.Value = 0
		}},
		{"init_gpu", "unsupported_allocation", func(p []corev1.Pod, _ map[string]*dto.MetricFamily, _ map[string]*dto.MetricFamily) {
			p[0].Spec.InitContainers = append(p[0].Spec.InitContainers, p[0].Spec.Containers[0])
		}},
		{"pending_pod", "allocation_pending", func(p []corev1.Pod, _ map[string]*dto.MetricFamily, _ map[string]*dto.MetricFamily) {
			p[0].Status.Phase = corev1.PodPending
		}},
		{"no_mapping", "incomplete_mapping", func(_ []corev1.Pod, k map[string]*dto.MetricFamily, _ map[string]*dto.MetricFamily) {
			delete(k, "karta_pod_workload_info")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := newObserver(testConfig())
			now := time.Unix(100000, 0)
			pods, karta, source := fixture(t, now)
			tc.mutate(pods, karta, source)
			readings, err := evaluate(o.cfg, pods, karta, source, now)
			if err != nil && tc.reason != "source_unavailable" {
				t.Fatal(err)
			}
			o.update(readings, err == nil, now)
			if got := stateOf(t, o, now); got != "unknown" {
				t.Fatalf("state %s", got)
			}
			m := collectAt(t, o, now)
			reason := labelsOf(m["workload_gpu_observer_state"][0])["reason"]
			if reason != tc.reason && (tc.reason != "incomplete_mapping" || reason != "allocation_mismatch") {
				t.Fatalf("reason %s want %s", reason, tc.reason)
			}
			if len(m["workload_gpu_observer_low_activity"]) != 0 {
				t.Fatal("unknown emitted low-activity decision")
			}
		})
	}
}
func setLabel(m *dto.Metric, name, value string) {
	for _, label := range m.Label {
		if label.GetName() == name {
			*label.Value = value
			return
		}
	}
}
func TestRestartGapFailureAndChangedAssignmentReset(t *testing.T) {
	for _, change := range []string{"gap", "failure", "gpu", "pod", "root", "repeated", "restart"} {
		t.Run(change, func(t *testing.T) {
			o := newObserver(testConfig())
			now := time.Unix(100000, 0)
			for second := 0; second <= 180; second += 10 {
				at := now.Add(time.Duration(second) * time.Second)
				pods, karta, source := fixture(t, at)
				r, _ := evaluate(o.cfg, pods, karta, source, at)
				o.update(r, true, at)
			}
			now = now.Add(190 * time.Second)
			pods, karta, source := fixture(t, now)
			switch change {
			case "gap":
				now = now.Add(30 * time.Second)
				pods, karta, source = fixture(t, now)
			case "failure":
				o.update(nil, false, now.Add(-5*time.Second))
			case "gpu":
				setLabel(source["DCGM_FI_DEV_GPU_UTIL"].Metric[0], "UUID", "replacement")
				setLabel(source["fake_gpu_source_sample_timestamp_seconds"].Metric[0], "UUID", "replacement")
			case "pod":
				pods[0].UID = "replacement"
				setLabel(karta["karta_pod_workload_info"].Metric[0], "pod_uid", "replacement")
				for _, family := range []string{"DCGM_FI_DEV_GPU_UTIL", "fake_gpu_source_sample_timestamp_seconds"} {
					for _, m := range source[family].Metric {
						setLabel(m, "pod_uid", "replacement")
					}
				}
			case "root":
				setLabel(karta["karta_pod_workload_info"].Metric[0], "workload_uid", "replacement")
				setLabel(karta["karta_exporter_observation_healthy"].Metric[0], "workload_uid", "replacement")
			case "repeated":
				for _, m := range source["fake_gpu_source_sample_timestamp_seconds"].Metric {
					*m.Gauge.Value -= 10
				}
			case "restart":
				o = newObserver(testConfig())
			}
			r, _ := evaluate(o.cfg, pods, karta, source, now)
			o.update(r, true, now)
			want := "warming"
			if change == "repeated" {
				want = "unknown"
			}
			if got := stateOf(t, o, now); got != want {
				t.Fatalf("got %s want %s", got, want)
			}
			if len(collectAt(t, o, now)["workload_gpu_observer_low_activity"]) != 0 {
				t.Fatal("reset retained idle result")
			}
		})
	}
}
func TestZeroGPUAndFreshness(t *testing.T) {
	o := newObserver(testConfig())
	now := time.Unix(100000, 0)
	pods, karta, source := fixture(t, now)
	pods[0].Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	for _, family := range []string{"DCGM_FI_DEV_GPU_UTIL", "fake_gpu_source_sample_timestamp_seconds"} {
		for _, m := range source[family].Metric {
			setLabel(m, "namespace", "")
		}
	}
	r, _ := evaluate(o.cfg, pods, karta, source, now)
	o.update(r, true, now)
	if got := stateOf(t, o, now); got != "no_gpu" {
		t.Fatalf("zero GPU state %s", got)
	}
	if len(collectAt(t, o, now)["workload_gpu_observer_low_activity"]) != 0 {
		t.Fatal("zero GPU idle decision")
	}
	if got := stateOf(t, o, now.Add(26*time.Second)); got != "unknown" {
		t.Fatalf("stale observation %s", got)
	}
}
func TestConcurrentCollectAndUpdate(t *testing.T) {
	o := newObserver(testConfig())
	registry := prometheus.NewRegistry()
	registry.MustRegister(o)
	now := time.Now()
	pods, karta, source := fixture(t, now)
	r, _ := evaluate(o.cfg, pods, karta, source, now)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				o.update(r, true, now)
				_, _ = registry.Gather()
			}
		}()
	}
	wg.Wait()
}

func TestMixedTimestampIdentityAndSourceHealth(t *testing.T) {
	now := time.Unix(100000, 0)
	pods, karta, source := fixture(t, now)
	setLabel(source["fake_gpu_source_sample_timestamp_seconds"].Metric[0], "pod_uid", "previous-pod")
	o := newObserver(testConfig())
	readings, err := evaluate(o.cfg, pods, karta, source, now)
	if err == nil {
		t.Fatal("mixed source allocation accepted")
	}
	o.update(readings, false, now)
	metrics := collectAt(t, o, now)
	if metrics["workload_gpu_observer_source_healthy"][0].GetGauge().GetValue() != 0 || stateOf(t, o, now) != "unknown" {
		t.Fatal("invalid source reported healthy")
	}
	_, karta, source = fixture(t, now)
	setLabel(karta["karta_pod_workload_info"].Metric[0], "owner_resolution", "unresolved")
	if _, err := evaluate(o.cfg, pods, karta, source, now); err == nil {
		t.Fatal("unresolved owner accepted")
	}
}

func TestEveryContainerRequiresCompleteCoverage(t *testing.T) {
	now := time.Unix(100000, 0)
	pods, karta, source := fixture(t, now)
	additional := *pods[0].Spec.Containers[0].DeepCopy()
	additional.Name = "second"
	pods[0].Spec.Containers = append(pods[0].Spec.Containers, additional)
	readings, err := evaluate(testConfig(), pods, karta, source, now)
	if err != nil || len(readings) != 1 || readings[0].reason != "incomplete_inventory" {
		t.Fatal("missing second container GPUs accepted")
	}
	setLabel(source["DCGM_FI_DEV_GPU_UTIL"].Metric[1], "container", "second")
	setLabel(source["fake_gpu_source_sample_timestamp_seconds"].Metric[1], "container", "second")
	for i := range pods[0].Spec.Containers {
		pods[0].Spec.Containers[i].Resources.Limits["nvidia.com/gpu"] = resource.MustParse("1")
		pods[0].Spec.Containers[i].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
	}
	readings, err = evaluate(testConfig(), pods, karta, source, now)
	if err != nil || readings[0].reason != "none" || readings[0].devices != 2 {
		t.Fatal("complete second container inventory rejected")
	}
}
