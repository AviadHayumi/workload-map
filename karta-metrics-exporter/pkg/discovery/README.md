<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Discovery

`Get(ctx, ref)` resolves one object to its workload root. For a Pod owned by a Job
owned by a JobSet, it returns the JobSet reference and the path from Pod to root.
A missing Karta does not make the Job the root. Only an explicitly ignored owner
allows traversal to stop early.

`New(reader, config)` takes an object reader and an explicit `IgnoredTopOwners`
GVK list. The reader checks Runtime's informer cache and performs metadata GETs.
An ignored GVK must match the owner's group, version and kind. The last verified
object below that owner becomes the root with resolution `IgnoredOwner`.

Traversal follows controller owner references. UID replacement, stale input
resource versions, malformed or multiple controller references, cycles and
missing owners fail resolution. A chain may contain at most 1024 objects.
`ControllerOwner` exposes the same reference validation for callers. Callers
inspect errors with `errors.Is`.

The reader owns metadata refresh and coalescing. Discovery retains no metadata
between calls. Returned references include the observed resource versions so
the caller can verify them before publication. The reader resolves API scope;
cluster-scoped owners have an empty namespace in the returned path.

This constructor implements ownership resolution only. It does not look up Karta
definitions, evaluate expressions or start watches. `GenericWorkload` carries
identity, path and resolution. `Dependencies` and `Result.MissingCoverage` remain
available for the component contracts; this resolver leaves `MissingCoverage`
empty. Definition selection, root coverage and runtime wiring remain integration
work. Intermediate owners do not require watches.
