<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta SDK: work with workloads through one interface

Build a workload inventory, collect Pod counts, or investigate a failing Pod
without writing a separate adapter for every workload type. Karta runs inside
your Go process. You supply the Kubernetes client and decide when to read or write.

![Karta SDK entry points and known or generic workload results](sdk-overview.png)

Two entry points serve different inputs: `karta.New(definition, object)` inspects
and edits an object you already hold. `karta.NewDiscoverer(reader, options)` finds
a workload and its Pods through your client or caller-fed index.

These APIs are available in this checkout. Use it or a pinned revision; the latest
published module may not contain them. The snippets below are function bodies
with errors returned to the caller. Linked runnable examples include their setup.

## Build a workload inventory

An inventory service needs the requested scale, container images and status of
each workload. Start with the Job in [ExampleSummarizeDesired](example_test.go):
it requests two replicas, uses `example/trainer:v1`, and has a Complete condition.
Its `input` object already has `apiVersion: batch/v1` and `kind: Job`.

```go
definition, err := catalog.Get(input.GroupVersionKind())
if err != nil { return err }
workload, err := karta.New(definition, input)
if err != nil { return err }
summary, err := workloadinterface.SummarizeDesired(ctx, workload)
if err != nil { return err }
root := summary.Instances[0]
fmt.Printf("%s: replicas=%d image=%s\n", root.Component,
    *root.Scale.Replicas, root.Extracted.PodTemplateSpec.Spec.Containers[0].Image)
fmt.Printf("phase=%s condition=%s\n", summary.Phases[0], summary.Status.Conditions[0].Type)
```

The example asserts:

```text
job: replicas=2 image=example/trainer:v1
phase=Completed condition=Complete
```

[SummarizeDesired](desired.go) is a small consumer adapter around `workload.Tree(ctx)`.
It visits the root and every child instance, retaining templates, scale, raw
conditions and normalized phases. Templates can be complete, split into spec and
metadata, or represented as fragmented fields; the adapter preserves those shapes.

This reads the supplied object without cluster calls. Set its exact GVK before
construction, including `TypeMeta` on typed Kubernetes objects. Desired scale is
not an observed Pod count: the [zero-Pod example](recipes_test.go) asserts
`root=training observed=0 desired=2`. Nested scale values are not additive Pod totals.

## Troubleshoot from one Pod

A failing training Pod may belong to a Job that belongs to a JobSet. Resolve the
terminal workload and inspect all its siblings, rather than stopping at that Job.
Wrap your existing dynamic client and REST mapper once:

```go
reader, err := clientgo.NewReader(dynamicClient, restMapper)
if err != nil { return err }
discoverer, err := karta.NewDiscoverer(reader, discovery.Options{})
if err != nil { return err }
observed, err := discoverer.WorkloadForPod(ctx, discovery.ObjectRef{
    GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
    Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID,
})
if err != nil { return err }
```

In [ExamplePodSource_fromPod](recipes_test.go), starting with `training-0-pod`
returns the JobSet named `training-set`. `observed.Pods()` contains three full
Pods: `training-0-pod`, `training-1-pod` and `evaluation-0-pod`.
`observed.Object()` returns the full terminal object.

Already know the root? Use `discoverer.Workload(ctx, rootRef)`, with its exact
GVK, namespace, name and current UID. It also supports workloads with zero Pods.
Passing the intermediate Job returns `discovery.ErrNotWorkloadRoot`.

Completed, terminating and unscheduled Pods remain in the result. Missing owners,
denied reads and incomplete coverage return errors, not an apparently healthy
empty inventory. Results are detached observations; acquire again to refresh.
The constructor starts no controller or watches and needs no controller-runtime.

### Read status, then traverse the workload

[ExamplePodSource_traverse](traversal_test.go) starts from `training-0-pod`.
It returns the whole JobSet and evaluates the captured root:

