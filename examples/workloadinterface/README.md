<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta SDK: work with workloads through one interface

Find a workload from one of its Pods, inspect its desired configuration, or select
Pods by component. Karta runs inside the consumer's Go process. The consumer
provides the Kubernetes client and decides when to read or write the cluster.

![Karta SDK entry points and known or generic workload results](sdk-overview.png)

These APIs are available in this implementation checkout. Use this checkout or a
pinned revision; the latest published module may not contain them.

| If you want to... | Start with... |
| --- | --- |
| Inspect or edit an object already in memory | `karta.New(definition, object)` |
| Find the workload that owns a Pod | `discoverer.WorkloadForPod(ctx, podRef)` |
| Find a workload, including one with no Pods | `discoverer.Workload(ctx, rootRef)` |
| Query repeatedly from your own cache | `karta.NewDiscoverer(index, options)` |

## Find a workload

Wrap your existing `dynamic.Interface` and `meta.RESTMapper`. No extra controller,
informer or controller-runtime dependency is needed.

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

For a Pod owned by a Job inside a JobSet, `observed.Root()` identifies the JobSet
and `observed.Pods()` contains all its descendant Pods, including siblings of the
starting Pod. Completed, terminating and unscheduled Pods are included.

Already have the workload reference? Call `discoverer.Workload(ctx, rootRef)`.
Supply its GVK, namespace, name and current UID. It must identify the terminal
owner: passing an intermediate Job in that JobSet returns `ErrNotWorkloadRoot`.
A valid workload with no Pods returns an empty slice. Missing owners, denied reads
and incomplete inventory return errors, so they cannot look like a healthy zero.

Runnable examples: [from a Pod and with zero Pods](recipes_test.go).

## Work with known and unknown types

Check `observed.Generic()`. A known type has a Karta definition matching the
terminal workload's exact group, version and kind. The default definitions come
from the [built-in catalog](../../pkg/catalog/catalog.go).

| Capability | Matching definition: `Generic() == false` | No definition: `Generic() == true` |
| --- | --- | --- |
| Identity and owned Pods | `Root()`, `Object()`, `Pods()` | Same APIs, including custom CRDs |
| Component selection | Names and instances declared by Karta | One `"pods"` component with anonymous instance `""` |
| Replica selection | Where the definition declares a replica axis | `ErrNoReplicaAxis` |
| Templates, scale and status | `DesiredTree(ctx)` | `ErrUnsupported` |
| Semantic edits | Construct `karta.Workload` with the definition | Supply a definition first |

For an unknown CRD, ownership still works through Kubernetes controller owner
references; Karta does not guess components or templates. `Definition()` returns
nil. A read failure is an error, not a reason to switch to generic mode.
See the [generic workload example](recipes_test.go).

To support another type, pass a `*v1alpha1.Karta` definition when constructing the
discoverer. Supplied definitions override built-ins for the same exact GVK:

```go
discoverer, err := karta.NewDiscoverer(reader, discovery.Options{
    Definitions: []*v1alpha1.Karta{customDefinition},
})
if err != nil { return err }
```

Definitions are copied at construction. The SDK does not automatically watch Karta
objects installed in the cluster; the caller supplies updates through a new discoverer.

## Select Pods by component

For the example JobSet, `replicatedjob` is the component, `training` is the named
instance, and `"0"` selects its first replicated Job:

```go
component, err := observed.Component("replicatedjob")
if err != nil { return err }
training, err := component.Instance("training")
if err != nil { return err }
replica, err := training.Replica("0")
if err != nil { return err }
pods, err := replica.Pods()
if err != nil { return err }
```

Call `component.Pods()` for every instance or `training.Pods()` for every replica
of that instance. Each narrower query retains its ancestor boundary. These calls
use the captured observation and perform no Kubernetes reads.

A component is a Karta name such as `replicatedjob`; an instance is a named entry
such as `training`; a replica identifies one repeated group of that instance.
For an unknown type, use `Component("pods")` and `Instance("")` instead.
Unresolved grouping can return `ErrIncompleteAttribution`; inspect
`MembershipIssues()` before showing a breakdown as complete. Workload-level
`Pods()` still retains the ownership-confirmed inventory.

The [scoped query example](example_test.go) asserts the exact Pod counts.

## Inspect desired state

For a known workload, `observed.DesiredTree(ctx)` extracts templates, scale and
status from the captured object. If you already have an object in memory, skip
cluster discovery and construct the local interface directly. Populate the object's
exact GVK first; for typed objects, set `TypeMeta` or resolve it through your scheme.

```go
definition, err := catalog.Get(object.GroupVersionKind())
if err != nil { return err }
workload, err := karta.New(definition, object)
if err != nil { return err }
desired, err := workload.Tree(ctx)
if err != nil { return err }
```

Walk `desired.Root` to include root templates and every child component instance.
`RootStatus` retains extracted phase and conditions; `Status.Phases` contains
normalized statuses. The [inventory adapter](desired.go) shows the traversal and
preserves full templates, split spec/metadata and fragmented fields.

Desired scale and observed Pod count answer different questions: a Job can request
two Pods while zero exist. Do not add nested scales to infer total Pods. The older
`Instances()` iterator visits children only, so it omits a template on the root.

## Edit a template or suspend

Use the local `karta.Workload` interface for changes. For an existing known
observation, construct it with `karta.New(observed.Definition(), observed.Object())`.
For the JobSet above, change only the `training` instance:

