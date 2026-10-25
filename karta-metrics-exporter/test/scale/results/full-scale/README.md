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
