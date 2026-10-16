<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta SDK: find, inspect and edit Kubernetes workloads

![Create one SDK, find a workload from a Pod, and reuse its observed structure](sdk-overview.png)

Start with a Pod. Karta finds its workload, returns all its Pods, and groups them
by component, named instance and replica when a definition is available.
Your controller keeps its Kubernetes client, event loop and decisions.

This guide uses a JobSet with training and evaluation Jobs.
The same calls work with other definitions and with ordinary ownership discovery.
These APIs are implemented in this checkout; they may not be in the published module yet.

## Create the SDK once

Reuse your application's Kubernetes dynamic client and REST mapper:

```go
reader, err := clientgo.NewReader(kube, restMapper)
if err != nil { return err }

sdk, err := karta.NewDiscoverer(reader, discovery.Options{
    MaxDepth: 32,
})
if err != nil { return err }
```

- `kube` is a `dynamic.Interface`: the Kubernetes client your application owns.
- `restMapper` is a `meta.RESTMapper`: it maps a kind, such as JobSet, to its API resource and scope.
- `reader` adapts those two objects to the SDK. There is no callback to implement.
- `MaxDepth` limits controller-owner hops. Leave it out to use 32.
- Keep `sdk` and reuse it. Construction starts no watches and performs no cluster reads.

The imports are `pkg/karta`, `pkg/catalog`, `pkg/discovery` and
`pkg/discovery/clientgo` under
`github.com/dsx-ai-factory/workload-map`.
The [complete executable example](cmd/inspect/main.go) includes imports, kubeconfig
loading, client and mapper construction, and the first Pod read.

Try it against a Pod in a cluster you choose:

```bash
go run ./examples/workloadinterface/cmd/inspect \
  -kubeconfig /path/to/kubeconfig -context your-context \
  -namespace team -pod training-0-pod
```

The command only reads. It prints the workload identity, each Pod's phase and its
component coordinates. It does not install an operator or submit workloads.

## Find the whole workload from one Pod

Here `pod` is the `*corev1.Pod` your event handler or Kubernetes client received:

```go
podRef := discovery.PodReference(pod)
observed, err := sdk.WorkloadForPod(ctx, podRef)
if err != nil { return err }

root := observed.Root()
pods := observed.Pods()
fmt.Printf("workload=%s/%s pods=%d\n", root.GVK.Kind, root.Name, len(pods))
```

`PodReference` copies the Pod's namespace, name and UID and supplies `v1/Pod`.
The UID identifies this lifetime of the Pod, so a same-name replacement cannot
silently become the input to your query.

The [runnable JobSet fixture](recipes_test.go) has this ownership:

```text
JobSet training-set
  Job training-0   -> Pod training-0-pod
  Job training-1   -> Pod training-1-pod
  Job evaluation-0 -> Pod evaluation-0-pod
```

Starting at `training-0-pod` returns `JobSet/training-set` and all three Pods.
It does not stop at `Job/training-0`.

| Value | What it contains |
| --- | --- |
| `observed.Root()` | Terminal workload identity: kind, namespace, name and UID. |
| `observed.Object()` | A full copy of that Kubernetes object. |
| `observed.Pods()` | Full Pod copies, including completed, terminating and unscheduled Pods. |
| `observed.PodMemberships()` | Each Pod UID mapped to its component coordinates and any unresolved fields. |

`observed` is a captured result. These accessors make no cluster calls.
Call `sdk.WorkloadForPod` again when you need a fresh result.

Already have the workload reference? Use `sdk.Workload(ctx, rootRef)`.
A reference contains its exact group/version/kind, namespace, name and current UID.
That call also works for a workload with zero Pods.

## Replace a controller's ownership walk

Use the same SDK when you need the physical chain rather than every sibling Pod:

```go
path, err := sdk.OwnershipPath(ctx, discovery.PodReference(pod))
if err != nil { return err }
for _, object := range path {
    fmt.Printf("%s/%s\n", object.GVK.Kind, object.Name)
}
```

For the training Pod, the result is:

```text
Pod/training-0-pod
Job/training-0
JobSet/training-set
```

The first entry is your input; the last is the terminal workload.
`MaxDepth` comes from the SDK options above.
This query reads only that chain: no Pod list and no component classification.
Add `-owners-only` to the command to try it.

An inventory controller can feed this path to its existing tree builder.
Keep its metadata display, path merging, watches and stored records.
The path returns identities; it does not contain labels or annotations.
A Karta definition is not required.

