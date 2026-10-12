<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# KEP-0004: Karta metrics exporter

- Status: provisional
- Authors: @AviadHayumi
- Issue: TBD before submission
- Created: 2026-10-12
- Last updated: 2026-10-12

## Summary

Expose pod-to-workload attribution and normalized state for workloads covered
by Karta definitions. Prometheus joins attribution with existing GPU, CPU and
memory measurements; Grafana groups usage by workload and component. This draft
defines the proposed exporter's metrics. Logic and deployment remain undecided.

## Motivation

A JobSet has busy prefill pods and idle decode pods. Grouping them requires
operator-specific labels. Karta already describes those relationships, enabling
common dashboards and capacity investigation. Normalized state adds context
about running, completed or degraded work.

### Goals

Expose attribution and normalized state through a small Prometheus contract.

### Non-goals

Collect usage measurements, choose optimization thresholds, or design exporter logic.

## Proposal

### Required metrics

The exporter must provide these two gauges. Workload labels are: `namespace`, `workload` (name),
`workload_group` (API group) and `workload_kind`.

| Metric | Additional labels | Value |
|---|---|---|
| `karta_pod_workload_info` | `pod`, `uid` (pod UID), `component`, `component_instance` | Always `1`: a pod's workload and component attribution. |
| `karta_workload_status` | `phase` | `1` if a normalized phase matches, otherwise `0`. Includes described workloads with no pods. |

For JobSet, `component="replicatedjob"` identifies the Karta component;
`component_instance="prefill"` or `"decode"` identifies a named instance.
An empty instance means no named instances. An unresolved component sets both
component labels to `<unknown>`; an unresolved instance sets only
`component_instance` to `<unknown>`. An unresolved workload has no mapping.

Each scrape contains at most one mapping per `namespace,pod`, to the outermost
described workload, such as JobSet rather than its child Job. Ambiguous ownership
has no mapping. Replace changed attribution, including old UIDs; remove deleted pods.

Status uses Karta's existing phase names: `Initializing`, `Running`, `Completed`, `Failed`,
`Degraded`, `Suspended`, `Suspending`, `Resuming` and `Undefined`.
Emit every phase. Phases are independent, not exclusive; definitions decide which match.
`Undefined` alone is `1` when evaluation succeeds but no other phase matches;
it does not signal an evaluation error. A missing status definition or evaluation
failure produces no status series. Absence is not zero.

### Consumption and limits

Prometheus joins mappings with DCGM GPU or cAdvisor CPU/memory measurements.
Decode's measurements then carry its workload, component and instance labels.
Recording rules can save the query for Grafana. Karta does not copy usage values.

This contract covers one cluster and workloads whose pods share their namespace.
Usage sources must supply `namespace` and `pod` labels; relabel existing
pod-identity labels if needed. Join on `namespace,pod`.
`uid` identifies the pod incarnation but does not protect this name-based join
from old telemetry during recreation; that behavior needs an implementation design.

Joins drop unmatched samples, so totals can be partial. Missing series cannot
distinguish deletion from observation failure. Neither these metrics nor `up=1`
proves completeness or freshness. They support inspection, not automated safety
decisions. Health signals are outside this draft and must be designed before
implementing the exporter.

<details>
<summary>Why this pattern?</summary>

[kube-state-metrics](https://github.com/kubernetes/kube-state-metrics/blob/main/docs/design/metrics-best-practices.md)
groups identity in `_info` metrics, separates changing state and favors raw facts
over derived totals. Karta applies that pattern to workload relationships.
[Prometheus label joins](https://prometheus.io/docs/prometheus/latest/querying/operators/#many-to-one-and-one-to-many-vector-matches)
attach those relationships to existing measurements.

</details>

<details>
<summary>Suggested metrics, outside the minimum contract</summary>

- Desired replicas per component instance: requested scale.
- Observed pod counts: only when existing series cannot supply the needed breakdown.
- Creation timestamp and generation: recreation and spec changes.
- Workload info: selected Karta definition and API version.

These need separate consumer justification.

</details>

### Exporter logic: template

- Definition selection and discovery: TBD.
- Ownership, attribution and pod recreation: TBD.
- Permissions, freshness, partial results and health metrics: TBD.
- Deployment, scraping and resource limits: TBD.

## Compatibility and migration

No Karta schema change. Proposed names and labels are experimental and may change
until promoted to a stable contract with a migration policy.

## Test plan

Use existing JobSet, DynamoGraphDeployment and PyTorchJob catalog definitions to
check attribution, unique ownership, phases, missing/deleted objects and joins.
Add failure and recovery tests when exporter logic is designed.

## Risks and alternatives

Limit labels to defined identity fields to control series growth. Per-operator
queries remain an alternative but repeat Karta's knowledge.

## History

- 2026-10-12: Metrics contract proposed; exporter logic deferred.
