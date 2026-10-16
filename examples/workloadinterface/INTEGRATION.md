<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Replace repeated workload logic

Start with the [operator recipe](OPERATORS.md): Pod watch, queue, reconciliation,
exact SDK call and output. It also shows how your own Karta watcher publishes
selected definitions without introducing another controller.

Use the discoverer for ownership and already computed Pod classification.
Keep the consumer's event loop, output format and writes.
The examples below are integration paths; they do not claim a completed migration
of another controller.

## What changes in the three consumers

These are different integration points. They do not all call `WorkloadForPod`.
The ownership and extraction changes were exercised in local consumer prototypes.
Those prototypes remain pinned to the earlier SDK revision `b841499553b7`;
they do not yet use the new live definition provider and have not been deployed.
The exporter adapters below are implemented in this SDK branch.

| Consumer | Before | SDK replacement | Still owned by the consumer |
| --- | --- | --- | --- |
| k8s-workload-discovery | A local controller-owner walking loop. | `ownership.Trace`, using its existing metadata lookup. | Cache, filters, physical-tree projection and writes. |
| External Workload Integrator (EWI) | An existing Karta component factory from the older module. | The component factory from this SDK, with definition-type conversion. | Component selection, native handlers, grouping, accounting and writes. |
| Karta metrics exporter | Local ownership, instance traversal and Pod attribution. | `ownership.Index`, `attribution.InstanceIDs` and `attribution.AttributeLegacy`. | Watch controller, definition selection, metric store and scraping. |

### k8s-workload-discovery: replace the owner walk

Before:

```text
Pod event -> queue -> read current Pod
  -> local loop: read owner, validate identity, repeat
  -> convert the path -> merge the physical tree -> write the workload record
```

After:

```text
Pod event -> queue -> read current Pod
  -> ownership.Trace(ctx, start, readOwner, maxHops)
       -> readOwner uses the existing metadata cache
  -> convert the path -> merge the physical tree -> write the workload record
```

The call inside the existing owner-walker adapter becomes:

```go
path, err := ownership.Trace(ctx, start, readOwner, maxHops)
```

`start` is an `ownership.Object` containing the Pod identity, owner references
and namespace scope. `readOwner` is the caller's `ownership.Lookup` function.
The returned path contains the Pod followed by its verified controller owners.

For `Pod -> ReplicaSet -> Deployment`, the consumer reverses that path and
merges it into its existing physical tree. No Karta definition is needed.
The local compatibility adapter retains verified partial paths for missing owners
and depth limits. Other structural errors fail. The higher-level `OwnershipPath`
method does not return a successful partial result.

