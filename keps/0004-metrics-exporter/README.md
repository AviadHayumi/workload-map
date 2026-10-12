<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# KEP-0004: Karta metrics exporter

- Status: provisional
- Authors: @AviadHayumi
- Issue: TBD before submission
- Created: 2026-10-12
- Last updated: 2026-10-12

## Summary

The proposed Karta metrics exporter reads definitions, workloads and pods from
the Kubernetes API. It uses the Karta library to interpret these objects and
exposes pod attribution, workload state and exporter health at `/metrics`.

Prometheus scrapes these metrics and joins attribution with existing GPU, CPU
and memory measurements. Grafana queries Prometheus to show usage by workload
and component.

![Karta uses Kubernetes objects to expose pod ownership and workload state. Prometheus joins these metrics with resource usage, and Grafana displays workload and component views.](metrics-flow.png)

This draft defines a proposed metrics contract, not an implemented exporter.
Discovery, caching and deployment details remain open.

## Motivation

A JobSet has busy prefill pods and decode pods with little GPU activity.
Grouping them requires operator-specific labels. Karta already describes those relationships, enabling
common dashboards and capacity investigation. Normalized state adds context
about running, completed or degraded work.

### Goals

Expose attribution, normalized state and exporter health through a small Prometheus contract.

### Non-goals

Collect usage measurements, choose optimization thresholds, or design exporter logic.

## Proposal

### Workload metrics

The exporter must provide these two workload gauges. Workload labels are: `namespace`, `workload` (name),
`workload_group` (API group) and `workload_kind`.

| Metric | Additional labels | Value |
|---|---|---|
| `karta_pod_workload_info` | `pod`, `uid` (pod UID), `component`, `component_instance` | Always `1`: identifies the pod's workload, component and named instance. Carries labels, not usage. |
| `karta_workload_status` | `phase` | `1` if a normalized phase matches, otherwise `0`. Workload-level state, including workloads with no pods; not component health. |

For JobSet, `component="replicatedjob"` identifies the Karta component;
`component_instance="prefill"` or `"decode"` identifies a named instance.
Group by both labels to keep prefill and decode separate.
An empty instance means no named instances. An unresolved component sets both
component labels to `<unknown>`; an unresolved instance sets only
`component_instance` to `<unknown>`. An unresolved workload has no mapping.

Each scrape contains at most one mapping per `namespace,pod`, to the outermost
described workload, such as JobSet rather than its child Job. Ambiguous ownership
has no mapping. Replace changed attribution, including old UIDs; remove deleted pods.

Status uses Karta's existing phase names: `Initializing`, `Running`, `Completed`, `Failed`,
`Degraded`, `Suspended`, `Suspending`, `Resuming` and `Undefined`.
After successful evaluation, emit every phase. Phases are independent, not exclusive;
definitions decide which match.
`Undefined` alone is `1` when evaluation succeeds but no other phase matches;
it does not signal an evaluation error. A missing status definition produces no
series and is not an error. Evaluation failure produces no series, increments
the error counter and marks the kind unhealthy. Absent status is unknown, not zero.

### What these metrics enable

`karta_pod_workload_info` supplies the labels needed to group existing measurements.
Its value of `1` preserves usage when multiplied in a Prometheus label join.
`karta_workload_status` adds lifecycle context, displayed by workload labels.
`Running` does not mean busy.

| Question | Measurement joined with Karta attribution |
|---|---|
| Which component instance uses CPU? | cAdvisor `container_cpu_usage_seconds_total`, converted to cores with `rate()`. |
| How much memory does it hold? | cAdvisor `container_memory_working_set_bytes`. |
| Which component instance has low GPU activity? | DCGM `DCGM_FI_DEV_GPU_UTIL`, where supported and attributed to pods. |

Grouping by workload, component and instance separates prefill, decode and frontend
without operator-specific queries. Frontend may use only CPU; missing GPU data is
not zero utilization.

These two metrics alone cannot detect idleness. For example, Karta maps
`decode-0` to `component="replicatedjob", component_instance="decode"`. DCGM reports
GPU activity for `decode-0`; the join assigns that activity to decode. The mapping
stays `1` whether the pod is busy or idle. Inspect all pods in the instance over time.

No Karta idle flag is proposed. Low CPU/GPU activity is an observation, not proof
of no useful work. Memory occupancy is not activity. Requests, queue depth and
latency from the application provide demand context before scaling decisions.

<details>
<summary>Query example: CPU by workload, component and instance</summary>

For one cluster, with one current mapping per pod and deduplicated usage series.
Duplicate scrapes inflate sums; Karta does not deduplicate usage:

```promql
sum by (namespace, workload_group, workload_kind, workload,
        component, component_instance) (
  rate(container_cpu_usage_seconds_total{
    namespace!="", pod!="", container!="", container!="POD"
  }[5m])
  * on (namespace, pod)
    group_left (workload_group, workload_kind, workload, component, component_instance)
    karta_pod_workload_info{
      component!="<unknown>", component_instance!="<unknown>"
    }
)
```

The result is CPU cores per component instance. Multiplication by `1` copies Karta's
labels onto each container measurement; the outer sum combines containers and pods.

For memory, replace `rate(...)` with `container_memory_working_set_bytes` using the
same filters. For GPU activity, use `DCGM_FI_DEV_GPU_UTIL{namespace!="",pod!=""}`
and replace the outer `sum` with `avg`. This averages observed GPU series equally, not
pods or requested capacity. Plot it over time to investigate sustained low activity.
Shared-device measurements may not isolate a pod's usage.
Use valid measurements at the appropriate device or GPU-instance scope.

