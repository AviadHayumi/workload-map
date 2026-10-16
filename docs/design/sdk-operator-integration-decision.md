<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Embed Karta in an operator

A Pod event should lead to one clear call:

```text
operator's Pod watch -> operator's queue -> read current Pod
  -> sdk.WorkloadForPod(ctx, discovery.PodReference(pod))
  -> workload identity + full Pods + Karta component coordinates
  -> operator's record, metric store or scheduling decision
```

For a physical ownership inventory, use `OwnershipPath` or `ownership.Trace`
instead. Those return the controller-owner chain without requiring a definition.
They do not build a logical component tree or list sibling Pods.

The [operator recipes](../../examples/workloadinterface/OPERATORS.md) show the
calls, inputs, outputs and definition-update path.
The [consumer comparison](../../examples/workloadinterface/INTEGRATION.md)
explains which existing policies remain with each consumer.

## Purpose and method

The question is how an operator can reuse its clients, watches and selected
Karta definitions without adopting Karta's choice of controller-runtime version.

Six repositories were inspected in local Greenlight reference checkouts.
Each research note records its exact commit and immutable source links.
No new repositories were downloaded. Upstream tests were read, not executed.
The recommendations below are design conclusions from those sources.

## Comparison matrix

| Project | Useful pattern | Limitation to account for |
| --- | --- | --- |
| client-go | Separate REST clients, cached listers and informer lifecycle. Handler registration has its own initial-sync barrier. | Informer sync does not certify asynchronously queued definition publication. Cached objects need copying. |
| controller-runtime | Read through the caller's chosen client; events map to queued reconciliation. | Its named client/option types couple versions. Default manager reads of unstructured objects bypass the cache. Cached lists do not support API pagination. |
| kube-state-metrics | Compile interpretation once; process object events into a metric store; scrapes read stored results. | Its default builder owns reflectors. Metric configuration changes restart the app. Its metric store is not an object reader. |
| OpenTelemetry Go | Inject a narrow provider; keep configuration/lifecycle on the implementation; publish immutable snapshots. | Global delegation happens once. Provider injection does not make every setting dynamically replaceable. |
| Kueue | Separate registered integrations from enabled integrations and product-specific root policy. | Integration enablement does not prove each dynamic cache is synchronized. Some event routing matches GroupKind, not exact GVK. |
| Gatekeeper | Separate desired registrations, active watches and applied definitions; reconcile changes explicitly. | Watch replacement can partially succeed. Startup readiness is not continuous readiness; some callbacks run under registry locks. |

The projects disagree on lifecycle and failure policy because they solve different
problems. Karta should adopt their separation of responsibilities, not combine
all of their controller machinery into one SDK.

## Options, ranked

### 1. Inject a reader and a live definition provider

Recommended. Keep the existing `discovery.Reader` and add a narrow
`DefinitionProvider`. Supply a small instance-owned registry for common use.
The consuming operator publishes its selected definitions into that registry.

- Effort: one provider contract, atomic definition replacement and reader adapters.
- Risk: cache coverage and definition readiness must be explicit.
- Fit: existing consumers keep their controller, transport and output format.

This follows client-go's client/lister separation, OpenTelemetry's injected
provider and snapshot technique, and Gatekeeper's explicit applied-state boundary.
See the corresponding research notes below for source evidence.

### 2. Inject a provider returning raw Karta objects on every query

Simple to implement, but it moves validation, copying and compilation into the
query path or requires another revision/cache protocol.
That is a poor default for repeated queries across many workloads.

An immutable compiled definition handle makes the publication boundary explicit.
A custom provider can reuse those handles and return lookup failures directly.
This extends the compilation-before-object-processing pattern in
kube-state-metrics; it does not claim that project has this provider interface.

### 3. Have the SDK create a manager, watches and a cache

Convenient for a standalone application, but unsuitable as the core integration
contract. It duplicates existing controllers, introduces lifecycle ownership,
and imposes framework/version choices on every consumer.

Kueue and Gatekeeper need those facilities as applications.
Their manager and watch interfaces should remain outside Karta's core.
A standalone executable can assemble the same SDK with its own chosen framework.

## Decision for Karta

### Keep the two inputs independent

```text
Kubernetes objects                  Karta definitions
caller reader                       caller provider
  direct API, cache or Index          selected catalog + watched definitions
             \                      /
               Karta discoverer
             ownership + interpretation
                        |
                 captured workload
```

`discovery.Reader` remains the object-access contract.
Its three methods use Kubernetes API machinery types, not controller-runtime types.
The SDK owns no manager, queue, informer or client shutdown.

