<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# controller-runtime: caller-owned reads and operator event integration

## TL;DR

Keep controller-runtime integration in the consuming operator or a separately
versioned adapter. Borrow its separation of reads, informer ownership, event
mapping, and level-based reconciliation. Do not put its client, option, source,
handler, queue, or reconcile types in a runtime-independent SDK contract.
Controller-runtime explicitly permits breaking changes between minor releases
and tests each minor against a particular client-go minor. [E1]

A caller-supplied client does not imply a particular consistency model. The
manager's default client uses the cache for eligible reads, but explicitly sets
unstructured caching to false. Passing an unstructured object to that default
client performs a direct API read. An explicit cache reader has different
coverage, synchronization, selector, and pagination semantics. [E2, E3, E6, E7]

This note describes only the pinned upstream implementation. Karta implications
are recommendations for the stated design problem, not assertions about an
existing Karta SDK implementation.

## How it works

### Client contracts and structural compatibility

`client.Reader` exposes `Get(context.Context, ObjectKey, Object, ...GetOption)`
and `List(context.Context, ObjectList, ...ListOption)`. `client.Client` adds
writes, status/subresource methods, scheme, REST mapping, and type/scope lookup.
`WithWatch` is a separate extension; ordinary `Client` does not promise Watch.
[E2]

The object values are broadly implementable: `Object` combines Kubernetes
metadata and runtime-object methods, while `ObjectList` combines list metadata
and runtime-object methods. This makes unstructured values usable by the
client, but does not make different method parameter types interchangeable.
`GetOption.ApplyToGet` and `ListOption.ApplyToList` themselves name
controller-runtime's option structs. [E2, E4, E5]

Design inference from those signatures: redeclaring an SDK `Object` interface
with the same methods does not give it the identity of the upstream named
interface. Removing the variadic arguments, replacing them with `...any`, or
substituting SDK-owned option interfaces also changes the method signature.
Such a local Reader does not become automatically implementable by an existing
controller-runtime client. Aliasing the upstream types preserves the dependency
that the SDK is supposed to avoid. Use an explicit adapter or caller-supplied
functions, with all signature translation at the consumer boundary. [E2, E4]

### Direct and cached reads

`client.New` reads directly when its cache or cache reader is absent. With a
cache reader, it delegates eligible reads, while `DisableFor` and `Unstructured`
control bypasses. Writes go to the API server. The cluster constructor supplies
a cache reader to the manager client, defaults `Unstructured` to false, and
constructs a separate uncached API reader. `GetAPIReader` exposes that reader.
The client does not fall back to the API server when an eligible cache read
returns an error: it returns the cache result. [E3]

The pinned implementation has an experimental read-your-writes option, disabled
by default and scoped to a representation. It should not become a portable SDK
guarantee, especially across controller-runtime versions. [E3]

`cache.Cache` combines `client.Reader` with informer management and field
indexing. A cache read obtains an informer for the requested kind. By default,
missing informers are created; the opt-in `ReaderFailOnMissingInformer` setting
returns a distinct error instead. Reading a cache that has not started also
fails. When an informer is created after the cache has started, it starts
immediately, and retrieval normally waits for it to synchronize. [E5, E6]

Informers are separated by GVK and representation: typed, unstructured, or
metadata-only. The unstructured path maps GVK to a REST resource and scope, then
uses a dynamic client for list/watch. `GetInformerForKind` constructs its object
through the scheme; `GetInformer` accepts the supplied object instead. An
adapter for dynamic kinds can use an unstructured object with its GVK set, as
supported by the client's unstructured contract, rather than assuming that
`GetInformerForKind` is independent of scheme registration. [E3, E5, E8]

### Event-to-reconcile path

The ordinary flow is:

```text
API list/watch -> caller-owned informer/cache -> predicates -> event handler
              -> mapped comparable request -> work queue -> reconciliation
              -> read current state through the selected reader
```

`source.Kind` uses the supplied cache and registers an informer handler. Its
startup path waits for cache synchronization and then for that handler to
receive its initial events. Controller startup waits for syncing sources before
launching workers. Predicates can discard create, update, and delete events
before handlers run; delete handling also supports tombstones. [E9, E10]

Mapping handlers can fan one event out to several requests. Their update path
maps both the old and new objects, deduplicating requests within that event.
The work queue deduplicates equal requests. The default request contains only
namespace and name, not the triggering event, object contents, GVK, or deletion
reason. Reconciliation is explicitly level-based and reads current state.
[E11, E12]

