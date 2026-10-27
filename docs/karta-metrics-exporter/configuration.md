<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter configuration

The exporter runs separately from the Karta operator. It reads Kubernetes objects
and serves the [KEP metrics](../keps/0001-metrics-exporter/README.md) at `/metrics`.
The same HTTP listener serves `/healthz` and `/readyz`.

## Helm values

This example deploys only the exporter and selects catalog definitions:

```yaml
operator:
  enabled: false
crdUpgrader:
  enabled: false
exporter:
  enabled: true
  image:
    tag: "<exporter-tag>"
  http:
    port: 8080
  kartaDefinitionSource:
    catalog:
      enabled: true
      include: []
      exclude: [core-pod-v1]
    cluster:
      enabled: false
  namespaceSelector:
    matchLabels:
      example.com/tenant: research
  objectSelector: {}
  podSelector: {}
  ignoredOwnerKinds: []
  requiredKinds: [jobset.x-k8s.io/v1alpha2/JobSet]
  rbac:
    # Replaces the default workload list. Verbs are get/list/watch.
    workloadRules:
      - apiGroups: [apps]
        resources: [deployments, replicasets, statefulsets]
      - apiGroups: [batch]
        resources: [jobs, cronjobs]
      - apiGroups: [jobset.x-k8s.io]
        resources: [jobsets]
  labelRules:
    - name: team
      metrics: [karta_pod_workload_info, karta_workload_status]
      sources: [workload]
      expression: |
        has(object.metadata.labels) && "example.com/team" in object.metadata.labels
          ? object.metadata.labels["example.com/team"] : ""
  serviceMonitor:
    enabled: true
    interval: 30s
    scrapeTimeout: 25s
```

The chart defaults are:

| Setting under `exporter` | Default |
| --- | --- |
| `enabled` | `false` |
| `http.port` | `8080` |
| `replicaCount` | `1` |
| `kartaDefinitionSource.catalog.enabled` | `false` |
| `kartaDefinitionSource.catalog.exclude` | `[core-pod-v1]` |
| `kartaDefinitionSource.cluster.enabled` | `true` |
| Source `include` lists and cluster `exclude` | Empty |
| `namespaceSelector`, `objectSelector`, `podSelector` | `{}` |
| `ignoredOwnerKinds`, `requiredKinds`, `labelRules` | Empty |
| `serviceMonitor.enabled` | `false` |

An empty image tag uses the chart app version. `http.port` sets the process
listener, probes and Service port. The optional ServiceMonitor requires its CRD
and a Prometheus selector that selects it. Prometheus should scrape each exporter
endpoint separately. The HTTP endpoint has no built-in authentication.

## Selection

| Setting | Input |
| --- | --- |
| `namespaceSelector` | Namespace labels |
| `objectSelector` | Resolved root labels |
| `podSelector` | Labels of each Pod whose row may be exported |
| `kartaDefinitionSource.cluster.labelSelector` | Cluster Karta labels |
| Source `include` / `exclude` | Karta definition names |

Selectors use Kubernetes `matchLabels` and `matchExpressions`. All conditions
must match. An empty selector matches all objects. To select namespace names,
use their `kubernetes.io/metadata.name` label.

