<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter configuration

Proposed configuration for [KEP-0001](../../keps/0001-metrics-exporter/README.md).
The exporter runs separately from the Karta controller. Helm configures its
definitions, observation scope and metric delivery.

## Helm values

```yaml
exporter:
  enabled: true # Deploy the exporter; default false.
  image:
    tag: "<exporter-tag>" # Exporter image version.
  metricsExporter: prometheus # Default: prometheus. Set otlp for native push.
  otlp: # Used only when metricsExporter is otlp.
    protocol: http/protobuf # Or grpc.
    endpoint: https://metrics.example.com:4318/v1/metrics
    interval: 30s
    timeout: 10s
    headersSecretRef: {name: karta-otlp-auth, key: headers} # Optional.
    tls: # Optional custom CA and mutual TLS.
      caSecretRef: {name: karta-otlp-tls, key: ca.crt}
      clientCertSecretRef: {name: karta-otlp-tls, key: tls.crt}
      clientKeySecretRef: {name: karta-otlp-tls, key: tls.key}
  http:
    port: 8080 # /healthz and /readyz; also /metrics in Prometheus mode.
  rbac:
    # Read workload roots and intermediate owners; the chart adds get/list/watch.
    # This list replaces the defaults. Include every selected workload type and its owners.
    workloadRules:
      - apiGroups: ["apps"]
        resources: ["deployments", "replicasets", "statefulsets"]
      - apiGroups: ["batch"]
        resources: ["jobs", "cronjobs"]
      - apiGroups: ["jobset.x-k8s.io"]
        resources: ["jobsets"]
  # Definitions describe each workload type. Include/exclude lists use definition names.
  kartaDefinitionSource:
    catalog:
      enabled: true # Built-in definitions; default false.
      include: []
      exclude: [core-pod-v1] # Clear to opt into bare-Pod catalog attribution.
    cluster:
      enabled: true # Cluster-installed Karta definitions; default true.
      include: []
      exclude: []
      labelSelector: {} # Select cluster Karta definitions by their labels.
  # Watch Pods and workload objects only in matching namespaces.
  namespaceSelector: # Default null or {}: all namespaces.
    matchExpressions:
      - key: example.com/tenant # Include namespaces carrying this label.
        operator: Exists
      - key: kubernetes.io/metadata.name
        operator: NotIn
        values: [kube-system, karta-system] # Skip these system namespaces.
  # Match the workload root's metadata.labels; retain owner reads for traversal.
  objectSelector: # Default null or {}: all resolved workload roots.
    matchExpressions:
      - key: example.com/purpose
        operator: NotIn
        values: [reservation]
  # Match each Pod's metadata.labels to select its exported row.
  # All Pods in selected namespaces stay watched.
  podSelector: # Default null or {}: all Pod attribution rows.
    matchExpressions:
      - key: example.com/purpose
        operator: NotIn
        values: [reservation]
  resourceRules: [] # Limit root API types; empty means no restriction.
  excludeResourceRules: [] # Exclusions take precedence.
  # Stop before these GVKs; the last verified object below becomes the workload root.
  ignoredOwnerKinds: # Default [].
    - example.com/v1/PlatformWrapper
  requiredKinds: [] # Optional expected APIs; failed checks mark dependent observations unhealthy.
    # Example: ["jobset.x-k8s.io/v1alpha2/JobSet"]
  metrics:
    workloadPhases: ["all"] # Emit every workload phase as a 0/1 series.
  # Let Prometheus Operator discover and scrape the exporter.
  serviceMonitor:
    enabled: true # Create the optional ServiceMonitor; default false.
    interval: 30s # How often Prometheus scrapes metrics.
    scrapeTimeout: 25s # Maximum duration of one scrape.
```

`exporter.enabled` defaults to `false`. The example enables it and the built-in
catalog. `http.port` serves `/healthz` and `/readyz` in both delivery modes, plus
`/metrics` in Prometheus mode. It also sets the container, probe and Service ports.

## Selection

