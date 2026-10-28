<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# kube-state-metrics and a simple exporter controller

## TLDR

A Pod update can replace that Pod's UID-keyed metrics without rebuilding workload metrics. A workload update can do the same for its own publication. kube-state-metrics supports this event-driven, per-object model. Reuse its lifecycle boundaries. Publish immutable snapshots instead of copying its mutable map traversal. Report observed LIST/WATCH health separately from object evaluation failures. Neither an unchanged object nor a quiet watch needs a per-object expiry timer.

## How it works

The builder creates stores by resource and namespace scope. Each reflector writes events into its store, with periodic resync disabled. Add generates metric families and stores their rendered bytes under the object's UID. Update calls Add. Delete removes only the matching UID. Replace clears the store and adds the objects from a replacement list. There is no age-based eviction in this store. [E1, E2]

API observations and metric generation follow different paths. The list/watch wrapper increments resource-level success/error counters around calls to the underlying client. A successful Watch call means a stream was established. The wrapper returns that stream without observing its later events or closure. The sharding wrapper passes watch Error and Bookmark events through to its consumer. [E3, E4]

The metric-generator signature returns a family, without an error result. Custom-resource evaluation collects errors, logs them, and still returns the metrics it could produce. Those evaluation errors do not become list/watch call failures. The store records a resource version before generation, so that value is not evidence that every metric evaluated successfully. [E2, E5]

The scrape handler briefly locks to capture its writer list, then releases the lock before writing the response. The actual metric entries remain mutable. Writers range over that map once per family. Replace also clears and repopulates it incrementally. Consequently, capturing the writer list does not give a scrape an immutable view of all samples. [E6]

## Relevance

Keep separate Pod and workload publications keyed by Kubernetes UID. Each controller owns replacement and deletion for its objects. An object event updates only its own publication. A scrape captures the published Pod and workload snapshots once and reads them without API calls or evaluation. Their capture times can differ; this design does not promise a Kubernetes transaction spanning resource types.

When an object's evaluation fails, replace its affected output with a publication that omits the failed samples. Record the evaluation failure separately. Other objects and the other resource publication continue normally. This is a proposed policy, supported by kube-state-metrics' separation of evaluation and transport errors. It is not a claim that kube-state-metrics implements this exact failure contract.

## Evidence

Origin and HEAD were verified locally as `https://github.com/kubernetes/kube-state-metrics.git` and `ca3c2e3ded4f9845ff9b025cacd1c17331c07496`. All links below refer to that SHA.

- E1: Scope and reflector startup: internal/store/builder.go:636 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/store/builder.go#L636-L673 and internal/store/builder.go:744 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/store/builder.go#L744-L761.
- E2: UID publication, deletion, replacement, and resource-version tracking: pkg/metrics_store/metrics_store.go:91 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_store.go#L91-L105 and pkg/metrics_store/metrics_store.go:160 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_store.go#L160-L238.
- E3: API-call instrumentation: pkg/watch/watch.go:108 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/watch/watch.go#L108-L175.
- E4: Watch control events: pkg/sharding/listwatch.go:170 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/sharding/listwatch.go#L170-L185.
- E5: Generation contract and local evaluation failures: pkg/metric_generator/generator.go:28 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metric_generator/generator.go#L28-L39 and pkg/customresourcestate/registry_factory.go:696 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/customresourcestate/registry_factory.go#L696-L746.
- E6: Scrape lock boundary and mutable traversal: pkg/metricshandler/metrics_handler.go:224 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metricshandler/metrics_handler.go#L224-L235 and pkg/metrics_store/metrics_writer.go:99 - https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_writer.go#L99-L135.

## Lessons

1. Use UID for replacement and deletion. A delayed deletion of an old incarnation must not remove a new object with the same name.
2. Build a replacement snapshot off to the side, then publish it in one operation. A failed or incomplete list must not delete objects through partial replacement.
3. Track source observations per watched resource and scope: initial synchronization, LIST failures, watch establishment, observed stream errors, and stream closure. Report these as observations, not proof that the API state is current.
4. Keep object evaluation failures separate. A malformed object does not make unrelated objects or the API connection unhealthy.
5. Retain current publications through quiet periods. Deletion and completed list reconciliation remove missing objects. A source failure changes source health; it does not start per-object expiry timers.

## What NOT to copy

Do not copy clear-then-repopulate replacement or family-by-family reads of a mutable map when immutable scrape snapshots are required. Do not equate watch-call success, the last resource version, or lack of recent events with complete source freshness. Do not add per-object proofs, expiry deadlines, or dependency synchronization between independent Pod and workload publications. The inspected code supports current-state publication; it does not establish those stronger guarantees.

This is source-based research. No tests were run and no Karta implementation was inspected for this report.
