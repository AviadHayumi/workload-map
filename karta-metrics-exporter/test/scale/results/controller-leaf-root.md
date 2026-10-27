<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Controller leaf/root comparison

The 2026-10-27 matched comparison completed all 17 planned runs. Every applicable
absolute gate passed. Three five-minute small-root pairs show similar CPU and
publication latency, with fewer allocated bytes. Retained heap increased in the
zero-Pod and giant-root fixtures. A giant-root deletion also took longer to clear
resource-type health after its workload rows had disappeared. These costs remain
visible below.

This is a local comparison of the production controller, SDK and collector
against a modeled loopback Kubernetes API. It does not establish production
API-server capacity or behavior under indefinite object turnover. The
[machine-readable results](controller-leaf-root.json) contain exact metrics,
all acceptance commands, source manifests, result hashes and diagnostic profile hashes.

## Inputs and conditions

Baseline revision: `cdcc8779dacf688463915500031690d09fef8910`. The candidate was a
modified working tree based on that revision. Its compiled inputs were frozen
before measurement. Both used Go 1.27.3 on darwin/arm64. The harness, diagnostic
command and module manifests were unchanged between variants.

| Input | SHA256 |
| --- | --- |
| Baseline local input manifest | `24197886f52089b4da440459c932f881dd623573fb6bc7f44772aaa4c4fd34af` |
| Candidate local input manifest | `7d4d1373b25b1d9d3328769294f10d29007c013369dd3aa34db58062336ebe0e` |
| Baseline diagnostic binary | `e25ced8bce3e29f94f7c2b17f77f830c7ca8a2c4460158730caeed9d5a7a402d` |
| Candidate diagnostic binary | `d696a407849cb218454a36c9f9c4465538334426038f836e3e3d397cbd8e4cb4` |
| Candidate normal binary | `b43751b0f5f2edcb7ad9832bd2cedc9105f4869cee5316a296c1aefe7d4768af` |
| Python harness | `e68b29b52cf634ddde27d51f1568338f781a476e641152c682b201fc595edd35` |
| Diagnostic command source | `a92fd025ec991b095850a3aaedecf6b2b964c045cdf418f40dfece3d53f6d83c` |

The manifest digest hashes the sorted-key compact JSON mapping of relative file
paths to SHA256 values. It covers local Go inputs, module locks, the harness and
its protocol document. The companion JSON includes every file hash. External
dependencies are pinned by the module locks.

Runs were sequential. Four local kind nodes were stopped throughout measurement
and diagnostics, then restored. Four other idle containers remained running.
No competing build, image rendering or benchmark ran during measurement.

| Setting | Value |
| --- | --- |
| GOMAXPROCS / workers | 4 / 2 |
| API QPS / burst | 20 / 40 |
| Sampled RSS cap / derived GOMEMLIMIT | 8192 MiB / 6144 MiB |
| Source maximum age / refresh | 120 s / 45 s; fault fixture refresh 5 s |
| Overall / stage / oracle guard | 1800 s / 180 s / 60 s |
| HTTP timeout / pure HTTP gate | 60 s / 25 s |
| Convergence poll / final settle | 5 s / 30 s |
| Diagnostic poll | 10 ms; 250 ms for 10,000 roots or 100,000 Pods |
| Publication p99 / queue drain | 5 s / 30 s; giant publication limit 15 s |
| Scheduler lateness | At most 1 s |
| Sustained heap gate | Full final 180 s sampling window; growth at most 5% |
| Post-delete heap gate | Startup heap plus max(256 MiB, 10% of observed peak heap) |

The [protocol](../runtime-scale.md) defines the oracle and gates. All eight churn
runs lasted 300 seconds. Each small-root run emitted and verified 30,000 updates.
Each giant run emitted and verified 15 updates. No sampled steady-state or churn
validity gaps occurred. Cold and fault runs exercised the lifecycle, but do not
claim the sustained churn or heap-growth gates. The normal-command smoke used
10 roots, one Pod per root, a 200 ms poll, no settle and 180 s / 60 s overall and
stage guards. It passed composition and oracle checks with cold convergence
at 1.0642 s. It did not measure normal-command retained heap.

## Matched results

Each cell is baseline / candidate. CPU seconds and allocated bytes cover the
whole process lifecycle, including diagnostics. Allocation is cumulative
`TotalAlloc`; RSS is the sampled peak; retained heap is `HeapAlloc` after GC.
Cold convergence includes polling delay. HTTP time excludes oracle parsing.

