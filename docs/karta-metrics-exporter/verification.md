<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter verification

Component, repository and deployed tests passed. Scale tests found the limits
listed below. Million-Pod capacity is not established by these results.

## Tested code

The tests used exporter code `f868589a`. The kind image was built at `c4a44553`
as `karta-metrics-exporter:alignment-c4a44553`. Later commits add test fixes,
documentation and evidence. The [monitoring report](../../karta-metrics-exporter/test/monitoring/results/2026-10-26.md)
records the source and image digests.

## Components and deployment

`make check` passed from a clean checkout. It covers Go tests, vet, lint,
module and generated-file checks, recorded workload replay, and envtest 1.31.0.
Some unchanged packages used Go's test cache. Separate race tests cover the
changed components; `make check` itself does not enable the race detector.

Discovery, definition inventory and Runtime have real API-server tests.
Store, Collector, CEL evaluation and label resolution have independent unit
and race tests. The [component guide](../../karta-metrics-exporter/README.md#verify)
shows how to run them. Helm lint/render checks, image-lock checks, shell lint
and the Prometheus query fixtures also passed.

The final image passed [all 26 operator cases](../../karta-metrics-exporter/test/e2e/results/2026-10-26.md):
23 default cases, bare-Pod exclusion and two alternate API versions. The tests
used the repository's real operators. They checked ownership UIDs, component
coordinates, status, label updates and deletion. No controller status was patched.
CPU fixtures prove operator reconciliation and attribution, not GPU inference.

The [monitoring test](../../karta-metrics-exporter/test/monitoring/results/2026-10-26.md)
passed actual Prometheus ServiceMonitor collection, NetworkPolicy checks,
permission denial and recovery, Helm upgrade and exporter removal. Its initial
fixture failure and the stdout/stderr fix are recorded in the same report.

## Scale results

These runs used a modeled Kubernetes API outside kind, a 48 GiB host and
`GOMAXPROCS=4`. They measure this exporter and fixture, not production API-server
or etcd capacity. [Commands, limits and raw evidence](../../karta-metrics-exporter/test/scale/README.md)
are retained for passing and failing runs.

| Workloads and Pods | Result | Main measurement or limit |
| --- | --- | --- |
| 100k workloads, no Pods | FAIL overall; lifecycle passed | Live heap grew 5.65% against a 5% target. All other gates and deletion recovery passed. |
| 100k workloads, one Pod each | PASS | 30k updates over five minutes; publication p99 upper bound 1.57s; peak RSS 7.57 GB. |
| 100k workloads, ten Pods each, 20 API requests/s | FAIL | Startup did not converge within 300s. Later checks were not reached. |
| Same shape, 100 API requests/s | FAIL | RSS exceeded 16 GiB. A partial HTTP scrape took 48.66s against a 25s limit. |
| One workload, 100k Pods | PASS | 15 updates over five minutes; maximum measured publication latency 2.47s; peak RSS 3.00 GB. |

GB is decimal. Passing rows met every declared gate, including cleanup.
The zero-Pod heap samples fluctuated rather than increasing steadily. That
observation does not change its failed result or establish a retention leak.

## Cached ownership lookup

The [same-toolchain comparison](../../karta-metrics-exporter/test/scale/results/owner-walk/README.md)
used Go 1.27.3 for all three implementations, three fresh processes per shape,
and 500ms samples at 1k and 10k roots. The current PoC was faster than the old
PoC in these samples. `k8s-workload-discovery` remained faster than current.
Dependencies, cache structures and returned values differ. This compares healthy
cached traversal, not full exporter throughput. Earlier measurements with
different Go versions remain separate.
