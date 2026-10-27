<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Open exporter issues and proposed fixes

Deleting a workload with 100,000 Pods removes its exported metrics, but the
process still has about 656 MB of live heap after forced garbage collection.
The original build retains about 649 MB in the same test. Output cleanup and
memory cleanup are separate acceptance checks.

This document records the remaining findings from the simplification comparison
on 2026-10-27. The measured source fingerprint and completed checks are in
[verification](verification.md). The
[measurement report](simplification.md#modeled-api-runtime) holds the full
before/after results. The investigations and fixes below are proposed work.
None of these open items is claimed fixed.

| Priority | Finding | Status |
| --- | --- | --- |
| First | Giant-workload memory after deletion exceeds the limit | Confirmed failed gate in both builds; cause unknown |
| Second | The sustained small-workload run uses 10.4% more CPU | Observed in one matched pair; repeatability and cause unknown |
| Before release | Deployed acceptance for the simplified image is missing | Validation gap; earlier-image results do not cover this build |

## Memory after deleting a giant workload

### What failed and why it matters

One workload with 100,000 Pods receives 15 updates over five minutes, then passes
through deletion, same-name recreation and final deletion. Metrics converge to
an empty workload set. The test requests garbage collection and checks live heap.

| Measurement | Original | Simplified |
| --- | ---: | ---: |
| Post-delete live heap, bytes | 649,147,912 | 655,640,192 |
| Allowed live heap, bytes | 271,062,552 | 271,053,816 |
| Result | Failed | Failed |

The gate allows the initial heap plus the larger of 256 MiB or 10% of sampled
peak heap. The simplified result is about 1.0% higher. This is an existing failed
acceptance check, not evidence that the simplification introduced the retention.
The risk is memory remaining after large workloads disappear. Repeated creation
and deletion could amplify that cost; this has not been established by the run.

The diagnostic endpoint calls `runtime.GC()` before reading memory statistics.
The failure concerns live Go heap after GC, not just process RSS. Both runs pass
the correctness, publication-latency, queue-drain and sustained heap-growth gates.
Empty metrics and empty work queues do not prove that every object owner has
released its references.

### Candidate causes to investigate

The strongest source-grounded hypothesis is a difference between informer-cache
cleanup and accepted-observation cleanup. `ensureWatch` creates a
`SharedIndexInformer` with its own object store. `verifyWatch` can remove a missing
object from Reader and Runtime bookkeeping after a metadata LIST. That path does
not directly remove the object from the informer's separate store. Metrics may
therefore disappear before the informer receives deletion or relist results.
See [watch ownership and verification](../../../karta-metrics-exporter/pkg/controller/watches.go).
This is a hypothesis, not a heap-profile diagnosis.

Two other ownership paths need checking:

- Reader metadata, failed-read records and invalidation records have their own
  pruning or expiry. Inspect `pruneMetadataLocked` in
  [metadata_owners.go](../../../karta-metrics-exporter/pkg/controller/metadata_owners.go)
  and `expireMetadata` in
  [reader.go](../../../karta-metrics-exporter/pkg/controller/reader.go).
- A scrape or diagnostic request can still hold a captured publication after
  Store removes it. `Capture` shares immutable publication pages until the reader
  finishes. See [Store capture](../../../karta-metrics-exporter/pkg/store/publication.go).

### Proposed investigation and fix

1. Repeat the same giant-workload scenario with `--heap-profiles`. The existing
   [scale harness](../../../karta-metrics-exporter/test/scale/runtime_scale.py)
   saves `heap-NNN.pprof` files. The
   [diagnostic wrapper](../../../karta-metrics-exporter/test/scale/cmd/main.go)
   already supports the heap-profile request.
2. Compare allocation sites before deletion, immediately after deletion and after
   source caches have caught up. Add test-only object counts for informer stores,
   Reader observations, roots, Pods, metadata, failures and Store publications.
   Record whether scrape or diagnostic requests are still active.
3. Preserve the immediate measurement. Add later measurements after completed
   reads and a full refresh/expiry interval to distinguish delayed release from
   persistent retention. In the current harness, `--settle-seconds` runs after the
   recorded heap gate. Increasing that flag alone cannot explain or change the
   failed measurement.
4. Fix the owner that retains the objects once the profile and counts identify
   it. Possible changes include correcting deletion or relist handling, pruning
   unused metadata, or shortening the lifetime of a captured view. Do not clear
   informer stores blindly or remove identity checks to reduce memory.

Closure requires a regression that repeatedly creates, deletes and recreates
workloads, including reuse of a name with a new UID. Assert that deleted objects
and their references are released, new UIDs remain correct, and the existing
post-delete heap gate passes. Rerun the giant scenario and relevant race tests.
Do not widen the gate or substitute a delayed sample for the original failure.

## CPU increase during sustained updates

### What was observed

The 1,000-workload, one-Pod-per-workload scenario delivers 30,000 updates over
five minutes. Process CPU rises from 44.13 to 48.72 seconds, an increase of 4.59
CPU-seconds or 10.4%. Publication latency remains about 0.263 seconds at the
measured p99 upper bound. All measured gates pass.

API request counts are identical. Diagnostic request counts are slightly lower
in the simplified run. The increase therefore is not explained by extra requests
in this pair. Each version has only one runtime measurement. The result cannot
yet establish a repeatable regression or identify its cause.

### Proposed investigation and fix

1. Repeat at least three matched pairs sequentially. Alternate their order. Keep
   the host, toolchain, event count, scrape cadence and diagnostic settings equal.
   Record process CPU separately for startup, steady updates and deletion.
2. If the increase persists, collect CPU profiles for the same update interval.
   Inspect the functions accounting for the extra CPU. Check reconciliation,
   expression evaluation, allocation/GC and instrumentation costs without
   assuming which one is responsible.
3. Remove or reuse only work shown to be redundant by those profiles. Keep sparse
   Pod updates, immutable pages, cancellation and final input-version checks.
   Add a focused benchmark for the identified expensive path.

Closure requires either a repeatable improvement after a targeted fix or repeated
measurements showing no consistent increase. Retain all measurements and their
variation. A single faster rerun is not enough to close the finding. Unit and race
checks must continue to pass after any source change.

## Final-image acceptance and capacity evidence

### What remains unverified

The simplified build passed exporter presubmit, unit tests, race checks and
integration tests against a real Kubernetes API server. Its deployed operator
and monitoring suites were compiled but were not executed for that image.
Earlier-image acceptance is separate evidence.

The gap covers the installed exporter with real operators, chart configuration,
RBAC, Prometheus collection, upgrade and removal. Source review and envtest cannot
prove the complete deployed lifecycle.

The local scale runs cover the shapes listed in the measurement report. The
single-workload 100,000-Pod case receives only 15 updates. Its p99 upper bound is
the maximum of those observations. Higher giant-workload update rates and larger
fleet sizes are not established capacity. Whole-process CPU and allocations also
include diagnostic and scrape work.

### Proposed acceptance work

1. Build the final production image from the revision being released. Record its
   immutable image identity and use that same image throughout acceptance.
2. Run the [deployed operator matrix](../../../karta-metrics-exporter/test/e2e/README.md)
   through `make test-exporter-e2e` in its isolated local cluster. Exercise the
   configured workload kinds and supported API profiles.
3. Run the [deployed monitoring suite](../../../karta-metrics-exporter/test/monitoring/README.md)
   in its separate isolated cluster. Verify Prometheus collection, permission
   denial and recovery, upgrade, network-policy behavior and exporter removal.
4. Run the repository's required presubmit and applicable CI checks for the final
   revision. Preserve the deployed reports separately from compilation results.
5. If the release promises capacity beyond the measured shapes, define that
   workload mix and event rate, then run it with explicit latency, memory and
   cleanup gates. Do not infer that capacity from the current microbenchmarks.

Closure requires successful deployed reports tied to the final image, with any
failures resolved or explicitly accepted as release limitations. Performance
claims must stay within the scenarios actually measured.

## Review status

Independent code and architecture reviews have no remaining findings. The
passed API and metric-correctness checks are recorded in
[verification](verification.md#automated-checks). They do not close the memory
finding, explain the CPU result or prove deployed acceptance.
