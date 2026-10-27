<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Runtime

`Runner.Run(ctx)` owns watches and workers. It returns after they stop.
`Runner.Ready()` reports whether required sources have current evidence.
`Dependencies` supplies Discovery, the definition inventory, label resolution
and the Store writer. Collector receives only Store's read interface.

Runtime watches Pods in selected namespaces. It shares root watches by resource
and namespace, including roots returned by Discovery without existing coverage.
Intermediate owners use cached metadata reads and periodic refresh, without
additional watches.

Runtime is the only observer of cluster Kartas. It checks its accepted Karta
snapshot before calling inventory `ReplaceCluster`. Inventory selects definitions;
Runtime owns source readiness and failure handling.

Direct Reflectors synchronously accept source observations into Reader and queue
work. Callbacks perform no API reads or expression evaluation. A typed input
queue handles ownership discovery for Pods and selected non-Pod candidates,
including children whose ancestors have no Pods. Input keys identify the object
address, API representation and UID. Discovery workers commit attribution or
candidate admission and queue the root UID. They do not publish rows.

The root queue owns status, Pod mapping, labels and complete publication. Slow
candidate owner reads cannot occupy its workers. All API representations of one
UID share its queue key. A separate
maintenance queue handles source synchronization, verification and owner refresh.
Each root has at most one publication worker at a time. It retains a prepared
label resolver for the exact immutable workload input. Pod-only updates reuse
that scope; a changed workload representation or version replaces it.

Before publishing, Runtime checks that its inputs still match accepted events.
Store locks its own data. Deleting a root removes its publication. A failed read
makes dependent observations invalid instead of removing the workload.

The [implementation design](../../../docs/design/metrics-exporter/README.md)
shows the component calls. [Verification and publication](../../../docs/design/metrics-exporter/consistency.md)
covers caching, concurrent updates and failures.

## Files

| File | Responsibility |
| --- | --- |
| [runtime.go](runtime.go) | Startup, typed input/root/maintenance queues, definition changes and shutdown |
| [sources.go](sources.go) | Direct Reflector acceptance, atomic replacement, watch planning, verification and recovery |
| [reader.go](reader.go) | Accepted objects, metadata reads, freshness and cross-version root boundaries |
| [owners.go](owners.go) | Batched owner acquisition, metadata refresh, demand pruning and path dependencies |
| [leaf.go](leaf.go) | Dispatch ownership inputs; resolve Pod attribution and enqueue root publication |
| [root.go](root.go) | Non-Pod admission, output selection, evaluation, publication and renewal |
