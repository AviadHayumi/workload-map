<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Kueue integration registry and watch lifecycle

## TL;DR

- Kueue keeps integration registration and enablement separate, scoped to one manager. This is a useful model for separating known Karta definitions from the selected mappings a consumer actually uses. [E1], [E2]
- Registry lookup and owner eligibility use exact GVK. Generic owner event routing and owner index names have weaker version boundaries. Karta should keep exact GVK through registration, indexing, and routing. [E1], [E5], [E6]
- Optional APIs can appear after startup. Kueue defers controller or watch installation until REST mapping succeeds. This does not provide a public per-GVK cache-readiness contract. [E3], [E7], [E8]
- Ownership traversal distinguishes known intermediates from enabled workload types. It checks UIDs, cycles, and depth, but its root policy is specific to Kueue. [E9]
- Copy the separation of registry, policy, and event mapping. Keep Kueue's manager, webhook, queue, and per-kind job adapters out of Karta's core SDK. [E1], [E10]

## How it works

The inspected origin is `https://github.com/kubernetes-sigs/kueue.git`.
The inspected HEAD is `a7422a0e4a9541297379327c02832926f9a4a51a`.
Both values were verified with Git in the supplied local reference checkout.
No network access was used. Links below identify that immutable revision.

### Registration and selection

`jobs.NewIntegrationManager` creates an instance and registers the built-in
integration packages. Each callback bundle supplies its job type, reconciler
factory, webhook setup, and optional indexes, scheme registration, support check,
and MultiKueue adapter. Registration rejects duplicate names and missing required
callbacks. It does not reject duplicate GVKs: its GVK-to-name map is assigned
directly. By contrast, MultiKueue adapter collection explicitly rejects duplicate
adapter GVKs. [E1], [E2]

`GetIntegrationByGVK` searches registered integrations using full GVK equality.
It does not restrict lookup to enabled integrations. The enabled set is separate
and guarded by a mutex. Tests verify that enabling a type in one manager does not
enable it in another. This distinguishes a known capability from its activation
in a specific consumer. [E1], [E2]

Configured framework names select built-in integrations. Setup also includes
declared implicit integrations and validates dependencies. External framework
registration accepts `Kind.version.group` and creates a
`PartialObjectMetadata` prototype. This records an externally managed owner
type; it does not synthesize a Kueue reconciler or a definition-driven adapter
for that type. [E3], [E4]

### API availability and watch installation

For a selected built-in integration, setup resolves its exact GVK through the
manager's REST mapper. If the API is absent, it installs the webhook and starts
a cancellable polling loop. The loop backs off from one second to two minutes.
Once discovery succeeds, setup creates reconcilers, attaches them to the manager,
sets up the webhook, and finally marks the integration enabled. The delayed
callback logs a setup error; the polling loop then returns. [E3]

This sequence separates configured selection from successful setup. It does not
separate all the states Karta needs. The enabled marker follows setup calls, with
no cache-sync check in that path. The public `EnableIntegration` method can also
set the marker directly. The application's ready probe checks certificate and
webhook readiness, not each integration's informer. [E1], [E3], [E8]

The generic reconciler watches its concrete job type and owns Kueue Workloads.
A job-specific setup hook can add additional event mapping. The batch Job hook
maps Workload create and update events to child Jobs by listing an owner-UID
index, then enqueues their names. That index is installed explicitly. [E5]

MultiKueue separates event mapping from the transport through the optional
`MultiKueueWatcher` interface. It supplies an empty list and converts a runtime
object to workload keys. The remote watch consumer invokes that mapping and queues
the resulting keys. A separate optional interface requests a local job watch.
Local watches are attached to the built controller after the exact API becomes
available, using the manager cache. [E7]

There are two relevant version boundaries. Kueue's owner eligibility predicate
matches complete API version and kind. However, the generic `Owns` builder uses
the vendored controller-runtime owner handler, which matches group and kind.
Its resulting reconcile request carries namespace and name. Separately, Kueue's
workload owner index function filters exact API version and kind, while its index
key includes only group and kind. These mechanisms should not be assumed to
supply exact-GVK dispatch across multiple versions. [E1], [E6]

### Ownership and roots

`isKnownOwner` recognizes all registered integrations, external types, and an
explicit ReplicaSet intermediate. `getJobTypeForOwner` recognizes enabled
integrations and external types. This lets traversal pass through a known but
disabled intermediate without treating that intermediate as the managed root.
Unknown owner types stop traversal. [E9]

Traversal follows controller owner references, fetches each parent in the child's
namespace, and checks that the fetched UID matches the reference. It detects
cycles and enforces a depth limit. Parent-read errors propagate. A stale UID stops
the search with the last eligible ancestor. The returned root is the highest
eligible managed ancestor, with eligibility affected by the queue-name policy.
It is not simply the farthest Kubernetes owner. [E9]

A `TopLevelJob` hook bypasses ancestry traversal. The Pod adapter returns true.
This is a concrete example of product policy overriding structural ownership.
It should inform a configurable Karta root policy, not become an SDK default.
[E9], [E10]

## Relevance to Karta

The useful boundary is a consumer-scoped registry whose catalog and selected,
usable mappings are different concepts. Kueue also demonstrates that knowing a
GVK does not imply that its API, watch, cache, and reconciliation path are ready.
Its APIs do not implement the proposed live definition-registration contract.
The inspected setup is driven by configured framework names and compiled
callback bundles. [E1], [E2], [E3]

