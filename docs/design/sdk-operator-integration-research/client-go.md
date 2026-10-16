<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# client-go: caller-owned readers and informer subscriptions

Source checkout: /Users/ahayumi/.greenlight/reference-worktrees/kubernetes/client-go/498754e35f3c.
Verified origin: https://github.com/kubernetes/client-go.git.
Verified HEAD: 498754e35f3ce91e08e19614771c7786c53fe600.
Only this local checkout was inspected. Evidence IDs below refer to immutable source URLs.
Karta recommendations are deductions from this source, not claims about existing Karta APIs.

## TL;DR

- Separate reading, observation, and lifecycle ownership. Dynamic clients perform direct REST reads; dynamic listers read a supplied indexer; informer factories accept an existing dynamic client and expose explicit Start, sync, and Shutdown operations. [E1, E2, E3]
- A live registry fed by callbacks needs its registration's HasSynced, not just the informer's HasSynced. The former includes delivery of initial notifications. Work dispatched asynchronously by a callback needs its own completion barrier. [E4, E5]
- Treat notifications as prompts to converge state. They can skip intermediate states, replay existing objects, repeat unchanged objects, or carry deletion tombstones. Keep callbacks short and document unsubscribe completion. [E4, E5, E6, E7]

## How it works

Direct reads are separate from cache reads. dynamic.Interface selects a GroupVersionResource, then optionally a namespace. Its ResourceInterface exposes context-aware Get, List, and Watch alongside writes. Get and List construct REST requests, execute Do(ctx), and decode unstructured results. NewForConfigAndClient accepts a caller-supplied HTTP client. The lower-level rest.Interface exposes HTTP verbs and request construction. None of these read paths consults an informer indexer. [E1]

Cached reads use a different interface. dynamiclister.Lister exposes Get, List, and Namespace over a supplied cache.Indexer. GetByKey returns the stored object pointer; List appends pointers from the indexer. Cache objects are shared: ThreadSafeStore explicitly requires treating Get/List results as read-only because mutation breaks concurrency and indexing guarantees. Cache absence is reported as NotFound by the lister; that is absence from this indexer's collection. [E2]

The dynamic informer factory composes these pieces. Its constructor accepts dynamic.Interface. ForResource memoizes a GenericInformer per GVR. NewFilteredDynamicInformer connects ListWatch callbacks to the supplied client's List and Watch, and exposes a lister over the resulting shared indexer. Namespace and list-option filters apply to the observed collection and its lister. Start starts requested informers; WaitForCacheSync snapshots only started informers. A resource added after a Start call needs another Start call. Shutdown prevents new starts and waits for running goroutines; the caller must close their stop channels. [E3]

The shared informer describes an eventually consistent cache, not a complete event log. For one object, cached states preserve observed order but can omit intermediate states. There is no ordering guarantee across different objects. A cache query uses one cache snapshot, but separate calls need not observe the same snapshot. Notifications follow cache/index updates, and those structures can advance again before a handler runs. Each handler gets sequential notifications, while different handlers are uncoordinated. [E4]

There are two initial-sync boundaries. SharedInformer.HasSynced indicates that its store has received an initial complete collection. ResourceEventHandlerRegistration.HasSynced also waits for that handler's initial notifications. A handler registered after startup gets synthetic Add notifications for cached objects. Registration pauses delta delivery while it takes that snapshot and queues those additions, avoiding a separate list-then-subscribe gap. OnAdd returns before the implementation marks an initial item finished. Consequently, if a callback only queues downstream work, registration sync does not certify downstream work completion. [E4, E5]

Handlers are informational: they return no error and must not mutate input objects or reachable data. The interface allows OnUpdate for relists even when content is unchanged. The shared implementation compares resourceVersions and routes unchanged-version notifications as resync notifications to listeners that requested them. A configured resync replays cached objects; it does not perform a server read. The implementation has an unbounded notification buffer per listener, so stalled callbacks can grow memory without bound. [E4, E6]