`Controller.Watch` also accepts sources after startup, but that branch calls
`Source.Start` and does not call `WaitForSync`. For `source.Kind`, asynchronous
startup or synchronization failures therefore are not made into readiness by
the return value of `Watch`. Dynamic registration needs its own observed
readiness/error policy. The controller interface exposes Watch, but no matching
Unwatch. Informer handler registrations can be removed individually; removing
an informer instead stops and deletes the shared entry. [E5, E9, E10, E13]

## Relevance to Karta

The following are proposed design constraints, inferred from the upstream
contracts above.

| Requirement | Consequence for an SDK/operator boundary |
| --- | --- |
| Accept the caller's existing cached or direct reader | Accept an SDK-owned read capability or function bundle. The operator closes over its selected reader and translates requests/options. No SDK constructor should need a manager merely to read. [E2-E4] |
| Avoid a controller-runtime version requirement in the SDK core | Keep all runtime imports and source/handler/queue setup in the consumer or a separate module. Kubernetes/apimachinery types can remain a deliberate dependency, but that is a separate version-compatibility decision. [E1, E2, E4] |
| Discover dynamically defined workload kinds | Treat GVK, scope, representation, and watch readiness as explicit integration inputs. An unstructured object is supported; missing API mappings and unsynchronized informers are still failures. [E5, E8, E9] |
| React to definition changes as well as workload changes | Have the operator reconcile definition changes, update the required registrations, and enqueue affected workload identities. A definition update can require re-evaluation without any workload object changing. This fan-out policy belongs to the integration, following the upstream mapping pattern. [E11, E12] |
| Reconcile objects of several kinds | Carry a kind-qualified SDK identity. The operator can map it to its owning controller's namespace/name key, use distinct controllers, or use a typed comparable request that includes kind. Default namespace/name keys alone cannot distinguish two kinds with the same names. [E12] |
| Report that no matching workload exists | Require adequate read coverage and successful synchronization first. A successful filtered list, or a cache NotFound, cannot prove absence beyond that reader's configured view. [E6-E8] |

For dynamic definitions, a reasonable proposed lifecycle is: calculate the
required kind set, register missing sources, observe initial synchronization,
re-evaluate affected objects, and retire unused registrations under the owner's
policy. Keep per-kind readiness and failure state. A missing CRD, denied
list/watch, or failed synchronization must not silently become an empty result.
Removing an individual handler is safer than removing an informer owned by the
caller and potentially used by other controllers. This lifecycle is a design
recommendation, not an upstream Karta feature. [E5, E9, E10, E13]

Completeness needs an explicit contract. Cache namespaces and selectors restrict
what ever enters the store. Transforms can remove fields. Metadata and full
object caches are separate; upstream warns that mixing representations creates
duplicate caches that are not synchronized with one another. A cache sync barrier
covers the informers known when it captures their sync functions, not an
arbitrary future definition set. [E6, E8, E14]

Queries also differ. Cached field selectors require exact matches backed by
registered indexes. Cached List rejects continuation tokens, can truncate for a
limit, and sets `continue-not-supported`; it is not an API pagination stream.
Raw list options are not guaranteed to be honored by every Reader. Therefore,
use an unbounded list only where the selected cached view is adequate, or use a
direct-reader adapter with explicitly supported pagination. Propagate unsupported
queries and incomplete views rather than synthesizing empty success. [E4, E7]

## Evidence

Read-only source verification: `git rev-parse HEAD` returned
`e8f9455e429046cea3a1033c2e8555bcdfc8b08c`; `git remote -v` reported
`https://github.com/kubernetes-sigs/controller-runtime.git` for origin. All
references below point to that exact commit. No network lookup or Karta source
inspection was used for this upstream map.

- E1. `VERSIONING.md:10-18,30-38` and `README.md:43-52`: minor-version breaking
  changes and dependency compatibility limits.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/VERSIONING.md#L10-L38
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/README.md#L43-L52
- E2. `pkg/client/interfaces.go:32-62,171-194` and
  `pkg/client/object.go:43-76`: exact client contracts and object/list types.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/client/interfaces.go#L32-L194
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/client/object.go#L43-L76
- E3. `pkg/client/client.go:84-141,294-315,426-460` and
  `pkg/cluster/cluster.go:58-73,194-225`: cache selection, unstructured bypass,
  direct API reader, and experimental consistency option.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/client/client.go#L84-L141
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/client/client.go#L294-L315
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/client/client.go#L426-L460
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cluster/cluster.go#L194-L225
- E4. `pkg/client/options.go:40-50,543-558,597-640`: named variadic options,
  field-index constraints, and raw-option limitations.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/client/options.go#L40-L50
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/client/options.go#L543-L640
- E5. `pkg/cache/cache.go:64-79`,
  `pkg/cache/cacheapi/cacheapi.go:45-153`, and
  `pkg/cache/informer_cache.go:40-104,145-189`: reader/informer split,
  handler ownership, missing/start errors, and informer retrieval.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/cache.go#L64-L79
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/cacheapi/cacheapi.go#L45-L153
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/informer_cache.go#L40-L189
- E6. `pkg/cache/cache.go:120-197,204-251` and
  `pkg/cache/internal/cache_reader.go:53-80`: resync semantics, cache scope,
  selectors/transforms, copy options, and NotFound from the local index.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/cache.go#L120-L251
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/internal/cache_reader.go#L53-L80
- E7. `pkg/cache/internal/cache_reader.go:108-184,187-230`: cached list
  selection, index requirements, limit, and unsupported continuation.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/internal/cache_reader.go#L108-L230