```go
desired, err := observed.DesiredTree(ctx)
if err != nil { return err }
fmt.Printf("phases=%v\n", desired.Status.Phases)
for component, instance := range desired.Instances() {
    if !component.HasPodDefinition { continue }
    id := ""
    if instance.InstanceKey != nil { id = *instance.InstanceKey }
    scope, err := observed.Component(component.Name)
    if err != nil { return err }
    named, err := scope.Instance(id)
    if err != nil { return err }
    pods, err := named.Pods()
    if err != nil { return err }
    fmt.Printf("%s/%s: desired=%d observed=%d\n",
        component.Name, id, *instance.Scale.Replicas, len(pods))
}
```

For this fixture, the result is:

```text
phases=[Running]
replicatedjob/evaluation: desired=1 observed=1
replicatedjob/training: desired=2 observed=2
```

The runnable example also prints every Pod name and phase.
Its raw workload condition is `Completed=False`.

These are three different observations:

- `desired.Status.Phases`: all matching Karta lifecycle phases, possibly none or several.
- `desired.RootStatus`: the root's extracted raw phase and conditions.
- `observed.Pods()[i].Status`: the individual Pod's Kubernetes status.

The snippet uses a JobSet with declared scale and status.
Other definitions may have nil status or scale; check before dereferencing.
`DesiredTree` evaluates spec and scale too, so handle its errors separately from membership.

`desired.Root` includes the workload itself.
`Instances()` walks the compatibility child view; for every root instance, recursively
walk `Root.Instances[].Children`, as [SummarizeDesired](desired.go) does.
Desired replicas are counts, not observed replica keys.

### Narrow to a component, instance or replica

![Workload, component, instance and replica scopes with their actual fixture Pods](sdk-workload-scopes.png)

For that JobSet, the definition calls its repeated Job component `replicatedjob`.
`training` and `evaluation` are named instances. Replica `"0"` identifies one Job
within its instance.

```go
jobs, err := observed.Component("replicatedjob")
if err != nil { return err }
training, err := jobs.Instance("training")
if err != nil { return err }
first, err := training.Replica("0")
if err != nil { return err }
pods, err := first.Pods()
if err != nil { return err }
fmt.Printf("workload=%d training/0=%d\n", len(observed.Pods()), len(pods))
```

The examples return these full Pod objects, shown here by name:

| Scope | Pods |
| --- | --- |
| Workload or `Component("replicatedjob")` | `evaluation-0-pod`, `training-0-pod`, `training-1-pod` |
| `Instance("training")` | `training-0-pod`, `training-1-pod` |
| Training `Replica("0")` | `training-0-pod` |
| Training `Replica("1")` | `training-1-pod` |
| Evaluation `Replica("0")` | `evaluation-0-pod` |

A replica key is scoped by workload, component and instance.
Grouping everything by `"0"` would mix training and evaluation.
Narrower queries preserve their ancestor boundaries and make no Kubernetes reads.

If a Pod's grouping is uncertain, a relevant scope returns
`discovery.ErrIncompleteAttribution`. Inspect `MembershipIssues()` before showing
a complete breakdown. Workload-level `Pods()` still contains every
ownership-confirmed member.

### Get the leader and workers of one inference group

[ExamplePodSource_groupIDs](traversal_test.go) uses a LeaderWorkerSet with two groups.
The catalog calls the grouping component `group`; its instance is anonymous (`""`).
Its `replicaSelector` reads the operator's group index.

```go
groups, err := observed.Component("group")
if err != nil { return err }
first, err := groups.Replica("0")
if err != nil { return err }
workers, err := first.Component("worker")
if err != nil { return err }
pods, err := workers.Pods()
if err != nil { return err }
```

The example queries both group IDs and both child components:

| Group ID | Leader Pods | Worker Pods |
| --- | --- | --- |
| `0` | `inference-0` | `inference-0-1` |
| `1` | `inference-1` | `inference-1-1` |

The example supplies the known keys `0` and `1`; the SDK does not enumerate replica IDs.
Its local fixture attaches Pods directly to the LeaderWorkerSet to isolate scope selection.
The [live suite](../../test/discovery/README.md) checks the group boundaries and real ownership chains
against actual operator-created Pod UIDs.

### Group the actual Pods into scheduling gangs

A replica group is separate from a scheduling gang.
[SchedulingGroups](groups.go) returns each gang's name, key tuple and full Pods:

