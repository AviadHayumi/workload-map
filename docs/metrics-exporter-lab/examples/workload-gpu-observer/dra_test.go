// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func draPtr[T any](v T) *T { return &v }

func draFixture(t *testing.T, mig bool) ([]corev1.Pod, map[string]*dto.MetricFamily, map[string]*dto.MetricFamily, resourcev1.ResourceClaim, resourcev1.ResourceSlice) {
	t.Helper()
	ps, k, _ := fixture(t, time.Unix(100000, 0))
	p := &ps[0]
	p.Spec.Containers[0].Resources = corev1.ResourceRequirements{Claims: []corev1.ResourceClaim{{Name: "accelerator"}}}
	p.Spec.ResourceClaims = []corev1.PodResourceClaim{{Name: "accelerator", ResourceClaimTemplateName: draPtr("template")}}
	p.Status.ResourceClaimStatuses = []corev1.PodResourceClaimStatus{{Name: "accelerator", ResourceClaimName: draPtr("claim")}}
	c := resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "demo", UID: "claim-uid"}, Spec: resourcev1.ResourceClaimSpec{Devices: resourcev1.DeviceClaim{Requests: []resourcev1.DeviceRequest{{Name: "gpu", Exactly: &resourcev1.ExactDeviceRequest{DeviceClassName: "gpu.nvidia.com", Count: 1}}}}}, Status: resourcev1.ResourceClaimStatus{ReservedFor: []resourcev1.ResourceClaimConsumerReference{{Resource: "pods", Name: p.Name, UID: p.UID}}, Allocation: &resourcev1.AllocationResult{Devices: resourcev1.DeviceAllocationResult{Results: []resourcev1.DeviceRequestAllocationResult{{Request: "gpu", Driver: "gpu.nvidia.com", Pool: "node", Device: "device"}}}}}}
	attrs := map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{"uuid": {StringValue: draPtr("GPU-a")}, "type": {StringValue: draPtr("gpu")}}
	metric := "DCGM_FI_DEV_GPU_UTIL"
	value := "3"
	extra := ""
	if mig {
		c.Spec.Devices.Requests[0].Exactly.DeviceClassName = "mig.nvidia.com"
		attrs["uuid"] = resourcev1.DeviceAttribute{StringValue: draPtr("MIG-a")}
		attrs["type"] = resourcev1.DeviceAttribute{StringValue: draPtr("mig")}
		attrs["profile"] = resourcev1.DeviceAttribute{StringValue: draPtr("3g.20gb")}
		attrs["parentUUID"] = resourcev1.DeviceAttribute{StringValue: draPtr("GPU-a")}
		metric = "DCGM_FI_PROF_GR_ENGINE_ACTIVE"
		value = "0.03"
		extra = `,GPU_I_ID="1",GPU_CI_ID="0",GPU_I_PROFILE="MIG 3g.20gb",dra_mig_device_uuid="MIG-a",dra_mig_profile="3g.20gb"`
	}
	slice := resourcev1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{Name: "slice", UID: "slice-uid"}, Spec: resourcev1.ResourceSliceSpec{Driver: "gpu.nvidia.com", NodeName: draPtr("node"), Pool: resourcev1.ResourcePool{Name: "node", Generation: 1, ResourceSliceCount: 1}, Devices: []resourcev1.Device{{Name: "device", Attributes: attrs}}}}
	source := metricFamilies(t, fmt.Sprintf("# TYPE %s gauge\n%s{hostname=\"node\",UUID=\"GPU-a\",namespace=\"demo\",pod=\"worker\",pod_uid=\"pod-uid\",container=\"worker\",dra_claim_name=\"claim\",dra_claim_namespace=\"demo\",dra_driver_name=\"gpu.nvidia.com\",dra_pool_name=\"node\",dra_device_name=\"device\"%s} %s\n", metric, metric, extra, value))
	return ps, k, source, c, slice
}

func TestDRAFullAndMIGWindowClaimReplacement(t *testing.T) {
	for _, mig := range []bool{false, true} {
		t.Run(fmt.Sprint(mig), func(t *testing.T) {
			cfg := testConfig()
			cfg.nativeDCGM = true
			o := newObserver(cfg)
			ps, k, s, c, sl := draFixture(t, mig)
			for sec := 0; sec <= 200; sec += 10 {
				if sec == 190 {
					c.UID = "replacement-claim"
				}
				if sec == 200 {
					for _, f := range s {
						*f.Metric[0].Gauge.Value = 0.9
						if !mig {
							*f.Metric[0].Gauge.Value = 90
						}
					}
				}
				i, err := indexDRA(cfg, []resourcev1.ResourceClaim{c}, []resourcev1.ResourceSlice{sl})
				if err != nil {
					t.Fatal(err)
				}
				now := time.Unix(100000+int64(sec), 0)
				r, err := evaluate(cfg, ps, k, s, now, i)
				if err != nil || len(r) != 1 || r[0].reason != "none" || r[0].devices != 1 {
					t.Fatalf("invalid attribution: %v %v", r, err)
				}
				o.update(r, true, now)
				want := "warming"
				if sec == 180 {
					want = "low_activity"
				}
				if sec == 200 {
					want = "active"
				}
				if got := stateOf(t, o, now); got != want {
					t.Fatalf("at %d got %s want %s", sec, got, want)
				}
			}
			ps[0].Status.Phase = corev1.PodSucceeded
			for _, f := range s {
				for _, label := range []string{"namespace", "pod", "pod_uid", "container", "dra_claim_name", "dra_driver_name", "dra_device_name"} {
					setLabel(f.Metric[0], label, "")
				}
			}
			r, err := evaluate(cfg, ps, k, s, time.Unix(100220, 0))
			if err != nil || r[0].reason != "no_gpu" {
				t.Fatalf("completion: %v %v", r, err)
			}
		})
	}
}