## Select the actual Pods of a component

![Workload, component, named instance and replica, with the actual fixture Pods](sdk-workload-scopes.png)

In the JobSet definition:

- `replicatedjob` is the component: a repeated Job template.
- `training` is a named instance: one entry in `spec.replicatedJobs`.
- `"0"` is a replica key: the first Job created for that instance.

```go
jobs, err := observed.Component("replicatedjob")
if err != nil { return err }
training, err := jobs.Instance("training")
if err != nil { return err }
firstJob, err := training.Replica("0")
if err != nil { return err }
trainingPods, err := firstJob.Pods()
if err != nil { return err }
fmt.Printf("training replica 0: %d Pods\n", len(trainingPods))
```

| Selection | Returned Pod names |
| --- | --- |
| Whole workload | `evaluation-0-pod`, `training-0-pod`, `training-1-pod` |
| `Component("replicatedjob")` | All three Pods. |
| Its `Instance("training")` | `training-0-pod`, `training-1-pod` |
| Training `Replica("0")` | `training-0-pod` |
| Evaluation `Replica("0")` | `evaluation-0-pod` |

These are local queries on `observed`. They preserve the enclosing instance;
replica `"0"` in training is different from replica `"0"` in evaluation.
A component selection also includes its descendant components.

### Select an inference group

LeaderWorkerSet uses a `group` component. Its replica key is the operator's group ID.
For an observation of a LeaderWorkerSet:

```go
groups, err := observed.Component("group")
if err != nil { return err }
firstGroup, err := groups.Replica("0")
if err != nil { return err }
workers, err := firstGroup.Component("worker")
if err != nil { return err }
workerPods, err := workers.Pods()
if err != nil { return err }
fmt.Printf("group 0 workers: %d Pods\n", len(workerPods))
```

The [group example](traversal_test.go) returns `inference-0-1` here.
Changing the final component to `leader` returns `inference-0`.
Keys are supplied by the consumer; replica-ID enumeration is not implemented.

A scheduling gang can span components. The [SchedulingGroups example](groups.go)
combines full Pods using the definition's explicit scheduling keys.
It reuses `PodMemberships()` and performs no second attribution pass.
It supports `gangScheduling.podGroups` with explicit keys; unsupported formats
return an error. Creating scheduler objects remains the consumer's job.

## Reuse the result in an integration or metrics collector

Do not run selectors again to recover coordinates the SDK already calculated:

```go
memberships := observed.PodMemberships()
for _, pod := range observed.Pods() {
    membership := memberships[pod.UID]
    if len(membership.Issues) != 0 {
        return fmt.Errorf("Pod %s has incomplete component attribution", pod.Name)
    }
    fmt.Printf("pod=%s component=%s phase=%s\n",
        pod.Name, membership.Component, pod.Status.Phase)
    for _, coordinate := range membership.Path {
        if coordinate.Instance != nil {
            fmt.Printf("  %s instance=%q\n", coordinate.Component, *coordinate.Instance)
        }
        if coordinate.Replica != nil {
            fmt.Printf("  %s replica=%q\n", coordinate.Component, *coordinate.Replica)
        }
    }
}
```

The training Pod's `replicatedjob` coordinate contains instance `training` and
replica `0`. The map and coordinates are detached copies keyed by Pod UID.
An empty instance string means anonymous; a nil pointer needs the accompanying
issues to distinguish an absent axis from an unresolved value.

An integration controller can persist these coordinates with its own stable IDs.
A collector can join them to measurements keyed by Pod identity.
Karta supplies ownership and structure; CPU/GPU measurements come from the telemetry source.

For simple counts, [CountPods](pods.go) consumes only `Pods()`.
Its asserted fixture returns three Pods: one running, one succeeded and one pending.
The terminating and unscheduled counts overlap those phases.

The existing exporter retains its event-driven metric store and scrape callbacks.
Its compatibility adapters already share ownership and legacy attribution code.
It has not migrated to this high-level discovery interface.
The [exporter comparison](COLLECTOR.md) explains the remaining behavior differences.

## Read desired scale and status

Pod count is observed state. A workload's template, requested scale and conditions
come from its root object and Karta definition:

```go
desired, err := observed.DesiredTree(ctx)
if err != nil { return err }
if desired.Status != nil {
    fmt.Printf("workload phases=%v\n", desired.Status.Phases)
}
```

