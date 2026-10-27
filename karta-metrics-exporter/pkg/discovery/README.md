<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Discovery

`Get(ctx, ref)` resolves one object to its workload root. For a Pod owned by a Job
owned by a JobSet, it returns the JobSet reference and the path from Pod to root.
A missing Karta does not make the Job the root. Only an explicitly ignored owner
allows traversal to stop early.

`New(reader, config)` takes an object reader and an explicit `IgnoredTopOwners`
GVK list. The reader checks Runtime's informer cache and performs metadata GETs.
Discovery does not select Karta definitions or start watches. Runtime selects the
definition and ensures root coverage after receiving the resolved workload.

Traversal follows controller owner references. UID replacement, malformed or
multiple controller references, cycles and missing owners fail resolution.
A chain may contain at most 1024 objects. `ControllerOwner` shares reference
validation with Runtime's root-boundary checks. Callers inspect errors with
`errors.Is`.

The reader owns metadata refresh and coalescing. Discovery retains no metadata
between calls. Returned references include the observed resource versions so
the caller can fence publication against changes. `GenericWorkload` carries
identity, path and resolution only; it does not evaluate Karta expressions.
