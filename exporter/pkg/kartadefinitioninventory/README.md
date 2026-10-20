<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta definition inventory

`New(dynamicClient, options)` loads the SDK catalog. `Run(ctx)` owns the cluster
Karta LIST/WATCH lifecycle. Run it once. Cancellation stops the watch and makes
cluster lookups unavailable. With cluster disabled, no client is required and
lookups are ready immediately. `Ready()` reports source availability.

`DefaultOptions()` enables cluster definitions and disables the catalog. The
catalog filter excludes `core-pod-v1`. Enable the catalog explicitly to use it.
A name inclusion or a nonempty label selector selects a definition. When both
are empty, all definitions match. Exclusions win. Unknown catalog names and
invalid label selectors reject construction.

`Lookup` matches the observed root's group and kind, independent of its version.
Cluster candidates override catalog candidates. Cluster ties use oldest creation
time, then name. Catalog ties use name. `Selection.GVK` is the winning
definition's root API representation. Invalid winners return an error rather than
falling back. Returned definitions are caller-owned deep copies.

A nil definition without an error means confirmed absence. An enabled cluster
source must finish every LIST page and establish its WATCH before lookups can
succeed. LIST, WATCH and decoding failures block all lookups, including catalog
fallback. The inventory retries with a full LIST after one second. Cluster
provenance includes the Karta UID and resource version. Catalog provenance uses
only source and name.

Each LIST page has a 30-second deadline. The WATCH keeps the caller's context
without that page deadline. Startup must supply a watch client without a global
HTTP timeout.

The controller independently watches Karta changes. There is no inventory
notification interface. Before publication, the controller must compare selection
provenance and competing candidates against its own accepted facts.

`Selections()` supports initial watch setup before Pods exist. It returns all
selected snapshots, including invalid winners, and a joined error for those
invalid winners. Their definition, GVK and origin remain available for watch
setup. Callers must process both results. A source failure instead returns no
selections. `ErrPending`
(alias `ErrNotReady`) identifies only an uninitialized source.
`ErrUnavailable` and `ErrPermissionDenied` distinguish source failures.
`ErrInvalidDefinition` identifies invalid winning definitions.

`NewSnapshot(options, clusterDefs)` builds an immutable selection once from a
complete cluster snapshot and the SDK catalog. Its `Lookup` and `Selections`
methods share the live inventory selection rules. The caller establishes source
readiness before construction. An omitted root status definition is valid for
selection and remains nil in the returned definition.

Snapshot construction elects candidates without SDK validation. `Candidate(gvk)`
returns a caller-owned winning definition and provenance without validation, even
when that definition is invalid. The controller can compare this result under
its publication lock. `Lookup` and `Selections` lazily validate each immutable
candidate once. Call those methods outside the controller lock.
