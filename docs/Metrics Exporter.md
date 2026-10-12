<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta Metrics Exporter

The exporter makes existing per-pod metrics addressable at workload and
component scope. It watches Karta CRs, the workloads they describe, and
pods, and publishes identity and state series. Recording rules shipped in
the same chart join those series with DCGM and cadvisor telemetry to
produce aggregated `karta:*` series.

The exporter holds no telemetry. It never scrapes DCGM, never queries
Prometheus, and never sees a GPU number. Prometheus evaluates recording rules on its own schedule. Recorded samples
do not preserve the timestamp or freshness of the original observation.

## Enabling

```bash
helm upgrade --install karta charts/karta \
  --set exporter.enabled=true \
  --set exporter.serviceMonitor.enabled=true
```

The recording rules (a PrometheusRule) are rendered by default whenever the
exporter is enabled and the prometheus-operator CRDs are installed.

## Metrics delivery

Karta instruments use the OpenTelemetry Go API and SDK. The first supported
output is the OTel Prometheus reader, served at `/metrics`. Prometheus still
scrapes the endpoint and evaluates the existing recording rules.

```bash
karta-exporter --metrics-exporter=prometheus --metrics-bind-address=:8080
```

`OTEL_METRICS_EXPORTER=prometheus` supplies the default when the flag is absent.
An explicit `--metrics-exporter` overrides that environment variable. When
neither is set, Prometheus is selected. Other values fail before cluster access;
native OTLP, multiple exporters, and `none` are not implemented.

The chart can pass the selector through `exporter.extraArgs`. Existing
`--workload-phases`, `--pod-phases`, `--pod-metrics-mode`, and `--use-catalog`
settings keep their behavior. No new Collector deployment is required.

The Prometheus reader preserves the existing names and labels. Scope and target
metadata are disabled on Karta series. Each collection observes a current store
snapshot, so obsolete mappings are not retained by the SDK. The provider disables
the SDK cardinality limit because overflow would discard pod identity. Memory
use still grows with the watched scope; this setting is not a memory bound.
Go and process metrics retain their existing Prometheus collectors.

Adding native OTLP later requires explicit configuration and tests for identity,
deletion, freshness and backend label translation. Installing the OTel SDK does
not enable that output automatically.

## Metric contract

Every workload-scoped series carries `namespace`, `workload`,
`workload_kind`, `workload_group`, and `workload_uid`. The workload UID is
Kubernetes `metadata.uid`; the attribution metric's `uid` is the Pod's
`metadata.uid`. Neither is computed from a name. Recreating an object with
the same name creates a new identity and a new time series.

Adding a label changes time-series identity. Roll out the exporter and
rules together, update explicit joins to include `workload_uid`, and allow
old unlabeled samples to age out of range queries. Name-only dashboards
continue matching, but can display both incarnations in historical views.

| Metric | Labels beyond the common set | Meaning |
| --- | --- | --- |
| `karta_pod_workload_info` | `pod`, `uid`, `component`, `component_instance`, `replica` | One series per attributed Pod, value 1. Inventory and both modes also add `pod_phase`. |
| `karta_workload_info` | `workload_version`, `karta` | Identity and provenance, value 1. `workload_version` lives only here so a CRD storage-version bump does not split dashboards. |
| `karta_workload_status` | `phase` | One 0/1 series per normalized phase. All selected phases are present, including zero; several can be 1 at once (for example Running and Degraded). Zero means not in the normalized matched set. Absent without a StatusDefinition or when disabled. |
| `karta_workload_component_replicas` | `component`, `component_instance` | Desired scale from the definition's replica selector. Units can be Pods, groups, or another operator-defined unit; absence means no replica selector result, not zero. |
| `karta_workload_component_pods` | `component`, `component_instance`, `phase` | Observed Pod count per selected phase, zero-filled for each declared or observed component instance. Counts and both modes only. Running is not Ready. |
| `karta_workload_component_info` | `component`, `component_instance` | Inventory, value 1, including empty declared instances. Inventory and both modes only. |
| `karta_workload_generation` | none | `metadata.generation`; useful for correlating a spec change with usage. |
| `karta_workload_created_timestamp_seconds` | none | `metadata.creationTimestamp` as unix seconds. A new workload UID gets its own creation anchor; absent when the field is unset. |

