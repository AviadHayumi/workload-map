<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Workload interfaces: inspection, mutation and live discovery

This branch combines the offline interface from
`feat/karta-workload-interface` at `b063a855` with the live discovery SDK.
The implementation lives on `codex/karta-workload-interface-sdk`.
The original branch remains unchanged.

The two entry points answer different questions:

| Entry point | Input | Result | Cluster access |
| --- | --- | --- | --- |
| `karta.New` | Karta definition and workload object | `karta.Workload`: templates, scale, status and local edits | None |
| `karta.NewDiscoverer` | Caller-owned reader and discovery options | `karta.Discoverer`: workload lookup by root or Pod | During lookup |
| `discovery.Workload` | Returned by a lookup | One captured workload with its observed Pods and component scopes | None from accessors |

A workload can request ten Pods while only eight exist. `Tree` reads the desired
structure from the object. `Pods` reports observed membership, including completed,
terminating and unscheduled Pods. These quantities should not be substituted for
each other.

## Run the examples

From the repository root:

```bash
go test ./examples/workloadinterface -run '^Example' -v
go test -race ./pkg/karta/... ./pkg/tree ./pkg/discovery/... ./examples/workloadinterface
```

The module path on this branch is
`github.com/dsx-ai-factory/workload-map`. Use a local checkout or pin a commit
from this branch; `go get ...@latest` does not select this implementation.
For a separate local consumer module:

```bash
go mod edit -replace=github.com/dsx-ai-factory/workload-map=/absolute/path/to/karta
go mod tidy
```

The Go files beside this README contain runnable examples and consumer tests.
The snippets below show the integration boundaries; they assume normal error
handling in the surrounding application.

## EWI-style inspection

An inventory consumer needs templates, replica settings and status. Its input
can be a small interface it owns:

```go
type DesiredInspector interface {
    Tree(context.Context) (*tree.WorkloadTree, error)
}
```

Resolve a definition by exact GVK, construct a local workload, and pass it to the
sample `SummarizeDesired` adapter:

```go
definition, err := catalog.Get(object.GroupVersionKind())
if err != nil {
    return err
}
workload, err := karta.New(definition, object)
if err != nil {
    return err
}
summary, err := workloadinterface.SummarizeDesired(ctx, workload)
if err != nil {
    return err
}
// summary.Instances includes the root and its descendants.
// Each entry retains Scale and the original ExtractedInstance shape.
// summary.Status retains raw phase, conditions and normalized statuses.
```

`WorkloadTree.Root` includes templates attached directly to a Job, Pod or
Deployment. `RootStatus` preserves the raw phase and conditions. Older
`Children`, normalized `Status` and `Instances()` behavior remains available.
Walk `Root` once for the complete desired tree. Walking both `Root` and
`Children` counts descendants twice.

The adapter preserves all three extraction forms: `PodTemplateSpec`,
`PodSpec` plus `Metadata`, and `FragmentedPodSpec`. It does not invent a complete
Pod from a partial definition. Replica scale belongs to its component instance;
adding every scale in a nested hierarchy does not give the total Pod count.

This is an integration example using public types. It does not migrate EWI's
production handlers, persistence, events, authorization or product-specific
fields. Those remain the consumer's responsibility.

## EWI-style template mutation

`karta.Workload` provides `UpdatePodTemplate`, `Suspend`, `Resume`, `Components`
and `Object` in addition to `Tree`. It owns a copy of the input. An edit is atomic
in that local copy: a failure does not leave half a patch applied.

```go
scheduler := "example-scheduler"
err := workload.UpdatePodTemplate(ctx, "replicatedjob", karta.PodPatch{
    SchedulerName: &scheduler,
    Labels: map[string]string{"example.com/team": "research"},
}, karta.WithInstances("workers"))
if err != nil {
    return err
}
edited, err := workload.Object()
if err != nil {
    return err
}
// The caller decides whether and how to persist edited using Kubernetes.
```

Here `replicatedjob` is the component name in the JobSet definition, and
`workers` is an instance ID from that workload. Names come from Karta; the
consumer does not locate `.spec.replicatedJobs[]` itself. An update without
`WithInstances` applies to all instances of the selected component.

Use `Components()` to inspect supported fields. Full templates, split
spec/metadata and fragmented fields share the same mutation entry point.
Read-only expressions are rejected when the requested field cannot be written.
Unknown instances, ambiguous container selection and unsupported fields return
errors. `Suspend` and `Resume` require a matching definition capability.

