<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter: implementation design

[KEP-0001](../../keps/0001-metrics-exporter/README.md) defines the metrics.
This design describes the implemented components and their contracts.
[Verification and publication](consistency.md) explains freshness, concurrent
updates and failures. [Configuration](../../karta-metrics-exporter/configuration.md)
describes deployment settings.

The [complete sequence](full-flow/design.md) follows startup, Pod discovery,
root status evaluation, publication and error recovery through the process.
[Open issues and proposed fixes](open-issues.md) records the remaining memory,
CPU and deployed-acceptance findings. The [controller audit](controller-audit.md)
records the leaf/root change, alternatives and 13 operator source findings.

## From one Pod to workload metrics

A Pod's owner may be a Job, while the workload you want to monitor is a JobSet.
The exporter follows Kubernetes controller owners to find that workload.
Its Karta describes how to read workload status and map Pods to components.

Take JobSet `train` in namespace `team-a`, with one leader and two workers.
Each Job creates one Pod:

![JobSet train owns three Jobs and their Pods. Pod labels select the component instance and replica group. JobSet conditions supply workload status.](./jobset-pod-attribution.png)

These are the relevant input fields, with the other object fields omitted:

```yaml
# JobSet team-a/train
spec:
  replicatedJobs:
    - name: leader
      replicas: 1
    - name: workers
      replicas: 2
status:
  conditions:
    - type: Completed
      status: "True"
---
# Pod team-a/train-workers-1-0, owned by Job train-workers-1
metadata:
  labels:
    jobset.sigs.k8s.io/replicatedjob-name: workers
    jobset.sigs.k8s.io/job-index: "1"
```

The catalog JobSet Karta contains this mapping fragment:

```yaml
childComponents:
  - name: replicatedjob
    specDefinition:
      podTemplateSpecPath: .spec.replicatedJobs[].template.spec.template
    instanceIdPath: .spec.replicatedJobs[].name
    podSelector:
      componentInstanceSelector:
        idPath: '.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"]'
      replicaSelector:
        keyPath: '.metadata.labels["jobset.sigs.k8s.io/job-index"]'
```

The SDK evaluates `podTemplateSpecPath` and `instanceIdPath` against the JobSet.
It evaluates the paths under `podSelector` against the Pod:

| Result | Input and operation | Value |
| --- | --- | --- |
| Component | `replicatedjob` is the only component with a Pod template. | `replicatedjob` |
| Component instance | JobSet names are `["leader", "workers"]`. The Pod label is `"workers"`. It matches `"workers" == "workers"`. | `workers` |
| Replica group | Read the Pod's `job-index` label. This is a group key, not a Pod count. | `1` |
| Workload status | Read the JobSet's `Completed=True` condition through its Karta status rule. | `Completed` |

These samples abbreviate the labels. The full KEP identity and validity labels
are omitted here:

```prometheus
karta_pod_workload_info{workload_name="train",pod_name="train-workers-1-0",component="replicatedjob",component_instance="workers",replica_group_id="1"} 1
karta_workload_status{workload_name="train",phase="Completed"} 1
```

## Components and lifecycle

The exporter is one process. Each component has one owner for its mutable state:

![Reflector acceptance, leaf attribution, root publication and maintenance](./exporter-component-flow.png)

[Diagram source](./exporter-component-flow.excalidraw).

| Component | Owns |
| --- | --- |
| Runtime | Watches, Karta snapshots, queues, input checks, SDK evaluation and publication |
| Discovery | Controller-owner traversal |
| Definition inventory | Filtered definitions and the selected Karta for each group/kind |
| Label evaluator | One compiled CEL expression against one full object |
| Label resolver | Ordered source fallback and workload results for one immutable input |
| Store | Immutable publications, failure keys, clock and synchronization |
| Collector | OTel gauges and formatting one captured view |

`internal/app.New` creates the Kubernetes reader, inventory, Discovery, resolver,
Store and Runtime. It creates an OTel Prometheus reader with unlimited instrument
cardinality and without OTel scope or target labels. Otherwise the SDK's default
cardinality limit would combine workload identities into an overflow series.

