<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Metrics exporter scale decision

The implementation keeps all requested workload and Pod identities, uses one
OTel callback, and avoids a persistent cache of per-phase attributes. A local
synthetic matrix verifies the complete output at 100,000 roots, including the
one-million-Pod case. It does not establish production Kubernetes capacity.
The measured 100,000-root cases require substantially more memory than the
chart's default 1 GiB limit.

## Ranked decisions

1. Keep a fixed scratch attribute array and pass `WithAttributeSet`. This removes
   duplicate temporary copies without retaining data between scrapes. It reduces
   allocated bytes by about 25-26% across the 100,000-root scenarios. Cardinality,
   values, labels, and deletion behavior remain unchanged.
2. Permit one production scrape in flight. Overlapping requests receive HTTP 503;
   health and readiness endpoints remain responsive. This bounds overlapping
   exposition work without returning a successful partial observation. It does
   not reduce the memory required by one scrape or by concurrent health checks.
3. Use existing namespace, phase-selection, resource, and scrape settings to fit
   the intended observation scope. Explicitly selecting fewer phases reduces
   generated samples. Replicas of the same complete scope are not shards.
4. Reject the tested persistent status-attribute cache. In the 100,000-root,
   zero-Pod experiment, it reduced allocation from 3.809 to 3.173 GB per scrape,
   but raised retained heap from about 65 to 754 MB and peak RSS from 3.665 to
   4.337 GB. The small timing sample also became slower. The retained-memory
   tradeoff does not address the current capacity constraint.
5. Defer cached serialized exposition and custom Prometheus readers. These could
   avoid repeated descriptor and label construction, but would add invalidation,
   transport, consistency, and lifecycle responsibilities. Measure an isolated
   alternative before changing the OTel-based design.
6. Do not truncate workload identities, silently drop series at the SDK's default
   limit, or round-robin partial populations through one scrape target. Those
   change correctness and downstream staleness behavior.

The source comparisons behind these choices are recorded in
[kube-state-metrics research](metrics-exporter-scale-research/kube-state-metrics.md),
[OpenTelemetry Go research](metrics-exporter-scale-research/opentelemetry-go.md),
and [Prometheus research](metrics-exporter-scale-research/prometheus.md).

## Measured HTTP path

Measurements were taken on 2026-10-13 with Go 1.26.3, darwin/arm64, Apple M4 Pro,
and GOMAXPROCS 14. Each scenario ran in a fresh process. An independent streaming
oracle checked every identity, label set, phase value, and workload-health value
before three timed, uncompressed scrapes. Timed work includes the store snapshot,
OTel callbacks and aggregation, Prometheus gathering, and HTTP text encoding.
The response writer counts and discards bytes; there is no socket or compression.
All cases use nine status phases and one health row per root, plus one mapping
per Pod. These scrape fixtures have no resource-type health rows.
Their short identity strings also keep each text sample compact; longer real
names and component values increase response size.

All MB and GB below are decimal. Peak RSS is the process high-water mark,
including fixture construction and oracle verification. It is not retained heap,
not bytes allocated per scrape, and not a container-memory recommendation.

| Roots | Pods/root | Samples | Text MB | Median scrape s | Allocated MB/scrape | Peak RSS MB |
| --- | --- | --- | --- | --- | --- | --- |
| 1,000 | 0 | 10,000 | 2.43 | 0.044 | 37.9 | 62.3 |
| 1,000 | 1 | 11,000 | 2.73 | 0.050 | 43.4 | 70.6 |
| 1,000 | 10 | 20,000 | 5.46 | 0.107 | 93.1 | 122.7 |
| 10,000 | 0 | 100,000 | 24.45 | 0.605 | 378.4 | 433.0 |
| 10,000 | 1 | 110,000 | 27.52 | 0.683 | 433.5 | 489.3 |
| 10,000 | 10 | 200,000 | 55.20 | 1.326 | 931.1 | 1008.3 |
| 100,000 | 0 | 1,000,000 | 246.48 | 7.641 | 3809.0 | 3752.2 |
| 100,000 | 1 | 1,100,000 | 277.63 | 9.319 | 4357.3 | 4487.7 |
| 100,000 | 10 | 2,000,000 | 558.03 | 21.047 | 9360.0 | 7812.4 |

