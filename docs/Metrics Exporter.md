<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter

The exporter publishes workload identity, workload state and observation health.
Karta definitions interpret the workload APIs; the exporter follows Kubernetes
controller ownership to select the highest described root. It does not collect
GPU, CPU or memory usage. Consumers join those existing measurements with the
identity series. The public contract is [KEP-0001](keps/0001-metrics-exporter/README.md).

## Deploy

The exporter runs in a separate Deployment from the Karta controller. For example:

```yaml
exporter:
  enabled: true
  replicaCount: 1
  namespace: team-a
  metricsExporter: prometheus
  useCatalog: true
  ignoredOwnerKinds: []
  requiredKinds: []
  metrics:
    workloadPhases: ["all"]
  serviceMonitor:
    enabled: true
    interval: 30s
    scrapeTimeout: 25s
```

Install these values with the Karta chart. exporter.namespace selects the namespace
to observe; an empty value observes all namespaces. The Helm release namespace
controls where the exporter is installed. Cluster Karta definitions override the
built-in catalog for the same group and kind. The catalog is disabled by default.

Prometheus scrapes the metrics Service at /metrics on port 8080 by default.
The optional ServiceMonitor requires its exact CRD and a matching Prometheus
selector. The chart does not install recording rules or an OpenTelemetry Collector.

Use exporter.rbac.workloadRules to grant get, list and watch for workload and
intermediate-owner resources. Additional read-only ClusterRoles labeled
karta.run.ai/exporter-rbac: "true" are aggregated into the exporter role.
The exporter also reads cluster-scoped Karta definitions. Resources, scheduling,
Service, service account and network policy settings remain in
[values.yaml](../charts/karta/values.yaml).

## Three metric families

All three families are gauges:

| Metric | Meaning |
| --- | --- |
| karta_pod_workload_info | One value-1 identity row for each attributed Pod. |
| karta_workload_status | A 0/1 row for each selected normalized phase of a root workload. |
| karta_exporter_observation_healthy | A 0/1 observation result for an API type or a root workload, with a bounded reason. |

Attribution and status retain namespace, workload name, group, version, kind,
workload_uid and owner_resolution. Attribution adds pod, pod_uid, component,
component_instance and replica_group_id. Status adds phase, karta and karta_source.
Health uses scope, namespace, group, version, kind, workload, workload_uid and reason.
The KEP defines each label and supplies complete examples.

Both UIDs are Kubernetes metadata.uid. A same-name replacement is a different
identity. workload_version identifies the selected definition's API version.
replica_group_id identifies a repeated group, not a count of replicas. Empty
component_instance and replica_group_id values are valid when grouping does not
apply. Do not remove either UID to reduce cardinality.

Root workloads with a status definition receive status even when they have no Pods.
A definition without status produces no status rows. Root health does not require Pods. A described
child beneath another described root does not publish independent root status.
A bare Pod has no default workload attribution. Owner walks follow controller
references within the namespace, verify UIDs, and stop after eight edges.

The default phase selection emits nine rows per root with status: Initializing, Running,
Completed, Failed, Degraded, Suspended, Suspending, Resuming and Undefined.
Several phases may match together. Undefined is used when no other phase matches.
Use exporter.metrics.workloadPhases to select named phases, ["all"] or ["none"].
Selection does not change attribution or health.

## Faults and recovery

Observation health is separate from application state. A Failed workload can be
observed correctly. A successful Prometheus scrape does not establish that the
exporter can still read the APIs it needs.

scope="resource_type" reports collection health before any workload name is known;
its workload identity fields are empty. scope="workload" includes the root UID
and covers interpretation and dependent observations. The failure reasons are
permission_denied, api_unavailable, invalid_definition, evaluation_failed and
unresolved_owner. A successful fresh verification replaces the previous reason
row with reason="none" and value 1. Missing health is unknown.

Collection faults invalidate their dependents. A missing, mismatched or unreadable
owner with an unknown root conservatively invalidates known roots in that namespace.
Unrelated observations remain usable. Recovery requires fresh verification; a scrape
or unrelated object event cannot establish recovery. Deleted objects lose their
current rows, while retained Prometheus samples remain available as history.