Startup and shutdown have these ordering constraints:

1. The command validates configuration, compiles labels and creates components.
2. The application binds the HTTP listener and registers collection. It starts
   Runtime and HTTP serving concurrently. Runtime owns the Karta watch as well
   as Pod and workload watches.
3. `/readyz` stays false until the
   [required sources and watches](consistency.md#readiness-and-collection) are ready.
   `/healthz` and `/metrics` remain available during initialization.
4. Shutdown cancels Runtime and stops HTTP serving. Workers finish before
   collection is unregistered. The provider then receives its own bounded
   shutdown context.

## Discover the workload root

For the opening example, Runtime passes a Pod reference to Discovery:

```go
result, err := discoverer.Get(ctx, discovery.ObjectRef{
    GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
    Namespace: "team-a", Name: "train-workers-1-0",
    UID: podUID, ResourceVersion: podResourceVersion,
})
```

Discovery follows only controller owner references. It checks the UID at every
step and rejects cycles, malformed controller references, multiple controller
owners and paths longer than 1,024 objects. A Karta on an intermediate object
does not stop traversal. With the Pod and JobSet already watched, the reads and result are:

```text
cached Pod -> metadata GET Job train-workers-1 -> cached JobSet train

Root:       JobSet team-a/train
Path:       [Pod train-workers-1-0, Job train-workers-1, JobSet train]
Resolution: resolved
```

The public types keep root resolution separate from Karta evaluation:

```go
type ObjectRef struct {
    GVK             schema.GroupVersionKind
    Namespace       string
    Name            string
    UID             types.UID
    ResourceVersion string
}

type GenericWorkload struct {
    Root       ObjectRef
    Path       []ObjectRef // Input first, root last.
    Resolution OwnerResolution
}

type Result struct {
    Workload *GenericWorkload
}
```

`OwnerResolution` has `resolved`, `ignored_owner` and `unknown` values. Discovery
returns the first two on a completed walk. `unknown` is used on a retained Pod
metric after its owner path fails. It is not a partially resolved Discovery result.
The [owner-boundary settings](../../karta-metrics-exporter/configuration.md#definition-sources-and-owner-boundaries)
define ignored owners and Pod-root opt-in.

Discovery depends only on the object reader. It returns a verified root and
owner path. Runtime then selects the Karta and establishes full-object coverage.
A definition does not stop owner traversal. Discovery's reader contract is:

```go
type ObjectReader interface {
    Cached(ObjectRef) (*unstructured.Unstructured, bool)
    GetMetadata(context.Context, ObjectRef) (*metav1.PartialObjectMetadata, error)
}
```

Coverage means an active full-object watch for an API resource and namespace.
Discovery is synchronous and starts no watches. Runtime's reader owns cached
observations, shared metadata GETs and refresh evidence. A completed walk returns
`Result{Workload: workload}`. A failed walk returns no workload and a wrapped
read, identity or owner-chain error.

For a cold JobSet root, the path is `cached Pod -> metadata GET Job -> metadata
GET JobSet`. Runtime uses the returned root GVK and namespace to start its watch,
then retries with a full JobSet object. Intermediate owners do not require
watches for traversal. A Job may already be watched because its own definition
is selected.

### Watches and queues

Runtime watches Pods and selected workload types. Selected definitions and
`requiredKinds` find roots before they have Pods. A newly resolved generic root
adds supplemental coverage. Watches are shared by API resource and namespace.
Cluster-scoped roots use an empty namespace. Pod-info keeps the Pod's own
namespace, so one cluster root can contain Pods from several namespaces.

Runtime caches full watched Pods and roots, with managed fields removed.
Intermediate reads keep metadata. A Job may already be watched independently
because its definition is selected.

Direct Reflectors accept objects into Reader without a second informer store.
Their callbacks invalidate changed evidence and enqueue work without API reads
or expression evaluation. Complete replacement checks provenance before making
present objects and UID-safe absence visible.

Pod events and selected non-Pod candidates enter a typed ownership-discovery
queue keyed by object address, API representation and UID. A discovery worker
resolves ownership and commits Pod attribution or candidate admission, then
queues the root UID. A selected child can reveal an unselected generic ancestor
even with no Pods. Candidate and Pod paths both use targeted ancestor invalidation.

Candidate owner reads can be slow. Keeping them in the discovery queue leaves
root workers available to publish changes for already admitted healthy roots.

Only root workers publish complete workloads. A root key contains just its UID,
so API representations share serialization. Source synchronization, verification
and metadata refresh use a separate maintenance queue. Each queue retries failed
work with a bounded delay. Different roots can reconcile concurrently. An event
accepted during evaluation schedules another pass. `leafWorkers` controls
ownership discovery for Pods and candidates; `rootWorkers` controls publication.
Both settings inherit `workers` when zero.

Namespace selection controls watched namespaces. [Selection settings](../../karta-metrics-exporter/configuration.md#selection)
control output after resolution. Supplemental watches stop during synchronization
when no selected kind, required kind or known root still needs them. Sharing a
watch means deleting one root does not stop coverage needed by another.

## Select the definition

Runtime looks up the selected definition before evaluation and checks it again
before publication:

```go
type Interface interface {
    Lookup(schema.GroupVersionKind) (Selection, error)
}

type Selection struct {
    Definition *v1alpha1.Karta
    GVK        schema.GroupVersionKind
    Origin     Origin
}

type Source string

const (
    SourceCatalog Source = "catalog"
    SourceCluster Source = "cluster"
)

type Origin struct {
    Source          Source
    Name            string
    UID             types.UID
    ResourceVersion string
}
```

Lookup uses the root group/kind. Source filters run first. A selected cluster
Karta wins over a catalog entry. Cluster ties use the oldest creation timestamp, then name. Catalog entries have
zero creation timestamps, so their names break ties. `Selection.GVK` identifies the representation to evaluate.
Catalog origins have no Kubernetes UID or resourceVersion. Returned definitions
are copies of the inventory snapshot.

Runtime owns cluster-source synchronization and availability. Once its Karta
watch has a complete usable snapshot, it passes that snapshot to
`Inventory.ReplaceCluster`. The inventory filters and selects definitions.
An invalid winner returns an error and cannot enable a catalog fallback.

Until an enabled source is ready, Runtime blocks evaluation and marks dependent
observations invalid. A source failure cannot establish absence. With a usable
source, `Selection{Definition: nil}` and a nil error confirm absence. Runtime then
publishes generic identity and creation time, with `<unknown>` component and
instance, an empty replica group and no status rows. Pod roots are excluded
unless a Pod definition is selected.

If selection uses another API version, Runtime obtains coverage for that
representation and verifies the same UID. Both versions identify one root
publication. ResourceVersions are compared for equality within a representation.

## Evaluate status and Pod coordinates

Runtime passes the selected definition and full root object to the Karta SDK.
It creates a component factory using `resource.NewComponentFactoryFromObject`. The root component's `GetStatus(ctx)` produces matching phases. Pod
mapping uses `NewPodQuerier(pod)`, `InferPodComponent`,
`InferPodComponentInstance` and `ExtractReplicaKey`.

For JobSet `train`, the root accessor reads `replicatedJobs` from the JobSet.
The Pod querier reads the worker Pod's labels. It returns component
`replicatedjob`, instance `workers` and group `1`. The JobSet's Completed
condition produces the Completed phase independently of that Pod mapping.

The SDK reuses compiled JQ programs for successful scalar queries. Each call still
evaluates the current object with its own context and limits. Programs that return
mutable values or fail are discarded. Assignments compile afresh. The cache has
at most 256 expression keys, and GC can reclaim idle programs. Each program serves
at most 16 calls.

One root calculation shares its component factory and extracted instances among
sibling Pods. Status and each mapping have separate results. An absent status
definition produces no status rows. When status rules exist but none match,
Undefined is the matching phase. A missing replica expression gives an empty
group. Invalid component or instance selection fails that Pod mapping.

Replica expressions can be inherited through Karta component `ownerRef` links.
For example, an LWS worker component inherits its logical group's expression and
reads `group-index` from the same Pod. These Karta links are distinct from the
Kubernetes owner references used to find the root.

## Resolve configured labels

`labelevaluator.Compile` produces a reusable evaluator for one expression.
Its `Evaluate(ctx, object)` method returns a string or an error.

`labelevaluator.Config` contains `Expression` and evaluation `Limits`. CEL sees
the full supplied object as `object`, including apiVersion, kind, metadata, spec
and status. Given this JobSet input:

```yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  labels:
    example.com/team: research
```

this expression returns `research`:

```cel
has(object.metadata.labels) && "example.com/team" in object.metadata.labels
  ? object.metadata.labels["example.com/team"] : ""
```

Runtime prepares a resolver for the immutable full root object:

```go
resolve := resolver.ForWorkload(workload)
result, err := resolve(ctx, "karta_pod_workload_info", pod)
```

For sources `[workload, pod]`, the rule evaluates the full root first. An empty
result or error tries the full Pod. A successful fallback preserves the first
source's error. Status rules cannot use a Pod source.

Runtime retains this function while the selected root input stays the same.
A workload expression runs once when its source is first reached. Its result is
shared across metrics and Pods. Pod-only updates reuse it; changed root inputs
create a new scope. The root queue serializes calls to that scope.

Each call returns fresh label maps and per-label errors. Empty results remove
old values. Cancellation stops the call and is never cached. Runtime saves labels
with metric records and reports evaluation errors in workload health. Collector
never evaluates expressions. See the [resolver contract](../../../karta-metrics-exporter/pkg/labelresolver/)
for bounds, reserved names and scope ownership.

## Publish and collect

Runtime prepares one immutable publication per root UID. It checks accepted
input versions, the local root change counter, selected definition and active
evidence before saving. A zero-Pod root follows the same path with an empty Pod
list. Store has separate write and capture interfaces:

```go
type Writer interface {
    Publish(*Publication)
    Published(types.UID) *Publication
    RemovePublication(types.UID)
    SetPublicationFailure(key, reason string)
    RetirePublicationFailure(key string)
    SetPublicationHealth([]HealthRecord)
}

type Capturer interface {
    Capture() CapturedView
}
```

The [stored types and ordering rules](consistency.md#concurrent-publication)
define `Publication`, `HealthRecord` and `CapturedView`. Store owns its mutex and
clock; Runtime does not bind an external lock. `Published(uid)` returns a
read-only `*Publication`. A caller replaces one Pod with
`publication.WithPod(uid, replacement)`, then calls `Publish` with that result.
A nil replacement removes the Pod.

`PodRecord.WorkloadGVK` stores the verified owner GVK for each Pod when no Karta
is selected. Pods can refer to the same root UID through different served API
versions. A selected Karta supplies the common GVK instead. Runtime must populate
this field on every Pod record; Collector emits its group, version and kind directly.

The application registers collection directly:

```go
registration, err := otel.Register(meter, publications)
```

It handles registration errors and unregisters after Runtime stops.

One callback captures Store once and emits `karta_pod_workload_info`,
`karta_workload_status`, `karta_workload_created_timestamp_seconds` and
`karta_exporter_observation_valid`. It checks every observation against the
captured time and failure map. Matching status phases emit 1 and the other
supported phases emit 0. The phases are Initializing,
Running, Completed, Failed, Degraded, Suspended, Suspending, Resuming and
Undefined. Verified positive creation timestamps are emitted separately.
Custom labels apply only to Pod-info and status.

Collection reads saved values without API or expression evaluation.
[Consistency details](consistency.md) define snapshot visibility, expiry,
deletions and the health metric's reason labels.

## Verification

Component race tests run without an API server. Envtest exercises real Kubernetes
API behavior for Discovery, inventory and Runtime. The real-operator matrix
checks raw CR status and Pod ownership independently of the SDK before checking
Prometheus output. Its [coverage and recorded results](../../../karta-metrics-exporter/test/e2e/)
identify tested versions and remaining gaps.

[Scale results](../../../karta-metrics-exporter/test/scale/) come from a modeled
API and do not size production clusters. The [simplification measurements](simplification.md)
compare component structure and focused benchmarks.