```go
gangs, err := workloadinterface.SchedulingGroups(ctx, observed)
if err != nil { return err }
for _, gang := range gangs {
    fmt.Printf("%s %v: %d Pods\n", gang.Name, gang.Keys, len(gang.Pods))
}
```

For JobSet, `job [training]` contains both training replicas.
`job [evaluation]` contains the evaluation Pod.
For PyTorchJob, the master and workers share one gang across their different components.
The [tests](groups_test.go) assert the full Pod bodies for both cases.

This example helper re-evaluates the public attribution and grouping APIs locally.
It supports legacy `gangScheduling.podGroups` with explicit grouping keys.
Generic workloads, the singular `podGroup` format and owner-fallback grouping return
`ErrUnsupported`; uncertain attribution returns an error instead of partial groups.
Creating a scheduler's PodGroup remains the consumer's job.

## Collect workload metrics

The existing exporter handles watch events, writes compact metric records, then
serves them from a store during a scrape.
The SDK branch shares its attribution and ownership code through compatibility adapters.
It does not replace the exporter controller with `NewDiscoverer`.

See [PoC collection before and after the SDK change](COLLECTOR.md) for the exact
metric path, unchanged contract and remaining integration work.

For a new consumer, use the observation as input to your collector. [CountPods](pods.go) needs only
an interface with `Pods() []*corev1.Pod`; it counts states without fetching again.
For the three-Pod Job in [ExampleCountPods](example_test.go):

```go
counts := workloadinterface.CountPods(observed)
fmt.Printf("total=%d running=%d completed=%d pending=%d\n",
    counts.Total, counts.Running, counts.Succeeded, counts.Pending)
fmt.Printf("terminating=%d unscheduled=%d\n", counts.Terminating, counts.Unscheduled)
```

```text
total=3 running=1 completed=1 pending=1
terminating=1 unscheduled=1
```

Here the Running Pod is also terminating, and the Pending Pod is unscheduled.
Those counts overlap; they are not extra Pods. Your collector publishes the
counts and chooses its refresh schedule. CPU or GPU measurements come from your
telemetry source; Karta supplies the workload identity and full Pod membership.

## Work with types that have no definition

Discovery follows Kubernetes controller owner references even for a custom CRD.
`observed.Generic()` tells you whether the terminal GVK lacks a Karta definition.
A failed read does not silently switch a known workload to generic mode.

| Result | Matching definition | No definition |
| --- | --- | --- |
| Identity and full bodies | `Root()`, `Object()`, `Pods()` | Same APIs |
| Pod grouping | Declared components, instances and replica axes | `Component("pods")`, anonymous `Instance("")` |
| Desired configuration | `DesiredTree(ctx)` evaluates the captured object | `discovery.ErrUnsupported` |
| Definition | `Definition()` returns a copy | `nil` |

[ExamplePodSource_generic](recipes_test.go) uses a custom `Training` root with one
Pod. Both `observed.Pods()` and its anonymous `pods` instance contain that Pod.
`Replica("0")` returns `discovery.ErrNoReplicaAxis`; Karta does not invent groups,
templates or editing rules.

The [catalog](../../pkg/catalog/catalog.go) provides default definitions.
To add a type or override an exact group/version/kind, construct the discoverer
with `discovery.Options{Definitions: []*v1alpha1.Karta{customDefinition}}`.
Definitions are copied then; updating a cluster Karta object does not update an
existing discoverer. Supply new definitions through a new constructor call.

## Add semantics to an existing discovery controller

If a controller already knows the root and its Pods, keep that discovery result.
Fetch the complete root object, then pass it to the local Karta interface:

```go
object, err := reader.Get(ctx, rootRef)
if err != nil { return err }
definition, err := catalog.Get(rootRef.GVK)
if err != nil { return err }
workload, err := karta.New(definition, object)
if err != nil { return err }
summary, err := workloadinterface.SummarizeDesired(ctx, workload)
if err != nil { return err }
```

[ExampleDesiredInspector_existingDiscovery](traversal_test.go) adds JobSet phases
and three declared instances, including the root, with one full-object read.
It retains the controller's existing three Pod members.

