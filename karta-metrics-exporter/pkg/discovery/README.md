<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Discovery

Discovery resolves a Kubernetes object to its workload root and returns the
verified ownership path. For a Pod owned by a Job owned by a JobSet, the root is
that JobSet and the path is Pod -> Job -> JobSet. A missing Karta definition does
not make the Job the root.

Traversal follows controller owner references. An explicitly ignored owner type
stops traversal before that owner is read. The configured type must match the
owner's group, version and kind. The last verified object below that owner becomes
the root with an ignored-owner resolution.

Objects are read from Runtime's shared collection cache. On a missing source,
the reader asks Runtime for metadata LIST/WATCH coverage. It returns a retryable
pending error until that source synchronizes. Runtime owns source sharing and
reconnects. Discovery retains no metadata between requests. Returned references
include the observed resource versions so the caller can verify them before
publication. The reader resolves API scope; cluster-scoped owners have an empty
namespace in the returned path.

UID replacement, stale input resource versions, malformed or multiple controller
references, cycles and missing owners fail resolution without returning a partial
workload. A chain may contain at most 1024 objects.

Discovery handles ownership only. It accesses objects through its reader and
does not own watches. Runtime selects Karta definitions and ensures full root
coverage before expression evaluation. `MissingCoverage` is not populated by
this implementation.
