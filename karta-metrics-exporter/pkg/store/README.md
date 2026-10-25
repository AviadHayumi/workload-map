<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Publication Store

Runtime publishes one immutable result per root UID. Collector calls `Capture`
to read publications and resource health from one point in time.

`Writer` exposes Runtime's operations. `Capturer` exposes only the read operation.
`Interface` combines them for startup wiring.

Call `BindPublication` before workers or collection start. Runtime holds that
mutex for writes and read-modify-write operations. `Capture` acquires it internally
before the Store read lock. Write methods do not acquire it again.

- `Publish` replaces a root's complete publication. Do not mutate it afterward.
- `Published` returns the immutable publication, or nil.
- `RemovePublication` removes the root and its Pod rows. An absent UID is safe.
- `SetPublicationFailure` sets a shared failure. An empty reason clears it.
- `SetPublicationHealth` replaces the resource health list.
- `Capture` fixes one time, record references and failure state for Collector.

The data types and interfaces are included here. Storage, locking, expiry checks
and copy-on-write Pod updates are not implemented. The implementation will add
`Publication.WithPod(uid, replacement)` and `CapturedView.Valid(evidence)`,
including the captured view's private failure state.
`Valid` checks explicit failure, dependency failures and expiry at the captured
time. A later failure update must not alter an existing captured view.

Store owns its data types and does not import discovery. Runtime translates the
resolved identity into a publication. This keeps storage and collection usable
without discovery or Kubernetes clients.