func TestDRAIncompleteEvidenceIsUnknown(t *testing.T) {
	cases := map[string]func(*corev1.Pod, *resourcev1.ResourceClaim, *resourcev1.ResourceSlice, map[string]*dto.MetricFamily){
		"pending": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Status.Allocation = nil
		},
		"wrong_consumer_uid": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Status.ReservedFor[0].UID = "old"
		},
		"shared_consumer": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Status.ReservedFor = append(c.Status.ReservedFor, c.Status.ReservedFor[0])
		},
		"admin_access": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Status.Allocation.Devices.Results[0].AdminAccess = draPtr(true)
		},
		"share_id": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Status.Allocation.Devices.Results[0].ShareID = draPtr(types.UID("share"))
		},
		"missing_allocation": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Spec.Devices.Requests[0].Exactly.Count = 2
		},
		"wrong_driver": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Status.Allocation.Devices.Results[0].Driver = "other.example"
		},
		"wrong_device": func(_ *corev1.Pod, c *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c.Status.Allocation.Devices.Results[0].Device = "other"
		},
		"missing_claim_status": func(p *corev1.Pod, _ *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			p.Status.ResourceClaimStatuses = nil
		},
		"init_claim": func(p *corev1.Pod, _ *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			p.Spec.InitContainers = append(p.Spec.InitContainers, p.Spec.Containers[0])
		},
		"shared_container": func(p *corev1.Pod, _ *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			c := p.Spec.Containers[0]
			c.Name = "second"
			p.Spec.Containers = append(p.Spec.Containers, c)
		},
		"wrong_profile": func(_ *corev1.Pod, _ *resourcev1.ResourceClaim, sl *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			sl.Spec.Devices[0].Attributes["profile"] = resourcev1.DeviceAttribute{StringValue: draPtr("1g.5gb")}
		},
		"wrong_uuid": func(_ *corev1.Pod, _ *resourcev1.ResourceClaim, sl *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			sl.Spec.Devices[0].Attributes["uuid"] = resourcev1.DeviceAttribute{StringValue: draPtr("MIG-other")}
		},
		"sharing_device": func(_ *corev1.Pod, _ *resourcev1.ResourceClaim, sl *resourcev1.ResourceSlice, _ map[string]*dto.MetricFamily) {
			sl.Spec.Devices[0].AllowMultipleAllocations = draPtr(true)
		},
		"wrong_dcgm_claim": func(_ *corev1.Pod, _ *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "dra_claim_name", "other")
		},
		"missing_dcgm_uid": func(_ *corev1.Pod, _ *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "pod_uid", "")
		},
		"missing_dcgm_mig_uuid": func(_ *corev1.Pod, _ *resourcev1.ResourceClaim, _ *resourcev1.ResourceSlice, s map[string]*dto.MetricFamily) {
			setLabel(s["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].Metric[0], "dra_mig_device_uuid", "")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			cfg.nativeDCGM = true
			ps, k, s, c, sl := draFixture(t, true)
			mutate(&ps[0], &c, &sl, s)
			i, err := indexDRA(cfg, []resourcev1.ResourceClaim{c}, []resourcev1.ResourceSlice{sl})
			if err != nil {
				t.Fatal(err)
			}
			r, err := evaluate(cfg, ps, k, s, time.Unix(100000, 0), i)
			o := newObserver(cfg)
			o.update(r, err == nil, time.Unix(100000, 0))
			if stateOf(t, o, time.Unix(100000, 0)) != "unknown" || len(collectAt(t, o, time.Unix(100000, 0))["workload_gpu_observer_low_activity"]) > 0 {
				t.Fatalf("incomplete DRA evidence accepted: %v", r)
			}
		})
	}
}

func TestDRAInventoryAndDirectClaim(t *testing.T) {
	cfg := testConfig()
	cfg.nativeDCGM = true
	ps, _, _, c, sl := draFixture(t, false)
	client := fake.NewClientset(&c, &sl)
	i, err := readDRA(context.Background(), client, cfg, ps)
	if err != nil {
		t.Fatal(err)
	}
	ps[0].Spec.ResourceClaims[0].ResourceClaimTemplateName = nil
	ps[0].Spec.ResourceClaims[0].ResourceClaimName = draPtr("claim")
	if r := requestsFor(ps[0], true, i); r.unsupported || r.counts["worker"] != 1 {
		t.Fatalf("direct claim rejected: %+v", r)
	}
	for _, slices := range [][]resourcev1.ResourceSlice{nil, {sl, sl}} {
		if _, err := indexDRA(cfg, []resourcev1.ResourceClaim{c}, slices); err == nil {
			t.Fatal("incomplete or duplicate pool accepted")
		}
	}
	sl.Spec.Pool.ResourceSliceCount = 2
	if _, err := indexDRA(cfg, []resourcev1.ResourceClaim{c}, []resourcev1.ResourceSlice{sl}); err == nil {
		t.Fatal("missing pool slice accepted")
	}
	if r := requestsFor(ps[0], false, i); !r.unsupported {
		t.Fatal("DRA accepted for non-native source")
	}
}
