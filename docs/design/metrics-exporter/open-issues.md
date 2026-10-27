<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Open exporter issues and proposed fixes

In the preceding comparison, deleting a workload with 100,000 Pods removed its
exported metrics, but left about 656 MB of live heap after forced garbage
collection. The original build retained about 649 MB in the same test. Output
cleanup and memory cleanup are separate acceptance checks.

This document records the remaining findings from the simplification comparison
on 2026-10-27. The measured source fingerprint and completed checks are in
[verification](verification.md). The
[earlier measurement report](simplification.md#modeled-api-runtime) preserves
the failed runs. The [final controller comparison](../../../karta-metrics-exporter/test/scale/results/controller-leaf-root.md)
completed all 17 acceptance runs and four supplemental heap diagnostics. Every
applicable absolute gate passed. The [final-image kind results](controller-audit.md#final-image-kind-acceptance)
close the operator-matrix and source-recovery portion of deployed acceptance.
The remaining limits and the disposition of historical findings follow.

| Priority | Finding | Status |
| --- | --- | --- |
| First | Historical giant-workload heap failure and current retained-memory cost | Historical failures not reproduced by either fresh variant; cause unproven. Current candidate retains more heap after queue-idle GC. |
| Historical finding | One sustained small-workload pair used 10.4% more CPU | Not repeated in three alternating current pairs; +0.3% between per-variant CPU medians. |
| Operational limit | Obsolete resource-type health can outlive workload deletion | Inherited periodic pruning; no workload-publication invariant violation identified. |
| Before release | Remaining final-image validation | Operator matrix and recovery passed for the exact r1 image. Dedicated monitoring lifecycle remains outside that result. Actual-branch post-commit presubmit is required before publication. |

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

### Current comparison and remaining scope

The fresh baseline and candidate passed their giant post-delete heap gates.
Neither reproduced the earlier failure. Different source/binary versions and
convergence polls (250 ms earlier, 5 seconds now) prevent attributing that change
to the refactor. Final settle also changed from zero to 30 seconds, but the
initial deletion GC precedes settle. Settle alone cannot explain that sample.

The candidate still has a retained-memory cost. Four supplemental diagnostics
checked idle queues twice, 250 ms apart, then forced GC and checked idle queues
again. Candidate live heap was higher in both fixtures: +2.22 MiB (+17.6%) for
10,000 zero-Pod roots and +21.30 MiB (+35.4%) for the giant lifecycle without
sustained churn. These diagnostics supplement the full-duration acceptance
runs. They do not replace them.

Sampled profiles point mainly to retained map and queue backing capacity.
Object entries and queue items are removed. Instrumented client-go queue-metric
maps also contribute; the default no-op metrics provider does not allocate
those maps. The normal-command smoke did not measure retained heap, so the
instrumented profile cannot predict its heap. Exact profile attribution and
all measurements are in the [report](../../../karta-metrics-exporter/test/scale/results/controller-leaf-root.md#retained-heap-and-deletion-health).

The samples do not identify every byte of the delta or establish bounded memory
under indefinite identity turnover. Five-minute growth gates passed. A repeated
create/delete endurance workload was not run.

### Earlier retention hypotheses

The measured build had separate informer and accepted-observation stores.
Metadata verification could remove a missing object from Reader and Runtime
before its informer processed deletion or relist results. That was a candidate
cause, not a heap-profile diagnosis.

The subsequent [controller change](controller-audit.md) removes the persistent
informer store. [Source acceptance](../../../karta-metrics-exporter/pkg/controller/sources.go)
now writes directly into Reader. This closes the duplicate ownership path, but
does not by itself explain or close the measured heap failure.

Two other ownership paths need checking:

- Reader metadata, failed-read records and invalidation records have their own
  pruning or expiry. Inspect `pruneMetadataLocked` in
  [owners.go](../../../karta-metrics-exporter/pkg/controller/owners.go)
  and `expireMetadata` in
  [reader.go](../../../karta-metrics-exporter/pkg/controller/reader.go).
- A scrape or diagnostic request can still hold a captured publication after
  Store removes it. `Capture` shares immutable publication pages until the reader
  finishes. See [Store capture](../../../karta-metrics-exporter/pkg/store/publication.go).

### Remaining investigation and possible fix

Preserve the historical failing samples and the current passing samples.
The [profile report](../../../karta-metrics-exporter/test/scale/results/controller-leaf-root.md#retained-heap-and-deletion-health)
already supplies lifecycle profiles and queue-idle GC checks. The next useful
measurement is repeated creation, deletion and recreation with new UIDs,
including reused names. Record retained heap and index/queue capacity across
cycles. Distinguish removed object references from backing storage that remains
allocated. Measure the normal command separately if a normal-command memory
claim is needed.

If repeated turnover shows unacceptable capacity growth, identify the retaining
structure before changing its lifecycle. A bounded rebuild or shrink strategy
may be appropriate, but is not implemented or established by this comparison.
Preserve source acceptance, identity checks and the original absolute heap gate.
Do not substitute a later sample for a failing immediate sample. The historical
failure remains unattributed; current retained-memory cost remains a measured
limitation even though all current gates pass.

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

### Current disposition

The final comparison ran three alternating five-minute pairs with 30,000
verified updates per run. CPU changed +0.3% between per-variant medians; the
individual candidate-minus-baseline results had mixed signs. The earlier 10.4%
increase therefore did not repeat. This is a descriptive result for the current
source pair, not a causal explanation of the earlier measurement or a guarantee
for other workloads. The [report](../../../karta-metrics-exporter/test/scale/results/controller-leaf-root.md#three-small-root-churn-pairs)
retains every pair, including the higher RSS and slower drain sample. No
CPU-specific production fix is proposed from these results.

## Obsolete metadata health after workload deletion

In the fresh giant-root comparison, workload rows disappeared 6.676 seconds
after deletion began in the baseline and 6.757 seconds in the candidate. The
candidate then retained an `apps/v1` ReplicaSet resource-type health failure with
reason `unresolved_owner` for about 45 more seconds. No valid workload rows
remained or reappeared during that health tail.

An authoritative owner refresh records an unresolved-owner failure when a
retained owner is missing. Reader retains that failure independently of workload
publication. Ordinary deletion removes attribution and publication without
immediately recomputing metadata demand. The next owner-maintenance pass prunes
obsolete failures and republishes health. See
[metadata pruning and refresh](../../../karta-metrics-exporter/pkg/controller/owners.go)
and [retained failure health](../../../karta-metrics-exporter/pkg/controller/reader.go).

`Runtime.refreshMetadata`, `Runtime.pruneMetadataLocked`, `Reader.failureHealth`
and `Reader.recordMetadataFailureLocked` are byte-for-byte unchanged from
`cdcc8779`. The candidate deletion crossed a scheduled 45-second refresh; the
baseline deletion started after it. Clearance aligned with the following
refresh. That ordering is an inference from the configured timer, API counters
and scrape timestamps; individual request timestamps were not recorded.

This is an inherited cleanup delay, not evidence of slower workload deletion or
a newly missing invalidation path. Queue and API delays can extend it beyond one
refresh interval. No production fix is recommended solely from this observation.

If prompt removal of obsolete health becomes a requirement, consider coalescing
a metadata-prune and health-publication pass after accepted deletion batches on
the existing maintenance queue. This proposal is not implemented. Avoid a full
Pod scan for every deletion. An empty path-dependency index alone cannot prove
that no consumers remain, because unresolved consumers may lack a completed
path. A deterministic characterization test should exercise the same refresh
and deletion ordering on both versions before changing this behavior.

## Final-image acceptance and capacity evidence

### What remains unverified

The earlier component comparison compiled the deployed suites without running
them. The subsequent controller image has now passed all 24 default kind cases,
both alternate API cases and focused bare-Pod exclusion. Its isolated probes
also passed zero-Pod ancestor changes and workload/Karta source denial, expiry
and recovery without restarting the exporter. The
[controller audit](controller-audit.md#final-image-kind-acceptance) records the
exact image and limits.

The dedicated monitoring suite has not been rerun for this image. Its upgrade,
network-policy and complete monitoring lifecycle remain unverified. A clean
snapshot passed unmodified `make check`, and Helm lint/validation passed. The
actual branch still requires `make check` after the final commit and before push.

All 17 planned local acceptance runs passed their applicable absolute gates.
Four supplemental heap diagnostics also completed. The scale runs cover the
shapes listed in the final comparison report. The
single-workload 100,000-Pod case receives only 15 updates. Its p99 upper bound is
the maximum of those observations. Higher giant-workload update rates and larger
fleet sizes are not established capacity. Whole-process CPU and allocations also
include diagnostic and scrape work.

### Proposed acceptance work

1. Run the [deployed monitoring suite](../../../karta-metrics-exporter/test/monitoring/README.md)
   for the same image in its separate isolated cluster. Verify Prometheus
   collection, upgrade, network-policy behavior and exporter removal.
2. Run required presubmit on the actual branch after the final commit. Preserve
   the snapshot result and deployed reports separately.
3. If product source changes, rebuild the image and repeat affected deployed
   checks and matched measurements.
4. If the release promises capacity beyond the measured shapes, define that
   workload mix and event rate, then run it with explicit latency, memory and
   cleanup gates. Do not infer that capacity from the current microbenchmarks.

Closure requires successful deployed reports tied to the final image, with any
failures resolved or explicitly accepted as release limitations. Performance
claims must stay within the scenarios actually measured.

## Review status

Independent code and architecture reviews have no remaining findings. The
passed API and metric-correctness checks are recorded in
[verification](verification.md#automated-checks) and the
[controller audit](controller-audit.md#verification-status). The final architecture
review found no remaining invariant blockers. Review does not explain the
historical heap failure, establish endurance capacity or replace the scoped
monitoring checks.
