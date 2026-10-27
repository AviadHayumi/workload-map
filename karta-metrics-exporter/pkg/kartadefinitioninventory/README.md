<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta definition inventory

The inventory selects one Karta per root group/kind from the bundled catalog and
a cluster snapshot. For example, a selected cluster JobSet definition replaces
the catalog JobSet definition. Its GVK tells Runtime which API representation
to evaluate.

`New(config)` loads the catalog. Runtime owns the cluster Karta watch and calls
`ReplaceCluster(definitions)` with a complete accepted snapshot. The inventory
applies source filters, elects definitions and validates the winners. It starts
no watches and owns no source-health state.

`Lookup(gvk)` returns the selection for that group/kind. A nil definition and no
error mean absence from the supplied snapshots. Runtime must establish cluster
source availability before using that absence. A pending, failed or expired
source cannot trigger generic or catalog fallback. An invalid winner returns
`ErrInvalidDefinition` together with its selection.

`Selections()` returns the selected definitions for root-watch planning, including
invalid winners. Each selection contains the SDK Karta definition, its GVK and an
`Origin`. Cluster origins include the Karta UID and resourceVersion. Catalog
origins contain the source and name. Public reads return independent copies.

Each source has an enable flag and definition-name include/exclude lists.
Cluster definitions also have a label selector. Empty includes allow all names.
Names and labels must both match; exclusions win. Unknown catalog include or
exclude names fail construction.

Cluster definitions take precedence over catalog entries. Within a source, the
oldest creation timestamp wins; names break ties. Catalog timestamps are zero,
so catalog matches use name order. Replacing the cluster snapshot also removes
definitions absent from it. The catalog changes with the bundled SDK version.

See the [definition update contract](../../../docs/design/metrics-exporter/consistency.md#definition-updates)
for Runtime's source validation and publication checks.