Namespace selection controls watch scope. All Pods in selected namespaces remain
observed, even when `podSelector` hides some Pod rows. The
[watch plan](../design/metrics-exporter/README.md#watches-and-queues) describes root
coverage and intermediate-owner reads.

Root selection happens after owner resolution. Excluding a JobSet also excludes
its descendant Pod rows; it does not promote the Jobs below it. Status describes
the root workload. Generic roots without a Karta pass the same root filters.
Namespace withdrawal removes affected Pod rows even beneath a cluster-scoped root.

### Definition sources and owner boundaries

Include and exclude names apply separately within each source. Empty includes
allow all names; exclusions win. Cluster definitions must also
match their label selector. The [inventory contract](../design/metrics-exporter/README.md#select-the-definition)
defines source choice, ties and failures.

To enable bare-Pod catalog attribution, enable the catalog, include `core-pod-v1`
if using a nonempty include list, and set `catalog.exclude: []`. A selected
cluster Pod definition also enables bare Pods. `objectSelector` applies to these
roots too. The same rule applies when ignoring the first owner makes a Pod the
root. A missing Pod definition leaves both kinds of Pod roots out of workload metrics.

`ignoredOwnerKinds` contains exact slash-separated GVKs, such as
`example.com/v1/Wrapper` or `v1/Node` for the core API group. Discovery
stops before a matching owner and uses the last verified object below it as the
root. A missing owner below that boundary still fails.

`requiredKinds` uses the same slash-separated format. It starts coverage before
Pods exist and makes that kind part of readiness. If its API is missing, readiness
stays false while discovery retries. Ignoring an owner does not disable an
independently required-kind check. Ordinary discovered-root failures remain
scoped to their health rows and dependent metrics.

### Workload API types

The chart and standalone config support root resource filters. Under Helm:

```yaml
exporter:
  resourceRules:
    - apiGroups: [batch]
      apiVersions: [v1]
      resources: [jobs, cronjobs]
  excludeResourceRules:
    - apiGroups: [batch]
      apiVersions: [v1]
      resources: [cronjobs]
```

These fields select root API types, not owner dependencies or RBAC permissions.
Excluding a root drops its Pod rows without promoting children to roots. A kind
can remain in `requiredKinds` even when output filters exclude it; its watch and
readiness check still apply. Empty `resourceRules` means no restriction. A root
must match one configured inclusion and no exclusion, as well as its label selector. Group, version and plural resource
name must all match within a rule. Entries in each list are alternatives.
`"*"` matches any value; `""` is the core API group. Every rule needs nonempty
group, version and resource lists. Subresources are not supported.

## Configured labels

`labelRules` adds labels to `karta_pod_workload_info` and `karta_workload_status`.
Each rule names its metrics, ordered sources and CEL expression. `workload` means
the full resolved root object. `pod` means that full Pod object and is allowed
only for Pod-info rules. Startup rejects any rule containing both the status
metric and a `pod` source, even if Pod-info is also listed. The expression reads
the input as `object`.

An empty result or error tries the next source. If no source returns a value,
the label is absent. A successful fallback still retains previous source errors
for health reporting. Updates replace values and remove empty ones. No label
value is fixed for the lifetime of an object.

Startup validates names, source/metric combinations and CEL. Built-in metric
labels and OTel scope labels are reserved. Known non-string expressions fail
compilation; dynamic non-string results fail evaluation. The defaults are:

```yaml
exporter:
  labelLimits:
    costLimit: 10000
    maxExpressionBytes: 4096
    maxValueBytes: 1024
    interruptCheckFrequency: 100
```

The last field controls cancellation checks during CEL comprehensions. The
[evaluator configuration](../../karta-metrics-exporter/pkg/labelevaluator/README.md#configuration)
lists label flags and environment variables. The [resolver contract](../../karta-metrics-exporter/pkg/labelresolver/)
describes error handling and reuse within an immutable workload scope.

## Runtime settings

Outside Helm, pass `--config path.yaml`. The file contains exporter settings
without the `exporter:` wrapper. Unknown or duplicate keys and multiple nonempty
YAML documents are rejected. For each setting, an explicit flag wins over its environment variable, which
wins over the file value, which wins over the default. Rules and selectors stay in YAML.

These runtime settings are available in the standalone file and command. Helm
also accepts `exporter.workers`, `exporter.leafWorkers` and `exporter.rootWorkers`.
The chart uses the defaults for the other runtime settings:

| YAML key | Flag | Environment variable | Default |
| --- | --- | --- | --- |
| Config path | `--config` | `KARTA_EXPORTER_CONFIG` | Empty |
| `refreshInterval` | `--refresh-interval` | `KARTA_EXPORTER_REFRESH_INTERVAL` | `45s` |
| `maxAge` | `--max-age` | `KARTA_EXPORTER_MAX_AGE` | `2m` |
| `requestTimeout` | `--request-timeout` | `KARTA_EXPORTER_REQUEST_TIMEOUT` | `30s` |
| `workers` | `--workers` | `KARTA_EXPORTER_WORKERS` | `2` |
| `leafWorkers` | `--leaf-workers` | `KARTA_EXPORTER_LEAF_WORKERS` | `0` |
| `rootWorkers` | `--root-workers` | `KARTA_EXPORTER_ROOT_WORKERS` | `0` |
| `apiQPS` | `--api-qps` | `KARTA_EXPORTER_API_QPS` | `20` |
| `apiBurst` | `--api-burst` | `KARTA_EXPORTER_API_BURST` | `40` |

`leafWorkers` controls ownership discovery for Pods and selected non-Pod
candidates. `rootWorkers` controls root evaluation and publication. Slow candidate
owner reads use discovery workers, leaving publication workers available for
already admitted roots. Each value inherits `workers` when zero or unset.
For example, `workers: 2`, `leafWorkers: 4` and `rootWorkers: 1` starts four
ownership-discovery workers and one publication worker. Existing configurations using only `workers` keep
that count for both queues. Queue overrides must not be negative.

Source verification uses two workers. Kubernetes clients share the API rate
limit. Durations, `workers` and API rate limits must be positive, and maximum age
must exceed the refresh interval. A delayed refresh does not extend expired evidence.

`--kubeconfig` selects local credentials. In-cluster startup uses the mounted
ServiceAccount. `--metrics-bind-address` defaults to `:8080`; a local run can use
`127.0.0.1:8080`. This flag serves all three HTTP endpoints and has no standalone
YAML key. Helm sets it from `exporter.http.port`. `--version` prints the binary version without starting clients.

## Metric delivery

The implemented delivery path is Prometheus pull through OTel instruments.
Status phase output follows the [Collector contract](../design/metrics-exporter/README.md#publish-and-collect).
Native OTLP delivery is deferred.

An external OTel Collector can scrape `/metrics` and forward samples to a backend.
That Collector owns its transport and authentication settings. CPU, memory and
GPU measurements come from other exporters; the [query guide](README.md) shows
how to join them with workload identity.

The optional ServiceMonitor selects the exporter Service. Without Prometheus
Operator, configure Prometheus to scrape the endpoints directly. The [consistency contract](../design/metrics-exporter/consistency.md#readiness-and-collection)
defines row removal and snapshot visibility.

## Deployment and permissions

| Value under `exporter` | Purpose |
| --- | --- |
| `rbac.workloadRules` | Read access for workload kinds and intermediate owners. |
| `resources` | CPU and memory requests/limits. |
| `nodeSelector`, `tolerations`, `affinity` | Pod placement. |

The chart defaults to a `1Gi` memory limit. For larger installations, set
`resources` using measurements from your workload mix. The [scale profiles](../../karta-metrics-exporter/test/scale/runtime-scale.md)
list their explicit memory and API budgets.

The main Helm example includes `rbac.workloadRules`. These rules use API groups
and plural resource names, as in Kubernetes RBAC. The chart supplies `get`, `list`
and `watch`: read one owner, list existing objects and receive updates. RBAC rules apply across the API versions of each resource.

Include the root and every intermediate owner needed for attribution:

| Workload | Workload rules | Why |
| --- | --- | --- |
| Deployment | `apps`: `deployments`, `replicasets` | Pods belong to ReplicaSets, which belong to the Deployment. |
| JobSet | `jobset.x-k8s.io`: `jobsets`; `batch`: `jobs` | Jobs connect the Pods to the JobSet. |
| LeaderWorkerSet | `leaderworkerset.x-k8s.io`: `leaderworkersets`; `apps`: `statefulsets` | StatefulSets connect the Pods to the LeaderWorkerSet. |
| RayJob | `ray.io`: `rayjobs`, `rayclusters`; `batch`: `jobs` | Read the RayCluster and any submitter Job created by the RayJob. |

The main example covers Deployments, StatefulSets, Jobs, CronJobs and JobSets.
Keep permissions aligned with the selected definitions and API types, including
intermediate owners. The list replaces the [default workload rules](../../charts/karta/values.yaml). Pod, Namespace
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