`DefinitionProvider` answers one exact-GVK lookup for the selected workload root.
A successful absence yields generic Pod grouping.
A provider error fails the query; it must not become a generic success.
A single observation retains one immutable definition even if publication changes
while that query runs. This does not create a transaction with Kubernetes reads.

The convenience registry validates the full replacement before publication.
A failed replacement returns an error and retains the last valid set.
A successful replacement removes definitions omitted from the new set.
Old observations retain their original interpretation.

### Let the operator reconcile definition changes

The initial implementation uses `Replace(selectedDefinitions...)`.
A definition watcher queues reconciliation; the worker reads the current selected
set from a synchronized source, then replaces the registry atomically.
The same function handles additions, updates, deletions and target-GVK changes.

This deliberately avoids a second event-ordering protocol inside the SDK.
Do not apply a stale delete payload by name or compare resourceVersions numerically.
Serialize selection/read/publication in the consumer and retry failed publication.
An informer callback that only enqueues work is not proof that the registry is ready.

After publication, the operator requeues affected workloads, including workloads
that previously used a definition which was removed.
For a small definition set, requeueing all tracked workloads is a simple correct
starting policy. At larger scale, index tracked workload roots by exact GVK.
The registry does not call consumer callbacks while holding its locks.

Definition readiness and workload-reader readiness are separate:

- The registry can intentionally contain no definitions. That means generic mode.
- An external definition source that has not synchronized must return an error or
  keep reconciliation gated; it must not be represented as intentionally empty.
- A registered definition does not prove that its CRD, RBAC or object cache is ready.
- Retaining the last valid set after an invalid update does not mark that update
  successfully applied. The operator exposes the failure and chooses its retry policy.

### Preserve defaults and existing contracts

Without a provider, `Options.Definitions` continues to override the built-in
catalog by exact GVK. With a provider, that provider is authoritative.
Reject simultaneous provider and static override options instead of inventing
an undocumented precedence order.

A definition does not choose the workload root.
Current full discovery still selects the terminal controller owner.
Ignored orchestration parents and highest-eligible-owner policies remain the
separate migration described in the consumer comparison.

No existing `Discoverer` method or desired-tree method needs replacement.
No metric name, label or Helm value changes are part of this work.
Adding an options field supports keyed literals; unkeyed external literals are
not a compatibility guarantee and must be changed to keyed construction.

### Reuse state where it already belongs

- Kubernetes cache: owned and synchronized by the caller.
- Optional `discovery.Index`: complete workload membership, fed explicitly by caller.
- Definition registry: currently selected immutable compiled definitions.
- Optional attribution cache: bounded derived membership, keyed by object and
  definition identity. A changed definition cannot reuse the old interpretation.
- Consumer store: persistent inventory, product records or compact metrics.

There is no need for a separate definition cache behind every discoverer query.
A provider should reuse an immutable handle while its definition is unchanged.
The registry owns no history of every prior definition revision.

### Be precise about version independence

The root SDK module and public reader/provider contracts must not import
controller-runtime. A consumer-side adapter translates the version that operator
already uses. A copied controller-runtime Reader signature would still be coupled
through its named Object, ObjectList and variadic option types.

Karta still uses versioned Kubernetes API machinery and client-go modules.
Go module version selection and the consumer's Kubernetes dependencies still need
compatibility testing. This design does not promise arbitrary Kubernetes module
combinations or an unchanged controller-runtime adapter for every release.

## What NOT to do

- Do not start a hidden second cache or watch from `NewDiscoverer`.
- Do not treat a scheduler-filtered Pod cache as complete workload membership.
- Do not map unavailable definitions, denied reads or unsynchronized caches to empty success.
- Do not infer that registering a GVK makes its API or cache ready.
- Do not replace the exporter's GroupKind selection and degraded metric semantics
  with exact-GVK strict discovery without a separate compatibility migration.
- Do not evaluate full workload discovery during every Prometheus scrape.
- Do not put product status precedence, resource accounting or persistent structure
  IDs in the shared interpretation layer.
- Do not turn generic ownership discovery into a requirement to install a Karta CR.

## Per-repo index

- [client-go](sdk-operator-integration-research/client-go.md): clients, informer readiness and event delivery.
- [controller-runtime](sdk-operator-integration-research/controller-runtime.md): consumer adapters and dynamic watches.
- [kube-state-metrics](sdk-operator-integration-research/kube-state-metrics.md): compiled interpretation and metric storage.
- [OpenTelemetry Go](sdk-operator-integration-research/opentelemetry-go.md): provider and immutable snapshot boundaries.
- [Kueue](sdk-operator-integration-research/kueue.md): registry activation and workload policy.
- [Gatekeeper](sdk-operator-integration-research/gatekeeper.md): dynamic definitions, errors and lifecycle.
