<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Immutable-selection candidate measurements

The immutable-selection candidate reduced the provisional many-Pod allocation
increase from about 1.83x to about 1.57x. The increase remains material. This batch
is retained as an intermediate result while smaller, profile-guided changes are
prepared. It does not establish that the performance requirement is met.

All twelve short churn runs passed exact identity/value checks and acknowledged
200 events over ten seconds. Candidate Pod recreation, ancestor UID mismatch /
recovery, and WATCH EOF/410/relist scenarios also passed. The baseline ancestor
scenario failed the new last-good identity/error contract; that result is a
behavior difference, not a performance comparison or an established regression.

## Matched method

Baseline: `ee7d2ac6fdb0c558b3e907e8c6e331a2afd9540e`, built from an archive.
The candidate was frozen for this batch. [Manifest](manifest.json) records its
source fingerprint, exact binary hash, harness hash, host, toolchain and every
command. All comparisons used Go 1.27.3, GOMAXPROCS=4, API QPS/burst 20/40,
two workers, one namespace, and Job -> ReplicaSet -> Pod ownership.

Each shape ran baseline then candidate, twice. Runs were serial. No heavy builds
or tests ran during measurement. Existing kind operators remained on the host.
Each churn stage ran at 20 events/s for ten seconds. The diagnostic poll interval
was 10ms. Baseline config included its existing maxAge setting; candidate config
omitted retired proof settings. Both used the same Python harness and the same
other settings. Neither run used CPU/heap profiling during these comparisons.

## Results

| Run | Cold s | RSS MiB | ACK p99 ms | CPU s | TotalAlloc MiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| [baseline-10-100-pod-r1](baseline-10-100-pod-r1.json.gz) | 0.235 | 63.1 | 42.5 | 1.37 | 281.0 |
| [baseline-10-100-pod-r2](baseline-10-100-pod-r2.json.gz) | 0.244 | 62.6 | 42.8 | 1.30 | 279.3 |
| [baseline-10-100-root-r1](baseline-10-100-root-r1.json.gz) | 0.244 | 63.1 | 26.7 | 1.82 | 421.7 |
| [baseline-10-100-root-r2](baseline-10-100-root-r2.json.gz) | 0.239 | 62.5 | 23.2 | 1.88 | 423.0 |
| [baseline-1000-1-root-r1](baseline-1000-1-root-r1.json.gz) | 0.395 | 138.7 | 38.9 | 2.86 | 1427.4 |
| [baseline-1000-1-root-r2](baseline-1000-1-root-r2.json.gz) | 0.394 | 128.8 | 37.9 | 2.86 | 1420.5 |
| [candidate-10-100-pod-r1](candidate-10-100-pod-r1.json.gz) | 0.239 | 65.1 | 19.9 | 1.17 | 356.6 |
| [candidate-10-100-pod-r2](candidate-10-100-pod-r2.json.gz) | 0.251 | 64.9 | 19.8 | 1.16 | 354.8 |
| [candidate-10-100-root-r1](candidate-10-100-root-r1.json.gz) | 0.238 | 65.4 | 22.1 | 2.89 | 664.7 |
| [candidate-10-100-root-r2](candidate-10-100-root-r2.json.gz) | 0.243 | 64.4 | 22.2 | 2.70 | 662.9 |
| [candidate-1000-1-root-r1](candidate-1000-1-root-r1.json.gz) | 1.442 | 141.2 | 59.4 | 3.08 | 1503.1 |
| [candidate-1000-1-root-r2](candidate-1000-1-root-r2.json.gz) | 0.406 | 135.6 | 29.6 | 2.99 | 1510.5 |

`root` changes root resource versions with stable status values. `pod` changes
visible Pod labels. ACKs check the current expected value, every identity and
mapping, and the exact workload version. A scrape can observe independent Pod
and workload revisions. Every workload's nine phase rows must remain consistent.
CPU is the last external process sample. TotalAlloc is cumulative at the final
forced GC. Both include the test wrapper, diagnostic polling and metric scrapes.
Cold convergence includes polling and oracle work. ACK times are polling upper
bounds, not exact publication timestamps.