| Setting under `exporter` | Selects |
| --- | --- |
| `namespaceSelector` | Namespace labels; use the built-in `kubernetes.io/metadata.name` label with `In` to include names or `NotIn` to exclude them. |
| `objectSelector` | Resolved workload roots by their labels. |
| `podSelector` | Pods whose attribution rows are exported, using each Pod's labels. |
| `resourceRules` / `excludeResourceRules` | Root API types, using group, version and plural resource name. Exclusions win. |
| `kartaDefinitionSource.cluster.labelSelector` | Cluster Karta definitions by their labels. |
| Definition-source `include` / `exclude` | Definition names. Empty includes mean all; exclusions win. |

Use [Kubernetes label selectors](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors)
to match each object's own labels. All conditions must match; `NotIn` also matches
missing labels.

Namespace selection limits watches and owner reads to the selected namespaces.

`objectSelector` selects workload roots. `podSelector` selects Pod rows; both must
match for a Pod row to appear. Workload status covers the full workload; joined
usage covers only included Pods. Excluding a root excludes its Pods too, without
treating its children as new roots.

Scope changes remove rows outside the selection. A failed refresh never expands
the scope.

### Watches and owner reads

The runtime always watches Pods in selected namespaces. It starts watches for
workload kinds selected by Karta definitions so workloads with no Pods are found.
Resolving a Pod also starts a shared watch for its root resource and namespace
when no watch already covers them. This applies even without a Karta definition.

Intermediate owners use metadata GETs and a shared cache. Traversing
Pod -> Job -> JobSet does not start a Job watch. Jobs can still be watched when
selected by a Karta definition or discovered as roots elsewhere.

Pod and root events trigger reconciliation. Periodic revalidation checks the
intermediate owners and renews verification. An intermediate change can remain
undetected until that check or another reconciliation. Failed checks or expired
verification invalidate dependent observations; a scrape does not renew them.

### Workload API types

```yaml
exporter:
  resourceRules:
    - apiGroups: ["batch"]
      apiVersions: ["v1"]
      resources: ["jobs", "cronjobs"]
    - apiGroups: ["jobset.x-k8s.io"]
      apiVersions: ["*"]
      resources: ["jobsets"]
  excludeResourceRules:
    - apiGroups: ["batch"]
      apiVersions: ["v1"]
      resources: ["cronjobs"]
```

A root matches its label selector, at least one inclusion rule when configured,
and no exclusion rule. Group, version and resource must all match within a rule;
list entries are alternatives. `"*"` matches any value; `""` is the core API group.
Core Pods use group `""`, version `v1` and resource `pods`.

