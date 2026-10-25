<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Intermediate-owner cold-start baseline

`bf2fcfc9-10000-1.json.gz` is the immutable pre-priming baseline. The wrapper
was built from clean commit `bf2fcfc92663d05dccaba1597d943d94649ff8af` before
priming edits began. Its SHA-256 is
`0dbe6b541df858b49225142750dd9fa6ba9490bb4409d0fedb119acea5215a0d`.
The report includes Go build metadata, configuration and the harness hash.

The modeled API contains 10,000 Job roots, each with one ReplicaSet intermediate
and one Pod. The process used GOMAXPROCS=4, QPS 20, burst 40, two workers,
45-second refresh, 500-item pages and a 180-second cold-stage deadline.
The total process limit was 240 seconds. No churn was requested. Local kind
nodes were stopped. Priming development and ordinary host work continued;
this is bounded bottleneck evidence, not an isolated-host capacity result.

The cold stage failed to converge within 180 seconds. The process ended after
183.68 seconds. The last scrape, captured at 176.90-178.60 seconds, contained
all 10,000 creation rows and 90,000 status rows but only 3,256 Pod mappings.
It reported valid resource health. Lifecycle stages after cold start were not
reached; no full-runtime acceptance claim follows from the partial root rows.

| Measurement | Observed value |
| --- | --- |
| Intermediate metadata GETs | 3,360 |
| Intermediate metadata LIST pages | 76 |
| Total modeled API requests | 3,708 |
| Intermediate GET rate over process lifetime | 18.29/s |
| Total request rate over process lifetime | 20.19/s |
| Last sampled object queue depth | 19,969 |
| Last sampled root queue depth | 0 |
| Peak sampled RSS | 963,887,104 bytes |
| Last sampled process CPU time | 51.26 s |
| Last HTTP response transfer | 0.8528 s |
| Last independent oracle duration | 0.8418 s |

Cold owner GET demand consumes most of the shared request budget. Even with
no other requests, 10,000 unique owner GETs at 20 QPS require about 500 seconds.
Batched refresh reduces repeated reads of retained metadata but does not
populate missing cold owners. This result supports measuring a bulk-priming
implementation at identical API budgets rather than silently raising limits.

The raw report marks its HTTP/oracle gates false because that harness revision
requires at least one fully converged scrape before those booleans can pass.
Here no scrape fully converged. Those booleans do not indicate a 25-second HTTP
timeout or oracle timeout; the separate timing fields above record their actual
cost. The failed report remains unchanged. The partial convergence count is
from the last scrape; total API counts include work until process shutdown.
