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

The final measurements were taken on 2026-10-14 against production commit
`02a613462995f11ac3825c6b3db7a7f51c485e0c` with Go 1.26.3, darwin/arm64,
Apple M4 Pro, 48 GiB memory, and GOMAXPROCS 14. Each scenario ran in a fresh
process. An independent streaming oracle checked every identity, label set, phase value, and workload-health value
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

| Roots | Pods/root | Samples | Text MB | Median scrape s | Allocated MB/scrape | Retained heap MB | Peak RSS MB |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1,000 | 0 | 10,000 | 2.43 | 0.044 | 37.9 | 1.6 | 61.1 |
| 1,000 | 1 | 11,000 | 2.73 | 0.051 | 43.4 | 2.2 | 71.6 |
| 1,000 | 10 | 20,000 | 5.46 | 0.107 | 93.0 | 5.5 | 122.0 |
| 10,000 | 0 | 100,000 | 24.45 | 0.608 | 378.4 | 7.4 | 385.8 |
| 10,000 | 1 | 110,000 | 27.52 | 0.637 | 433.4 | 13.8 | 492.7 |
| 10,000 | 10 | 200,000 | 55.20 | 1.351 | 931.1 | 45.2 | 897.4 |
| 100,000 | 0 | 1,000,000 | 246.48 | 7.469 | 3808.9 | 65.0 | 4223.9 |
| 100,000 | 1 | 1,100,000 | 277.63 | 8.341 | 4357.2 | 126.4 | 4582.9 |
| 100,000 | 10 | 2,000,000 | 558.03 | 18.423 | 9360.0 | 462.6 | 9324.7 |

The [final raw results](../../exporter/test/scale/results/final-20261014-lifecycle/README.md)
contain all three scrape durations, exact bytes, allocation counts, GC cycles,
retained heap, exit statuses, and source/binary hashes. All nine cases include
the final same-snapshot Pod/root confirmation and lifecycle changes. The
123 source/module hashes matched the clean binary-build manifest and were
unchanged before and after measurement. HEAD stayed at the measured commit;
the working tree was dirty from documentation and acceptance edits outside
that manifest. Every scale process ran serially after live acceptance, with
no concurrent build, race suite, image build, or acceptance load. The dedicated
acceptance cluster remained idle; the host was not otherwise empty.

The three-run medians are descriptive, not statistical acceptance thresholds.
The baseline one-million-Pod case overlapped other benchmark work; its timing
must not be used to claim a clean latency improvement. The earlier allocation
experiment reduced allocated bytes by about 25-26%, but the final lifecycle
measurement does not establish a uniform RSS improvement. In particular, the
largest scrape reached 9.325 GB peak RSS versus 7.812 GB in the previous matrix.
Both include fixture and oracle work, and neither includes production informer
caches or concurrent health verification.

The largest case emitted 558,033,869 bytes and 2,000,000 samples. Its three
scrapes took 16.972, 18.423, and 19.427 seconds. The 25-second chart scrape timeout
leaves 5.573 seconds beyond the slowest local sample, before real network
transfer, CPU contention, informer work, and Prometheus ingestion. This local
margin does not establish an adequate production timeout.

## Where allocation goes

Separate before/after allocation profiles cover three complete HTTP scrapes
at 10,000 roots and zero Pods. Fixture construction and the oracle run are
subtracted from these profiles. Sampling makes these approximate shares.
These profiles isolate attribute construction before the final Pod/root
confirmation check; their source hashes are preserved separately.

| Allocation site | Historical baseline share | Historical fixed-array share |
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

The final controller tests populated Job/Pod informer state and served
paginated loopback API responses. Both fresh processes verified all 100,003
health rows: 100,000 workloads and three resource types. Requests used a page
limit of 1,000, QPS 50, burst 100, and the unchanged default 60-second production
verification deadline. The 15-minute outer test timeout did not change that
production deadline.

| Roots | Pods/root | Healthy rows | Job LISTs | Pod LISTs | Karta LISTs | Fallback GETs | Verification s | Deadline headroom s |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 100,000 | 1 | 100,003 | 100 | 200 | 1 | 0 | 8.749 | 51.251 |
| 100,000 | 10 | 100,003 | 100 | 2,000 | 1 | 0 | 47.229 | 12.771 |

The Pod request totals contain one complete metadata pass and one complete
full-object pass: 100 plus 100 pages for 100,000 Pods and 1,000 plus 1,000 pages
for one million Pods. The tests asserted exact identities, values, resource-type
coverage, metadata request counts, and zero unexpected requests. See the
[health summary](../../exporter/test/scale/results/final-20261014-lifecycle/health-summary.json)
and raw logs for actual durations and counts. Initial sandbox attempts failed
to open a loopback listener before constructing fixtures; those startup
failures are retained and are not health-capacity observations.

This is synthetic API verification, not an API-server throughput measurement.
The objects have one container and compact metadata. Large Pod specifications,
annotations, admission changes, and production network latency are not modeled.
The 12.771-second headroom in the largest final pass is one observation, not a
production guarantee or evidence about concurrent scrape and health work.

The initial full-object first-Pod-pass implementation failed the unchanged
60-second budget at 100,000 roots plus one million Pods. It completed 1,092 of
2,101 required LISTs, then reported `api_unavailable`; verification returned
after 62.188 seconds. This historical failure remains in
[the health baseline](../../exporter/test/scale/results/health-baseline-100000-10.json).

The metadata first-pass optimization reads Pod metadata and indexes immutable
snapshot Pod records by pointer. The fixture also encodes typed Pod lists
directly, removing an artificial reflection conversion in the test API. The
previous optimized run passed all 100,003 rows after 2,101 LISTs in 58.568 seconds,
leaving only 1.432 seconds under the unchanged budget; it remains in
[the earlier health result](../../exporter/test/scale/results/health-final-100000-10.json).
Intermediate failures are retained. That fixture correction prevents attributing
the full historical timing change solely to production code. The final lifecycle
run supersedes the previous capacity observation for the current source revision;
no controlled experiment attributes its timing difference to one code change.
When Pods are selected workload roots, the first pass needs full objects and
does not use this metadata optimization.

## Footprint and deployment guidance

- Set `exporter.resources` for the selected scope. The default request of 256 MiB
  and limit of 1 GiB do not cover the measured 100,000-root cases. The final
  10,000-root, 100,000-Pod scrape process used about 0.897 GB peak RSS before production
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

This scale series does not measure production cluster capacity, authorization
failure load at this population, watch ingestion rate, slow-client behavior,
network bandwidth, Prometheus TSDB append throughput, or combined
health-plus-scrape peak. The chart's
100m CPU request is not comparable to this local host's available CPU. These
results support implementation choices and explicit capacity limits, not a
claim that the default deployment supports 100,000 workloads.

Reproduction commands and opt-in controls are in the
[scale harness guide](../../exporter/test/scale/README.md).
