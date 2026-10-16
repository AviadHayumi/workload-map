// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package collector

import (
	"time"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func (c *Collector) collectIdentities(observer metric.Observer, snapshot store.Snapshot) {
	now := time.Now()
	kinds := map[schema.GroupVersionKind]bool{}
	workloads := map[types.UID]bool{}
	for _, h := range snapshot.Health {
		value, _ := h.Status(now)
		if h.Scope == "resource_type" {
			kinds[schema.GroupVersionKind{Group: h.Ref.Group, Version: h.Ref.Version, Kind: h.Ref.Kind}] = value == 1
		}
		if h.Scope == "workload" {
			workloads[h.UID] = value == 1
		}
	}
	resolved := make(map[types.UID]identity.Result, len(snapshot.Workloads))
	for uid, record := range snapshot.Identities {
		ref, result := record.Ref, record.Result
		if !kinds[schema.GroupVersionKind{Group: ref.Group, Version: ref.Version, Kind: ref.Kind}] {
			result = identity.Result{Status: "stale", Type: identity.Unknown}
		}
		c.resourceSubmitter.observe(observer, 1, ref.Namespace, ref.Name, string(uid), ref.Group, ref.Version, ref.Kind, result.Submitter, result.Source, result.Type, result.Rule, result.Status)
	}
	for _, workload := range snapshot.Workloads {
		if workload.OwnerResolution != "" {
			resolved[workload.UID] = snapshot.Identities[workload.UID].Result
		}
	}
	for _, pod := range snapshot.Pods {
		if _, ok := resolved[pod.WorkloadUID]; ok {
			resolved[pod.WorkloadUID] = identity.Merge(resolved[pod.WorkloadUID], snapshot.Identities[pod.UID].Result)
		}
	}
	for _, workload := range snapshot.Workloads {
		result, ok := resolved[workload.UID]
		if !ok {
			continue
		}
		if result.Status == "" {
			result.Status, result.Type = identity.Unknown, identity.Unknown
		}
		if !workloads[workload.UID] {
			result = identity.Result{Status: "stale", Type: identity.Unknown}
		}
		ref := workload.Ref
		c.workloadSubmitter.observe(observer, 1, ref.Namespace, ref.Name, string(workload.UID), ref.Group, ref.Version, ref.Kind, result.Submitter, result.Source, result.Type, result.Rule, result.Status)
	}
}