Deletion is not guaranteed to arrive as a complete final object. DeltaFIFO.Replace detects keys absent from a replacement list and synthesizes DeletedFinalStateUnknown. Its Obj can be stale or missing. DeletionHandlingMetaNamespaceKeyFunc preserves the tombstone's key without requiring its object. Identity in the informer is resource plus namespace/name, not UID: deletion and recreation under the same name can appear as a single update with different old/new UIDs. [E4, E7]

Unregistration has an explicit lifecycle contract. RemoveEventHandler is idempotent and thread-safe, but already-queued callbacks may execute after it returns. ShutDownEventHandler removes the registration and waits for its callback goroutine to finish. It must not be called from that registration's own callback, which would deadlock. A stopped informer cannot restart or accept new registrations. [E4, E5]

The reflector owns transport recovery. ListAndWatchWithContext attempts watch-list when enabled and falls back to regular listing on failure. A successful list updates the store and records the list's resourceVersion; watch requests resume from the last recorded version. Expired or too-large list versions trigger a fresh-version list. The initial regular list uses resourceVersion "0"; recovery from an unavailable version uses "". Watch-list initial objects are collected in a temporary store and committed through Replace after the initial-events-end bookmark. These are transport progress mechanisms, not a transaction across arbitrary SDK reads or derived registries. [E8, E9]

## Relevance to Karta

The following are proposed design implications, not verified descriptions of Karta code.

A small read contract can have separate adapters for a caller's direct dynamic client and cached lister. The SDK should describe the selected adapter's freshness, selector scope, NotFound behavior, and object ownership. It should not implicitly start another cache just because a cached reader was supplied. This follows the distinct client/lister interfaces and explicit factory lifecycle. [E1, E2, E3]

A definition provider can read a current snapshot independently of how it is populated. An optional informer adapter can maintain that snapshot through add/update/delete events and notify an operator about affected keys. If publication happens synchronously in callbacks, registration sync is a useful initial readiness signal. If publication is queued or compilation is asynchronous, the provider needs a separate readiness condition covering successful publication of initial work. The informer only tracks callback completion. [E4, E5, E6]

The operator should own scheduling and retry policy. A provider callback can update or invalidate registry state and enqueue affected workload keys; expensive reconciliation belongs outside that callback. This fits informational, non-error-returning handlers and prevents the unbounded listener buffer from becoming an implicit reconciliation queue. A callback that re-reads the cache must accept seeing a state newer than its event payload. [E4, E6]

Subscription ownership should be distinct from informer ownership. An adapter borrowing an operator's informer can remove its own handler without stopping the shared informer. Its close contract should state whether callbacks may still run on return. Resources used by callbacks should be released only after quiescence, and blocking close should run outside callbacks. [E4, E5]

## Evidence

