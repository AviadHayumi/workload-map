<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Full-scale candidate measurements

## 100k roots with no Pods: churn failure

`8e51df56-100000-0.json.gz` uses clean candidate source
`8e51df5653485874d926f32cfb5be3dae82b9cbb`, binary SHA-256
`e4b38b91ff10c13b1e30bb7ae29865be0df9440acc9baebaa721fe80027e2911`.
It is candidate evidence, not final correctness approval. Subsequent independent
review found alias-proof ordering issues outside this single-version fixture.

The [host context](host-context.json) records 48 GiB RAM, zero swap, stopped kind
nodes and 47.07 GB free disk. No other benchmark, test, build or Docker operation
ran during the measurement. Settings were GOMAXPROCS=4, QPS 20, burst 40, two
workers, 45-second refresh, 135 seconds quiet, and a requested 300-second churn
at 100 events/s. Diagnostic polling was 250 ms. The predeclared scheduler
lateness limit was one second. RSS was capped at 8 GiB and cold start at 180 s.
With no Pods, this shape tests zero-Pod root coverage, not intermediate traversal.

Cold convergence passed in 47.434 seconds: 100,000 creation rows and 900,000
phase rows were present and valid. Two steady scrapes and all six quiet scrapes
passed. Pure HTTP transfers in those completed checks were 10.16-13.09 seconds;
the independent oracle took about 8.1-8.6 seconds. Peak sampled RSS was
6,696,304,640 bytes, below the cap.

Churn failed after 638 emitted and 538 verified events. All 100 changing roots
were pending, so the emitter's next scheduled root remained blocked. It exceeded
the unchanged one-second lateness limit. Before blocking, emitted events tracked
100.001 events/s with at most 3.2 ms lateness. The raw maximum-lateness field
covers emitted events only; the stalled tick is represented by `scheduler_error`.
The exact count, scheduler and full five-minute goals were not met. Final heap,
delete-all and later lifecycle checks were not reached. No 100k/1 run followed.

### Retained sequence

Times below are seconds from process launch, using the report's monotonic clock.

| Time | Observation |
| --- | --- |
| 267.95 | Object/root queues empty; TotalAlloc 161.60 GB; GC count 139 |
| 268.98 | Object/root depths 49,792 / 85,881 |
| 273.16 | Root depth 23,501; TotalAlloc 173.07 GB; GC count 150 |
| 273.624 | Churn's first event emitted |
| 274.17-278.22 | Both queues empty; events continued at scheduled pace |
| 278.628 | First churn metric HTTP transfer began |
| 279.23 | Object/root depths 56,811 / 56,814 |
| 279.994 | Last emitted event; 638 total |
| 280.24 | Object/root depths 44,601 / 83,761; root queue latency maximum 4.97 s |
| About 281 | Emitter stopped for pending-root backpressure |
| About 289.79-298.00 | First churn scrape's Python oracle ran after HTTP transfer |

At the frozen source, the periodic ticker schedules all roots before stream
verification. The metadata maintenance path also schedules all roots, even with
no retained intermediate metadata. `verifyWatch` then schedules root refresh and
object resolution for unchanged UID/resourceVersion entries. Root reconciliation
reevaluates status and labels. These overlapping population-wide waves provide
a source-supported explanation for the measured backlog and allocation bursts.
For example, TotalAlloc increased by 13.2 GB and GC count by 13 during the
267.95-274.17-second wave, before sustained churn had meaningfully progressed.

The entire run's diagnostic HTTP calls totaled 3.016 seconds; this does not
isolate server CPU or allocation cost. The first Python churn oracle began after
the emitter had already failed, so its parsing did not trigger the initial
backpressure. Concurrent Go metric collection could amplify CPU/GC contention;
no exclusive profile was captured. The evidence supports reducing duplicate
background fanout before changing request budgets, workload shape or gates.

## Scheduled-renewal repeat

`b6c53803-100000-0.json.gz` records clean source
`b6c53803d634a4df4dc09f453f22fd186f588784`. The immutable wrapper SHA256 is
`7266f38e75000bf6a562224d96b1d791cb71ccd1c4d66a0c5c99f32421e23bdd`.
`b6c53803-host-context.json` records all four kind nodes stopped, 48 GiB RAM,
14 logical CPUs and no swap. Desktop, security and indexer background CPU was
present. No concurrent benchmark, build or test ran during this measurement.
Test-only source edits after binary capture do not change its recorded source.

