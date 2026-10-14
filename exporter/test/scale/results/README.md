<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Recorded synthetic scale measurements

Recorded on 2026-10-13. These are synthetic local measurements, not production
Kubernetes capacity results. See the [decision](../../../../docs/design/metrics-exporter-scale-decision.md)
and [reproduction guide](../README.md).

- `baseline-N-P.json`: original attribute construction, N roots and P Pods/root.
- `final-N-P.json`: fixed scratch attributes with `WithAttributeSet`.
- `pre-root-confirmation/`: preserved fixed-array measurements before the final
  same-snapshot Pod/root confirmation check.
- `experiment-fixed-array-100000-0.json`: control for the cache experiment.
- `experiment-status-cache-100000-0.json`: rejected persistent status-attribute cache.
- `health-final-100000-1.json`: isolated fresh verification and exact API counts.
- `health-final-100000-10.json`: complete fresh verification with only 1.43 seconds
  of measured headroom below the default 60-second budget.
- `health-baseline-100000-10.json`: failed unchanged-budget coverage before metadata projection.
- `health-baseline-100000-1.json`: earlier full-object health pass at 100,000 Pods.
- `health-experiment-*.json`: intermediate health results, with fixture and
  implementation changes named explicitly.
- `churn-final-100000-1.json`: deletion and UID replacement oracle result.
- `profiled-*.json` and `profile-summary.json`: separate diagnostic profile runs.
- `profiled-source-hashes.json`: implementation used by the allocation profiles,
  before the final same-snapshot Pod/root confirmation check.
- `*-source-hashes.json`: SHA-256 hashes of measured source files, using repository-relative paths.

Each HTTP matrix case starts a fresh process, verifies all output, then measures
three serial uncompressed scrapes. `process_peak_rss_bytes` includes fixture and
oracle high-water memory. `retained_heap_bytes` is measured after a final GC while
the fixture and handler remain live. Allocation fields describe timed scrapes.
The current recorded matrix predates the later same-snapshot Pod/root confirmation
check. The recorded health runs predate per-object cache-mismatch isolation.
Source hashes identify the exact implementations; refreshed measurements require
an isolated heavy benchmark window.

The baseline 100,000-root, 10-Pod case overlapped other benchmark work. Its timing
is contended. The final matrix was serialized against heavy benchmarks. Three
samples do not establish statistical significance or portable latency budgets.
The cache-control experiment also overlapped baseline work; the retained-memory
increase is the reason for rejection, not a claim of precise latency regression.

The store and HTTP paths exclude production informer caches, real API-server
latency, network I/O, compression and Prometheus ingestion. Controller health
results separately use real loopback HTTP requests against synthetic API pages.
No private hosts, endpoints, user paths, credentials, or object names are present.