| Fixture | Cold s | CPU s | Allocated GiB | Peak RSS MiB | Post-delete heap MiB | Worst HTTP s |
| --- | --- | --- | --- | --- | --- | --- |
| 1,000 roots / 1 Pod | 5.190 / 5.208 | 2.30 / 2.15 | 0.91 / 0.95 | 134.44 / 132.14 | 6.33 / 6.38 | 0.0997 / 0.1032 |
| 10,000 roots / 1 Pod | 13.163 / 13.478 | 24.64 / 25.08 | 9.31 / 9.65 | 992.84 / 1030.14 | 31.39 / 33.56 | 0.9962 / 0.9874 |
| 10,000 roots / 0 Pods | 6.667 / 6.758 | 14.01 / 14.75 | 5.69 / 6.23 | 598.92 / 635.50 | 12.39 / 14.75 | 0.8514 / 0.8833 |
| 1 root / 100,000 Pods | 27.341 / 27.677 | 183.56 / 182.79 | 75.54 / 74.28 | 2941.98 / 3084.09 | 68.29 / 82.06 | 1.1179 / 1.2808 |
| Source faults: 100 / 1 | 5.059 / 5.064 | 0.97 / 0.79 | 0.15 / 0.15 | 48.72 / 49.17 | 3.16 / 3.10 | 0.0200 / 0.0230 |

The fault fixture passed watch EOF, HTTP 410 recovery and an update during a
metadata LIST. Worst HTTP increased 15% in this pair, from 20 ms to 23 ms.
Giant worst HTTP increased 14.6%, from 1.1179 s to 1.2808 s. These are observed
single-run maxima, not repeatable latency estimates. The later short giant
diagnostic pair observed 1.0719 s / 1.0934 s, about a 2% difference.

### Three small-root churn pairs

Each fixture has 1,000 roots and one Pod per root, at 100 updates/s. Order was
baseline then candidate, candidate then baseline, baseline then candidate.
Publication latency is a diagnostic polling upper bound. It is separate from
HTTP scrape time.

| Pair | CPU s | Allocated bytes | Peak RSS bytes | Publication p99 ms | Drain ms | Post-delete heap bytes |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 58.25 / 58.73 | 9139649568 / 8596303808 | 147177472 / 148570112 | 30.911 / 29.419 | 34.166 / 17.848 | 6714776 / 6845024 |
| 2 | 59.83 / 56.69 | 9135367944 / 8426743248 | 144146432 / 153665536 | 29.586 / 30.622 | 17.819 / 14.708 | 6729808 / 6868008 |
| 3 | 57.33 / 58.41 | 9134292296 / 8461917224 | 152616960 / 152879104 | 31.672 / 31.112 | 12.674 / 17.354 | 6723008 / 6775832 |

Changes between the per-variant medians are +0.3% CPU, -0.9% publication p99,
-7.4% allocation and +3.9% peak RSS. These are descriptive changes between
medians, not medians of paired percentage changes or confidence intervals.
Pair 3 drain increased 36.9%, or 4.68 ms; the increase did not repeat in pairs
1 and 2. Candidate heap growth ranged from +0.118% to +0.576%, below the 5% gate.

The giant pair ran at 0.05 updates/s. Publication p99 was 2.503949 s / 2.503121 s
and drain was 0.278079 s / 0.281329 s. With only 15 events per run, p99 equals
the maximum observed publication latency. Heap growth was +0.0027% / -3.6615%.

### API traffic

These are modeled API request counts across the complete lifecycle. LIST counts
include pagination and repeat reads. Diagnostic requests use a separate endpoint
and contribute to process cost, not these API counts. The JSON includes every
resource/method count and response-byte total.

| Fixture | All requests | ReplicaSet metadata GETs | Pod metadata LISTs | Job metadata LISTs |
| --- | --- | --- | --- | --- |
| 1,000 roots | 69 / 43 | 38 / 12 | 5 / 5 | 5 / 5 |
| 10,000 roots | 308 / 300 | 86 / 76 | 61 / 62 | 61 / 62 |
| 10,000 zero-Pod roots | 80 / 80 | 0 / 0 | 3 / 3 | 41 / 41 |
| Small churn pair 1 | 139 / 111 | 38 / 7 | 25 / 27 | 21 / 21 |
| Small churn pair 2 | 139 / 107 | 38 / 3 | 25 / 27 | 21 / 21 |
| Small churn pair 3 | 139 / 118 | 38 / 14 | 25 / 27 | 21 / 21 |
| Giant | 3495 / 3299 | 2 / 2 | 2801 / 2802 | 19 / 22 |
| Source faults | 111 / 77 | 38 / 3 | 16 / 16 | 17 / 18 |

Giant Job metadata GETs were 204 / 0. Request differences can include timer
phase and lifecycle duration; they do not establish a universal API reduction.

## Retained heap and deletion health