exporter.requiredKinds lists exact group/version/kind values for APIs that must be
observable even before discovery succeeds. Core kinds use version/kind. Missing
optional catalog APIs can be skipped; explicit required APIs cannot. Discovery is
retried without an exporter restart.

exporter.ignoredOwnerKinds stops the walk before reading an exact owner GVK. For
Pod -> Job -> JobSet -> PlatformWrapper, ignoring the wrapper selects JobSet and
sets owner_resolution="ignored_owner". Without a described root below the boundary,
no attribution is emitted. Missing owners below the boundary remain faults. The
same boundary applies to attribution, root status and health.

/metrics stays available during incomplete startup so failures can be observed.
/readyz reports initial informer synchronization. The metrics endpoint exposes
workload metadata; use the existing network policy settings for ingress control.

## Queries and Grafana

Grafana queries the Prometheus data source. A simple observation-failure panel is:

```promql
karta_exporter_observation_healthy{scope="workload"} == 0
```

The KEP contains complete CPU, memory, GPU and low-utilization examples, including
identity uniqueness and observation-health guards. The tested optional attribution
recording rule is in
[exporter/test/rules/kep-rules.yaml](../exporter/test/rules/kep-rules.yaml).
The platform team owns any installed recording rules, telemetry sources, thresholds
and dashboard queries. Run make exporter-rules-test after changing those examples.

Keep cluster identity on both sides when querying several clusters. Prefer Pod UID
joins when the producing telemetry source supplies that UID. A name-only join cannot
prove that a sample came from the current Pod lifetime. Collapse identical exporter
replicas using the full attribution identity and reject conflicting identities.

A time-window policy needs current health and healthy observations throughout that
window, source freshness, adequate sample coverage and expected device coverage.
Missing telemetry is not zero usage. Low sampled GPU utilization alone does not
prove that an application has no queued demand.

## Delivery

The implementation uses the OpenTelemetry Go API and SDK with a Prometheus reader.
The only supported delivery selection is prometheus. OTEL_METRICS_EXPORTER supplies
the CLI default; an explicit --metrics-exporter overrides it. Unsupported selections
fail startup. Helm uses exporter.metricsExporter.

An external Collector may scrape /metrics and forward data, but that bridge remains
an integration requiring end-to-end label and freshness validation. Native OTLP
HTTP/gRPC and combined delivery are deferred. Installing the SDK does not enable
those modes.

## Capacity and validation

With W roots, P attributed Pods, S selected phases and R observed API types, one
exporter exposes approximately P + S*W + W + R Karta series. At 100,000 roots, the
nine phase rows and workload health alone contribute 1,000,000 series. Each exporter
replica repeats the observations. Pod churn adds historical identities in the backend.

Use namespace scope and phase selection to bound the observed work. Size CPU, memory,
scrape timeout and backend retention from measured workloads. The HTTP handler permits
one metrics request at a time; an overlapping request receives HTTP 503 and can retry
on its next scrape. Set the scrape interval above the measured collection latency.
Health and readiness endpoints remain available during collection. Full Pod objects are
cached by default so custom selectors can read arbitrary fields. Fresh verification
also makes paginated API reads; a fast metrics snapshot does not prove acceptable
API-server load or recovery latency at scale.

[Exporter Acceptance](Exporter%20Acceptance.md) documents the 21 real-operator
fixtures, isolated fault tests and evidence limits. The
[recorded acceptance run](../exporter/test/e2e/results/acceptance-2026-10-14.json)
passed all 21 workload cases, 21 fault checks and four transport checks on arm64
Kubernetes 1.34.3 with two exporter replicas. These CPU/mock cases establish
attribution and observation behavior. The
[scale harness](../exporter/test/scale/README.md) separates synthetic snapshot costs
from API-backed workload tests. Current results and outstanding evidence belong in
the [implementation audit](design/metrics-exporter-implementation.md). A local kind
run and a synthetic 100,000-root benchmark do not establish production capacity.
