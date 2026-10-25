<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Connected runtime after continuity fixes

The corrected executable passed 100,000 Jobs with one Pod each and one Job with
100,000 Pods. The 100,000-Job, million-Pod case exceeded the 8 GiB sampled RSS
limit during cold start. That case did not reach complete output, so these
results do not establish million-Pod capacity.

These are real exporter executions against a modeled loopback Kubernetes API.
They exercise the production informers, workqueue, Discovery, inventory, SDK,
publication and OTel Prometheus handler. They are not real-cluster capacity
measurements. The fixtures use direct Job-to-Pod ownership; intermediate owner
chains are outside this test. See the [harness guide](https://github.com/AviadHayumi/workload-map/blob/01352db5c4be54b0b3eb6d53a0a04fb5faa34520/exporter/test/scale/runtime-scale.md)
for the oracle and API model, and the [earlier measurements](../runtime-20261020/README.md)
for the initial deadline failure and diagnostic profile.

## Pinned inputs

- Clean source: `63d74fc975b9be228ed2d43fbf488a982708ed0a`.
- Executable SHA-256: `b4cc31cca216b9b8fe2fa99a2a71e51c3e5f94a89837292876a9a9a7d80e404b`.
- Build manifest SHA-256: `e79893e20111713f09044c6aabcf71f2cde10d223c499f0a825cf60dc1d3a7ea`.
- Harness SHA-256: `08d1ffe0959eacf3f46ec25b13064d8ec66f6673d9ccde73559a0ff481788936`.

No Go overlay was used. The host is macOS 26.7.1 on arm64 with 48 GiB physical
memory, Go 1.26.3 and Python 3.14.4. Each exporter uses GOMAXPROCS=4 and
GOMEMLIMIT=6GiB. Cases ran serially on a shared development host.

All three cases use an 8 GiB sampled RSS limit, a 180-second stage deadline,
a 60-second HTTP/socket timeout, 500-object pages and five-second polling.
The one-Pod case has a 600-second total limit; the other two use 480 seconds.
The RSS monitor samples once per second, so it can overshoot before stopping
the process. The HTTP timeout is not a separate deadline for the whole streaming
oracle; the stage deadline is checked after each scrape. The unchanged oracle
requires all nine phases, every expected UID and timestamp, exact metric labels,
resource health, and verified `scale-user@example.test` identity supplied through
an explicit CEL configuration.

## Results

| Roots | Pods per root | Outcome | Cold start seconds | Steady scrape seconds | Peak sampled RSS GiB | Total seconds | API requests |
| ---: | ---: | --- | ---: | --- | ---: | ---: | ---: |
| 100,000 | 1 | All stages passed | 127.02 | 38.01, 37.08 | 7.072 | 559.84 | 5,335 |
| 100,000 | 10 | RSS limit during cold start | Incomplete | Not reached | 8.193 | 57.72 | 1,989 |
| 1 | 100,000 | All stages passed | 27.56 | 2.00, 2.16 | 4.848 | 94.95 | 1,045 |

Both passing cases had zero steady validity gaps. A pass requires every stage;
eventual recovery after a deadline does not count. The zero-Pod case was measured
on the earlier clean `3aa0289d` executable and passed there. It was not repeated
on this source revision.

| Stage | 100,000 roots, one Pod each | One root, 100,000 Pods |
| --- | ---: | ---: |
| Last-Pod update, after API progress barrier | 76.27 s | 9.40 s |
| Last-Pod update, including API progress barrier | 81.37 s | 11.69 s |
| Status and Pod churn | 120.27 s | 12.60 s |
| Deletion cleanup | 101.54 s | 8.60 s |
| Final scrape after 30-second wait | 24.27 s | 0.005 s |

The isolated Pod update changes only resourceVersion, so its public values stay
unchanged. Its API barrier confirms WATCH delivery and a complete metadata LIST;
it does not measure exact event-to-publication latency. Status changes and
deletions do change the oracle's expected output. The first case changes 100
roots and deletes 10,000 roots with their Pods. The huge-root case changes its
sole root and deletes it with all 100,000 Pods.

The fixed one-Pod run passed the stage that previously exceeded 180 seconds.
Its first update scrape was missing one Pod; the second was exact. Neither
update scrape showed expired observations or lost identity. Status churn still
needed three scrapes, with affected rows temporarily absent. Deletion also needed
three scrapes to remove obsolete identities and restore complete-membership
identity. These are convergence observations, not a claim that updates are
visible immediately.

The million-Pod run was stopped at the 52.61-second RSS sample, which reached
8,796,864,512 bytes. The harness finished recording its failed result at 57.72
seconds. At termination, the fixture had served 777,000 full Pod objects and
100,000 full Jobs. That does not establish how many were retained by the client.
No Pod WATCH or complete metric oracle was reached. No cleanup or steady-state
capacity conclusion can be drawn from that case.

## API and memory interpretation

No LIST continuation expired in these runs. The one-Pod case served 2,540 Pod
metadata pages containing 1,270,000 rows and 2,361 Job metadata pages containing
1,180,500 rows. Its bounded fixture history caused one WATCH expiry, which
recovered. The huge-root deletion exceeded the 20,000-event history and caused
four WATCH expirations. Informer relists recovered and the final output contained
no workload or Pod metrics. These are modeled API history events, not external
cluster failures.

Scrape timings include HTTP work, loopback transfer and the Python oracle.
The one-Pod steady response is about 333 MB uncompressed. Whole-process RSS
includes informer state, controller records, OTel aggregation, response encoding
and Go heap reservations. It is not a retained-controller-heap measurement.

After cleanup, the one-Pod run's final pre-scrape Go HeapAlloc was
3,264,327,272 bytes and process RSS was 6,896,975,872 bytes. After deleting the
huge root, HeapAlloc was 378,699,248 bytes while RSS remained 5,024,186,368 bytes.
The difference illustrates why RSS alone cannot establish a live-object leak.
No retained-memory reduction is proposed without a heap profile of the actual
runtime. The earlier CPU/mutex profile diagnoses the freshness failure, not the
million-Pod memory limit.

The seven fixture/oracle unit tests and the independent catalog/SDK expectation
test passed. The raw JSON files preserve successful and failed scrapes, request
counts, process metrics, sampled RSS and exact execution bounds. These tests and
measurements do not replace real-cluster integration or broader owner-chain
correctness tests.