- E1: dynamic/interface.go:29-50; dynamic/simple.go:77-117 and 247-287; rest/client.go:46-56. Direct GVR-based interfaces, supplied HTTP transport, and REST execution.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/dynamic/interface.go#L29-L50
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/dynamic/simple.go#L77-L117
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/dynamic/simple.go#L247-L287
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/rest/client.go#L46-L56
- E2: dynamic/dynamiclister/interface.go:24-40; dynamic/dynamiclister/lister.go:36-90; tools/cache/thread_safe_store.go:31-45. Cached Get/List, pointer sharing, and mutation restrictions.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/dynamic/dynamiclister/interface.go#L24-L40
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/dynamic/dynamiclister/lister.go#L36-L90
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/thread_safe_store.go#L31-L45
- E3: dynamic/dynamicinformer/informer.go:35-199; dynamic/dynamicinformer/interface.go:25-48. Injection, per-GVR sharing, filters, start/sync/shutdown, and lister composition.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/dynamic/dynamicinformer/informer.go#L35-L199
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/dynamic/dynamicinformer/interface.go#L25-L48
- E4: tools/cache/shared_informer.go:46-275. Consistency, identity, delivery, resync, cache readiness, registration readiness, and lifecycle contracts.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/shared_informer.go#L46-L275
- E5: tools/cache/shared_informer.go:999-1063, 1126-1154, and 1505-1514. Atomic late registration, asynchronous removal, blocking shutdown, and callback completion accounting.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/shared_informer.go#L999-L1063
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/shared_informer.go#L1126-L1154
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/shared_informer.go#L1505-L1514
- E6: tools/cache/controller.go:265-285; tools/cache/shared_informer.go:1091-1109 and 1337-1371. Informational callbacks, immutable inputs, unchanged-version resync handling, and unbounded listener queues.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/controller.go#L265-L285
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/shared_informer.go#L1091-L1109
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/shared_informer.go#L1337-L1371
- E7: tools/cache/delta_fifo.go:610-695 and 793-800; tools/cache/controller.go:607-624. Deletion detection during Replace, stale or absent tombstone payloads, and deletion-safe keys.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/delta_fifo.go#L610-L695
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/delta_fifo.go#L793-L800
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/controller.go#L607-L624
- E8: tools/cache/reflector.go:420-434, 467-508, 587-605, 719-780, and 1111-1143. Retry ownership, watch-list fallback, list/watch resourceVersions, and recovery.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/reflector.go#L420-L434
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/reflector.go#L467-L508
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/reflector.go#L587-L605
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/reflector.go#L719-L780
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/reflector.go#L1111-L1143
- E9: tools/cache/reflector.go:887-907; tools/cache/shared_informer.go:219-222. Watch-list replacement after its bookmark and the lack of a synchronized store snapshot in LastSyncResourceVersion.
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/reflector.go#L887-L907
  https://github.com/kubernetes/client-go/blob/498754e35f3ce91e08e19614771c7786c53fe600/tools/cache/shared_informer.go#L219-L222

## Lessons for Karta

1. Specify cached and direct read semantics separately, while letting the caller supply either implementation. Treat cached inputs as immutable or copy before modifying them. [E1, E2]
2. Give a derived registry its own initial-ready condition. Cache sync alone is insufficient; callback sync is insufficient if the callback delegates publication. [E4, E5]
3. Prefer the informer's registration replay over an independently implemented list-then-subscribe sequence. Preserve updates and deletions while handling the initial snapshot. [E5]
4. Normalize deletions to identity plus optional last-known payload. Handle same-name replacement using UID where object incarnation matters, and make registry application idempotent under resync. [E4, E6, E7]
5. Let the informer owner manage network loops and cancellation. An adapter owns its subscription, and its cleanup contract must account for callbacks that remain active after unregistration. [E3, E4, E5, E8]
6. Delegate watch recovery and resourceVersion handling to client-go. Do not expose a transport progress token as proof that arbitrary provider computations or cross-resource reads are synchronized. [E4, E8, E9]

## What NOT to copy

- Do not expose the entire write-capable dynamic.ResourceInterface merely to satisfy a read-only SDK dependency. Its Get/List subset illustrates the narrower requirement. [E1]
- Do not promise a complete event history, cross-object ordering, or exact correspondence between a notification payload and the current cache. The informer contract provides none of these. [E4]
- Do not use resync as a freshness poll or an exact timer. It replays local objects, and actual timing can exceed the requested period. [E4]
- Do not run compilation, network operations, or reconciliation inline in informer handlers without a strict latency bound. The listener queue is unbounded. [E4, E6]
- Do not equate RemoveEventHandler returning with callback quiescence, close handler-owned channels immediately afterward, or call blocking shutdown from the handler itself. [E4, E5]
- Do not assume every API in this inspected commit exists in Karta's dependency version. The evidence identifies one exact client-go snapshot; dependency compatibility needs a separate check before implementation.