If a new consumer wants the root and all its Pods instead of a physical tree,
use the [Pod-watch example](OPERATORS.md#2-reconciliation-calls-karta):
`WorkloadForPod(ctx, discovery.PodReference(pod))`.
That is a different output contract from this owner-walk replacement.

### EWI: reuse the SDK component reader

EWI already used Karta. The implemented prototype upgrades that extraction
boundary to this SDK; it does not replace all native handlers or grouping logic.

```text
Before: workload event -> select definition -> older Karta component factory
          -> selected-component conversion -> workload spec and status

After:  workload event -> select definition -> SDK component factory
          -> selected-component conversion -> workload spec and status
```

After converting the existing definition to this SDK's Karta Go type, the
shared extraction starts with:

```go
factory := resource.NewComponentFactoryFromObject(definition, object)
rootComponent, err := factory.GetRootComponent()
if err != nil { return err }
childComponents, err := factory.GetChildComponents()
if err != nil { return err }
// Pass the selected components to the existing converter.
```

`definition` is a `*v1alpha1.Karta` from this SDK module.
`object` is the workload object already read by the consumer.
The prototype bridges the old and new definition types through their JSON schema.
Its converter keeps the existing component selection, accounting and phase rules.

Replacing native extraction with `Tree`, or replacing membership and grouping
with `PodMemberships`, is broader work. It is not implemented by this prototype.
See [selective extraction](#preserve-an-existing-parsers-selection-rules) for why
an unconditional recursive-tree replacement can change output.

The [live definition provider](OPERATORS.md#4-select-definitions-and-update-them-from-your-own-watch)
is available for a later adapter. Publishing definitions does not start or
synchronize the consumer's workload watches.

### Karta metrics exporter: share algorithms during events

Before:

```text
Pod event -> exporter-local owner index
  -> exporter-local instance traversal and attribution -> metric store
Prometheus scrape -> metric-store snapshot -> existing metric families
```

After, in this SDK branch:

```text
Pod event -> ownership.Index.Resolve
  -> attribution.InstanceIDs + attribution.AttributeLegacy -> metric store
Prometheus scrape -> metric-store snapshot -> existing metric families
```

The existing exporter entry points delegate to these calls:

```go
// Inside the existing Attribute adapter:
return Result(attribution.AttributeLegacy(
    ctx, pod, entry.Summary, entry.Definitions, instanceIDs,
))

// Inside the existing InstanceIDs adapter:
return attribution.InstanceIDs(ctx, entry.Karta, workload, false)
```

The [owner adapter](../../exporter/pkg/owner/index.go) aliases the shared index.
The [attribution adapter](../../exporter/pkg/attribute/attributor.go) contains both
calls above. The [collector](../../exporter/pkg/collector/collector.go) still reads
`Store.Snapshot()`; it does not discover workloads during a scrape.

Metric names, labels and Helm values are unchanged by these adapters.
This is not complete parity with the latest PoC branch: newer cluster-scope and
other PoC changes still need integration. The [exporter comparison](COLLECTOR.md)
records those differences and the inspected revisions.

## Read one ownership path

An inventory service can replace its owner-walking loop with:

```go
path, err := discoverer.OwnershipPath(ctx, discovery.PodReference(pod))
if err != nil {
    return err
}
for i := len(path) - 1; i >= 0; i-- {
    ref := path[i]
    fmt.Printf("%s/%s\n", ref.GVK.Kind, ref.Name)
}
```

The returned references run from the Pod through its controller owners to the
terminal root. This loop prints them in the reverse order for a tree.
The query reads only that chain. It does not list sibling Pods or evaluate a
Karta definition.

`PodReference` includes the Pod UID, even when the typed Pod has no TypeMeta.
A built-in `discovery.Index` resolves the path within one local generation.
Other readers use `Mapping` and `Get`; those reads are not an API transaction.

The consumer still merges paths and stores any labels or annotations its UI
needs. References contain identity, not that metadata. `DesiredTree` describes
logical Karta components and does not replace a physical ownership tree.

On failure, `OwnershipPath` returns an error and no successful path.
An `ownership.Error` can carry the verified prefix for diagnostics.
Treating that prefix as a partial inventory remains a consumer policy.

### Reuse an existing metadata cache

A controller with an established metadata cache can keep it through the
lower-level [`ownership.Trace`](../../pkg/ownership/trace.go) API.
Supply the starting object as `ownership.Object`: its `Ref` identity, complete
`Owners` references and `Namespaced` scope.
The `ownership.Lookup` function reads the same metadata for each requested owner
from the existing cache. Pass an explicit maximum owner-hop count; zero in this
lower-level function permits no owner hops.

Keep UID-aware eviction and refresh in that adapter.
This avoids introducing a second owner cache.
Unlike `OwnershipPath`, the lower-level function returns a verified prefix
alongside a traversal error. Applications using the standard Kubernetes reader
can use the simpler SDK method above.

## Use the classification discovery already computed

A workload integration controller can discover members and read their coordinates
in one observation:

```go
observed, err := discoverer.WorkloadForPod(ctx, discovery.PodReference(pod))
if err != nil {
    return err
}
for uid, member := range observed.PodMemberships() {
    fmt.Printf("pod=%s component=%s path=%v issues=%v\n",
        uid, member.Component, member.Path, member.Issues)
}
```

`PodMemberships` returns a detached `map[types.UID]attribution.Membership`.
It exposes the classification already stored in the observation, without
rerunning selectors. Keep `Issues` visible when a component or axis is unresolved.
The map's iteration order is unspecified.

For a JobSet, coordinates distinguish the `replicatedjob` component, a named
instance such as `training`, and a replica such as `0`.
Use `Pods` or component, instance and replica scopes when full Pod objects are
needed. Use `karta.New(...).Tree(ctx)` for templates, scale and declared status.

| SDK result | Consumer responsibility |
| --- | --- |
| Confirmed root and complete Pod membership | Eligibility and presentation filters |
| Component, instance and replica coordinates | Stable record IDs and output conversion |
| Templates, scale, phases and raw conditions | Resource accounting and product status precedence |
| Immutable observations | Watches, retries, reconciliation and writes |

Full discovery requires complete Pod coverage and owner closure.
A scheduler-filtered cache cannot satisfy that contract.
Replacing label-derived membership also requires controller ownership to identify
the same Pod set.

### Preserve an existing parser's selection rules

`Tree` evaluates templates and scale throughout the definition, plus the root status.
It is appropriate when the caller wants that complete result.
It can change the behavior of a parser that deliberately reads only selected components:

- An ignored descendant can now fail an expression during extraction.
- Scale on a structural component can fail even if the caller only needs its children.
- A child definition can appear below several parent instances.
  Summing every appearance can count the same globally extracted child more than once.

For a compatibility migration, keep the consumer's component selection and accounting.
The lower-level [`resource.ComponentFactory`](../../pkg/resource/component_factory.go) lets
that adapter request the components it already uses.
Compare the complete consumer output against its original parser before replacing
selection rules with recursive tree traversal.
This compatibility path does not migrate ownership, Pod membership or persistent IDs.

### Enrich members the consumer already selected

When the consumer has confirmed a root and a selected Pod set, retain that
membership source and use the advanced attribution seam:

```go
plan, err := attribution.Compile(definition)
if err != nil {
    return err
}
prepared, err := attribution.Prepare(ctx, plan, rootObject)
if err != nil {
    return err
}
for _, pod := range selectedPods {
    member, err := attribution.Evaluate(ctx, pod, prepared)
    if err != nil {
        return err
    }
    fmt.Printf("pod=%s component=%s issues=%v\n",
        pod.Name, member.Component, member.Issues)
}
```

Reuse the plan for the definition and prepare once for the root observation.
Classification does not prove ownership or complete coverage.
Strict attribution also does not reproduce a consumer's owner-kind fallback or
first-match rule automatically.

See [performance and reuse](PERFORMANCE.md) for the state each layer retains.

## Preserve the exporter contract

The exporter adapters already share the owner index, legacy Pod attribution and
instance traversal. The exporter still owns its controller and compact metric store:

```text
object event -> resolve root -> classify Pod -> update metric record
scrape       -> read metric-store snapshot -> emit existing metrics
```

A new consumer can project `PodMemberships()` directly into its records.
The existing exporter deliberately retains `AttributeLegacy`, including
first-match selection, degraded fields and its health rules.
Changing it to strict discovery is a separate migration decision.

Keep root-status evaluation separate when metrics do not need scale or templates.
An unrelated extraction failure should not suppress useful status.
The [collector comparison](COLLECTOR.md) records the implemented adapters,
definition and boundary differences, and newer PoC fixes still to integrate.

## Keep a workload below an orchestration parent

A controller can own several independently managed workloads:

```text
WorkloadEnvelope
  +-- JobSet train-a
  |     +-- Job -> Pod a0
  |     +-- Job -> Pod a1
  +-- JobSet train-b
        +-- Job -> Pod b0
```

All arrows here are Kubernetes controller-owner edges.
`WorkloadEnvelope` is a fictional example kind.

| Policy | Starting from Pod a0 | Returned Pods |
| --- | --- | --- |
| Current `NewDiscoverer` | Terminal WorkloadEnvelope | a0, a1, b0 |
| Proposed stop before WorkloadEnvelope | JobSet train-a | a0, a1 |
| Same proposed policy, starting from b0 | JobSet train-b | b0 |

Without a definition for the envelope, the current result is generic.
Adding a JobSet definition alone does not change the selected root.
`Workload(ctx, trainARef)` currently returns `ErrNotWorkloadRoot` because train-a
still has a controller owner. `MaxDepth` is an error limit, not a root selector.

### What already exists

The exporter uses `ownership.Index.Resolve` with an exact-GVK ignored-owner predicate.
It stops before reading that owner and keeps the highest accepted workload
candidate found below it. A successful boundary result reports `ignored_owner`.
If no candidate exists, this path does not invent a generic workload.
This is a lower-level compatibility API, separate from `discovery.Index`.

The candidate rule matters:

```text
Pod -> defined Job -> undefined Bridge -> ignored Envelope

Exporter candidate selection: Job
Proposed generic cutoff:      Bridge
```

Ignoring an owner is not the same as skipping through it to a grandparent.
It stops traversal at that edge. Nor does it globally exclude that kind from
all independent observations.

### Proposed SDK behavior

This is a design discussion. No ignored-owner constructor option is implemented.
The proposed first policy is a copied, immutable set of exact parent GVKs:

- Preserve terminal-owner discovery by default.
- Stop before mapping or reading a configured controller owner. Keep the last
  verified child as the workload, even if that child has no Karta definition.
- Validate the reference's syntax before stopping. Its referenced UID cannot be
  checked against an object intentionally not read.
- Preserve original object bodies and owner references. Do not hide the parent by
  editing objects inside a reader wrapper.
- Keep sibling workloads separate. Use the same boundary for finding the starting Pod's workload,
  explicit-root queries and every Pod's membership check.
- An explicit query for the parent still reads that parent. Cut child edges must
  not cause the parent's observation to reabsorb those separate workloads.
- Expose why traversal stopped if a consumer needs boundary provenance.
  Preserving the exporter's `ignored_owner` metric label requires this.

Exporter-style selection of the highest accepted candidate is a separate policy.
It needs an explicit rule for accepted kinds and the no-candidate case; it must
not silently change generic discovery when a definition is added.

This cannot be implemented only in `NewDiscoverer`:

- `Collect` currently follows the full owner chain.
- `Index.Replace` requires full owner closure and partitions Pods by terminal root.
- Capture, explicit-root validation and attribution reuse depend on that partition.

Acquisition and indexing must share the policy. Otherwise an unreadable ignored
parent can fail initial collection, or sibling Pods can be merged before the query.
Changing the policy must rebuild the affected inventory and invalidate derived state.
Client reuse or `CacheOptions` alone does not solve this.

### Acceptance cases before claiming support

Run each case through direct discovery, indexed capture and attribution reuse:

- Two sibling workloads under one parent stay separate; all Pod phases remain visible.
- A missing or unreadable ignored parent causes no lookup of that parent.
- Exact API versions match correctly; malformed or multiple controller references fail.
- Unknown children remain generic; zero-Pod children can be queried explicitly.
- Missing owners, UID replacement, invalid scope, cycles and depth errors below
  the boundary still fail instead of silently selecting a different workload.
- Reparenting and policy changes update membership while prior observations stay unchanged.
- Exporter candidate selection, field degradation and boundary provenance retain
  their deliberate behavior, including cluster-scoped owner cases.

These are required future tests, not claims about the current live-test suite.
