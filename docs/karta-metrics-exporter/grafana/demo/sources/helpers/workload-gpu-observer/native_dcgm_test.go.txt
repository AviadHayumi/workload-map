// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation
package main

import (
	"testing"
	"time"
)

func TestNativeDCGMWindowAndUID(t *testing.T) {
	cfg := testConfig()
	cfg.nativeDCGM = true
	o := newObserver(cfg)
	start := time.Unix(100000, 0)
	for seconds := 0; seconds <= 180; seconds += 10 {
		now := start.Add(time.Duration(seconds) * time.Second)
		pods, karta, _ := fixture(t, now)
		source := metricFamilies(t, `# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{hostname="node",UUID="GPU-a",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker"} 3
DCGM_FI_DEV_GPU_UTIL{hostname="node",UUID="GPU-b",namespace="demo",pod="worker",pod_uid="pod-uid",container="worker"} 0
`)
		r, err := evaluate(cfg, pods, karta, source, now)
		if err != nil {
			t.Fatal(err)
		}
		o.update(r, true, now)
		want := "warming"
		if seconds == 180 {
			want = "low_activity"
		}
		if stateOf(t, o, now) != want {
			t.Fatal("wrong native activity state")
		}
		if seconds == 180 {
			metrics := collectAt(t, o, now)
			if labelsOf(metrics["workload_gpu_observer_state"][0])["source"] != "dcgm" {
				t.Fatal("wrong source")
			}
			setLabel(source["DCGM_FI_DEV_GPU_UTIL"].Metric[0], "pod_uid", "replaced-uid")
			r, err = evaluate(cfg, pods, karta, source, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			o.update(r, true, now.Add(time.Second))
			if stateOf(t, o, now.Add(time.Second)) != "unknown" {
				t.Fatal("reused pod accepted")
			}
		}
	}
	o.update(nil, false, start.Add(200*time.Second))
	if stateOf(t, o, start.Add(200*time.Second)) != "unknown" {
		t.Fatal("source failure accepted")
	}
}
func TestNativeDCGMInvalidDevices(t *testing.T) {
	for _, body := range []string{
		`DCGM_FI_DEV_GPU_UTIL{hostname="other",UUID="GPU-a"} 0`,
		`DCGM_FI_DEV_GPU_UTIL{hostname="node",UUID="MIG-a"} 0`,
		`DCGM_FI_DEV_GPU_UTIL{hostname="node",UUID="GPU-a"} 9223372036854775794`,
		`DCGM_FI_DEV_GPU_UTIL{hostname="node",UUID="GPU-a"} NaN`,
		"DCGM_FI_DEV_GPU_UTIL{hostname=\"node\",UUID=\"GPU-a\"} 0\nDCGM_FI_DEV_GPU_UTIL{hostname=\"node\",UUID=\"GPU-a\"} 1",
	} {
		source := metricFamilies(t, "# TYPE DCGM_FI_DEV_GPU_UTIL gauge\n"+body+"\n")
		if _, err := nativeSamples(source, testConfig(), time.Now()); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
