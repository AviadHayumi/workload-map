// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation
package main

import (
	"math"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func migFixture(t *testing.T, now time.Time) ([]corev1.Pod, map[string]*dto.MetricFamily, map[string]*dto.MetricFamily) {
	t.Helper()
	pods, karta, _ := fixture(t, now)
	pods[0].Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{"nvidia.com/mig-3g.20gb": resource.MustParse("2")},
		Requests: corev1.ResourceList{"nvidia.com/mig-3g.20gb": resource.MustParse("2")},
	}
	source := metricFamilies(t, `# TYPE DCGM_FI_PROF_GR_ENGINE_ACTIVE gauge
DCGM_FI_PROF_GR_ENGINE_ACTIVE{hostname="node",UUID="GPU-a",GPU_I_ID="1",GPU_CI_ID="0",GPU_I_PROFILE="MIG 3g.20gb",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker"} 0.03
DCGM_FI_PROF_GR_ENGINE_ACTIVE{hostname="node",UUID="GPU-a",GPU_I_ID="2",GPU_CI_ID="0",GPU_I_PROFILE="MIG 3g.20gb",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker"} 0
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{hostname="node",UUID="GPU-a",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker"} 100
DCGM_FI_DEV_GPU_UTIL{hostname="node",UUID="GPU-b"} 0
`)
	return pods, karta, source
}

func TestNativeMIGWindowAndParentDeduplication(t *testing.T) {
	cfg := testConfig()
	cfg.nativeDCGM = true
	o := newObserver(cfg)
	start := time.Unix(100000, 0)
	for seconds := 0; seconds <= 200; seconds += 10 {
		now := start.Add(time.Duration(seconds) * time.Second)
		pods, karta, source := migFixture(t, now)
		want := "warming"
		switch seconds {
		case 0, 200:
			*source["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0].Gauge.Value = .8
			want = "active"
		case 190:
			want = "low_activity"
		}
		r, err := evaluate(cfg, pods, karta, source, now)
		if err != nil || len(r) != 1 || r[0].devices != 2 || r[0].reason != "none" {
			t.Fatalf("invalid MIG attribution: %v %v", r, err)
		}
		if want == "active" && r[0].maximum != .8 {
			t.Fatalf("MIG ratio rescaled or parent double-counted: %v", r[0])
		}
		o.update(r, true, now)
		if got := stateOf(t, o, now); got != want {
			t.Fatalf("at %d got %s, want %s", seconds, got, want)
		}
	}
}

func TestNativeMIGIncompleteEvidenceNeverIdle(t *testing.T) {
	cases := map[string]func([]corev1.Pod, map[string]*dto.MetricFamily){
		"missing_partition": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric = s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[:1]
		},
		"missing_profile_counter": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) { delete(s, "DCGM_FI_PROF_GR_ENGINE_ACTIVE") },
		"duplicate_instance": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric = append(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric, s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0])
		},
		"multiple_compute_instances": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[1], "GPU_I_ID", "1")
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[1], "GPU_CI_ID", "1")
		},
		"wrong_profile": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "GPU_I_PROFILE", "MIG 1g.5gb")
		},
		"missing_compute_id": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "GPU_CI_ID", "")
		},
		"missing_uid": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "pod_uid", "")
		},
		"reused_uid": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "pod_uid", "old-pod")
		},
		"invalid_ratio": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			*s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0].Gauge.Value = math.NaN()
		},
		"unsupported_sentinel": func(_ []corev1.Pod, s map[string]*dto.MetricFamily) {
			*s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0].Gauge.Value = 9223372036854775794
		},
		"init_gpu": func(p []corev1.Pod, _ map[string]*dto.MetricFamily) {
			p[0].Spec.InitContainers = append(p[0].Spec.InitContainers, p[0].Spec.Containers[0])
		},
		"mixed_resources_in_one_container": func(p []corev1.Pod, _ map[string]*dto.MetricFamily) {
			p[0].Spec.Containers[0].Resources.Limits["nvidia.com/gpu"] = resource.MustParse("1")
			p[0].Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			cfg.nativeDCGM = true
			now := time.Unix(100000, 0)
			pods, karta, source := migFixture(t, now)
			mutate(pods, source)
			r, err := evaluate(cfg, pods, karta, source, now)
			o := newObserver(cfg)
			o.update(r, err == nil, now)
			if stateOf(t, o, now) != "unknown" || len(collectAt(t, o, now)["workload_gpu_observer_low_activity"]) != 0 {
				t.Fatal("incomplete MIG evidence produced an activity decision")
			}
		})
	}
}

func TestNativeMIGReplacementRestartsWindow(t *testing.T) {
	cfg := testConfig()
	cfg.nativeDCGM = true
	o := newObserver(cfg)
	start := time.Unix(100000, 0)
	for sec := 0; sec <= 190; sec += 10 {
		now := start.Add(time.Duration(sec) * time.Second)
		pods, karta, source := migFixture(t, now)
		if sec == 190 {
			setLabel(source["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "GPU_I_ID", "3")
		}
		r, err := evaluate(cfg, pods, karta, source, now)
		if err != nil {
			t.Fatal(err)
		}
		o.update(r, true, now)
		if sec == 180 && stateOf(t, o, now) != "low_activity" {
			t.Fatal("window did not complete")
		}
		if sec == 190 && stateOf(t, o, now) != "warming" {
			t.Fatal("changed instance retained old low window")
		}
	}
}