The [JobSet status fixture](traversal_test.go) reports `[Running]` and preserves its
raw `Completed=False` condition. There may be zero or several matching Karta phases.
Pod status remains available on each returned Pod.

`desired.Root` contains component instances and their child components.
For a complete traversal, visit every root instance and recurse into its children.
[SummarizeDesired](desired.go) shows that traversal while retaining templates, scale
and raw conditions. It is an example consumer adapter, not a new SDK subsystem.
Do not sum nested replica counts to infer the observed Pod total.

`DesiredTree` evaluates spec, scale and status together. A status-only exporter
may retain its existing separate status read so a template error cannot suppress it.

## Inspect or edit an object you already have

A controller that already has a complete workload object can skip discovery.
Here `input` is an `*unstructured.Unstructured` returned by its dynamic client,
with the exact API version and kind set:

```go
definition, err := catalog.Get(input.GroupVersionKind())
if err != nil { return err }
editor, err := karta.New(definition, input)
if err != nil { return err }
view, err := editor.Tree(ctx)
if err != nil { return err }
if view.Root != nil {
    fmt.Printf("root component=%s instances=%d\n", view.Root.Name, len(view.Root.Instances))
}
```

`catalog` is `github.com/dsx-ai-factory/workload-map/pkg/catalog`.
`karta.New` also accepts typed Kubernetes objects that implement
[`resource.KubernetesObject`](../../pkg/resource/object.go).
Set their exact GVK and use `input.GetObjectKind().GroupVersionKind()` for catalog selection.
This local path performs no cluster reads. Handle `catalog.ErrNotFound` by keeping
ordinary inventory when semantic inspection is optional.

`editor` owns a mutable local copy; `observed` above remains an unchanged snapshot.
For a discovered known workload, create the editor from
`observed.Definition()` and `observed.Object()`.

For the JobSet, change only the training instance's scheduler:

```go
err = editor.UpdatePodTemplate(ctx, "replicatedjob", karta.Patch{
    "spec": map[string]any{"schedulerName": "example-scheduler"},
}, karta.WithInstances("training"))
if err != nil { return err }
updated, err := editor.Object()
if err != nil { return err }
// Persist updated with your Kubernetes client and handle update conflicts.
```

The [named-edit example](traversal_test.go) verifies evaluation stays unchanged.
`Components()` reports writable fields and suspend support.
`Suspend(ctx)` and `Resume(ctx)` apply the definition's actions to the same local copy.
Unsupported edits return `karta.ErrNotSupported`; a failed edit leaves the copy unchanged.
Serialize access to an editor. Only your explicit client write changes Kubernetes.

For a separate preparation Job or runtime, reuse the same client and inspect its
object through Karta. See [decisions using referenced resources](REFERENCES.md).
Automatic CEL reference resolution remains a separate prototype.

## Choose options and refresh behavior

| Setting | Default | Meaning |
| --- | --- | --- |
| `MaxDepth` | `0` selects 32 | Maximum controller-owner hops; exceeding it returns an error. |
| `Definitions` | Built-in catalog | Add or override definitions by exact group/version/kind. |
| `Cache` | `nil`, disabled | Reuse successful attribution for a concrete `*discovery.Index`. |

Definitions are copied at construction. Construct a new SDK when they change.
`MaxDepth` does not select a child as the workload root.
The existing `Collect` helper uses a fixed 32-hop limit when initializing an index.

For repeated queries, choose how the SDK obtains its inputs:

| Input | Work done on acquisition | Who keeps it current? |
| --- | --- | --- |
| Direct reader | Read the workload's Pod coverage and resolve owners. | The API or cache behind your reader. |
| `discovery.Index` | Select a workload from one published local generation. | Your existing watch/reconcile loop. |
| Previously returned `observed` | Local accessors only; no refresh. | Acquire again when needed. |

Initialize an index from complete coverage, then pass it to the same constructor:

```go
dataset, err := discovery.Collect(ctx, reader, namespace)
if err != nil { return err }
index := discovery.NewIndex()
generation, err := index.Replace(ctx, dataset)
if err != nil { return err }
sdk, err := karta.NewDiscoverer(index, discovery.Options{
    Cache: &discovery.CacheOptions{MaxWorkloads: 1000, MaxPods: 10000},
})
if err != nil { return err }
```

`namespace` is the inventory namespace; empty means all namespaces.
Pass explicit zero-Pod root references after `namespace` to include those objects.
Publish current objects with `index.Apply(ctx, generation, changes)` and keep its
returned generation. On lost coverage, invalidate and rebuild the index.
Close the gap between initial collection and the watch feed; the SDK does not do it.