The 10-root/100-Pod root-update allocation ratio remained 1.57x in both repeats.
Its sampled CPU rose from 1.82/1.88s to 2.89/2.70s. ACK p99 remained near 22ms.
Pod-update p99 improved from 42.5/42.8ms to 19.9/19.8ms in these bounded runs.
The latter does not cancel the root-update cost increase.

The candidate used one shared metadata ReplicaSet watch and no full ReplicaSet
watch. The baseline used metadata GET/LIST. For the second many-Pod root-update
repeat, the candidate made five LIST page requests and no GETs. The baseline
made 13 LIST page requests plus five metadata GETs. This is one namespace with
one intermediate kind; it does not prove a general watch-count bound.
[Machine summary](summary.json) contains per-operation counts, server-side
non-watch duration mean/max, queue drain, heap, and stage data. Fixture durations
include encoding/socket writes and are not production API latency.

## Lifecycle and failure scenarios

The candidate deleted and recreated 50 Pods while retaining their ten roots.
Final checks rejected old Pod UIDs and required unchanged root identities.
Deletion/recreation converged in 0.222/0.230s, including polling and oracle time.
Ten ReplicaSet owner UID mismatches became invalid retained mappings with
`unresolved_owner` in 0.007s; restoring references recovered in 0.225s.

The baseline passed Pod deletion/recreation. Its ancestor mismatch stage failed
the 90-second deadline: health used `api_unavailable`, and mapping coordinates
changed. Baseline `pkg/controller/root.go:229` explicitly changes failing Pod
owner resolution to `unknown`. The new oracle requires the preserved last-good
identity and `unresolved_owner`, so this is a contract incompatibility. The failed
run never reached recovery. Its duration/allocations must not be compared with
the successful candidate as a speedup. The failed raw report is retained.

The candidate fault run used two-item pages. Pod EOF, Job/ReplicaSet 410, and a
forced Pod recovery LIST all recovered. A Pod changed during the paginated LIST
and converged to the later watch event. Final deletion/recreation still passed.
These are modeled API tests, not replacement evidence for a real API server.

## Separate diagnostic profiles

Separate baseline/candidate runs used identical CPU instrumentation and heap
sampling. They are not included in the timing comparison. CPU instrumentation
exists only in the test wrapper and is enabled by `SCALE_CPU_PROFILE`. Heap
profiles contain cumulative sampled allocations as well as sampled live heap.
The analyzer is cached `github.com/google/pprof` revision 92041b743c96; no product
dependency was added. Profiles, commands and text outputs are under [profiles](profiles/).

| Exclusive allocation stack bucket | Baseline MiB | Candidate MiB |
| --- | ---: | ---: |
| Controller |244.1|448.4|
| Diagnostic publications endpoint |39.3|68.2|
| Metrics collection |115.8|127.1|
| Diagnostic GC/stats |3.5|3.7|
| Other |14.2|23.7|
| Total sampled allocations |416.9|671.0|

The candidate wrapper groups independent Pod rows at each ACK poll. This adds
about 28.9MiB, roughly 11% of the sampled allocation difference. Controller stacks
add about 204.2MiB, roughly 80%. The observer overhead therefore explains part of
the increase. It does not explain all of it. [Attribution data](profiles/attribution.jsonl)
and [analyzer](profiles/attribute.go.txt) retain the exclusive classification rules.

Candidate `prepareWorkload` accounts for 73.0MiB cumulatively, versus about 1MiB
in the baseline. Its children include `NewStructureSummary` 42.5MiB and
`NewComponentFactoryFromObject` 30.5MiB. The direct `ensureWatch` source listing
shows 25.0MiB, including 24.0MiB in REST mapping. Cumulative values overlap their
callers and must not be added to the exclusive table.

CPU samples totaled 1.58s baseline and 2.39s candidate. Controller-attributed stacks
were 0.51/0.48s, diagnostic publication stacks 0.08/0.10s, and other stacks,
mostly runtime/scheduling/kernel work, 0.88/1.78s. This short CPU sample does not
identify one controller function as the cause of the total CPU difference.

Two short repeats do not establish statistical confidence, ten-thousand/root
capacity, five-minute throughput or three-minute heap stability. No such claim
is made. The remaining measured cost increase must be reassessed after changes.
