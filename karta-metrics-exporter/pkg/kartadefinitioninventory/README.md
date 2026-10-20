<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta definition inventory

This package defines the definition lookup contract. Implementation follows.
`Lookup` selects for the observed root's group and kind.
The returned `GVK` identifies the selected definition's API representation.
A nil `Definition` with no error means confirmed absence after sources initialize.
An error means lookup is unresolved or unavailable. Callers ignore that result.
Source failure must not appear as absence or trigger a fallback selection.

`Definition` uses the existing SDK Karta API type.
`Origin` records the catalog or cluster source and its identity.
`Origin.Revision` is a source resourceVersion or catalog content digest.
`Selection.Revision` is the local selection revision, including confirmed absence.
Returned selections and definitions are caller-owned snapshots.

Constructors, mutation, filtering, preparation and caches remain under design.