The [raw results](../../exporter/test/scale/results/README.md) contain all three
scrape durations, exact bytes, allocation counts, GC cycles, retained heap,
and source hashes. This matrix predates the later same-snapshot Pod/root
confirmation check. The three-run medians are descriptive, not statistical
acceptance thresholds. The final matrix was serialized against other heavy
benchmarks. The baseline one-million-Pod case overlapped other benchmark work;
its timing must not be used to claim a clean latency improvement.

At 100,000 roots, the allocation change is consistent but peak RSS is not
uniformly lower:

| Pods/root | Baseline allocated GB | Final allocated GB | Reduction | Baseline peak RSS GB | Final peak RSS GB |
| --- | --- | --- | --- | --- | --- |
| 0 | 5.179 | 3.809 | 26.4% | 3.896 | 3.752 |
| 1 | 5.906 | 4.357 | 26.2% | 4.151 | 4.488 |
| 10 | 12.521 | 9.360 | 25.2% | 9.517 | 7.812 |

The final largest case emitted 558,033,869 bytes and 2,000,000 samples. Its three
scrapes took 18.67, 21.05, and 21.42 seconds. The 25-second chart scrape timeout
leaves less than four seconds beyond the slowest local sample, before real
network transfer, CPU contention, informer work, and Prometheus ingestion.

## Where allocation goes

Separate before/after allocation profiles cover three complete HTTP scrapes
at 10,000 roots and zero Pods. Fixture construction and the oracle run are
subtracted from these profiles. Sampling makes these approximate shares.
These profiles isolate attribute construction before the final Pod/root
confirmation check; their source hashes are preserved separately.

| Allocation site | Baseline share | Final share |
| --- | --- | --- |
| Collector temporary attribute slice | 14.20% | Removed by fixed scratch array |
| `metric.WithAttributes` temporary copy | 13.99% | Removed by `WithAttributeSet` |
| OTel immutable attribute representation | 13.78% | 18.59% |
| Prometheus label pairs | 19.29% | 28.90% |
| Prometheus descriptor construction | 15.90% | 22.10% |
| Prometheus attribute-to-label conversion | 6.43% | 7.25% |
| Store snapshot | 0.87% | 1.10% |

Remaining allocation is dominated by Prometheus conversion and immutable
attribute representation. Snapshot copies are measurable but are not the main
allocation source. The snapshot-only one-iteration benchmarks at 100,000 roots
copied about 42.4 MB with no Pods, 66.4 MB with 100,000 Pods, and 282.4 MB with
one million Pods. These are separate microbenchmarks, not additional scrape
allocations to add to the table above.

The short baseline CPU profile also showed substantial runtime GC and scheduling
cost, with `gcDrain` at roughly 21% cumulative samples. It is diagnostic evidence,
not a portable CPU budget. Raw profile-derived shares and source hashes are in
[profile-summary.json](../../exporter/test/scale/results/profile-summary.json).

## Correctness and fresh verification

Every final matrix case passed the independent exposition oracle. The 100,000-root,
100,000-Pod churn test then deleted 10% of roots and replaced another 10% under
unchanged names with new UIDs. Output changed from 900,000 status, 100,000 health,
and 100,000 Pod samples to 810,000, 90,000, and 90,000 respectively. No old identity,
overflow row, duplicate, missing phase, or unexpected metric family was accepted.
Small concurrent HTTP and store tests passed the race detector. The HTTP
concurrency test intentionally bypasses production admission to exercise the SDK
and store concurrently.