The same 20 QPS, burst 40, two workers, 45-second refresh and 120-second maxAge
were used. All acceptance gates stayed unchanged. Cold convergence passed in
45.31 seconds. Both steady checks, six quiet checks and completed churn checks
retained exact 100,000 creation and 900,000 phase rows. Peak sampled RSS was
6,149,095,424 bytes. HTTP and oracle timing gates passed.

The run failed the one-second scheduler-lateness gate after 212.37 seconds of
churn: 21,198 events emitted and 21,123 verified, short of 30,000. Actual emitted
throughput was 99.81 events/s. The last emitted tick was 0.399 seconds late;
the subsequent stalled tick is represented by `scheduler_error`. The full
five-minute, final latency, drain, heap and deletion gates remain unverified.
This is a retained failure, not a passing capacity result. No one-Pod run followed.

The final samples show a different backlog from the earlier candidate:

| Elapsed seconds | Root queue | Object queue | Raw root retry counter |
| --- | ---: | ---: | ---: |
| 480.871 | 0 | 0 | 1,229,220 |
| 481.880 | 0 | 0 | 1,229,321 |
| 482.892 | 98,865 | 0 | 1,230,297 |
| 483.902 | 73,721 | 0 | 1,249,280 |

The overlapping continuity HTTP request began at 481.891 seconds and took
12.012 seconds. Its offline Python oracle began after the emission failure,
so it did not initiate that stall. A scheduled explicit GC occurred near
481.956 seconds. The API counters also show repeated Namespace/Pod WATCH
connections around the fixture's 240-second lifetime. These are possible
contributors, not an isolated causal attribution. Diagnostics consumed 19.24
seconds of cumulative HTTP time across 1,272 requests; their exclusive CPU cost
is not separated from runtime work. The retained queue, GC, API and event
samples support further diagnosis without changing the gates.

A separate small fixture reproduction distinguished EOF from expiration. The
fixture retains a global 20,000-event deque across resource kinds. Idle active
streams advance over unrelated events internally, but emit no bookmarks. Their
clients can therefore reconnect after the 240-second normal EOF with an old RV.
Job-only churn can then cause a Pod or Namespace reconnect to receive 410.
With a two-event deque, three Job-only updates reproduced a Pod RV1 expiration;
a fresh empty Pod LIST returned RV4, and a subsequent ordinary EOF returned no
ERROR event. `watch_expirations` counts the 410, not normal EOF. This global
retention model is not a claim about Kubernetes per-resource watch-cache
retention. The recorded failure includes this supported source-recovery event;
no gate or model behavior was changed after the result.

The raw retry counter above includes healthy delayed renewals. The pinned
client-go v0.37.1 `delaying_queue.go` calls `metrics.retry()` for every
`AddAfter`, and `metrics.go` increments `NewRetriesMetric`. The wrapper forwards
that counter unchanged. `AddRateLimited` also delegates to `AddAfter`. Therefore
1.2 million retries are not evidence of 1.2 million failed reconciles. The
recorded ready-queue depth surge is separate evidence; raw counters are retained.
The small stale-reconnect reproduction returns HTTP 200 followed by an in-stream
ERROR 410. It does not establish an HTTP 410 response while opening the stream.

## Reconnect-fix repeat

`17be01f1-100000-0.json.gz` and its matching compressed log record clean source
`17be01f1c344d2dca885db9355d45f8a51fc2f15`. Binary SHA256 is
`973d47851f09c24c61bbddb773e35aa577112e90eeb9345d1c4bcd93aa1ee687`.
The matching host-context file records all four kind nodes stopped and no swap.
No acceptance gate, API budget or modeled expiration behavior changed.

This run completed every lifecycle stage in 771.618 seconds but failed overall.
Cold convergence took 45.4056 seconds and peak RSS was 6,368,772,096 bytes.
All 30,000 events were emitted and verified over the full 300-second window.
Actual emission throughput was 99.99996 events/s. Verified publication p99 was
0.7902 seconds, maximum 1.3646 seconds, scheduler lateness at most 0.4883 seconds,
and queue drain 12.757 seconds. HTTP/oracle timing and deletion recovery passed.
These measurements do not override the two failures below.

