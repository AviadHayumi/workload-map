<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Independent-controller comparison

The [optimized comparison](optimized/README.md) is the latest measured candidate.
The many-Pod root-update allocation ratio fell from 1.83x here, through 1.57x in
the [immutable-selection comparison](selection-cache/README.md), to 1.34x. The
remaining increase is explicit. All three batches and the intermediate diagnostic
profiles remain evidence.

These are provisional bounded runs of the initial working-tree candidate against
baseline `ee7d2ac6fdb0c558b3e907e8c6e331a2afd9540e`. All eight runs passed final
identity/value checks, deletion/recreation and 200 verified status updates over
10 seconds. They do not establish 10k/100k capacity, five-minute churn acceptance,
three-minute heap stability, or the final candidate after integration fixes.

The 10-root/100-Pod shape allocated about 1.83 times as much with the candidate.
Its sampled CPU time also increased. Latency and RSS remained close at this rate.
These observations include diagnostics and metrics collection. They do not yet
attribute the extra work to a particular controller operation.

## Method

Each run used a fresh process, Go 1.27.3, GOMAXPROCS=4, API QPS/burst 20/40,
workers=2, one namespace, and Job -> ReplicaSet -> Pod ownership. Each repeat ran
baseline then candidate, serially. The 10-second status-churn stage emitted
20 events/s with a 10ms diagnostic polling interval. The complete lifecycle also
performed Pod label updates, bulk root/Pod changes, partial deletion, same-name
recreation and deletion of all workloads. Both binaries used the same Python
fixture/oracle hash recorded in their reports. Only the baseline received the
retired `maxAge: 120s` setting.

The baseline was built from a `git archive` in a temporary directory. The candidate
was built from the in-progress checkout. Binary SHA256 and Go build information
are in every raw report. [Host context](host-context.json) records toolchain,
hardware and a source-file fingerprint taken at the start of the batch. That
fingerprint is supporting context, not a guarantee that concurrently edited files
exactly match the earlier binary build. Final-source verification remains needed.

An existing kind operator cluster remained running. Fixture unit tests overlapped
the beginning of the first baseline run; repeat 2 had no such overlap. Background
host activity was not isolated. Two repeats are insufficient for statistical
confidence. Pure HTTP scrape time excludes the independent Python oracle. ACK
latency includes diagnostic transport and polling. Sampled RSS and CPU are not
precise event timings or isolated retained store memory.

## Observations

| Run | Cold s | RSS MiB | ACK p99 ms | Drain ms | CPU s | TotalAlloc MiB | Post-delete heap MiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| [baseline-1000-1-r1](baseline-1000-1-r1.json.gz) | 0.387 | 134.6 | 31.8 | 12.7 | 2.72 | 1431.5 | 6.37 |
| [candidate-1000-1-r1](candidate-1000-1-r1.json.gz) | 0.391 | 141.7 | 42.4 | 17.6 | 3.01 | 1571.4 | 6.83 |
| [baseline-1000-1-r2](baseline-1000-1-r2.json.gz) | 0.417 | 137.0 | 33.1 | 15.2 | 2.95 | 1429.3 | 6.40 |
| [candidate-1000-1-r2](candidate-1000-1-r2.json.gz) | 0.390 | 139.6 | 28.2 | 22.1 | 3.07 | 1573.8 | 6.87 |
| [baseline-10-100-r1](baseline-10-100-r1.json.gz) | 0.241 | 62.3 | 24.6 | 14.5 | 1.74 | 420.3 | 8.07 |
| [candidate-10-100-r1](candidate-10-100-r1.json.gz) | 0.250 | 64.0 | 22.3 | 17.8 | 2.70 | 774.1 | 4.39 |
| [baseline-10-100-r2](baseline-10-100-r2.json.gz) | 0.244 | 63.0 | 23.6 | 16.1 | 1.47 | 420.6 | 8.68 |
| [candidate-10-100-r2](candidate-10-100-r2.json.gz) | 0.246 | 64.4 | 25.6 | 19.7 | 2.75 | 767.6 | 4.40 |

CPU is the last external process sample. TotalAlloc is cumulative at the final
forced GC snapshot and includes the whole staged lifecycle and wrapper overhead.
The post-delete live-heap bound passed in every run. Short runs cannot establish
heap growth over the full required window.

[Machine summary](summary.json) retains API counts, non-watch request duration
count/sum/max, and watch peaks for every run. API duration is server-side fixture
handling, including JSON encoding and socket writes. It excludes client queueing
and transport and does not model real API-server latency. The candidate adds one
shared metadata ReplicaSet watch; no traversal-only full ReplicaSet watch was
observed. This watch count is bounded for the exercised single-namespace topology,
not evidence for arbitrary kind/namespace growth.

## Oracle migration

The diagnostic endpoint groups independent results only to verify final ACKs.
It accepts Pod rows before a root row, but cannot acknowledge the root until all
expected results exist and are healthy. In-flight Pod/workload validity can differ.
The nine phase rows from one workload must still agree. Final UIDs, coordinates,
phase values, labels, and deletion checks remain strict.

Candidate config omits retired proof settings. The fixture serves real metadata
watch streams and counts their representation. Later harness changes also force
full Pod recovery LISTs for the fault scenario rather than waiting for a removed
refresh timer. That fault mode was not enabled in these comparison runs.
