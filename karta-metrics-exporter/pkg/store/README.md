<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Publication Store

Runtime publishes one immutable result per root UID. Collector calls `Capture`
to read publications and resource health from one point in time.

`Writer` exposes Runtime's operations. `Capturer` exposes only the read operation.
`Interface` combines them for startup wiring.

## Synchronization

Store owns its mutex. Every method is safe to call concurrently and completes
atomically. `Capture` fixes one time, record references, health and failure state
under the Store lock. It releases the lock before Collector walks the view.
Pass a fake clock through `Options.Now` in tests. Nil uses `time.Now`.

Runtime owns input verification and operation ordering. It serializes accepted
events with final verification and publication. A `Published` read followed by
`Publish` is two operations, so Runtime must serialize that sequence too. It
rechecks deletion and input versions before publishing. Store's mutex does not
make a stale calculation current.

Collection can happen between writes. Runtime must invalidate affected evidence
before clearing a shared failure. Only newly verified data can become valid.
Failure updates use the same Runtime ordering as publications. One Runtime owner
replaces the complete health list.

## Operations

- `Publish` replaces a root's complete publication. Do not mutate it afterward.
- `Published` returns the immutable publication, or nil.
- `RemovePublication` removes the root and its Pod rows. An absent UID is safe.
- `SetPublicationFailure` sets a shared failure. An empty reason or `none` clears it.
- `SetPublicationHealth` replaces the resource health list.
- `Capture` returns a view that later writes cannot change.

`New(Options)` constructs the Store. `CapturedView.Valid(evidence)` checks an
explicit failure first, then shared dependency failures, then expiry at the
captured time. Zero expiry and the expiry instant are invalid. Failures set
before their first publication remain available. Store counts
publication and health references to each key. It removes a retired key when its
last reference disappears. Replacement adds new references before removing old
ones. Shared Pod pages retain their references without scanning their rows.
Runtime keeps a health reference for each active stream and explicitly clears
failures after invalidating affected evidence. A never-referenced failure must
also be cleared explicitly when its dependency leaves scope.

Publication ownership transfers on `Publish`. The publication and all nested
maps, slices and pages must remain immutable, including values returned by
`Published` and `Capture`. Store shares these references to avoid copying all Pod
rows on every scrape. Runtime creates replacement maps and slices when inputs
change. Health inputs are copied, including dependency slices.

`Publication.WithPod(uid, replacement)` copies changed pages and the page pointer
list. Sorted pages stay ordered, split at capacity and merge after deletion when
they fit. A nil
replacement removes the Pod. A replacement must have the requested UID and its
nested data follows the same immutable ownership contract. `BuildPodPages` sorts
a copy of the input rows and reuses unchanged pages. `Publication.Pod` returns an
immutable row. Page-bound validation takes time proportional to the number of
pages. Lookup then searches sorted rows. Arbitrary caller-provided unsorted pages
use a linear fallback. Runtime builds initial and refreshed populations with
`BuildPodPages`, rather than repeatedly inserting every Pod with `WithPod`.

Each immutable page caches its dependency counts before Store takes its write
lock. Shared pages reuse these counts. A full expiry refresh still creates new
rows, but shared stream dependencies are counted once per page under the lock.
Helpers share nested label maps and evidence dependency slices;
callers must replace these rather than mutate them.

Store owns its data types and does not import discovery. Runtime translates the
resolved identity into a publication.
