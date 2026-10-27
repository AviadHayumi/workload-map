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

Object events queue ownership resolution. Resolved roots queue status, Pod
mapping and label calculations. A separate source queue handles synchronization
and verification. Each root has at most one publication worker at a time. It retains a prepared
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
| [runtime.go](runtime.go) | Startup, queues, definition changes and shutdown |
| [watches.go](watches.go) | Watch selection, object events and watch recovery |
| [reader.go](reader.go) | Cached objects, metadata requests and freshness checks |
| [metadata_owners.go](metadata_owners.go) | Find needed owners, preload their metadata and remove unused entries |
| [metadata_refresh.go](metadata_refresh.go) | Refresh retained owners through LIST or GET |
| [reconcile.go](reconcile.go) | Resolve ownership, evaluate Karta and labels, then publish |
| [renewal.go](renewal.go) | Track affected Pods and schedule publication renewal |
| [root_boundary.go](root_boundary.go) | Validate controller owners and root identity across API versions |
| [selection.go](selection.go) | Apply workload output filters |