For Karta, an external definition watcher can own selection and update a registry
of exact-GVK mappings. The core SDK can accept the consumer's read/write
capabilities without owning a controller-runtime manager. A separate adapter can
turn watch events into workload keys. This is a design recommendation based on
Kueue's boundaries, not a claim about an existing Kueue API.

Cached and direct clients need an explicit contract. Kueue's setup obtains
`mgr.GetClient()`, `mgr.GetFieldIndexer()`, and `mgr.GetCache()`; its reverse
lookups use registered field indexes. A framework-neutral Karta API should not
silently require those indexes. Indexed lookup can be an optional consumer
capability, with a separately defined direct-client path. [E3], [E5], [E7]

## Evidence

All paths are relative to the verified Kueue repository root.

| ID | Source | What was inspected |
| --- | --- | --- |
| E1 | pkg/controller/jobframework/integrationmanager.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/integrationmanager.go#L49-L282) | Callback coupling, instance registry, public enable method, exact lookup, duplicate checks, external metadata types. |
| E2 | pkg/controller/jobs/jobs.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobs/jobs.go#L39-L69), pkg/controller/jobs/jobs_test.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobs/jobs_test.go#L42-L60) | Per-instance built-in registration and isolation test. |
| E3 | pkg/controller/jobframework/setup.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/setup.go#L34-L200), pkg/controller/jobframework/setup_test.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/setup_test.go#L91-L183) | Selection, dependencies, deferred API setup, enable timing, indexes, delayed-discovery tests. |
| E4 | apis/config/v1beta2/configuration_types.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/apis/config/v1beta2/configuration_types.go#L562-L586), cmd/kueue/main.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/cmd/kueue/main.go#L588-L618) | Configured built-in names and external exact-GVK strings passed into setup. |
| E5 | pkg/controller/jobframework/reconciler.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/reconciler.go#L2050-L2085), pkg/controller/jobs/job/job_controller.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobs/job/job_controller.go#L89-L139), job index setup (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobs/job/job_controller.go#L477-L482) | Generic watches, customization hook, reverse event mapping, required index setup. |
| E6 | pkg/controller/core/indexer/indexer.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/core/indexer/indexer.go#L58-L104), vendored builder (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/vendor/sigs.k8s.io/controller-runtime/pkg/builder/controller.go#L330-L352), vendored owner handler (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/vendor/sigs.k8s.io/controller-runtime/pkg/handler/enqueue_owner.go#L144-L199) | Exact index extraction, versionless index name, GroupKind owner routing. |
| E7 | pkg/controller/jobframework/multikueue.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/multikueue.go#L30-L79), remote event dispatch (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/admissionchecks/multikueue/multikueuecluster.go#L453-L515), deferred local watches (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/admissionchecks/multikueue/workload.go#L1206-L1243) | Optional watch mapping and delayed cache-backed watch installation. |
| E8 | cmd/kueue/main.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/cmd/kueue/main.go#L633-L658) | Application readiness checks certificates and webhook startup. |
| E9 | owner eligibility (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/integrationmanager.go#L329-L381), root selection (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/reconciler.go#L355-L386), ancestor traversal (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/reconciler.go#L898-L977) | Known versus managed owner types, queue policy, UID checks, cycle and depth guards. |
| E10 | pkg/controller/jobframework/interface.go (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/interface.go#L35-L71), top-level hook (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobframework/interface.go#L225-L231), Pod policy (https://github.com/kubernetes-sigs/kueue/blob/a7422a0e4a9541297379327c02832926f9a4a51a/pkg/controller/jobs/pod/pod_controller.go#L354-L356) | Job behavior interface, controller-runtime clients, explicit root override. |

Source inspection only. Upstream tests were read, not executed.

## Lessons for Karta

These are proposed design choices derived from the inspected implementation.

1. Keep the registry local to a consumer. Distinguish known definitions, selected
   exact-GVK mappings, and mappings ready for use.
2. Let external watchers publish registration updates. Define replace and remove
   behavior, duplicate-GVK conflict handling, and concurrent lookup semantics.
   Kueue's startup registration maps are not a live-update API.
3. Track API availability, watch installation, and initial synchronization
   separately. Surface an explicit not-ready result or status. Do not equate a
   successful setup call with a populated cache.
4. Carry the full GVK in registry and event-mapping identity. Use version-aware
   index names where versions must remain distinct. A namespace/name queue key
   alone needs routing context.
5. Make ownership traversal policy explicit. Separate types that may be traversed
   from types eligible as workload roots. Preserve UID checks, bounded traversal,
   and distinguish an unknown type from a failed read.
6. Put controller-runtime watch setup and indexes in an optional integration
   package. Let callers choose caching, direct reads, retry policy, and readiness
   handling without importing those choices into definition interpretation.

## What NOT to copy

- Do not require a reconciler factory and webhook merely to register a mapping.
  Kueue's callback bundle is an application integration contract. [E1]
- Do not make every new workload kind implement suspension, pod readiness, and
  queue-oriented methods. These are Kueue admission semantics. [E10]
- Do not expose Kueue's enabled boolean as Karta readiness. Its setup and probe
  paths do not establish the required per-GVK synchronization guarantee. [E3], [E8]
- Do not assume ordinary owner handlers or synthetic index keys preserve exact
  GVK identity. Inspect routing and key construction separately. [E6]
- Do not inherit queue-name selection, ReplicaSet special cases, same-namespace
  parent fetching, or Pod root overrides as universal ownership rules. [E9], [E10]
- Do not copy the registry as a concurrent live-definition store. Ordinary
  registration writes its maps without the enabled-set lock, has no removal
  operation in this implementation, and does not reject duplicate GVKs. [E1]