A separate controller test populated Job/Pod informer state and served
paginated loopback API responses. At 100,000 Jobs plus 100,000 Pods, verification
completed in 13.226 seconds with all 100,003 health rows healthy. It issued exactly
301 LIST requests: 100 Job pages, 100 metadata and 100 full Pod pages, and one Karta
collection. It made no fallback GETs. Requests used a page limit of 1,000, QPS 50,
burst 100, and the unchanged default 60-second verification deadline. See
[the health result](../../exporter/test/scale/results/health-final-100000-1.json).
This is synthetic API verification, not an API-server throughput measurement.
The objects have one container and compact metadata. Large Pod specifications,
annotations, admission changes, and production network latency are not modeled.

The initial full-object first-Pod-pass implementation failed the unchanged
60-second budget at 100,000 roots plus one million Pods. It completed 1,092 of
2,101 required LISTs, then reported `api_unavailable`; verification returned
after 62.188 seconds. This establishes a fresh-coverage boundary despite the
complete HTTP exposition. The failure is preserved in
[the health baseline](../../exporter/test/scale/results/health-baseline-100000-10.json).

The measured optimization reads Pod metadata in the first pass and indexes
immutable snapshot Pod records by pointer. The fixture also now encodes typed
Pod lists directly, removing an artificial reflection conversion in the test API.
At 100,000 roots plus one million Pods, all 100,003 health rows passed after
2,101 LISTs: 100 Job pages, 1,000 metadata Pod pages, 1,000 full Pod pages, and
one Karta collection. There were zero fallback GETs. Verification took 58.568
seconds, leaving only 1.432 seconds under the unchanged 60-second budget. See
[the measured health result](../../exporter/test/scale/results/health-final-100000-10.json).
This narrow margin does not establish adequate production headroom. Intermediate
failures are retained, and the fixture correction prevents attributing the full
timing change solely to production code. When Pods are selected workload roots,
the first pass needs full objects and does not use this metadata optimization.
These health measurements also precede the later isolation of per-object cache
mismatches; the JSON files identify the exact measured source hashes.

## Footprint and deployment guidance

- Set `exporter.resources` for the selected scope. The default request of 256 MiB
  and limit of 1 GiB do not cover the measured 100,000-root cases. Even the
  10,000-root, 100,000-Pod scrape process approached 1 GiB before production
  informer caches and verification state are included. Size from an integrated
  run with those components and headroom, not from retained heap alone.
- Set `exporter.serviceMonitor.interval` and `scrapeTimeout` together. Timeout
  must not exceed interval. For the largest case, increase both from the 30s/25s
  defaults before testing real transport, and verify freshness requirements.
  A longer interval reduces repeated work; it does not shrink one response.
- Use `exporter.namespace` when one namespace is the intended scope. Use separate,
  complete namespace targets to partition a larger observation deliberately.
  Increasing `replicaCount` with identical scope duplicates watch and scrape work.
- Set `exporter.metrics.workloadPhases` only according to required status queries.
  With 100,000 roots and 100,000 Pods, selecting one phase would produce 300,000
  samples instead of 1,100,000 by arithmetic. That alternative was not benchmarked.
- The binary's `--health-check-interval` defaults to 30 seconds between completed
  verification passes. `--health-max-age` defaults to two minutes, must exceed
  twice that interval, and supplies a half-max-age pass deadline. These CLI flags
  are not currently exposed as chart values. Do not extend freshness tolerances
  merely to turn an incomplete verification green.
- Budget downstream Prometheus as well. It must receive and parse the full
  decompressed response. Sample/body limits reject a scrape; they are not a safe
  way to export only part of the workload population.

No production cluster, real authorization failure load, watch ingestion rate,
slow-client behavior at this population, network bandwidth, Prometheus TSDB
append throughput, or combined health-plus-scrape peak was measured. The chart's
100m CPU request is not comparable to this local host's available CPU. These
results support implementation choices and explicit capacity limits, not a
claim that the default deployment supports 100,000 workloads.

Reproduction commands and opt-in controls are in the
[scale harness guide](../../exporter/test/scale/README.md).
