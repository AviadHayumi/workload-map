// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func workloadSubmitters(snapshot store.Snapshot, now time.Time) map[types.UID]string {
	healthy := make(map[types.UID]bool, len(snapshot.Workloads))
	for _, h := range snapshot.Health {
		if h.Scope == "workload" {
			value, _ := h.Status(now)
			healthy[h.UID] = value == 1
		}
	}
	roots := make(map[types.UID]store.WorkloadRecord, len(snapshot.Workloads))
	resolved := make(map[types.UID]identity.Result, len(snapshot.Workloads))
	for _, workload := range snapshot.Workloads {
		if healthy[workload.UID] && workload.OwnerResolution != "" && workload.OwnerFailure == "" {
			roots[workload.UID] = workload
			resolved[workload.UID] = snapshot.Identities[workload.UID].Result
		}
	}
	for _, pod := range snapshot.Pods {
		root, ok := roots[pod.WorkloadUID]
		if !ok || pod.OwnerResolution == "" || root.OwnerResolution != pod.OwnerResolution || root.Ref != pod.Workload {
			continue
		}
		resolved[pod.WorkloadUID] = identity.Merge(resolved[pod.WorkloadUID], snapshot.Identities[pod.UID].Result)
	}
	submitters := make(map[types.UID]string, len(resolved))
	for uid, result := range resolved {
		if result.Status == identity.Complete || result.Status == identity.Partial {
			submitters[uid] = result.Submitter
		}
	}
	return submitters
}
