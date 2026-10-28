<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Result store

The Pod worker writes one `PodResult` per Pod UID. The workload worker writes one
`WorkloadResult` per canonical root UID. For a Pod mapped to a Job, changing the
Pod's component replaces only the Pod result. A concurrent Job status update
replaces only the workload result.

Pod results carry the full root identity and Karta provenance. They can be
collected before the root result exists. A cluster-scoped root has an empty
namespace, while its Pod records retain their own namespaces.

## Synchronization

`Writer` exposes updates and reads. `Capturer` exposes `Capture`.
`Interface` combines them for startup wiring. Store owns its mutex. Each method
is atomic and safe for concurrent use.

Upserts copy the result and its maps and slices before publishing it. `Pod`,
`Workload` and `Capture` return immutable result pointers. Callers must not mutate
those pointers or their nested data. A worker makes its own copy before preparing
an update. `Capture` copies the result pointer lists, discovery failures and
source health under one lock. Later writes do not change an existing view.

A read followed by an upsert remains two operations. Workers own ordering,
accepted identities and deletion handling. Store does not validate whether a
calculation used the latest object. Workload removal does not remove Pod results.
Pod removal does not remove a workload result with the same UID.

## Results and validity

`HasValue` distinguishes a successful value from a first-attempt error. On a
failed attempt, the worker retains any last-good record, including its root and
Karta identity, and supplies the error and required sources. Store replaces the
value and error together. One object's success cannot clear another's error.

`SourceKey` contains the actual GVR and collection namespace. An all-namespace
watch has one shared key with an empty namespace. `SourceHealth` contains the GVK,
initial synchronization state and observed LIST/WATCH error. A missing source or
an unsynchronized source is unavailable. Permission denial takes precedence over
other transport failures.

`CapturedView.Valid(sources, reason)` checks the saved operation error first, then
every required source. Healthy rows have no age limit. `Options.Now` is retained
for construction compatibility and is unused. Source recovery changes source
health only. An object error remains until its worker supplies a replacement.

Workload `Error` affects creation and status. `StatusError` affects status only.
`CreationSources` contains the sources needed to establish the root and creation
time. `Sources` also includes sources needed for status and Karta evaluation.
A Karta-source failure alone therefore retains the creation timestamp.

Selected candidates that have no confirmed root use `SetDiscoveryFailure` and
`RemoveDiscoveryFailure`. Their UID-keyed errors are separate from workload
results, so candidate admission cannot overwrite a root value. `Capture.Failures`
returns these errors with the original input identity.

## Verify

From the exporter module directory:

```sh
go test -race ./pkg/store
GOMAXPROCS=4 go test ./pkg/store -run '^$' -bench BenchmarkPodMutations -benchmem
```

Tests cover copied inputs, frozen captures, exact source keys, source recovery,
independent ownership and concurrent updates. Benchmarks measure lookup, single
Pod replacement, insert/delete, full replacement and capture at 1000 and 100000
Pods. Single-Pod replacement does not visit other Pods in the same workload.