One churn continuity scrape, process time 378.451-399.695 seconds, returned:

```text
karta_exporter_observation_valid{namespace="scale",reason="unresolved_owner",resource_group="batch",resource_kind="Job",resource_version="v1",scope="workload",workload_name="job-78",workload_uid="job-78-g0"} 0
```

Root 78 was intentionally changing. The oracle permits temporary invalidity
with `api_unavailable`, but rejected this `unresolved_owner` reason. This was
workload health, not resource-type health. A later scrape recovered; the one
continuity gap remains a failed correctness result.

The forced-GC heap-growth gate also failed: 861,982,360 to 2,297,551,072 bytes,
a 166.54% increase. Sample timing matters for diagnosis:

| Churn seconds | HeapAlloc bytes | Concurrent full metrics HTTP |
| --- | ---: | --- |
| 120.1753 | 861982360 | No |
| 150.1483 | 787472752 | No |
| 180.2142 | 772033664 | No |
| 210.2312 | 841708584 | No |
| 240.1201 | 1802442008 | Yes |
| 270.0247 | 3511967464 | Yes |
| 300.9061 | 2297551072 | Yes |

All sampled ready queues were empty. The last GC overlaps process time
562.264-573.487 seconds of metrics HTTP work; the first is outside metrics HTTP.
`/gc` calls `runtime.GC()` then reads `MemStats.HeapAlloc`. It does not expose
HeapInuse or a normalized HeapLive metric. GC/ACK requests are sequential within
the churn thread, but the independent metrics scrape and monitor can overlap.
The diagnostic lock protects traffic counters only. Reachable collection
allocations can therefore affect these samples. This is a comparability issue
to investigate, not proof of a persistent cache leak or permission to waive the
gate. The result remains failed. Post-delete forced-GC HeapAlloc was 92,887,032
bytes and passed recovery. No one-Pod measurement followed.

## Protocol 2 repeat

`941b33a6-100000-0.json.gz` and its compressed log record clean source
`941b33a6a2d4076782161b463a523abac253ec0a`, including producer fix `4153bed4`
and measurement protocol 2. Binary SHA256 is
`192c7d5bc8bd6aedeebcd94fc99aa04ca91f129373d37c1ed790e215c2966426`.
Host context confirms all four kind nodes stopped and zero swap. All workload,
API budgets and thresholds remain unchanged; sampling/drain protocol differences
are declared in the harness guide. The earlier failures are not reclassified.

Correctness passed with zero continuity gaps and all lifecycle stages completed
in 789.668 seconds. Full acceptance failed the unchanged 5% heap-growth gate,
despite exit status zero and `complete: true`. Every other measured gate passed:

| Measurement | Result |
| --- | --- |
| Cold convergence | 45.7887 s |
| Peak sampled RSS | 6,357,401,600 bytes |
| Emitted / verified events | 30,000 / 30,000 |
| Actual emission rate | 99.99908/s |
| Publication p99 / max upper bound | 0.70915 / 1.10928 s |
| Maximum scheduling lateness | 0.68501 s |
| Queue drain after ACK/window completion | 1.42609 s |
| Actual GC sampling coverage | 208.11872 s |
| Post-delete HeapAlloc | 102,328,936 bytes |

All HTTP and oracle timing gates passed. The post-GC samples were outside
client metrics HTTP requests, as required by protocol 2:

| Actual completion, churn seconds | HeapAlloc bytes |
| --- | ---: |
| 92.2527 | 777671720 |
| 120.3300 | 858908088 |
| 150.3724 | 789287328 |
| 180.2403 | 843237024 |
| 221.2986 | 856618704 |
| 247.3161 | 861317960 |
| 273.9211 | 778604440 |
| 300.3714 | 856388584 |

The first-to-last ratio is +10.122%, above 5%. The sequence oscillates and
returns near baseline at 273.921 seconds; this alone does not identify the
retained allocations or establish a leak. Runtime work and diagnostics can
still overlap these GC requests. No alternative baseline or threshold was
substituted. Full acceptance remains failed, and the one-Pod run was not started.
