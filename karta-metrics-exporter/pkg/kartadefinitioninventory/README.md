<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta definition inventory

This package selects definitions from cluster and bundled catalog snapshots.
`Lookup` selects for the observed root's group and kind.
The returned `GVK` identifies the selected definition's API representation.
A nil `Definition` with no error means confirmed absence after sources initialize.
An error means lookup is unresolved or unavailable. Callers ignore that result.
Source failure must not appear as absence or trigger a fallback selection.

`Definition` uses the existing SDK Karta API type.
`Origin` records the catalog or cluster source and its identity.
For cluster definitions, `UID` and `ResourceVersion` come from the Karta object.
Catalog definitions use source and name; `UID` and `ResourceVersion` are empty.
The catalog is bundled with the SDK. Updating it requires a new SDK version.
Returned selections and definitions are caller-owned snapshots.

`Config` is constructor input. Catalog and cluster sources each have an enable
flag and definition-name include/exclude lists. Cluster definitions also have a
label selector. Empty includes mean all. Names and labels must both match;
exclusions win. Selected cluster definitions take precedence over catalog ones.

`New(config, source)` loads the bundled catalog. `Run(ctx)` maintains the cluster
snapshot using the injected `ClusterSource` LIST and WATCH operations. A broken
stream makes lookup unavailable until a fresh LIST and WATCH succeeds.
`Lookup` reads elected cluster and catalog maps by group and kind. Elections and
SDK validation run when the source snapshot changes. An invalid winner returns
`ErrInvalidDefinition`; it never enables fallback to another definition.
Unknown catalog include or exclude names fail construction.

`Selections()` returns an atomic snapshot for root watch planning.
`ClusterOrigins()` returns all filtered cluster identities, including unelected
definitions, for equality comparison with an independent Karta watch. It does not
notify consumers or start workload watches.

Cluster definitions match by group and kind. The oldest creation timestamp wins;
ties use the definition name. Catalog matches use name order. All public reads
return independent copies. Deletes only remove the matching UID.