Handle `catalog.ErrNotFound` by retaining ordinary inventory without semantic enrichment.
Do not treat other errors as an unknown workload type.
Metadata alone is insufficient for template, scale and status extraction.

The controller still owns:

- Its physical owner tree, device filters and lifecycle policy.
- Writes, retries, observation freshness and inventory health.
- The decision to persist semantic information or keep it in memory.

Karta's desired tree describes logical components; it is not a physical owner tree.
A device-filtered Pod cache also cannot satisfy discovery's complete-coverage contract.
Use the local enrichment path above, or provide complete coverage before discovery and filter afterward.

## Apply template policy or suspend a workload

A submission policy can set a scheduler on the Job's Pod template before the
caller creates or updates it. Reuse the local `workload` from the inventory
example:

```go
err = workload.UpdatePodTemplate(ctx, "job", karta.Patch{
    "spec": map[string]any{"schedulerName": "example-scheduler"},
})
if err != nil { return err }
edited, err := workload.Object()
if err != nil { return err }
// Send edited with your client and handle update conflicts.
```

[ExampleDesiredInspector_mutation](example_test.go) verifies that the input
scheduler stays empty and the edited template uses `example-scheduler`.
`karta.PodPatch` is the typed alternative. Use `Components()` to inspect writable
Pod fields and suspend capability. `karta.WithInstances("training")` targets a
named instance when the definition supports it; omitting it updates every instance
of the selected component.

[ExampleDesiredInspector_namedMutation](traversal_test.go) targets only `training`:

```go
err = editor.UpdatePodTemplate(ctx, "replicatedjob", karta.Patch{
    "spec": map[string]any{"schedulerName": "example-scheduler"},
}, karta.WithInstances("training"))
if err != nil { return err }
```

The output keeps `evaluation` unchanged.
The captured observation keeps both original templates.
Here `editor` is constructed from `observed.Definition()` and `observed.Object()`.

For a discovered known workload, first create the local editor with
`karta.New(observed.Definition(), observed.Object())`. A generic workload needs a
definition before it can use these semantic edits.

```go
if err := workload.Suspend(ctx); err != nil { return err }
suspended, err := workload.Object()
if err != nil { return err }
// The Job copy now has spec.suspend=true. Persist it explicitly if intended.
```

`workload.Resume(ctx)` applies the definition's resume actions. The runnable
[suspend/resume example](recipes_test.go) verifies `spec.suspend` becomes true,
then false, while the original Job remains untouched. Unsupported operations
return `karta.ErrNotSupported`.

Each mutation either succeeds completely or leaves the local copy unchanged.
It never writes Kubernetes or changes a captured observation. Serialize access
to the mutable `karta.Workload`; the caller owns persistence and conflict handling.

## Query repeatedly and choose where state lives

Reuse the same `observed` value when counting or selecting its Pods: these are
local queries. Call the discoverer again when you need a new observation. With a
direct reader, each acquisition reads Pod coverage and resolves owners again.

For repeated acquisitions from an existing application cache, publish its state
into `discovery.Index`. This initialization example collects one namespace and
includes `rootRef` explicitly so a zero-Pod root is retained:

```go
dataset, err := discovery.Collect(ctx, reader, namespace, rootRef)
if err != nil { return err }
index := discovery.NewIndex()
generation, err := index.Replace(ctx, dataset)
if err != nil { return err }
discoverer, err := karta.NewDiscoverer(index, discovery.Options{})
if err != nil { return err }
```

For example, publish a changed Pod from your application's current cache.
Here `currentPod` is its complete `*unstructured.Unstructured` object with
`apiVersion: v1`, `kind: Pod`, and its current UID:

```go
generation, err = index.Apply(ctx, generation, discovery.Changes{
    Upserts: []*unstructured.Unstructured{currentPod},
})
if err != nil { return err }
```

Keep the returned `generation`. It is an index publication token, not a
Kubernetes `resourceVersion`.

The update feed must cover the whole inventory it declared:

