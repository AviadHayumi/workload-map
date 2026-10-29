<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Controller

The Pod queue publishes one Pod mapping. The workload queue admits selected
candidates and publishes one canonical root UID. A selected child can admit a
root before any Pod exists. Neither worker replaces the other domain's results.

Both workers read shared collection caches. Evaluated resources, including Pods,
use full objects. Other ancestors use metadata watches. A metadata source remains
accepted until its full replacement synchronizes. Complete LIST replacement
removes missing UIDs. Failed and incomplete lists retain accepted objects.

Root events enqueue associated Pods. A Pod registers its membership before
reading the current root body. Intermediate ownership changes enqueue one
coalesced scope sweep. Ordinary Pod changes only enqueue that Pod unless it has
also appeared as an ancestor. UID checks reject stale deletes and replaced
objects. Retired source callbacks cannot alter an active cache.

Inventory owns Karta watches and definition selection. The application runs it
alongside Runtime. Runtime reads selected definitions through `inventory.Observer`
and queues a source reconciliation when its change channel signals. Failed reads
retain the previous selections and mark definition health unavailable. Recovery
transitions enqueue existing Pods, roots and zero-Pod candidates. Definition
changes scan cached objects and memberships to enqueue the affected workload
kinds and unresolved Pods. An unchanged snapshot leaves in-flight evaluations
valid.

Each reconciliation prepares its own SDK factory and calls `labelresolver.Resolver`
with the full workload and, for Pod metrics, the full cached Pod object. Compiled label rules,
accepted definitions and their immutable structure summaries are shared. The
controller checks the selected definition snapshot again before publication.
Failed evaluations retain the last successful value with its original provenance
and an explicit error. A definition failure does not invalidate a root's verified
creation time.

`Run(ctx)` starts sources and workers and waits for shutdown. `Ready()` checks
initial synchronization and observed errors for required sources. Healthy quiet
sources do not expire. Watch recovery uses client-go reconnect and relist behavior.
The source queue only manages collection lifecycles and configuration changes.

| File | Responsibility |
| --- | --- |
| [runtime.go](runtime.go) | Queues, root membership, definition snapshots and shutdown |
| [sources.go](sources.go) | Shared LIST/WATCH sources, promotion, health and event routing |
| [reader.go](reader.go) | UID-checked cached reads and API alias root boundaries |
| [leaf.go](leaf.go) | Scope sweeps, Pod discovery, mapping and publication |
| [root.go](root.go) | Candidate admission, canonical root status and publication |

See [consistency](../../../docs/design/metrics-exporter/consistency.md) for the
independent result and source-health contracts.
