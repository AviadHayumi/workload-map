// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation
package main

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
)

var migProfile = regexp.MustCompile(`^[1-9][0-9]*g\.[1-9][0-9]*gb$`)

// Native DCGM provides no per-sample collection timestamp. The observation
// window therefore measures successful HTTP reads, not driver freshness.
func nativeSamples(source map[string]*dto.MetricFamily, cfg config, now time.Time) ([]gpuSample, error) {
	var samples []gpuSample
	seen := make(map[string]bool)
	partitioned := make(map[string]bool)
	for _, m := range source["DCGM_FI_PROF_GR_ENGINE_ACTIVE"].GetMetric() {
		l := labelsOf(m)
		if l["GPU_I_ID"] == "" && l["GPU_CI_ID"] == "" && l["GPU_I_PROFILE"] == "" {
			continue
		}
		uuid, node := l["UUID"], l["hostname"]
		profile := strings.TrimPrefix(l["GPU_I_PROFILE"], "MIG ")
		gi, giErr := strconv.Atoi(l["GPU_I_ID"])
		ci, ciErr := strconv.Atoi(l["GPU_CI_ID"])
		key := uuid + "/gi/" + l["GPU_I_ID"]
		value := m.GetGauge().GetValue()
		// A Kubernetes MIG resource represents one complete GPU instance. Multiple
		// compute instances in that GPU instance need a separate allocation model.
		if m.Gauge == nil || !strings.HasPrefix(uuid, "GPU-") || node != cfg.node || !migProfile.MatchString(profile) || giErr != nil || ciErr != nil || gi < 0 || ci < 0 || seen[key] || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return nil, errors.New("ambiguous, unsupported or invalid native MIG device")
		}
		seen[key], partitioned[uuid] = true, true
		samples = append(samples, gpuSample{dra: draSample{claim: l["dra_claim_name"], namespace: l["dra_claim_namespace"], driver: l["dra_driver_name"], pool: l["dra_pool_name"], device: l["dra_device_name"], migUUID: l["dra_mig_device_uuid"], profile: l["dra_mig_profile"]}, uuid: key + "/ci/" + l["GPU_CI_ID"], node: node, namespace: l["namespace"], pod: l["pod"], uid: l["pod_uid"], container: l["container"], resource: "nvidia.com/mig-" + profile, value: value, at: now})
	}
	for _, m := range source["DCGM_FI_DEV_GPU_UTIL"].GetMetric() {
		l := labelsOf(m)
		uuid, node := l["UUID"], l["hostname"]
		if partitioned[uuid] {
			continue
		}
		value := m.GetGauge().GetValue()
		if m.Gauge == nil || uuid == "" || node != cfg.node || seen[uuid] || strings.HasPrefix(uuid, "MIG-") || l["GPU_I_ID"] != "" || l["GPU_CI_ID"] != "" || l["GPU_I_PROFILE"] != "" || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return nil, errors.New("ambiguous, unsupported or invalid native DCGM device")
		}
		seen[uuid] = true
		samples = append(samples, gpuSample{dra: draSample{claim: l["dra_claim_name"], namespace: l["dra_claim_namespace"], driver: l["dra_driver_name"], pool: l["dra_pool_name"], device: l["dra_device_name"], migUUID: l["dra_mig_device_uuid"], profile: l["dra_mig_profile"]}, uuid: uuid, node: node, namespace: l["namespace"], pod: l["pod"], uid: l["pod_uid"], container: l["container"], resource: "nvidia.com/gpu", value: value / 100, at: now})
	}
	if len(samples) == 0 || len(samples) > 1024 {
		return nil, errors.New("empty or excessive native DCGM inventory")
	}
	return samples, nil
}