The cache limits retained attribution entries and Pod membership records.
It does not bound index memory, cache HTTP reads or start informers.
Both limits must be positive. Unsupported expressions and failed attribution bypass reuse.
See the [reader and index contract](../../docs/Live%20Workload%20Discovery.md) for complete feed rules.

## Unknown workload types and failures

No matching definition is required to find an ownership chain or workload:

| Capability | With a definition | Without a definition |
| --- | --- | --- |
| Workload identity and full Pods | Available | Available |
| `Generic()` | `false` | `true` |
| Component scopes | Definition's names | One `pods` component with anonymous instance. |
| Desired tree and semantic edits | Definition-dependent | Unsupported; no structure is invented. |

A missing owner or denied read is an error, not a generic workload or an empty result.
Direct workload discovery requires every listed Pod's ownership chain to be valid;
even an unrelated broken chain can fail collection.
Use `OwnershipPath` when the task needs only one chain.

Classification issues do not remove ownership-confirmed Pods from `Pods()`.
Read `MembershipIssues()` or each entry's issues before publishing a complete breakdown.
A relevant component/instance/replica query returns `ErrIncompleteAttribution`.
Refresh and reconcile rather than treating an error as zero Pods.

For `Pod -> Job -> WorkloadEnvelope`, discovery currently selects the envelope.
An option to stop before that parent is not implemented.
The [boundary design](INTEGRATION.md#keep-a-workload-below-an-orchestration-parent)
explains separate child workloads and the existing exporter's different candidate policy.

## Reuse one implementation across projects

| Consumer | SDK operation | Logic the consumer retains |
| --- | --- | --- |
| Ownership inventory | `OwnershipPath` | Physical tree merging, metadata display and persistence. |
| Workload integration | `WorkloadForPod`, `DesiredTree`, `PodMemberships` | Eligibility, stable IDs and product status conversion. |
| Existing known root and Pod set | Local interpretation and attribution | Its established membership policy and event handling. |
| Metrics collector | Captured Pods and memberships | Metric contract, measurement joins, store and delivery. |

The [integration guide](INTEGRATION.md) shows the exact replacement boundaries.
A filtered Pod cache cannot silently become complete workload coverage.
Existing integrations can keep their membership path and adopt local interpretation first.

The SDK reduces repeated work through:

- Reusing owner lookups within one collection.
- Optional metadata-only reads for additional intermediate owners.
- Updating affected ownership descendants instead of rebuilding the whole index.
- Preparing definitions once per SDK and instances once per workload observation.
- Reusing eligible attribution when indexed objects are unchanged.
- Returning stored Pod memberships instead of running selectors again.

The [performance guide](PERFORMANCE.md) identifies each mechanism, its cost and its evidence.
These improvements do not establish parity with every consumer or production capacity.

<details>
<summary>Inside a workload query</summary>

![Direct discovery followed by local workload queries](sdk-discovery-flow.png)

1. Read the input by identity and follow controller owners.
2. Obtain complete Pod coverage for the terminal root's scope.
3. Confirm membership, then calculate component coordinates.
4. Return one detached observation for all subsequent local queries.

Indexed queries select one published generation instead of collecting again.
A concrete `*Index` also gives `OwnershipPath` a single-generation chain.
Other readers perform separate reads; a Kubernetes query is not a cross-resource transaction.
The [zoomable sequence](sdk-discovery-flow.preview.html) explains the direct path.

</details>

## Run the examples and inspect the evidence

```bash
go test ./examples/workloadinterface -run '^Example' -v
```

The examples use local fixtures with asserted output.
The complete command above uses the cluster and Pod you select.
Use the Go version in [go.mod](../../go.mod). For a separate module during development:

```bash
go mod edit -replace=github.com/dsx-ai-factory/workload-map=/absolute/path/to/karta
go mod tidy
```

- [Validation](VALIDATION.md): completed checks and their source scope.
- [Live acceptance tests](../../test/discovery/README.md): real Job, JobSet and LeaderWorkerSet flows.
- [Recording replay](../../test/discovery/RECORDINGS.md): phases, mutations, grouping and known exclusions.
- [Performance](PERFORMANCE.md): mechanisms and reproducible benchmark scope.

Diagram sources sit next to this file as `.excalidraw`; open them at excalidraw.com to edit.
The sequence source is [PlantUML](diagrams/sdk-discovery-flow.puml).
