<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# controller-runtime

## TLDR

Use one full informer for each selected resource and namespace scope. Read evaluation inputs from that same informer cache. Use metadata informers only for kinds needed solely to follow ownership. Keep two domain queues: Karta configuration work and workload evaluation work. Owner and Pod events feed workload keys into the latter.

For example, a Pod points to ReplicaSet UID `rs-1`, which points to Deployment UID `deploy-1`. A ReplicaSet metadata event schedules affected workload evaluation. The worker reads current Pod, ReplicaSet metadata, and Deployment data. A delayed delete for `rs-1` must not remove a replacement ReplicaSet `rs-2` with the same name.

These are bounded exporter recommendations derived from upstream code. The checkout HEAD was verified as `e8f9455e429046cea3a1033c2e8555bcdfc8b08c`. No implementation tests were run.

## How works

The informer registry reuses an existing informer by GVK and object representation. Creation is locked and checked again under the lock. Metadata, unstructured, and structured objects use separate maps. The sharing guarantee therefore applies within one configured cache and representation. It does not automatically merge independent caches, namespace scopes, or API versions. Metadata informers use a metadata client for both LIST and WATCH. [E1, E2]

A kind source gets the informer, registers its handler, waits for the cache, then waits for that handler to receive its initial events. The controller starts workers only after its sources finish synchronization, with a bounded timeout. Store synchronization and handler synchronization are separate conditions. Neither condition means the workload evaluation queue has completed its initial work. [E3, E4]

Handlers enqueue object keys. Workers reconcile current state rather than replay event payloads. The ordinary request contains namespace and name, with no UID or event type. The controller calls Done after processing, retries nonterminal errors with rate limiting, and forgets retry history after success. A given queue key is not reconciled concurrently by that controller. [E5, E6]

## Relevance

The Karta queue should own watch requirements and Karta create, update, and deletion. Shared watches remain while any Karta needs them. The workload queue should own evaluation and metric-row replacement. Pod or intermediate-owner changes schedule affected workloads without introducing a third domain queue.

Use one shared full-object cache as the input to both event handling and evaluation for selected kinds. Extract ownership metadata from those full objects. Start a metadata source only when no full source covers that traversal kind and scope. Replacing metadata coverage with full coverage requires synchronizing the full source before switching readers. Prevent obsolete source callbacks from mutating current derived state.

Register handlers before declaring a source usable. Wait for cache and handler synchronization. Then reconcile the known initial workload keys before declaring initial publication complete. An initially missing owner source is pending collection, not evidence that the owner does not exist. A read may observe a newer object than the event that scheduled it; evaluate the current object.

Use workload queue keys containing resource, namespace, and name. Keep UID in stored ownership edges and metric rows, and compare it before cleanup. This preserves serialization across same-name recreation. A delayed delete should schedule reconciliation, not directly erase rows by name. If the cache now holds a new UID, replace the old UID's derived state and evaluate the replacement. A NotFound result can remove the stored incarnation only when reading the active, synchronized source. Source retirement needs separate explicit cleanup.

Transient read or evaluation dependencies retain their workload key for retry. Repeated deterministic expression errors should become an explicit failed result and wait for relevant input changes, rather than spin forever. The exact classification belongs to exporter code.

## Evidence

- E1: pkg/cache/internal/informers.go:299 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/internal/informers.go#L299-L394. Registry lookup, synchronization, representation maps, and creation.
- E2: pkg/cache/internal/informers.go:483 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/internal/informers.go#L483-L527. Metadata client LIST and WATCH.
- E3: pkg/internal/source/kind.go:112 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/source/kind.go#L112-L130. Handler registration followed by cache and handler synchronization.
- E4: pkg/internal/controller/controller.go:298 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/controller/controller.go#L298-L315. Sources start before workers.
- E4: pkg/internal/controller/controller.go:363 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/controller/controller.go#L363-L404. Bounded source startup and synchronization.
- E5: pkg/reconcile/reconcile.go:62 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/reconcile/reconcile.go#L62-L68. Requests contain namespace/name, not event payload or UID.
- E5: pkg/handler/enqueue.go:86 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/handler/enqueue.go#L86-L95. Delete enqueues a name key.
- E6: pkg/internal/controller/controller.go:422 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/controller/controller.go#L422-L440. Done is deferred for each work item.
- E6: pkg/internal/controller/controller.go:485 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/controller/controller.go#L485-L516. Rate-limited errors, delayed retries, and Forget on success.
- E7: pkg/cache/cacheapi/cacheapi.go:118 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/cache/cacheapi/cacheapi.go#L118-L130. Handlers have no cross-handler ordering guarantee.
- E8: pkg/internal/source/event_handler.go:126 - https://github.com/kubernetes-sigs/controller-runtime/blob/e8f9455e429046cea3a1033c2e8555bcdfc8b08c/pkg/internal/source/event_handler.go#L126-L167. Delete tombstones are decoded before invoking the handler.

## Lessons

A keyed reconciliation loop plus current cache reads can replace time-based ownership proofs. Retain only current ownership indexes, reverse dependencies needed to schedule affected workloads, and UID-qualified results. Reverse dependencies are required for prompt invalidation when an intermediate owner changes; upstream queues do not discover those dependents automatically.

Keep the two queues independent, but serialize publication and configuration changes at their shared state boundary. Per-key queue serialization does not protect shared maps or coordinate the two queues. Handler synchronization also does not establish an atomic snapshot across Pod and workload informers. [E6, E7]

## What NOT to copy

- Do not copy the name-only request as a complete object identity. UID validation is exporter-owned behavior.
- Do not copy event payloads into a second full-object store solely to make evaluation possible. Read the shared informer cache.
- Do not keep metadata and full informers for every selected kind. Their upstream maps are distinct and can duplicate collection.
- Do not treat every callback as an ordered transaction across handlers or kinds. Delete tombstones and delayed work still require current-state reconciliation. [E7, E8]
- Do not replace the proof TTL graph with a generic graph framework or another expiry system. Use current owner references and the smallest dependency indexes needed for invalidation.
- Do not claim source synchronization proves current network freshness or that all evaluations have completed. The inspected controller-runtime code delegates reflector internals to client-go; those internals were outside this inspection.