The offline engine evaluates JSON numbers as `float64`. Integer tokens outside
`[-2^53, 2^53]` are rejected before object, patch or suspend-action conversion,
so they cannot be silently rounded. `Object()` restores standard Kubernetes
unstructured number types, including integer metadata getters such as
`GetGeneration()`. Decimal numbers follow normal `float64` semantics. The
lower-level resource API retains its existing conversion contract.

Serialize access to a mutable offline workload. A returned observation from
live discovery remains independent of those edits. Local edits are not proof
that a controller accepted or applied a cluster update.

## Metrics exporter: observed Pods

A read-only aggregator does not need the mutation interface:

```go
type PodSource interface {
    Pods() []*corev1.Pod
}
```

`*discovery.Workload` implements this interface. The sample `CountPods` consumer
counts phases plus overlapping terminating and unscheduled categories:

```go
reader, err := clientgo.NewReader(dynamicClient, restMapper)
if err != nil {
    return err
}
discoverer, err := karta.NewDiscoverer(reader, discovery.Options{})
if err != nil {
    return err
}
observed, err := discoverer.WorkloadForPod(ctx, discovery.ObjectRef{
    GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
    Namespace: pod.Namespace,
    Name: pod.Name,
    UID: pod.UID,
})
if err != nil {
    return err
}
counts := workloadinterface.CountPods(observed)
// counts.Total includes every ownership-confirmed Pod in this workload.
// observed.Root() supplies the workload UID, name, namespace and GVK.
```

Inject the existing dynamic client and RESTMapper. Karta does not load a
kubeconfig, start informers or choose a second cache for the consumer. No
controller-runtime dependency is added to the SDK. `NewDiscoverer(nil, ...)`
can be constructed; live calls return `discovery.ErrNoClient`.

Use `Workload(ctx, rootRef)` when the workload has no Pods. A root reference must
identify the terminal owner, including its current UID. Missing owners, denied
reads and incomplete coverage return errors; they are not a successful zero.
Unknown workload kinds retain generic ownership and all descendant Pods, but
have no invented semantic template or scale information.

The existing exporter already shares ownership and attribution implementation
with the SDK. This example adds no metric and changes no existing label, Helm
value or exporter root-selection policy. Strict discovery attribution and the
exporter's compatibility behavior intentionally differ for malformed or
ambiguous definitions. Replacing an exporter acquisition path requires checking
those policies, not just matching method names.

## Drill down without losing the workload boundary

A JobSet's workers component instance can contain multiple Jobs:

```go
component, err := observed.Component("replicatedjob")
if err != nil { return err }
workers, err := component.Instance("workers")
if err != nil { return err }
replica, err := workers.Replica("1")
if err != nil { return err }
pods, err := replica.Pods()
if err != nil { return err }
```

For LeaderWorkerSet, use `Component("group")`, `Instance("")`,
`Replica("0")`, then `Component("worker")`. The last scope remains inside group
0. It does not include workers from every group. Scope queries return errors for
unresolved semantic membership; workload-level `Pods()` still retains the
ownership-confirmed inventory. Inspect `MembershipIssues()` before presenting
semantic breakdowns as complete.

## Repeated queries and 100,000 workloads

A direct reader is useful for ad hoc queries. It lists complete Pod coverage and
walks ownership. Do not repeat a full namespace inventory for every workload on
every scrape.

Use the caller-fed `discovery.Index` for repeated reads. Publish a complete
`Collect`/`Replace` snapshot, then ordered events from the consumer's canonical
cache with `Apply`. On lost coverage or ordering, invalidate and rebuild. Pass
the index to `karta.NewDiscoverer`; consumer code remains the same.

The index captures a local generation, not a Kubernetes-wide transaction. It
retains objects and Pod indexes in memory. Previous benchmarks exercised 100,000
workload roots, but do not establish production capacity or faster performance
for every query. Size memory, update throughput, scrape output and cold-start
behavior against the deployment's actual workload mix.

## Acceptance tests

The [validation record](VALIDATION.md) lists the integration fixes, regression
tests, reproducible commands and exact live workload coverage.

The dedicated [kind test guide](../../test/discovery/README.md) creates seven
workloads and fourteen Pods, including real JobSet and LeaderWorkerSet operators.
It checks exact Pod UIDs with an independent Kubernetes oracle, direct and
indexed discovery, instance/replica boundaries, zero-Pod and completed workloads,
owner changes, UID reuse, RBAC failures and cluster-scoped roots.

The same live suite calls this README's `CountPods` and `SummarizeDesired`
functions. It verifies root-bearing templates and local mutation isolation.
These are Kubernetes ownership and consumer integration tests; they do not
exercise GPU drivers, DCGM or product-specific EWI deployment.

See [Live Workload Discovery](../../docs/Live%20Workload%20Discovery.md) for the
full reader, index, error and freshness contracts.