```go
workload, err := karta.New(observed.Definition(), observed.Object())
if err != nil { return err }
scheduler := "example-scheduler"
err = workload.UpdatePodTemplate(ctx, "replicatedjob", karta.PodPatch{
    SchedulerName: &scheduler,
    Labels: map[string]string{"example.com/team": "research"},
}, karta.WithInstances("training"))
if err != nil { return err }
edited, err := workload.Object()
if err != nil { return err }
// Persist edited with the caller's Kubernetes client and conflict handling.
```

Omit `WithInstances` to update every instance of the selected component.
`Components()` reports names, writable Pod fields and suspend capability.
`Suspend(ctx)` and `Resume(ctx)` apply the definition's actions; unsupported
operations return `karta.ErrNotSupported`.

Each edit succeeds completely or leaves the local copy unchanged. It does not
write to Kubernetes or alter the original observation. Serialize access to a
mutable workload. See the runnable [template patch](example_test.go) and
[suspend/resume](recipes_test.go) examples.

## Reuse an index for repeated queries

A direct lookup inventories Pods and resolves owners. Controllers and exporters
should reuse a caller-fed `discovery.Index` instead of repeating that inventory
for every workload on every scrape:

```go
dataset, err := discovery.Collect(ctx, reader, namespace, rootRef)
if err != nil { return err }
index := discovery.NewIndex()
generation, err := index.Replace(ctx, dataset)
if err != nil { return err }
discoverer, err := karta.NewDiscoverer(index, discovery.Options{})
if err != nil { return err }
```

Pass explicit roots to `Collect` when they have no Pods. Feed ordered changes from
your canonical cache through `index.Apply(ctx, generation, changes)` and keep its
returned generation. On lost coverage or ordering, call `index.Invalidate(err)`
and rebuild with `Replace`. A collection followed by an unrelated watch is not a
complete feed; the caller must close that gap.

Observations are detached snapshots. Acquire again to see updates. The index adds
no background watchers and is not a Kubernetes-wide transaction. See the
[reader and index contract](../../docs/Live%20Workload%20Discovery.md) for feed,
freshness and memory considerations.

## Use only the interface your consumer needs

| Consumer | Small interface | Working example |
| --- | --- | --- |
| EWI-style inventory | `DesiredInspector` with `Tree(ctx)` | [SummarizeDesired](desired.go) accepts a local `karta.Workload` |
| Metrics exporter | `PodSource` with `Pods()` | [CountPods](pods.go) accepts `*discovery.Workload` |

The examples demonstrate integration points. They do not replace EWI's handlers
or the exporter's acquisition policy. Existing metrics and Helm values are unchanged.

<details>
<summary>Inside a discovery request: from one Pod to the whole workload</summary>

![Direct discovery and subsequent local scope queries](sdk-discovery-flow.png)

[Open the zoomable sequence](sdk-discovery-flow.preview.html) or
[view the SVG](sdk-discovery-flow.svg).

1. Construct the discoverer. This validates configuration without cluster reads.
2. Fetch the UID-bound Pod and follow controller owners to the terminal workload.
3. List complete Pod coverage in that workload's namespace, or all namespaces for
   a cluster-scoped root. Resolve every listed Pod's owner chain before selecting
   the workload's descendants. Even an unrelated broken chain can fail collection.
4. Match a definition and attribute components, or expose generic Pod membership.
   Missing owners and incomplete reads return an error. Uncertain grouping remains
   visible through scope errors and `MembershipIssues()`.
5. Return a detached observation. Subsequent identity, Pod and scope queries are
   local. `DesiredTree` evaluates the captured object only when requested.

The SDK and reader run in the application process; only the reader talks to the
Kubernetes API. With an index, lookups use a published local generation instead
of this direct acquisition path.

Source: [resolver](../../pkg/discovery/resolver.go),
[collection](../../pkg/discovery/collect.go),
[ownership](../../pkg/ownership/trace.go),
[workload and scopes](../../pkg/discovery/workload.go).
Editable diagrams: [Excalidraw](sdk-overview.excalidraw),
[PlantUML](diagrams/sdk-discovery-flow.puml). Open the Excalidraw source at
excalidraw.com to edit.

</details>

<details>
<summary>Run the examples and use this checkout</summary>

From the repository root:

```bash
go test ./examples/workloadinterface -run '^Example' -v
```

The snippets use `ctx`, a caller-owned client/RESTMapper, and the workload objects
shown in the runnable examples. Import `karta`, `discovery`, `discovery/clientgo`,
`catalog` and `api/runai/v1alpha1` beneath
`github.com/dsx-ai-factory/workload-map/pkg`; `schema` is
`k8s.io/apimachinery/pkg/runtime/schema`.

For a separate consumer module, use the Go version required by [go.mod](../../go.mod).
Add the imports, then point the module at this checkout before resolving dependencies:

```bash
go mod edit -replace=github.com/dsx-ai-factory/workload-map=/absolute/path/to/karta
go mod tidy
```

The [validation record](VALIDATION.md) covers regression tests and the previously
run [kind suite](../../test/discovery/README.md): seven workload roots and fourteen
Pods, including JobSet and LeaderWorkerSet operators, generic and zero-Pod
workloads, completed Jobs, ownership changes and failed reads.

</details>