The zero-Pod retained-heap increase was 2.36 MiB (+19.0%). Peak RSS increased
6.1%. Giant retained heap increased 13.77 MiB (+20.2%). Both deletion gates
passed. The original giant post-delete GC samples differed in quiescence:
baseline had one queued source item and one active item; candidate queues were
idle. This limits direct attribution of that particular heap difference.

Four additional sequential lifecycle diagnostics enabled heap profiles, without
sustained churn. After deletion, two queue-idle observations 250 ms apart were
followed by GC and another idle check. All four checks passed. The extra
diagnostic requests and profile hashes were recorded separately.

| Diagnostic | Baseline heap bytes | Candidate heap bytes | Difference |
| --- | --- | --- | --- |
| 10,000 zero-Pod roots | 13216184 | 15546264 | +2.22 MiB, +17.6% |
| Giant without sustained churn | 63177696 | 85513624 | +21.30 MiB, +35.4% |

The repeat confirms a retained-memory cost after queue-idle GC. Sampled heap
profiles locate most candidate giant retention at map insertion and queue
storage allocation sites: object/watch indexes (24.13 MiB), queue metrics
`addTimes` (14.69 MiB), the input queue set (10.58 MiB), its slice (10.06 MiB),
failure-reference maps (5.64 MiB) and the Pod-to-root index (3.12 MiB). Source
inspection shows removal of object entries and queue items; queue Pop clears
references but retains backing storage. Zero-Pod profiles also include root
and candidate index map allocations.

This evidence supports retained map and slice capacity as the main source of
these samples. It does not identify every byte of the measured delta or prove
bounded memory under unlimited new identities. The five-minute growth gates
passed; a repeated create/delete endurance workload was not run. Profile
sampling estimates are not an exact heap census and must not be subtracted
from the measured heap totals.

The diagnostic command enables client-go queue metrics. Its `addTimes` maps
are instrumentation overhead and are absent with the default no-op metrics
provider. The full instrumented regression remains reported. The normal-command
smoke has different observability, so these profiles cannot predict its heap.
Offline analysis used profile-embedded symbols. An initial invocation also
passed a binary that the analyzer rejected as a profile. Corrected analysis
completed without that warning. The original output paths were overwritten;
the failed invocation was reproduced and recorded, not represented as the
original output bytes.

### Giant deletion health tail

Total delete-all convergence was 6.6805 s / 51.8762 s. Workload rows disappeared
after about 6.68 s / 6.75 s. Candidate then retained an `unresolved_owner`
resource-type health failure for ReplicaSet until the next owner refresh.

Candidate deletion began at 402.209 s. Workload rows were absent by 408.966 s,
but the health failure remained through 449.066 s and cleared by 454.082 s.
Baseline deletion began at 411.950 s and was empty and healthy by 418.626 s.
Both runs used the 45 s refresh interval. Candidate ReplicaSet LIST count
advanced from 8 to 9 during deletion without adding returned objects; baseline
stayed at 9. This aligns with candidate deletion crossing the approximately
405 s refresh tick and a failure being pruned at the next refresh.

The relevant refresh, failure-pruning and health-publication functions match
baseline. The evidence supports an inherited, timer-phase-dependent health
tail. The exact request timestamp was not instrumented. The longer observed
health convergence remains a limitation; it is not evidence of a deterministic
7.8-fold regression in removing workload rows. Same-name recreation also had
different timer phases: 44.0659 s / 33.1299 s.

## Historical failures and reproduction

Earlier giant runs retained 649147912 / 655640192 bytes and failed their
post-delete heap gates. Those failures remain failures. Neither variant
reproduced them in this matched run, including the fresh baseline. This does
not establish that the refactor fixed the earlier failure.

Known setup differences include source/binary versions, a 250 ms versus 5 s
convergence poll, and zero versus 30 s final settle. The initial deletion GC
occurs before settle, so settle alone cannot explain that initial sample.
The older client budgets used harness defaults; this run supplied 20/40 and
two workers explicitly. The harness hash was unchanged. See the
[verification history](../../../../docs/design/metrics-exporter/verification.md).

To reproduce, build `./test/scale/cmd` from each source set in the exporter
module with the recorded Go toolchain and module locks. Build `./cmd` for the
candidate composition smoke. Use separate baseline and candidate binary paths.
Run the ordered argument arrays in the companion JSON sequentially from the
repository root, replacing only binary and output paths. The giant arguments
explicitly retain 15 s publication, 30 s drain, 8192 MiB RSS, 180 s stage and
60 s oracle limits; the harness derives 6144 MiB GOMEMLIMIT. Keep each raw
result, including failures. Profile diagnostics are supplemental and do not
replace the full 300-second acceptance runs.
