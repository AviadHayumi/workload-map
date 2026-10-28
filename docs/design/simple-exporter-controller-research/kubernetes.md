<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Kubernetes controller patterns

## TL;DR

An intermediate ReplicaSet can change its owner while its Pods stay unchanged. An exporter must then refresh those Pods' root membership. Kubernetes provides useful small patterns for this: separate domain queues, cached owner traversal with UID checks, and a queued scan that finds affected roots. They support a scope sweep without requiring a persistent graph of every owner path.

Recommendation: keep the Pod and root queues. Coalesce intermediate-owner changes into a scope work item in the Pod queue. That work item lists cached Pods in the affected scope and enqueues them. Pod reconciliation compares previous and current membership and dirties both roots. This exact composition is a Karta proposal, not a generic controller found upstream.

## How it works upstream

The taint eviction controller has separate Pod and Node workqueues. It waits for both informer caches to synchronize. Workers retrieve the current object from a lister. A Node change finds affected Pods through the informer's node index. This is evidence for separating domains while sharing informer data, not for copying its eviction machinery.

The DaemonSet controller queues node changes. Its node worker retrieves the current Node, lists cached DaemonSets, reads Pods from an index, and enqueues affected DaemonSets. A scan over cached candidates is an established alternative to maintaining reverse relationships for every possible dependency.

The Deployment controller resolves a Pod's ReplicaSet and then its Deployment through listers. At both steps it rejects a name match with a different UID. When a ReplicaSet changes controller reference, it queues both the old and current Deployment. Deletion handlers also unwrap informer tombstones, whose contents may be stale.

The workqueue deduplicates equal pending keys. An Add during processing marks the key dirty; Done then queues it again. This supplies scan coalescing and preserves a change arriving during a scan. It does not provide a timed debounce window or synchronization between two different queues.

## Relevance to Karta

| Decision | Recommendation |
| --- | --- |
| Pod/root queues | Keep two domains. Use stable lookup keys and read current informer state in workers. |
| Intermediate owner changes | Enqueue a scope sentinel in the Pod queue. Scan the cached scope, then enqueue ordinary Pod keys. Select a scope that contains all affected descendants. |
| Changed root membership | Compare the stored Pod membership with the newly resolved membership. Dirty the previous and current root. A final Pod-to-root membership record does not require storing every intermediate edge. |
| UID safety | Check the owner reference UID at every cached traversal step. Store the Pod/root UID with derived state. Cleanup for an old UID must not erase state belonging to a replacement object with the same name. |
| Failures | Retry read errors. Distinguish a failed lookup from NotFound. A failed scope scan must remain eligible for retry. |
| Concurrency | Use queue dirty semantics for invalidation during a scan. Synchronize shared snapshot writes separately; different queues do not serialize each other. |

The cost is proportional to cached Pods in each dirty scope. Measure sweep frequency and size before adding a reverse graph. These sources support the mechanics; they do not establish that Karta watches every relevant owner change. Verify owner reassignment, intermediate deletion, replacement UIDs, and invalidation during a sweep in Karta tests.

## Evidence

Inspected local Kubernetes commit `6737adf59793fd79cb4a0c65ef36340b66f4643e`. These are source observations. No upstream or Karta tests were run for this report.

- Two domain queues and informer-backed readers: `pkg/controller/tainteviction/taint_eviction.go:89-104` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/tainteviction/taint_eviction.go#L89-L104, construction and index lookup at 191-221 - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/tainteviction/taint_eviction.go#L191-L221, cache synchronization at 300-303 - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/tainteviction/taint_eviction.go#L300-L303.
- Current-state Pod lookup and NotFound handling: `pkg/controller/tainteviction/taint_eviction.go:492-510` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/tainteviction/taint_eviction.go#L492-L510.
- Queued cache scan that fans out to root controllers: `pkg/controller/daemon/daemon_controller.go:1515-1587` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/daemon/daemon_controller.go#L1515-L1587. The worker retries errors and forgets successful keys before the cache scan implementation below it.
- Old and new roots on owner change: `pkg/controller/deployment/deployment_controller.go:299-316` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/deployment/deployment_controller.go#L299-L316. Cached two-hop owner traversal and UID checks: 431-477 - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/deployment/deployment_controller.go#L431-L477.
- Stale tombstones are explicitly acknowledged and unwrapped: `pkg/controller/deployment/deployment_controller.go:340-367` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/deployment/deployment_controller.go#L340-L367. Same-name replacement is explicitly detected by UID: `pkg/controller/daemon/daemon_controller.go:302-320` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/daemon/daemon_controller.go#L302-L320.
- UID-keyed deletion is a small separable garbage-collector pattern: `pkg/controller/garbagecollector/graph.go:239-260` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/garbagecollector/graph.go#L239-L260. This supports preserving object incarnation in derived state, not adopting the graph.
- Pending-key coalescing and rerun after concurrent invalidation: `staging/src/k8s.io/client-go/util/workqueue/queue.go:227-250` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/staging/src/k8s.io/client-go/util/workqueue/queue.go#L227-L250, Get/Done at 265-301 - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/staging/src/k8s.io/client-go/util/workqueue/queue.go#L265-L301.

## Lessons

Queue invalidation and reconcile current cached state. Keep identity checks even when lookup uses namespace and name. A cache scan can be simpler than a dependency index when changes are infrequent enough. Retain only the derived membership needed to remove old metric contributions and update both roots.

## What NOT to copy

- Garbage collector virtual nodes, absent-owner bookkeeping, dependent graph maintenance, and deletion/finalizer transitions. Its deletion policy is a different problem from refreshing metrics. For example, `graph_builder.go:803-876` - https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/garbagecollector/graph_builder.go#L803-L876 handles virtual deletion and dependent scheduling that the proposed sweep does not need.
- Taint eviction's timed deletion queue, shard channels, or eviction API writes. Only the domain queue and cache lookup patterns apply here.
- Deployment adoption, live API rechecks before writes, or controller expectations. The exporter is deriving state, not creating and deleting workload resources.
- A proof TTL merely because owner traversal exists. The cited event-and-cache patterns do not require one. Removing any existing TTL still requires verifying Karta's event coverage and retry behavior.
