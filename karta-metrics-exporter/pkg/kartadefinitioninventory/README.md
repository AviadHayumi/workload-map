<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta definition inventory

The exporter needs one Karta definition for each workload group and kind.
For example, a cluster Karta for `apps/Deployment` overrides the bundled
Deployment definition. Deleting it makes the next lookup use the catalog again.

## Storage and lookup

The inventory owns two maps:

- `cluster`: Karta name to decoded definition or decode error. Watch events
  replace or delete entries. A complete relist replaces the whole map.
- `catalog`: workload group and kind to the selected bundled definition.
  It is built once and never changed.

One `sync.RWMutex` protects the cluster map and source health. Each lookup checks
health and selects a winner under the same read lock. Validation and copying
run after releasing the lock. Entries are replaced whole, so an update cannot
change a definition that a reader is using.

`Lookup(gvk)` ignores the requested version and scans the cluster definitions
for the requested group and kind. Source filters apply before selection.
The oldest cluster definition wins, with its name breaking timestamp ties.
If none matches, lookup uses the catalog winner. The catalog prefers Kubernetes
version priority, then name. An invalid winner returns an error instead of
silently selecting another definition.

Keeping every cluster candidate allows deletion, label changes and root-kind
changes to select the next winner without another cache or index. Lookup is
linear in the number of cluster definitions. Catalog lookup is a map access.

Returned definitions are deep copies owned by the caller. `Origin` identifies
the source and name. Cluster results also carry the Karta UID and resource
version. `Selections()` returns the selected definitions in a stable order.
It includes invalid winners with an error so watch setup still knows their kinds.

## Startup and recovery

1. `New` builds the catalog and creates the initial-load channel.
2. Start `Run(ctx)` in a goroutine. The client-go reflector lists and watches
   cluster Kartas. It owns retries and reconnects.
3. Call `WaitLoaded(ctx)` before starting consumers. The first complete list
   replaces the cluster map and closes the channel. An empty list also counts.
4. Each watch add or update replaces one entry. Delete removes it.

`WaitLoaded` waits only for initial loading. It returns immediately when the
cluster source is disabled. Cancellation or stopping before the first load
unblocks the wait with an error. `Run` can be started only once.

Initial loading does not promise ongoing health. `Ready`, `Lookup` and
`Selections` check current source state. Before loading, lookup returns
`ErrNotReady`. A list failure, watch error or unexpected stream closure makes
lookup unavailable. It must not look like a missing definition or cause catalog
fallback. Permission failures retain their distinct error.

After a watch failure, the reflector must complete a fresh list before lookup
can succeed again. A list cannot clear a failure that happened after it began.
A decode error remains until that Karta is corrected or deleted. Stopping `Run`
makes cluster lookups unavailable; a catalog-only inventory keeps serving.

A nil `Definition` with no error means the sources are ready and neither source
has a matching definition. `Snapshot` provides the same selection rules for
fixed inputs without a running Kubernetes watch.