Sentinel values: `component="<unknown>"` means component inference failed
for that pod; `component_instance="<unknown>"` means the instance id did
not match any declared instance. Sentinel pods stay in workload-level
aggregates and are excluded from component-level rules. A plain empty
`component_instance=""` is a legitimate single-instance component, not a
failure.

Recorded aggregate names (part of the same contract):

```text
karta:gpu_utilization:{workload|component|component_instance}
karta:gpu_memory_used_bytes:{workload|component|component_instance}
karta:gpu_memory_total_bytes:{workload|component|component_instance}
karta:cpu_usage_cores:{workload|component|component_instance}
karta:memory_working_set_bytes:{workload|component|component_instance}
karta:join_coverage:ratio
```

GPU memory "total" is FB_USED plus FB_FREE, since DCGM's FB_TOTAL counter
is not in dcgm-exporter's default counter list. Utilization is the mean of
arriving GPU observations, not an allocated-GPU-weighted workload percentage.
Memory is summed as reported. Duplicate scrapes or shared GPU telemetry can
inflate results; source deduplication and allocation policy belong upstream. CPU and memory come from
cadvisor: metrics-server is an API rather than a Prometheus exposition
source, and node exporter is node-granular and cannot be attributed per
pod.

Self-observability series (not part of the consumer contract):
`karta_exporter_kartas`, `karta_exporter_workloads`,
`karta_exporter_unattributed_pods`, `karta_exporter_attribution_errors_total`,
`karta_exporter_last_event_timestamp_seconds`.

## Example queries

Component-instance imbalance (prefill vs decode):

```promql
karta:gpu_utilization:component_instance{workload="my-jobset"}
```

Idle workload detection (the consumer defines the threshold and window,
the exporter does not):

```promql
max_over_time(karta:gpu_utilization:workload[15m]) < 5
```

Time in phase:

```promql
avg_over_time(karta_workload_status{phase="Running"}[1h])
```

Desired scale minus Running Pods (only where the definition counts Pods):

```promql
karta_workload_component_replicas
  - on (namespace, workload, workload_group, workload_kind, workload_uid, component, component_instance)
    karta_workload_component_pods{phase="Running"} > 0
```

## Duration signals

Policies like "idle for X", "running for X", and "completed X ago" are
built from the series above at query time. The exporter never emits a
duration, an age, or a judged value; the anchor is the created timestamp
gauge and the `time() - gauge` idiom, and the state series carry the
history. Thresholds, windows, and tolerances belong to the consumer.

Density floor: with evaluation interval I and window X, the expected
sample count is E = X/I; require at least E minus the number of skipped
evaluations you tolerate. At I=30s and X=15m, E=30, so `>= 29` tolerates
one skipped evaluation. This is a count of recorded observations, not proof of uninterrupted state
or telemetry freshness. A tolerated gap can hide a transition; repeated
recordings can reuse an older raw sample. Destructive policies need their
own source-freshness and workload validation.