- Publish changed Pods, workload roots and intermediate owners in `Upserts`.
- Publish deleted objects in `Deletes` as UID-bearing `discovery.ObjectRef` values.
- Supply mappings for new kinds and all required served versions of changed objects.
- After `ErrGenerationConflict`, reread the current application cache before retrying.

On lost coverage or ordering, call `index.Invalidate(err)` and rebuild with `Replace`.

The caller must close the gap between initial collection and its update feed.
An unrelated watch started afterward is not enough.
The index starts no watches and does not make Kubernetes reads transactional.

Optionally reuse successful component attribution on this index by changing the
constructor options:

```go
discoverer, err := karta.NewDiscoverer(index, discovery.Options{
    Cache: &discovery.CacheOptions{MaxWorkloads: 1000, MaxPods: 10000},
})
if err != nil { return err }
```

`Cache: nil` is the default. The cache requires a `*discovery.Index`; it does not
cache direct HTTP reads. Both limits must be positive and bound retained workloads
and Pod membership records, not bytes or the index itself. Eligible definitions
reuse attribution; other expressions evaluate normally.

Every acquisition still checks the current index and returns a new observation.
Changed root or Pod records invalidate affected reuse; an invalid index returns an
error. [ExamplePodSource_attributionCache](recipes_test.go) acquires twice, then
publishes a Pod update: the saved observation remains `Running`, and a new one
reports `Succeeded`.

See the [reader, index and cache contract](../../docs/Live%20Workload%20Discovery.md)
for update ordering, cache eligibility and freshness responsibilities.

<details>
<summary>Inside a discovery request: from one Pod to the whole workload</summary>

![Direct discovery and subsequent local scope queries](sdk-discovery-flow.png)

[Open the zoomable sequence](sdk-discovery-flow.preview.html) or
[view the SVG](sdk-discovery-flow.svg).

1. Construction validates definitions without cluster reads.
2. Fetch the UID-bound Pod and follow controller owners to the terminal workload.
3. Obtain complete Pod coverage in the root's namespace, or all namespaces for a
   cluster-scoped root. Resolve every listed Pod's chain, then select descendants.
   Even an unrelated broken owner chain can fail direct collection.
4. Attach definition-based grouping, or expose generic Pod membership.
5. Return a detached observation. Identity, Pod and scope queries are local;
   `DesiredTree(ctx)` evaluates the captured object only when requested.

With an index, acquisition uses a published local generation instead of the
direct read path above. Neither path starts background synchronization.

Source: [resolver](../../pkg/discovery/resolver.go),
[collection](../../pkg/discovery/collect.go),
[ownership](../../pkg/ownership/trace.go),
[workload and scopes](../../pkg/discovery/workload.go).
Editable diagrams: [overview](sdk-overview.excalidraw),
[workload scopes](sdk-workload-scopes.excalidraw),
[sequence](diagrams/sdk-discovery-flow.puml).

</details>

<details>
<summary>Run the examples and use this checkout</summary>

From the repository root:

```bash
go test ./examples/workloadinterface -run '^Example' -v
```

Import `karta`, `discovery`, `discovery/clientgo`, `catalog` and `api/runai/v1alpha1`
beneath `github.com/dsx-ai-factory/workload-map/pkg`. The example consumer adapter
is `github.com/dsx-ai-factory/workload-map/examples/workloadinterface`.
`schema` is `k8s.io/apimachinery/pkg/runtime/schema`; `fmt` is from the standard
library. `unstructured` is `k8s.io/apimachinery/pkg/apis/meta/v1/unstructured`.
`ctx`, objects, clients, mappings and feed changes belong to your caller.

For a separate module, use the Go version in [go.mod](../../go.mod) and point to
this checkout before resolving dependencies:

```bash
go mod edit -replace=github.com/dsx-ai-factory/workload-map=/absolute/path/to/karta
go mod tidy
```

The [validation record](VALIDATION.md) covers the examples and live kind suite.
The [recording replay](../../test/discovery/RECORDINGS.md) checks historical phases,
mutations and grouping, with its exclusions and existing tree errors listed.
The runnable examples above use local fixtures with asserted outputs.

</details>