- E8. `pkg/cache/internal/informers.go:271-330,352-479`: sync snapshot,
  representation-specific caches, automatic informer startup, selector/transform
  application, REST mapping, and unstructured list/watch.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/internal/informers.go#L271-L330
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/internal/informers.go#L352-L479
- E9. `pkg/source/source.go:40-97` and
  `pkg/internal/source/kind.go:61-133,151-163`: source lifecycle, retries,
  cache/handler synchronization, and cancellation.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/source/source.go#L40-L97
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/source/kind.go#L61-L163
- E10. `pkg/internal/controller/controller.go:237-251,298-310,334-417` and
  `pkg/internal/source/event_handler.go:62-168`: startup versus later Watch,
  predicates, and tombstones.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/controller/controller.go#L237-L251
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/controller/controller.go#L298-L417
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/source/event_handler.go#L62-L168
- E11. `pkg/handler/enqueue_mapped.go:48-69,97-150`: fan-out and old/new
  update mapping with deduplication.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/handler/enqueue_mapped.go#L48-L150
- E12. `pkg/reconcile/reconcile.go:58-136`: request identity, level-based
  behavior, comparable typed requests, queue deduplication, and retry contract.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/reconcile/reconcile.go#L58-L136
- E13. `pkg/controller/controller.go:177-197` and
  `pkg/cache/internal/informers.go:337-349`: public Watch-only registration
  surface and shared informer removal.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/controller/controller.go#L177-L197
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/internal/informers.go#L337-L349
- E14. `pkg/builder/controller.go:177-203`: metadata watch/read consistency
  and duplicate representation caches.
  https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/builder/controller.go#L177-L203

## Lessons for Karta

1. Make read behavior an explicit caller decision. A consumer adapter should
   identify whether it delegates to a cache, the manager client with its actual
   options, or the direct API reader. [E3]
2. Keep read and subscription capabilities separate. A read-only SDK can use a
   direct client without owning informers. An operator adapter can reuse its
   manager cache for notifications and supply the chosen reader independently.
   Keep representation and freshness differences visible. [E2, E5, E14]
3. Translate signatures, not only names. Consumer-written closures or an
   independently versioned adapter can convert SDK queries to the caller's
   controller-runtime options without importing those types into SDK core.
   Removing controller-runtime does not remove the need to state supported
   Kubernetes library versions. [E1, E2, E4]
4. Treat notifications as requests to recompute. Definition changes and workload
   events should enqueue affected identities; reconciliation should use the
   current definitions and current reader view. [E11, E12]
5. Track readiness and coverage per dynamic kind. Expose partial results or an
   error when completeness cannot be established, and preserve the distinction
   between missing objects, missing informers, and failed synchronization.
   [E5-E10]

## What NOT to copy

- Do not embed `client.Reader`, `client.Client`, `cache.Cache`, `source.Source`,
  or `reconcile.Request` in a supposedly runtime-independent SDK API. A narrow
  runtime interface is still a runtime dependency. [E1, E2, E5, E9, E12]
- Do not assume that `mgr.GetClient()` means unstructured reads are cached, or
  that cached reads automatically observe a preceding write. [E3]
- Do not implement API-style pagination or arbitrary field selectors over the
  cache interface. Its supported query semantics are narrower. [E4, E7]
- Do not turn cache resync into a completeness promise. It generates local
  artificial updates, does not synchronize the cache with the server, and may
  be filtered by predicates. [E6]
- Do not stop shared informers when one definition disappears without an
  ownership policy. Prefer retiring the integration's own registration. [E5,
  E13]
- Do not mistake a successful late `Watch` return or a global cache sync for
  proof that every newly requested kind is ready. [E8-E10]