GPU idle for 15 minutes (threshold and window are the consumer's):

```promql
(
    max_over_time(karta:gpu_utilization:workload[15m]) < 2
  and
    count_over_time(karta:gpu_utilization:workload[15m]) >= 29
)
and on (namespace, workload, workload_kind, workload_group, workload_uid)
  karta_workload_status{phase="Running"} == 1
and on (namespace, workload, workload_kind, workload_group, workload_uid)
  (time() - karta_workload_created_timestamp_seconds) > 900
```

The Running gate restricts results to a currently observed Running workload.
The age gate requires an object old enough for the selected window.
`workload_uid` separates same-name recreations; retain it throughout joins
and aggregation. A source without Pod UID can still be wrongly associated
with a newly created Pod using the old name. Coverage is useful for checking
attribution, but cannot prove telemetry freshness or complete GPU coverage.

Running in the recorded observations over 4 hours:

```promql
min_over_time(karta_workload_status{phase="Running"}[4h]) == 1
  and count_over_time(karta_workload_status{phase="Running"}[4h]) >= 440
  and on (namespace, workload, workload_kind, workload_group, workload_uid)
    (time() - karta_workload_created_timestamp_seconds) > 14400
```

Any sampled dip out of Running prevents a match for that window. Degraded turning 1
alongside Running does not: status is multi-valued and the Running
series stays 1. An observed-sample estimate of time in phase is
`avg_over_time(karta_workload_status{phase="Running"}[4h]) * 4 * 3600`.

Observed Completed over a 24-hour window:

```promql
min_over_time(karta_workload_status{phase="Completed"}[24h]) == 1
  and count_over_time(karta_workload_status{phase="Completed"}[24h]) >= 2640
  and on (namespace, workload, workload_kind, workload_group, workload_uid)
    (time() - karta_workload_created_timestamp_seconds) > 86400
```

This checks recorded state, not an exact completion event timestamp. A Karta
status definition does not universally guarantee that Completed is terminal.
Gaps, source freshness, selection settings, and retention constrain all
these recipes. Workloads without a StatusDefinition have no status family.
Validate the current object before acting on a policy result.

Do not build `timestamp(metric > N)` self-referential latch rules for
these signals: a latch silently resets on a Prometheus restart, on a
skipped evaluation, and on a missing series, and each reset restarts the
timer without any signal that it happened. The fixtures for the three
recipes above live in `exporter/test/rules/duration-tests.yaml` and must
change together with this section.

## Selecting output

Defaults retain all phases, direct Pod counts, and all recording rules.
Selection removes entire dimensions that a consumer does not use:

```yaml
exporter:
  metrics:
    workloadPhases: [Running, Completed, Failed]
    podPhases: [Running, Pending]
    podMetricsMode: counts
  prometheusRule:
    resources: [cpu, memory]
    levels: [workload, component]
```

Use `["all"]` for every phase or `["none"]` to disable a phase family.
Unknown or duplicate phase names fail startup. CLI equivalents are
`--workload-phases=Running,Completed,Failed`, `--pod-phases=Running,Pending`,
and `--pod-metrics-mode=counts`. Filtering phases does not remove inventory
or desired scale. Resource and level selection affects recording rules only.

The optional `inventory` mode replaces the five phase-count rows per
component with one `karta_workload_component_info` row, and adds `pod_phase`
to each Pod mapping. `both` emits both forms for comparison. An unset Pod
phase stays empty; it is not converted to Kubernetes Unknown. The
`podPhases` selection applies to direct counts, not Pod attribution.

For example, derive Running Pods for each known inventory key:

```promql
count by (namespace, workload, workload_kind, workload_group, workload_uid, component, component_instance) (
  max by (namespace, pod, uid, workload, workload_kind, workload_group, workload_uid, component, component_instance) (
    karta_pod_workload_info{pod_phase="Running"}
  )
)
or
0 * max by (namespace, workload, workload_kind, workload_group, workload_uid, component, component_instance) (
  karta_workload_component_info
)
```

Inventory supplies zeros for known empty instances. It cannot prove zero
when the exporter is unavailable or its watches are incomplete. For
conflicting exporter replicas, apply the uniqueness guard described below
before counting. Inventory mode is an opt-in API change: consumers of
`karta_workload_component_pods` need this replacement query or a recording
rule. Compare total retained series, recording cost, and query latency;
phase changes create historical Pod series even if live series are fewer.

## Joining telemetry

The exporter does not depend on DCGM or cAdvisor. Consumers without
recording rules still receive identity, status, desired scale, and the
selected Pod representation. The chart contains the tested join expressions
for GPU, CPU, and memory; copy those expressions when querying directly.

Rules first collapse identical exporter replicas using the full attribution
identity (including both UIDs and component assignment). They require one
identity for the selected join key. Conflicting identities are suppressed
at every rollup level, rather than choosing one or multiplying usage.
`pod_phase` is not part of this identity or any resource recording.

Default matching uses `(namespace, pod)` for source compatibility. It
cannot distinguish old telemetry from a replacement Pod with the same name
when only one mapping is visible. When every selected telemetry source
carries the producing Pod UID, normalize its label to `uid` and configure:

```yaml
exporter:
  prometheusRule:
    joinLabels: [namespace, pod, uid]
```

Do not manufacture source UID by looking up the current Pod name. Do not
turn on UID matching for sources missing that label: unmatched data will
be absent. One Prometheus scraping multiple clusters also needs `cluster`
on both source and mapping, in `joinLabels`, and in rollup groupings; the
shipped grouping assumes one cluster.

## Attribution health

`karta:join_coverage:ratio` is the fraction of GPU series that join the
identity series unambiguously. It is zero when arriving GPU samples have
no valid mapping, and absent when no GPU samples arrive. A drop can mean
unmanaged Pods, conflicting attribution, or mismatched source labels such
as `exported_pod`. It does not detect a device that sends no telemetry and
is not a source-freshness check. The
source metric names and join labels are Helm values
(`exporter.prometheusRule.*`), so a mismatch is fixed by a values change,
not a release.

`karta_exporter_unattributed_pods{reason}` shows pods the exporter could
not fully attribute: `no_owner` (the owner chain dead-ends; usually a kind
missing from the Karta's `additionalChildKinds`), `unknown_instance`, and
`jq_error`.

## RBAC for custom Kartas

The exporter's ClusterRole enumerates resources; there are no wildcards.
The bound role aggregates every ClusterRole labeled
`karta.run.ai/exporter-rbac: "true"`. To grant access for a custom Karta:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: karta-exporter-mykind
  labels:
    karta.run.ai/exporter-rbac: "true"
rules:
  - apiGroups: ["example.io"]
    resources: ["mykinds"]
    verbs: ["get", "list", "watch"]
```

Default readable kinds are in `exporter.rbac.workloadRules` in values.yaml.

## Scoping and behavior notes

- By default the exporter serves only workloads whose Karta CR is applied
  to the cluster. With `exporter.useCatalog=true` the built-in catalog
  definitions cover workload kinds that have no CR; a Karta CR always
  overrides the catalog entry for its kind.
- One Karta serves each (group, kind). When two Kartas target the same
  kind, the oldest wins and the others surface as
  `karta_exporter_kartas{valid="false", reason="shadowed"}`.
- The pod cache is trimmed to metadata, `spec.nodeName`, and
  `status.phase`. A custom Karta whose pod selectors read other pod fields
  needs `exporter.fullPodCache=true`; without it, its pods surface only as
  unattributed.
- `/readyz` stays false until all informers sync, so a restarting exporter
  is a visible scrape gap, never plausible zeros.
- The metrics endpoint serves cluster-wide workload identity and is
  unauthenticated. Enable `exporter.networkPolicy.enabled` to restrict
  ingress; authenticated serving is a planned follow-up.

## Cardinality

For W workloads with status, P attributed Pods, and C component instances,
the two dense families contribute up to 9W + 5C series. Reducing selected
phases directly reduces this cost. Inventory mode replaces 5C with C but
adds phase churn to P mappings. Recording rules can erase that saving if
all five counts are materialized again. Benchmark the complete deployment
before choosing a mode.

Keep both UIDs when reducing cardinality. Dropping identity labels can
merge different objects or create duplicate samples. Prefer selecting
unused phases, resources, and recording levels instead.