Grafana can run these queries directly. Optional Prometheus recording rules save
the results; they do not require additional Karta exporter metrics.

</details>

### Consumption limits

The initial contract covers one cluster. Pods must be in the same namespace as
the workload object; cross-namespace attribution is unsupported. For pod-based
joins, sources must supply the measured pod's `namespace` and `pod` labels;
relabel existing identity labels if needed. Scraping must preserve these labels
on both sides of the join; exporter-target labels must not replace them.
DCGM needs Kubernetes pod attribution enabled.

Name-based joins do not protect against old telemetry during pod recreation,
even though the mapping has `uid`. The example excludes unmatched pods and
pods with an unknown component or instance from its per-instance totals.
Missing data is not zero, and totals may be partial.
The workload metrics and `up=1` do not prove completeness or freshness.
Exporter health must be monitored separately.

### Exporter health metrics

These three health metrics are required by the proposed contract.
`karta_workload_status{phase="Failed"}` reports workload failure, not exporter
failure. Prometheus `up` reports scrape success only.

| Metric | Type and labels | Meaning |
|---|---|---|
| `karta_exporter_resource_healthy` | Gauge: `group`, `version`, `kind` | `1` when the required kind is fully observed and evaluated within the declared scope; `0` during startup, errors, partial results or stale observation. |
| `karta_exporter_errors_total` | Counter: `stage`, `reason` | Failed operation attempts, including retries. |
| `karta_exporter_resource_last_success_timestamp_seconds` | Gauge: `group`, `version`, `kind` | Last verified successful observation of that kind; `0` until the first success. |

Deployment configuration must declare required namespaces and kinds, including
Pods and every kind needed for ownership, attribution or status. Emit health rows
for each required kind even if discovery or watch creation fails. Missing
permissions, unavailable API versions, unresolved required owners and evaluation
failures keep the affected kind unhealthy. Optional catalog kinds outside this
scope do not count as failures. Recovery requires verified success.

Freshness must be verified even in a quiet cluster. Scrapes, cached reads and
other kinds' events must not advance the timestamp. Scope configuration,
verification, freshness limits and recovery mechanisms remain TBD. Health does
not prove coverage of types with no Karta; keep unmatched telemetry visible as unknown.

Keep error labels bounded: stages are `discovery`, `collection`, `attribution`
and `status`; reasons are `forbidden`, `unavailable_type`, `unresolved`,
`evaluation`, `api` and `other`. Put object names and error text in logs.

<details>
<summary>Example monitoring queries</summary>

```promql
# A required resource is currently unhealthy.
karta_exporter_resource_healthy == 0
```

```promql
# Recent errors, grouped by where and why they happened.
sum by (stage, reason) (increase(karta_exporter_errors_total[5m])) > 0
```

```promql
# No verified success within the example five-minute freshness budget.
time() - karta_exporter_resource_last_success_timestamp_seconds > 300
```

Also monitor failed scrapes and missing expected health series. For a single
exporter target, using its actual configured job label:

```promql
(up{job="karta-exporter"} == 0)
or absent(up{job="karta-exporter"})
or absent(karta_exporter_resource_healthy{job="karta-exporter"})
or absent(karta_exporter_resource_last_success_timestamp_seconds{job="karta-exporter"})
```

These are example expressions, not installed alerts. Check individual missing
kinds against the declared scope; the absence queries above detect whole missing
metric families. Choose alert durations and freshness budgets for the deployment.
Monitor DCGM, cAdvisor and application telemetry separately. Missing, unhealthy
or stale required health signals make dependent attribution and status unknown.
Partial or stale telemetry cannot support an idle verdict.

</details>

<details>
<summary>Why keep attribution separate from usage?</summary>

This follows the info-metric pattern used by kube-state-metrics: identity is exposed
as labels on a value of `1`, while measurements remain in their existing exporters.
Prometheus combines them by matching pod identity. Karta adds workload meaning
without collecting or duplicating resource usage.

</details>

<details>
<summary>Suggested metrics, outside the minimum contract</summary>

- Desired replicas per component instance: expected scale, even when no pods exist.
- Observed pod counts: only when existing series cannot supply the needed breakdown.
- Creation timestamp and generation: recreation and spec changes.
- Workload info: selected Karta definition and API version.

These need separate consumer justification. For usage relative to requested CPU
or memory, existing Kubernetes resource-request metrics can use the same pod mapping.

</details>

### Exporter logic: template

- Definition selection and discovery: TBD.
- Ownership, attribution and pod recreation: TBD.
- Permission checks, observation freshness, partial results and recovery: TBD.
- Deployment, scraping and resource limits: TBD.

## Compatibility and migration

No Karta schema change. Proposed names and labels are experimental and may change
until promoted to a stable contract with a migration policy.

## Test plan

Use existing JobSet, DynamoGraphDeployment and PyTorchJob catalog definitions to
check attribution, unique ownership, phases, missing/deleted objects and joins.
When logic is designed, test revoked permissions, unavailable API versions,
unresolved owners, evaluation failures and recovery. Unrelated events must not
make a failed resource healthy or refresh its last-success timestamp.

## Risks and alternatives

Limit labels to defined identity fields to control series growth. Per-operator
queries remain an alternative but repeat Karta's knowledge.

## History

- 2026-10-12: Metrics contract proposed; exporter logic deferred.