The fields follow Kubernetes [MatchResources](https://kubernetes.io/docs/reference/kubernetes-api/definitions/match-resources-v1-admissionregistration/),
without admission operations or subresources. Each supplied rule needs nonempty
group, version and resource lists. Invalid selectors, malformed rules and unknown
catalog names reject configuration. Other served API versions do not create
duplicate observations.

### Definition sources and owner boundaries

Source filters apply before precedence. A selected cluster Karta overrides a
selected catalog definition for the same group/kind. Disabled sources contribute
none. Cluster definition names and their label selector must both match. Ties use
oldest creation time, then name.

The catalog excludes `core-pod-v1` by default. Clearing that exclusion, or selecting
a cluster Pod Karta, enables bare-Pod workloads. `objectSelector` also applies to
those roots.

`ignoredOwnerKinds` stops the owner walk before an exact GVK. The last verified
object below it becomes the root; missing owners below that boundary still fail.
A Karta definition on an intermediate owner does not by itself stop the walk.
An ignored owner can still be selected independently. `requiredKinds` checks
remain active; failed checks invalidate dependent observations.

## Metric labels

`metricLabels` adds labels to selected metrics with CEL expressions. This rule
reads a team from the resolved workload root, then falls back to the attributed Pod:

```yaml
exporter:
  metricLabels:
    team:
      metrics: [karta_pod_workload_info]
      sources: [workload, pod]
      expression: |
        has(object.metadata.labels) && "example.com/team" in object.metadata.labels
          ? object.metadata.labels["example.com/team"] : ""
      evaluation:
        events: [create, update]
        mode: continuous
```

`object` is the full source object, including `apiVersion`, `kind`, metadata,
spec and status when present. For a Pod owned by JobSet `train`, the evaluator
first receives that JobSet. Its relevant fields are:

```yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  name: train
  namespace: team-a
  labels:
    example.com/team: research
```

The expression returns `"research"`, adding `team="research"` to the Pod's
`karta_pod_workload_info` row. Sources run in order until one returns a nonempty
string. An empty result or evaluation error tries the next source. Errors are
reported without dropping the base metric. If every source fails or is empty,
the rule has no value.

The evaluator compiles each expression once. Runtime manages source order,
events and cached values. Initial discovery evaluates existing objects. Later
evaluations follow `evaluation.events`. A root update can refresh labels on its
Pods even if those Pods did not change. Collection reads saved labels without
running CEL.

| Mode | Behavior |
| --- | --- |
| `continuous` (default) | Replace the value on each configured event. Remove it if no source returns a value. |
| `untilValue` | Retry until a nonempty value is found, then keep it. Requires update events. A Pod fallback stays fixed even if the root later gets a value. |

Cache values per metric, subject UID and label name. Clear them on deletion,
leaving scope, a changed workload association or changed configuration. Restart
rebuilds the cache.

A rule with a Pod source can target only `karta_pod_workload_info`. Workload-only
rules can also target `karta_workload_status`. No arbitrary Pod supplies labels
for a workload metric. Rules use the existing watch scope; full source objects
are fetched when the watch cache contains only metadata.

`metricLabels` defaults to `{}`. Events default to `[create, update]`. Invalid
configuration fails startup. Statically known non-string expressions are rejected;
dynamic results are checked during evaluation. Built-in labels and reserved Prometheus/OTel names
cannot be replaced. Each expression is limited to 8 KiB, each evaluation to 10,000 CEL cost units,
and each value to 512 bytes. Exceeding a limit is an error. Each new
label value can create another time series.

## Metric delivery

| Mode | Configuration | Requirements |
| --- | --- | --- |
| Prometheus pull | `metricsExporter: prometheus` | Prometheus scrapes `/metrics`. ServiceMonitor is optional. |
| Native OTLP push | `metricsExporter: otlp` | An OTLP receiver; endpoint and protocol under `otlp`. |
| Collector bridge | Prometheus mode, scraped by a Collector | The Collector forwards to the chosen backend. |

Combined native pull and push is deferred. CPU, memory and GPU measurements come
from other exporters; the [query guide](README.md) shows how to join them.

### Prometheus

![Karta provides workload identity; Prometheus joins it with resource measurements for dashboards and other consumers.](../../keps/0001-metrics-exporter/metrics-architecture.png)

`serviceMonitor.enabled: true` creates a ServiceMonitor. Its CRD must be installed
and the Prometheus selector must match it. The Prometheus Operator turns it into
scrape configuration. Without the Operator, Prometheus uses its own scrape configuration.
See [ServiceMonitor discovery](https://prometheus-operator.dev/docs/developer/getting-started/#using-servicemonitors).

Prometheus discovers and scrapes each exporter endpoint separately. A single
load-balanced Service address can hide conflicting observations during a rollout.
Target identity remains available until query aggregation. The HTTP endpoint has
no built-in authentication; access is restricted by network policy or an
authenticated proxy.

### Native OTLP

In OTLP mode, `serviceMonitor.enabled` is `false`. An OTel periodic reader sends
the current metric snapshot, including unchanged values, to `otlp.endpoint`.
HTTP uses `POST /v1/metrics`; gRPC uses `MetricsService.Export`.
For gRPC, the endpoint is `https://metrics.example.com:4317`, without `/v1/metrics`.

`interval` and `timeout` accept positive whole-millisecond durations. The timeout
bounds the collection/export cycle and the client request. Unsupported protocols,
invalid durations and invalid endpoints reject startup.

Authentication and TLS Secrets live in the exporter's namespace. The chart mounts
only the referenced TLS keys read-only at `/etc/karta/otlp`. Kubernetes supplies
the files; no PVC or Secrets API read permission is needed. Missing referenced
Secrets or keys prevent startup.

`headersSecretRef` supplies comma-separated `key=value` headers through a generated
Secret-backed environment variable. HTTPS uses system trust by default. A custom
CA is optional; mutual TLS requires both client certificate and key. Secret
changes require an exporter restart. See [Secret projection](https://kubernetes.io/docs/concepts/storage/projected-volumes/)
and [OTLP settings](https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/).

Removed objects stop appearing in exports. The backend needs expiry or stale-series
handling; omission is not an immediate delete command. Delivery success does not
certify observation validity. See the [OTLP gauge model](https://opentelemetry.io/docs/specs/otel/metrics/data-model/#gauge).

### Collector bridge

An [OTel Collector](https://opentelemetry.io/docs/platforms/kubernetes/helm/collector/)
can scrape `/metrics` and forward the samples. PromQL joins require a compatible
query backend. The Collector and backend control label translation, deletion and
freshness.

## Deployment and permissions

| Value under `exporter` | Purpose |
| --- | --- |
| `rbac.workloadRules` | Read access for workload kinds and intermediate owners. |
| `resources` | CPU and memory requests/limits. |
| `nodeSelector`, `tolerations`, `affinity` | Pod placement. |
| `networkPolicy` | Access to the metrics endpoint. |

The main Helm example includes `rbac.workloadRules`. These rules use API groups
and plural resource names, as in Kubernetes RBAC. The chart supplies `get`, `list`
and `watch` for every listed resource. These grants remain broader than runtime
use: intermediate owners need metadata GETs for traversal, while selected
workload kinds and discovered roots also use LIST/WATCH. See
[watches and owner reads](#watches-and-owner-reads). API versions are selected by
`resourceRules`, not RBAC.

Include the root and every intermediate owner needed for attribution:

| Workload | Workload rules | Why |
| --- | --- | --- |
| Deployment | `apps`: `deployments`, `replicasets` | Pods belong to ReplicaSets, which belong to the Deployment. |
| JobSet | `jobset.x-k8s.io`: `jobsets`; `batch`: `jobs` | Jobs connect the Pods to the JobSet. |
| LeaderWorkerSet | `leaderworkerset.x-k8s.io`: `leaderworkersets`; `apps`: `statefulsets` | StatefulSets connect the Pods to the LeaderWorkerSet. |
| RayJob | `ray.io`: `rayjobs`, `rayclusters`; `batch`: `jobs` | Read the RayCluster and any launcher Job created by the RayJob. |

The main example covers Deployments, StatefulSets, Jobs, CronJobs and JobSets.
Keep permissions aligned with the selected definitions and API types, including
intermediate owners. The list replaces the default workload rules. Pod, Namespace
and enabled definition-source reads are
separate built-in rules. Namespace reads are needed to evaluate namespace labels.
Using an existing ServiceAccount does not disable the chart's role binding.

To add a workload type without replacing the Helm list, an administrator can
create a separate ClusterRole:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: karta-exporter-trainingruns
  labels:
    karta.run.ai/exporter-rbac: "true"
rules:
  - apiGroups: ["example.com"]
    resources: ["trainingruns"]
    verbs: ["get", "list", "watch"]
```

Replace the example group and resource with the workload API. Kubernetes merges
this rule into every exporter role selecting that label. The label is not specific
to one Helm release. Keep extension rules read-only; aggregation itself does not
restrict their verbs.

Namespace selection reduces watches, not authorization. Restricting access to named
namespaces requires Roles/RoleBindings in place of a cluster-wide workload binding;
adding a narrow binding does not remove existing grants. Namespace objects still
need cluster-scoped read access.

This follows [Kubernetes RBAC aggregation](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#aggregated-clusterroles).
The [kube-state-metrics chart](https://github.com/prometheus-community/helm-charts/blob/main/charts/kube-state-metrics/values.yaml)
also supports extra resource rules, namespace-scoped roles and aggregation.

Platforms can deploy exporters with disjoint namespace scopes. More replicas
watch the same scope rather than divide it. Karta does not manage that deployment.
