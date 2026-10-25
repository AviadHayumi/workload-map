<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Connected runtime measurements, 2026-10-20

The assembled exporter passed the 100,000-root case without Pods. With one Pod
per root, cold start and steady output passed, but a Pod update failed the
180-second convergence deadline. These measurements do not establish capacity
for 100,000 workloads with Pods.

## Measured executable and model

The two `100000-*.json` files use clean source commit
`3aa0289dd4e4bdaab2257c409999287b8f9c0622`. The executable SHA-256 is
`4dc0ba6db6d9988bd1e852618fd43f26323cc5c3955472487b9aa94bfd399d4d`.
The build manifest SHA-256 is
`1bd4943110ed1e3da82b6da898fa962a787f9f63c4e10e0dbc507b1868f407be`.
It was built with Go 1.26.3 for darwin/arm64, without a Go overlay.

The executable runs its production informers, workqueue, Discovery, inventory,
SDK evaluation, publication and OTel Prometheus handler. A separate Python
process serves a modeled Kubernetes API on loopback. There is no etcd,
authentication, admission, scheduler or real cluster in this measurement.
See the [reproduction guide](https://github.com/AviadHayumi/workload-map/blob/01352db5c4be54b0b3eb6d53a0a04fb5faa34520/exporter/test/scale/runtime-scale.md) for fixture behavior.

Every Job and Pod has the synthetic submitter `scale-user@example.test`.
The explicit CEL configuration reads `karta.run.ai/submitted-by`. Every expected
status and Pod row must include that identity. The oracle also checks every UID,
all nine phase values, creation timestamps, exact labels, component `job`, empty
instance and replica group, resource health and deletion cleanup.

The host has 48 GiB physical memory and runs macOS 26.7.1 on arm64. Python is
3.14.4. Exporter processes use GOMAXPROCS=4 and GOMEMLIMIT=6GiB. Cases run serially,
but the development host is shared with other build and test work. This is not
an isolated host benchmark.

## Results

| Roots | Pods per root | Outcome | Cold start seconds | Steady scrape seconds | Peak sampled RSS GiB | Total seconds | API requests |
| ---: | ---: | --- | ---: | --- | ---: | ---: | ---: |
| 100,000 | 0 | All stages passed | 63.13 | 21.96, 20.34 | 6.083 | 248.20 | 1,934 |
| 100,000 | 1 | Pod-update deadline failed | 115.62 | 38.26, 38.37 | 7.532 | 410.97 | 4,245 |

Both cases keep an 8 GiB sampled RSS limit, a 180-second stage deadline and a
60-second HTTP timeout. The zero-Pod case has a 480-second total limit; the
one-Pod case has a 600-second total limit. Neither run hit its RSS or total-time
limit. RSS is sampled once per second, so this is not a hard OS memory limit.

Scrape time includes handler work, loopback transfer and the Python streaming
oracle. Convergence includes retries and polling. It is not callback-only time.
At 100,000 roots and one Pod per root, a complete response is about 333 MB before
compression. Its two steady scrapes took 24.97 and 24.20 seconds to receive
response headers, and 38.26 and 38.37 seconds to finish validation. These timings
do not separate publication, OTel aggregation and response encoding.

The zero-Pod case converged after 100 status changes in 48.82 seconds. Deleting
10,000 roots converged in 44.38 seconds. The final scrape after a 30-second wait
passed in 19.34 seconds, with exactly 90,000 creation and 810,000 phase rows.
Its final pre-scrape Go HeapAlloc metric was 1,727,288,432 bytes and process RSS
was 6,108,135,424 bytes. These are process measurements, not an isolated measure
of retained controller state. OTel exposition and Go heap reservations affect
whole-process memory.

The one-Pod case changed only the last Pod's resourceVersion in the first root.
The harness waited for WATCH delivery and completion of a metadata LIST at the
new version. Its first four subsequent scrapes failed. Observed failures
included empty submitter identities and expired observations; one scrape had
1,000,000 invalid status and Pod rows and no creation rows. The fifth scrape
was exact, but completed about 203 seconds into the stage. The strict result
remains a failure. Status churn, deletion and settled cleanup were not reached.
The API barrier is not a measurement of exact callback completion latency.

No continuation expired in either final case. The maximum page size was 500.
The one-Pod run served 2,000 Pod metadata pages containing 1,000,000 rows and
1,821 Job metadata pages containing 910,500 rows. Each resource opened two
WATCH streams because the fixture closes healthy watches after 240 seconds.

## Smaller comparison and investigation history

`10000-10-comparison.json` is a corrected-fixture comparison on the earlier
WATCH-fixed binary, SHA-256
`f2ffe592bc5607f2fb09280148e1e5b5b6e29ad79be2a3e4264e3876f669e65f`.
It passed every stage with 10,000 roots and 100,000 Pods in 122.51 seconds,
with peak sampled RSS 4.407 GiB. Cold start took 23.43 seconds; steady scrapes
were 3.64 and 3.72 seconds. Status churn and deletion took 21.74 and 21.54 seconds.
It used 1,710 API requests and one WATCH per resource. Its source was an earlier
frozen overlay snapshot, so it is not a clean-build result for the commit above.

Earlier diagnostic runs exposed a real exporter issue: a global 30-second REST
client timeout terminated healthy WATCH streams and invalidated observations.
The controller owner removed that timeout from streaming clients and retained
bounded LIST requests. The comparison and assembled binaries contain that fix.

A separate fixture defect expired a continuation on every unrelated write.
The corrected fixture retains bounded collection snapshots across updates and
deletions. Earlier churn results with the old continuation behavior are not used
as final evidence here. Unit tests cover stable continuations, explicit expiry,
collection binding, the snapshot bound, WatchList fallback, WATCH events and
oracle rejection of missing, duplicate, obsolete or invalid metrics.

## Coverage still open

The 100,000-root, ten-Pod case and the one-root, 100,000-Pod case have not yet
been measured on this assembled executable. Further heavy runs are held while
the controller owner profiles the one-Pod freshness delay. No memory reduction
is proposed from RSS or HeapAlloc alone. Workqueue age and callback backlog are
not exposed by this executable and are not measured by these artifacts.

The seven Python fixture/oracle tests and
`TestRuntimeFixtureCatalogExpectations` passed. Those tests validate the harness
contract; they do not turn the failed capacity case into a pass.
