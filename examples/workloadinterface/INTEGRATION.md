<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Proposal: share workload discovery and interpretation through Karta

- Status: proposed integration; selected adapters have local validation
- Authors: @AviadHayumi
- Updated: 2026-10-18
- Scope: consumer integration, with no CRD, metric or Helm contract change

## Summary

![Existing consumer logic, the proposed SDK replacement, and each consumer's output](images/consumer-integration.png)

Keep each application's watches, caches and output writes. Replace one function
inside its existing event-processing flow with a shared Karta implementation.
Karta is a library called by that application; it starts no watches or controllers.

For a JobSet update, EWI still reads its cached JobSet and writes an
`ExternalWorkload`. The SDK reads the components between those two steps.
For a Pod update, workload-discovery still gathers its eligible Pods and writes
its physical `Workload` tree. The SDK validates owner chains during that work.

These first adapters do not call `NewDiscoverer` or `WorkloadForPod` and do not
enable the SDK's optional attribution cache. Broader discovery and grouping
integration is a separate phase below.

This proposal covers three consumers:

- k8s-workload-discovery: follow controller owners and build a physical inventory.
- External Workload Integrator (EWI): interpret workload components and maintain workload records.
- Karta metrics exporter: attach workload coordinates to Pods and expose metrics.

The [operator recipe](OPERATORS.md) contains the complete wiring.
The [usage guide](README.md) shows how to inspect and traverse the returned workload.

## Motivation

Consider the JobSet used by the [executable examples](traversal_test.go):

```text
JobSet training-set
  Job training-0   -> Pod training-0-pod
  Job training-1   -> Pod training-1-pod
  Job evaluation-0 -> Pod evaluation-0-pod
```

A Pod event starts with only one member of this tree.
An inventory controller needs its physical owners.
An integration controller needs templates, replicas and workload status.
A metrics exporter needs the Pod's workload, component, instance and replica group.

Today these consumers either maintain their own traversal/classification code or
use a different Karta version behind their own adapter.
A change in ownership, definition interpretation or Pod classification must be
checked in each place. The proposal shares those algorithms while preserving
the different outputs and policies around them.

### Goals

- Replace repeated ownership and interpretation logic with explicit SDK calls.
- Reuse each operator's existing client and cache through small interfaces.
- Keep controller-runtime types out of the SDK integration contracts.
- Preserve existing consumer output before expanding the migration.
- Make known and unknown workload kinds usable through the same discovery API.

### Non-goals

- Move watch lifecycle, queues, retries, persistence or scheduling decisions into Karta.
- Replace every EWI handler or its persistent grouping model in one change.
- Change exporter metrics, labels, failure values or Helm options.
- Claim equal performance or complete consumer migration from a passing example.

## Proposal

### What changes in the three consumers

| Consumer | Logic today | First replacement | Consumer keeps |
| --- | --- | --- | --- |
| k8s-workload-discovery | Its controller-owner walking loop | `ownership.TraceRoot` with retained metadata | Metadata cache, filters, merged physical tree and Workload writes |
| EWI | Older Karta component reader | SDK `resource.ComponentFactory` | Handler selection, accounting, phases, grouping, IDs and writes |
| Karta metrics exporter | Local owner index, instance extraction and Pod attribution | `ownership.Index`, `attribution.InstanceIDs`, `attribution.AttributeLegacy` | Watch controller, root policy, definition selection, metric store and scraping |

"Today" means the consumer logic before these adapters. "After" below means the
first integration phase, not a replacement of the entire controller.
WLD and EWI adapters are tested local changes, not deployed consumer migrations.
The exporter adapters are implemented in this repository.
[Measured revisions and comparison scope](benchmarks/consumer-comparison/README.md#what-was-replaced)
are recorded separately from the proposal.

### Integration phase 1: watches, SDK calls, caches and writes

The existing applications own every watch and write in this table.
Adding the SDK does not add another Kubernetes watcher or cache to these paths.

| Consumer | Existing event sources retained | Shared call inside the handler | Application output |
| --- | --- | --- | --- |
| WLD | Pods, its Workload records, and selected metadata changes on discovered root kinds; ResourceClaims when DRA filtering is enabled | `ownership.TraceRoot` with WLD's owner lookup | Physical `Workload` tree and status |
| EWI | Supported workload kinds and Pods; Karta definitions configure additional supported kinds | `resource.NewComponentFactoryFromObject` and component reads | `ExternalWorkload` spec and observed status |
| Exporter | Pods, Karta definitions, selected workload kinds and required owner kinds | `ownership.Index`, `InstanceIDs`, `AttributeLegacy` | In-memory metric records, exposed on scrape |

The word "cache" refers to different data:

| Data reused | Who maintains it in phase 1? | What it avoids |
| --- | --- | --- |
| Kubernetes objects from list/watch | Each application's existing informer cache | Repeated API reads for watched objects |
| WLD owner metadata | WLD's existing bounded LRU with expiry | Repeated Job/JobSet metadata reads during owner walks |
| Exporter ownership and metric records | Exporter event handlers update the shared ownership index and existing metric store | Rebuilding ownership and classification on every scrape |
| SDK derived attribution (`discovery.Options.Cache`) | Not enabled by these adapters | Available separately for higher-level indexed discovery |

### k8s-workload-discovery: replace the owner walk

#### Today: WLD watches and assembles the physical tree

The inspected WLD binary selects `WorkloadTreeReconciler`:

1. An eligible Pod event maps to a Workload key through the existing Pod index.
   For a new Pod without an indexed workload, the mapper walks its owners.
2. The worker reads the Workload and current Pods through WLD's existing
   controller-runtime client/cache.
3. For an existing Workload, it rebuilds the tree from eligible Pods.
   For a missing Workload, it finds a representative Pod and builds the new tree.
4. WLD reverses and merges owner paths, writes the physical tree and lets its
   separate status controller update status.

```text
Pod event -> map Workload key (owner walk if needed) -> WLD queue
  -> read Workload and current Pods -> WLD's owner-walking loop
  -> merge physical paths -> write Workload tree -> update status separately
```

WLD also handles Workload rebuild requests and registers root-kind watches for
relevant label/annotation changes. DRA filtering adds ResourceClaim events.
Its alternative Pod-keyed controller also uses the same `OwnerWalker`.
These event sources and controller choices stay with WLD after integration.

A Karta definition is not required to follow the owners of an unknown kind.
For `training-0-pod`, the chain is `Pod -> Job -> JobSet`.
WLD retains labels and annotations while building the tree.

#### After phase 1: call the SDK from the existing walker

Inside `OwnerWalker.WalkPod`, replace the traversal with:

```go
_, err := ownership.TraceRoot(ctx, start, readOwner, maxHops)
```

- `start` supplies the Pod identity, owner references and namespace scope.
- `readOwner` is a function WLD supplies. The SDK calls it for each needed owner.
- `maxHops` preserves WLD's configured depth limit.
- The lookup retains each fetched object's metadata for WLD's tree builder.
- WLD handles the error, reverses the verified path and merges it with other Pods.

```text
Pod event -> same Workload-key mapping and WLD queue
  -> read Workload and Pods -> SDK TraceRoot whenever WLD walks owners
  -> WLD readOwner callback -> WLD owner cache / API reader
  -> WLD converts and merges verified paths -> same Workload output
```

`TraceRoot` does not list sibling Pods or build WLD's output tree.
WLD still collects its eligible Pods and merges their paths.
Its existing filtering can exclude Pods that full SDK discovery would include,
including unscheduled or terminating Pods.

#### How WLD uses its existing cache

For each requested Job or JobSet, `readOwner` calls WLD's `fetchPartial`:

1. Look up metadata by GVK, namespace and name.
2. Return a valid cached entry when its UID matches the owner reference.
3. On a miss, expiry or UID mismatch, read metadata through the API reader.
4. Cache the successful read; SDK traversal validates the returned identity.

The current production-path default is 1,024 entries with a 30-second TTL.
Those are WLD settings, not SDK defaults. The benchmark uses its separately
recorded cache sizes and TTL.
For two Pods sharing a JobSet, the second walk can reuse that JobSet metadata.
No second owner cache or SDK attribution cache is introduced.

A root-label or annotation event only queues reconciliation; it does not refresh
or invalidate this LRU. For an existing Workload, reconciliation reads root
metadata separately through `fetchOwnerMeta` and the API reader. When that read
succeeds, it updates the persisted root labels and annotations. This direct read
leaves the LRU alone.
Owner walks can still reuse older cached metadata. Expiry is checked on the next
lookup; it does not schedule a refresh or reconciliation. Another event or a
consumer-requested rebuild must cause the next walk. The 30-second TTL is not a
promise that every tree node refreshes within 30 seconds. Phase 1 preserves this
existing distinction between root-metadata refresh and owner-walk caching.

The SDK adds stricter UID, scope, cycle and controller-owner validation.
These are intentional differences from WLD's original walker.

<details>
<summary>Partial paths, errors and the alternative path API</summary>

For a missing owner or depth limit, the adapter preserves the verified prefix
under WLD's existing partial-result policy. Fetched but rejected objects are
excluded. Other structural errors fail instead of publishing an accepted path.
References alone do not contain the labels and annotations needed by WLD.

At this lower level, `maxHops=0` permits no owner hops.
WLD resolves its own default before making the call.
A separate reader-based integration can call `sdk.OwnershipPath` instead.
That query lists no sibling Pods and returns no successful path on error;
its typed error can retain a diagnostic prefix.

[Traversal implementation](../../pkg/ownership/trace.go) and
[reader-based path tests](../../pkg/discovery/ownership_path_test.go).

</details>

### EWI: reuse the SDK component reader

#### Today: EWI watches workload objects and selects a parser

EWI watches its built-in supported kinds plus eligible root/child kinds supplied
by Karta definitions. A kind must exist and have the required read permissions.
It also watches Pods and Karta definitions through its existing controllers.
Native handlers keep precedence: a type with a native handler does not
necessarily pass through the generic Karta reader.

Use JobSet as the generic-Karta example:

```text
JobSet Add/Update -> EWI informer updates its object cache -> EWI task queue
  -> worker reads current JobSet and matching Karta definition
  -> older Karta component reader -> EWI converter
  -> EWI writes ExternalWorkload spec and observed status
```

EWI waits for its caches to synchronize before starting workers.
On Add, it can fall back to the copied event object if the fresh read fails;
updates follow its existing retry/missing-object handling.
Root selection, Pod grouping and resource accounting are separate EWI logic.

#### What happens when a Pod changes

This existing EWI path is separate from the JobSet parse above:

- Eligible Pod events enter EWI's queue. Add/Update reads the current cached Pod;
  Delete uses the event object. Scheduler and namespace filters still apply.
- A standalone Pod uses EWI's native Pod parser for requested resources and
  observed status. It does not call the SDK component factory.
- A Pod owned by a generic JobSet follows EWI's owner chain to that JobSet's
  `ExternalWorkload` and receives missing top-owner labels. This ordinary path
  does not call `KartaTopOwnerHandler.Parse`; the JobSet's own reconciliation does.
- Pod Add/Delete can enqueue the separate `StructureUpdater`. It maintains
  persistent element IDs, writes `ExternalWorkload.Status.StructureElements`
  and labels newly processed Pods with their element paths.
- Pod Update does not trigger that grouping hook. Label edits or reparenting do
  not gain a guarantee of regrouping both old and new workloads. Existing nonempty
  ownership labels are not repaired by this SDK adapter.

Some consumer-specific handlers can separately request root/status refreshes.
Phase 1 preserves these routes; it does not turn every Pod event into an SDK parse.

#### After phase 1: use the shared component reader in Parse

Keep that event flow. Inside `KartaTopOwnerHandler.Parse`, convert the selected
definition to SDK types and call:

```go
factory := resource.NewComponentFactoryFromObject(definition, object)
rootComponent, err := factory.GetRootComponent()
if err != nil { return err }
childComponents, err := factory.GetChildComponents()
if err != nil { return err }
// EWI's existing converter consumes these components.
```

The converter reads `GetExtractedInstances(ctx)` for Pod-bearing components
and `GetStatus(ctx)` for the root. EWI then calculates requested resources,
requested Pods and its product-specific status before writing `ExternalWorkload`.
The SDK component reader does not write Kubernetes objects.

```text
Same JobSet watch -> same EWI cache and queue -> same handler selection
  -> SDK component reader -> same EWI selection and accounting
  -> same tested ExternalWorkload output
```

#### How EWI uses its existing cache

- Supported-type `Get` and `List` read EWI's existing informer cache.
- The matching Karta definition is selected through that cached client.
- Unknown-type `Get` can use EWI's existing direct-API fallback.
- The SDK receives the already-read object and definition, not a Kubernetes client.
- Component extraction makes no Kubernetes reads. EWI's surrounding conversion
  can still read related objects through its existing client.

Every parse makes a private typed copy of the definition and constructs a new
component factory. This replaces the first prototype's JSON round trip.
There is no retained conversion/result cache and no SDK discovery index.
Definition edits become visible on a later parse; this adapter does not add a
workload-wide requeue when a definition changes.

EWI does not call `NewDiscoverer`, `WorkloadForPod`, `DefinitionRegistry.Replace`
or enable `discovery.Options.Cache` in this phase.
Its Pod cache is scheduler-filtered, so it cannot claim complete workload Pod
coverage for the higher-level discovery API.
[Component-factory implementation](../../pkg/resource/component_factory.go).

<details>
<summary>Existing definition-watch lifecycle and conversion validation</summary>

EWI's Karta controller maintains definition readiness and read permissions.
In the inspected consumer, a newly eligible kind without an existing registered
handler can trigger a process restart to register its informer.
The live-provider recipe later in this document does not replace this lifecycle
in phase 1. No automatic reclassification guarantee is added for ordinary edits.

The typed-copy tests compare the complete serialized definition with the old
conversion and check input isolation. Conversion remains part of the measured
parse cost. No converted definition survives a parse.

</details>

#### Preserve an existing parser's selection rules

The first phase retains EWI's component selection and accounting.
A recursive `Tree` is not a drop-in replacement for that selection.

<details>
<summary>Why the first adapter retains selective parsing</summary>

A recursive `Tree` reads the complete declared structure:

- An ignored descendant may now fail an expression.
- Scale on a structural component may fail even if only its children are needed.
- One globally extracted child can appear beneath multiple parent instances.
  Summing each appearance can count the same resources more than once.

Keep current selection and accounting in the first migration.
Native handler precedence and workload-boundary selection remain with EWI.

</details>

#### Later: share classification without replacing persistent grouping

Today, EWI's grouping path also:

1. Reads its selected Pod set and existing element labels.
2. Skips updates when the persisted element set is unchanged.
3. Reads needed owner chains and classifies Pods using component rules.
4. Reuses or creates element IDs, updates workload status and labels Pods.

Its classification can use selectors, physical owner-kind fallback and logical groups.
The SDK does not automatically reproduce all those policies.

The proposed next adapter keeps the consumer's confirmed root and selected Pods,
and replaces only compatible definition-driven classification:

```go
plan, err := attribution.Compile(definition)
if err != nil { return err }
prepared, err := attribution.Prepare(ctx, plan, rootObject)
if err != nil { return err }
for _, pod := range selectedPods {
    member, err := attribution.Evaluate(ctx, pod, prepared)
    if err != nil { return err }
    fmt.Printf("pod=%s component=%s path=%v issues=%v\n",
        pod.Name, member.Component, member.Path, member.Issues)
}
```

Compile once per definition and prepare once per root observation.
The result supplies component coordinates and unresolved issues.
It does not prove ownership or complete Pod coverage.
The consumer converts coordinates into its existing IDs, groups and writes.

This grouping replacement is proposed, not implemented in the EWI prototype.
EWI can append multiple matching components in sorted order.
SDK `Evaluate` chooses one most-specific producer and reports unrelated matches
as ambiguous. Compare those paths, owner-kind fallback and persistent IDs before
replacing the existing grouping engine.
A scheduler-filtered Pod cache is insufficient for full SDK workload discovery.

### Karta metrics exporter: share algorithms during events

Today, the exporter owns Pod and Karta informers. Selected workload kinds use
full-object informers; child and required owner kinds use metadata informers.
Missing intermediate owner kinds can cause another metadata watch to be registered.
Its callbacks process events directly; the SDK does not add a work queue.

The exporter has two paths before integration:

```text
Object event -> local owner index -> local instance extraction / Pod attribution
             -> compact metric records
Prometheus scrape -> metric-store snapshot -> existing metric families
```

Replace the event-time algorithms with shared SDK implementations.
The existing exporter adapters now delegate as follows:

```go
// InstanceIDs adapter:
return attribution.InstanceIDs(ctx, entry.Karta, workload, false)
```

```go
// Attribute adapter:
return Result(attribution.AttributeLegacy(
    ctx, pod, entry.Summary, entry.Definitions, instanceIDs,
))
```

The [owner adapter](../../exporter/pkg/owner/index.go) aliases `ownership.Index`.
The [attribution adapter](../../exporter/pkg/attribute/attributor.go) contains the
calls above. The resulting flow is:

```text
Object event -> shared owner index -> shared instance extraction / Pod attribution
             -> the same compact metric records
Prometheus scrape -> metric-store snapshot -> the same metric families
```

`AttributeLegacy` preserves first-match selection, degraded fields and existing
failure reasons. Switching to strict discovery is a separate behavior change.
The [collector](../../exporter/pkg/collector/collector.go) still reads `Store.Snapshot()`.
Scraping does not discover workloads or reevaluate their definitions.
Root-status evaluation stays separate from template and scale extraction.

The same three data layers remain after integration:

| Layer | Event-time use | Scrape-time use |
| --- | --- | --- |
| Exporter informer stores | Supply current Pods, roots and owner metadata | None |
| Shared `ownership.Index` | `UpsertTrackedObject`, `Resolve` and `DeleteObject` maintain ownership | None |
| Existing metric store | Handlers publish derived workload and Pod records, including `UpsertPod` | `Snapshot` supplies metric values |

Instance IDs are reused from stored workload state when available; extraction is
not repeated for every Pod unnecessarily. The optional higher-level SDK
attribution cache is not part of these stores.

Existing Karta-change handling also stays with the exporter: update definition
selection, reconcile required watches, rebuild cached workload records and force
Pod attribution. Sharing the algorithms does not move this lifecycle into the SDK.

#### When existing Pod metrics change

- An accepted workload event rebuilds its stored workload state and instance IDs.
  Existing Pod records are reevaluated when owner resolution or the component/
  instance set changes, or when a definition rebuild forces attribution.
- A changed owner edge reprocesses cached descendants. An owner that arrives late
  also retries Pods waiting for it. A root label or template edit alone does not
  universally trigger Pod re-attribution.
- Relevant Pod updates, including labels and owner references, reattribute that
  Pod from the current informer object. Replacing its record moves its workload
  association; Pod deletion removes its record.
- Workload/owner deletion removes the indexed object and reprocesses descendants.
  Affected Pods can become unresolved or attach to another accepted root under
  the existing ownership policy.
- A scrape reflects the records published by completed event handling. It does
  not wait for every informer event or refresh stale inputs itself.

The [event handlers](../../exporter/pkg/controller/events.go) retain this lifecycle
before and after replacing the shared algorithms.

The [exporter comparison](COLLECTOR.md) shows a complete Pod metric and remaining
PoC differences, including newer cluster-scope behavior.
Sharing these algorithms does not mean every newer PoC change is integrated.

### Use the classification discovery already computed

This is a separate, broader integration phase. The SDK functions exist and have
their own tests, but the WLD/EWI/exporter adapters above do not use this route.
EWI grouping migration and acceptance of different membership policies remain open.

For a new consumer that needs the complete workload, use the higher-level API:

```go
observed, err := sdk.WorkloadForPod(ctx, discovery.PodReference(pod))
if err != nil { return err }

root := observed.Root()
fmt.Printf("workload=%s/%s generic=%t pods=%d\n",
    root.GVK.Kind, root.Name, observed.Generic(), len(observed.Pods()))
for uid, member := range observed.PodMemberships() {
    fmt.Printf("pod=%s component=%s path=%v issues=%v\n",
        uid, member.Component, member.Path, member.Issues)
}
```

For `training-0-pod`, the workload is `JobSet/training-set` with all three Pods.
The exact root group/version/kind determines which selected definition applies:

| Definition selection | Returned workload | Classification |
| --- | --- | --- |
| JobSet definition available | Same JobSet and complete Pod set | `replicatedjob`, named instance, replica key |
| No selected definition for this GVK | Same JobSet and complete Pod set | Generic `pods` component |
| Definition provider cannot read its source | Query fails | No false generic result |

Unknown definition does not mean unknown ownership.
`PodMemberships()` returns detached classification already computed in the observation.
It does not rerun selectors; retain `Issues` when a coordinate is unresolved.
Map iteration order is unspecified.

Use component, instance and replica scopes for full Pods, and `DesiredTree(ctx)`
for declared templates, scale and status. That logical tree is different from
the physical owner tree. [Tree-first examples](README.md#read-the-workload-tree)
show traversal and the exact Pods returned at each scope.

### Keep clients, caches and definition watches with the consumer

This section describes the higher-level integration just introduced.
It is not additional setup required by phase 1, and it does not describe EWI's
current definition controller.
Construct the SDK using a caller-supplied `discovery.Reader` and selected definitions:

```go
definitions, err := discovery.NewDefinitionRegistry(selectedDefinitions...)
if err != nil { return err }
sdk, err := karta.NewDiscoverer(reader, discovery.Options{
    DefinitionProvider: definitions,
    MaxDepth: 32,
})
if err != nil { return err }
```

`selectedDefinitions` is the complete chosen set of `*v1alpha1.Karta` values.
This explicit provider is authoritative; omitted kinds are generic.
Without a provider, constructor `Definitions` overrides the built-in catalog.
Do not combine `Definitions` with `DefinitionProvider`.

| Existing data source | Integration choice | Requirement |
| --- | --- | --- |
| Kubernetes dynamic client | Shipped client-go reader and REST mapper | Complete paginated Pod lists and readable owner chain |
| Existing metadata cache | `ownership.Lookup` for owner tracing | Scope and UID-aware metadata reads |
| Operator client or full-object cache | Consumer adapter implementing `discovery.Reader` | Complete declared coverage, readiness and detached objects |
| Caller-fed workload index | `discovery.Index` | Complete initial data, ordered changes and recovery |

No SDK interface requires a controller-runtime client type.
Its adapter stays in the consumer, using that project's chosen version.
Kubernetes Go types still need compatible module versions; this is not a promise
that every dependency combination compiles unchanged.

`MaxDepth` limits owner hops; its high-level zero value selects 32.
Optional `CacheOptions` reuses derived attribution only with a concrete
`*discovery.Index`. Omitting `Cache` disables this reuse. When enabled,
`MaxWorkloads` and `MaxPods` must both be positive; zero does not mean unlimited.
These bound retained attribution entries and Pod membership records, not index
bytes, informer objects or total process memory.

The consumer initializes the index with complete coverage and publishes ordered
object changes. A changed indexed root, Pod record, membership or compiled
definition prevents reuse of the prior attribution on the next query.
Unsupported expressions and failed attribution bypass reuse. Previously returned observations remain unchanged.
No cache flush is needed for a correctly published change; missed watch events
still require consumer recovery. The SDK cache cannot repair a stale input index.

In this broader integration, definition updates follow this consumer-owned flow:

```text
Karta event -> definition queue -> read complete current selection
  -> definitions.Replace(selected...) -> enqueue affected workload roots
  -> acquire and publish each root through its serialized reconciliation
```

A failed replacement keeps the last valid selection and returns an error.
The consumer reports/retries it, retains selected catalog entries in the next
publication, and gates readers until initial synchronization succeeds.
An intentionally empty selection is different from an unavailable source.
The [tested operator recipe](OPERATORS.md#4-select-definitions-and-update-them-from-your-own-watch)
shows catalog merging, updates and deletion without adding an SDK watcher.

### Keep a workload below an orchestration parent

![Current terminal-owner discovery versus the proposed workload boundary](images/workload-boundary.png)

Some controllers manage independently useful child workloads.
For EWI's WorkloadRunner case, the consumer can treat the runner as reference-only
and choose a supported child handler. The high-level SDK cannot yet express that policy.

The diagram uses a fictional `WorkloadEnvelope` that owns two JobSets.
With no envelope definition, current `NewDiscoverer` returns the generic envelope
and all three Pods. Adding a JobSet definition does not change the selected root.
An explicit child query currently returns `ErrNotWorkloadRoot`.
`MaxDepth` is an error limit, not a workload-boundary selector.

The proposed first policy is an immutable set of exact parent GVKs to stop before:

- Keep terminal-owner discovery as the default.
- Validate the owner reference, then stop before mapping or reading that parent.
- Return the last verified child, even without a matching Karta definition.
- Preserve object bodies and original owner references.
- Keep sibling workloads separate during collection, indexing and querying.
- Expose why traversal stopped; do not claim to verify an unread parent's UID.

No ignored-parent constructor option is implemented.
This proposal needs a shared policy across `Collect`, `Index.Replace`, captures,
explicit-root validation and attribution-cache invalidation.
Changing only the final query could merge siblings before the cutoff is applied.
An explicit parent observation must not reabsorb workloads separated by cut edges.

The exporter's lower-level `ownership.Index.Resolve` already has a different rule:
it stops before an ignored exact GVK and keeps the highest accepted workload
candidate below it. Without a candidate it does not invent a generic workload.
A successful boundary result uses `ignored_owner`.

```text
Pod -> defined Job -> undefined Bridge -> ignored Envelope

Existing exporter candidate selection: Job
Proposed generic child cutoff:        Bridge
```

Stopping before a parent is not skipping through it to a grandparent.
Exporter/EWI candidate selection needs a separate acceptance rule and no-candidate
behavior. It must not silently become the generic discovery policy.

## Compatibility and migration

Adopt one operation at a time:

1. Keep a frozen baseline and compare complete consumer outputs.
2. Replace ownership or extraction while preserving the consumer's surrounding policies.
3. Accept intentional error differences and measure CPU, allocations and retained memory.
4. Migrate membership/grouping only after its separate compatibility cases pass.
5. Deploy through the consumer's own rollout and rollback process.

The SDK starts no watches or background reconciliation.
On Pod deletion or reparenting, the consumer refreshes the former workload too.
Concurrent workers must serialize acquisition plus publication by workload root;
a write lock alone cannot prevent an older observation overwriting a newer one.

Public Karta definitions, exporter metric names/labels and Helm values stay unchanged
in these first adapters. Existing Go `Tree()` usage remains valid.
Detailed [client contracts](../../docs/Live%20Workload%20Discovery.md) and
[operator concurrency rules](OPERATORS.md) remain part of integration review.

## Validation and test plan

| Replacement | Evidence available | Required before broader adoption |
| --- | --- | --- |
| WLD owner walk | Complete normal-fixture output, GET-count comparisons and focused race checks | Accept stricter malformed-chain behavior; validate consumer deployment |
| EWI component reader | Complete parse-output comparisons, input immutability and focused race checks | Compare multi-match paths, fallback selection and persistent IDs before replacement |
| Exporter algorithms | Matching complete fixture exposition and exporter race checks | Integrate newer PoC behavior and test the target deployment |
| SDK discovery | Recorded-object replay, live membership, mutation and grouping evidence | Keep documented exclusions; do not infer deployed consumer coverage |
| Proposed parent boundary | Design only | Direct, indexed and cached boundary tests before claiming support |

The [validation record](VALIDATION.md) identifies exact test scope and revisions.
Historical replay includes the documented Grove extraction limitation and excluded
recordings; those cases are not new passing claims.
[Consumer measurements](benchmarks/consumer-comparison/README.md#correctness-controls)
identify the output checks used alongside timing.

For the proposed parent boundary, acceptance must cover:

- Sibling isolation, zero-Pod children and all Pod phases.
- No lookup of a missing or unreadable ignored parent.
- Exact API versions and rejection of malformed/multiple controller references.
- UID replacement, invalid scope, cycles and depth errors below the boundary.
- Reparenting and policy changes while old observations remain unchanged.
- Exporter candidate selection, degraded fields and boundary provenance.
- Cluster-scoped parents and explicit parent queries without reabsorbing cut children.

### Performance is part of acceptance

The [optimization report](benchmarks/consumer-optimization/README.md) compares the
original consumers, first SDK integration and optimized adapters across 300
successful processes. Raw samples include CPU, retained heap, peak RSS and GC.
The [first report](benchmarks/consumer-comparison/README.md) remains available.

| Measured integration | Optimized result against the original consumer |
| --- | --- |
| WLD | Operation time +2.9-8.7%; mixed CPU +3.0%, down from +12.9% in the first SDK integration |
| WLD allocations | Deep warm chain -3.5% bytes/op; the first integration's +34% regression is removed |
| EWI | Small parses 9.0-10.2% faster with 10.0-11.0% less CPU; 128 instances close |
| Exporter | Owner lookup 29.1% faster with zero allocations; Pod events, workload updates and scrapes within 0.5% |
| Retention | Bounded-fixture retention stays close; 100k ownership index about 144.7 MiB in all versions |

These results do not establish performance parity or production throughput.
WLD's remaining overhead is not resolved. Strict validation stays enabled, but
the measurements do not assign that cost to individual checks.
The 100k test covers the ownership index, not a complete exporter deployment.

## Risks and alternatives

| Choice | Tradeoff |
| --- | --- |
| Keep separate implementations | Preserves each existing path, but fixes and semantics continue to diverge |
| Force every consumer through `WorkloadForPod` | Requires complete coverage and full objects even when only an owner path or metric record is needed |
| Replace EWI parsing with recursive `Tree` immediately | Changes which expressions are evaluated and can change resource accounting |
| Share lower-level algorithms first | Keeps migration reviewable, but adapters and consumer policies still need tests |
| Hide an orchestration parent in a reader wrapper | Makes objects disagree with their real ownership and can produce inconsistent membership |

Related design: the [operator integration decision](../../docs/design/sdk-operator-integration-decision.md)
explains reader/provider separation. [Resource-reference discussion](REFERENCES.md)
covers reading external workload objects; it does not implement a root cutoff.

## Implementation history and remaining work

- 2026-10-16: local ownership and component-reader adapters, shared exporter
  algorithms and consumer comparisons are available. EWI grouping migration and
  high-level parent boundaries remain proposal-only; consumer deployment is pending.
- 2026-10-18: documented the retained watches, exact SDK calls, cache ownership
  and output writes for phase 1. This clarification changes no implementation or
  deployment state; the higher-level discovery/provider/cache route remains a
  separate integration choice.

Diagram sources: [consumer integration](images/consumer-integration.excalidraw) and
[workload boundary](images/workload-boundary.excalidraw). Open them at excalidraw.com to edit.
