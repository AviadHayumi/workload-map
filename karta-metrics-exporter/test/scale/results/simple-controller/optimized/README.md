<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Optimized independent-controller measurements

The final measured candidate passed all fourteen bounded runs. The many-Pod
root-update allocation increase fell from 1.83x in the initial implementation,
to 1.57x after immutable selection caching, to 1.34x after the four profile-backed
changes. This improves the original finding but does not eliminate it. Sampled
CPU for that shape remained 21% and 38% above baseline. These results do not
establish that the no-serious-performance-regression requirement is met.

The candidate reused immutable structure summaries, skipped empty selector label
copies, avoided a duplicate root path append, and avoided repeated watch setup
when the source was already cached. The prior [profile evidence](../selection-cache/README.md#separate-diagnostic-profiles)
explains why those operations were changed. No further controller changes were
made during this measurement batch.

## Method

Baseline: `ee7d2ac6fdb0c558b3e907e8c6e331a2afd9540e`, built from an archive.
[Manifest](manifest.json) records the frozen candidate source fingerprint, binary
hash, Python harness hash, toolchain, host and exact commands. Both variants used
Go 1.27.3, GOMAXPROCS=4, QPS/burst 20/40, two workers, one namespace, and
Job -> ReplicaSet -> Pod ownership. The baseline alone received its retired
`maxAge: 120s` setting. The same binaries and harness were used for every repeat.

Each shape ran baseline then candidate, twice, serially. Existing kind operators
remained on the host. No heavy parent builds or tests overlapped this batch.
Churn lasted ten seconds at 20 events/s. Diagnostic ACK polling ran every 10ms.
All twelve churn runs acknowledged all 200 events with exact identities, expected
labels, mappings and workload versions. CPU and heap profiling were disabled.

## Results

| Run | Cold s | RSS MiB | ACK p99 ms | CPU s | TotalAlloc MiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| [baseline-10-100-pod-r1](baseline-10-100-pod-r1.json.gz) | 0.242 | 61.7 | 39.3 | 1.06 | 281.5 |
| [baseline-10-100-pod-r2](baseline-10-100-pod-r2.json.gz) | 0.241 | 62.1 | 40.1 | 1.21 | 281.0 |
| [baseline-10-100-root-r1](baseline-10-100-root-r1.json.gz) | 0.238 | 63.6 | 24.8 | 1.92 | 423.0 |
| [baseline-10-100-root-r2](baseline-10-100-root-r2.json.gz) | 0.239 | 61.4 | 22.4 | 1.65 | 417.4 |
| [baseline-1000-1-root-r1](baseline-1000-1-root-r1.json.gz) | 0.414 | 143.1 | 33.4 | 3.73 | 1424.5 |
| [baseline-1000-1-root-r2](baseline-1000-1-root-r2.json.gz) | 0.399 | 129.1 | 37.6 | 3.51 | 1420.8 |
| [candidate-10-100-pod-r1](candidate-10-100-pod-r1.json.gz) | 0.242 | 63.0 | 18.6 | 1.02 | 321.0 |
| [candidate-10-100-pod-r2](candidate-10-100-pod-r2.json.gz) | 0.244 | 63.2 | 18.9 | 1.17 | 330.0 |
| [candidate-10-100-root-r1](candidate-10-100-root-r1.json.gz) | 1.057 | 60.3 | 22.7 | 2.33 | 565.8 |
| [candidate-10-100-root-r2](candidate-10-100-root-r2.json.gz) | 0.241 | 63.0 | 20.1 | 2.27 | 559.0 |
| [candidate-1000-1-root-r1](candidate-1000-1-root-r1.json.gz) | 0.410 | 137.1 | 38.6 | 3.21 | 1454.6 |
| [candidate-1000-1-root-r2](candidate-1000-1-root-r2.json.gz) | 0.405 | 138.9 | 27.1 | 3.09 | 1429.1 |

`root` changes root resource versions with stable status values. `pod` changes
visible Pod labels. The complete run also checks bulk changes, partial deletion,
same-name recreation and deletion of all workloads. CPU is the last external
process sample. TotalAlloc is cumulative at the final forced GC. Both include
diagnostic endpoints and metrics collection. RSS is externally sampled. Cold
convergence and ACK latency include polling and oracle time; they are upper
bounds, not exact controller event timings.

The 10-root/100-Pod root-update allocation ratios were 1.337x and 1.339x.
ACK p99 and RSS remained close to baseline. One candidate cold start took 1.057s;
the second took 0.241s. The outlier is retained, not discarded. The 1000-root/
one-Pod allocation ratios were 1.021x and 1.006x. Pod-label churn allocated
1.141x and 1.175x with similar CPU and lower observed p99. Those other shapes
do not cancel the root-update allocation and CPU finding.

The previous diagnostic profile attributed about 29MiB of extra allocation to
the candidate test wrapper grouping independent Pod rows at each ACK poll.
Controller stacks accounted for about 204MiB of the earlier difference. The
wrapper therefore explained part of that difference. Those profiles predate the
four changes and cannot be subtracted from this batch as an exact correction.
A final-candidate profile was not collected in this window. The remaining 34%
whole-process increase is not an isolated production-controller measurement.

[Machine summary](summary.json) retains queue drain, post-delete heap, API counts,
watch peaks, server-side non-watch duration and stage results. In the second
many-Pod root repeat, candidate made five LIST page requests and no GETs versus
baseline's thirteen LIST pages and five metadata GETs. Candidate used one shared
metadata ReplicaSet watch; no full ReplicaSet watch appeared. This is evidence
for the exercised one-namespace topology only.

Fixture non-watch mean/max handling duration in that repeat was 1.786/6.749ms
candidate and 1.026/7.402ms baseline. The request mix differs. These measurements
include encoding and socket writes, exclude client queueing, and are not
production API-server latency. Post-delete live heap was 4.64MiB candidate and
7.84MiB baseline. All bounded heap gates passed; sustained heap stability was
not measured.

## Lifecycle and failures

The [candidate lifecycle run](candidate-10-10-lifecycle-owner.json.gz) deleted
50 Pods while retaining ten roots, then recreated the same names with new UIDs.
The exact identity oracle passed in 0.220/0.225s. Ten intermediate ReplicaSet
owner UID mismatches retained the last-good mapping with `unresolved_owner`
in 0.224s. Restoring references recovered in 0.229s. This exercises a UID mismatch
and recovery, not successful reparenting to a different existing root.

The earlier [baseline lifecycle run](../selection-cache/baseline-10-10-lifecycle-owner.json.gz)
passed Pod deletion/recreation but failed the new ancestor last-good identity
contract. Its failure classification and owner coordinates differ by design.
That unsuccessful run is not used for a speedup comparison. It was not repeated
here because its contract incompatibility remains unchanged.

The [candidate fault run](candidate-10-1-faults.json.gz) passed Pod EOF,
Job/ReplicaSet 410 recovery and a Pod update during a forced paginated full LIST.
It used two-item pages. Final deletion/recreation checks also passed. These are
modeled API tests, separate from real API-server integration evidence.

Two short repeats do not establish statistical confidence, 10k/100k capacity,
five-minute throughput, or three-minute heap stability. The original 1.83x
finding is reduced, not erased. A broad performance acceptance claim would be
unsupported by these measurements.
