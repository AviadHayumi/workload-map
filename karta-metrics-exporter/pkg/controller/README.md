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

Runtime owns the accepted Karta snapshot and definition election. Failed reads
retain the previous selection. Each reconciliation prepares its own SDK factory
and label resolver closure. Compiled label rules, accepted definitions and their
immutable structure summaries are shared. Failed evaluations retain the last
successful value with its original provenance and an explicit error.

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
