<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Recorded synthetic scale measurements

Recorded on 2026-10-13 and 2026-10-14. These are synthetic local measurements,
not production Kubernetes capacity results. See the [decision](../../../../docs/design/metrics-exporter-scale-decision.md)
and [reproduction guide](../README.md).

- `final-20261014-lifecycle/`: final frozen-source matrix, churn, and fresh-health
  validation at commit `02a613462995f11ac3825c6b3db7a7f51c485e0c`. All nine
  scrape scenarios and both health cases passed. This supersedes older matrix
  and health capacity observations for the current implementation.

All files below outside `final-20261014-lifecycle/` are historical 2026-10-13
measurements; their original names are preserved.

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
The parent-directory matrix predates the later same-snapshot Pod/root confirmation
check, and its health runs predate per-object cache-mismatch isolation. The final
2026-10-14 series repeats those cases after the lifecycle changes. Its
`source-build.json` identifies a clean build and its before/after manifests record
123 unchanged source/module hashes, GOMAXPROCS 14, Go 1.26.3 on darwin/arm64,
and a dirty worktree caused by documentation and acceptance edits outside that
manifest. All processes ran serially in an isolated heavy-test window. Binary
hashes and all exit statuses are included. The two initial sandbox listener
failures are preserved separately from the successful health retries.

The baseline 100,000-root, 10-Pod case overlapped other benchmark work. Its timing
is contended. Both later matrices were serialized against heavy benchmarks. Three
samples do not establish statistical significance or portable latency budgets.
The cache-control experiment also overlapped baseline work; the retained-memory
increase is the reason for rejection, not a claim of precise latency regression.

The store and HTTP paths exclude production informer caches, real API-server
latency, network I/O, compression and Prometheus ingestion. Controller health
results separately use real loopback HTTP requests against synthetic API pages.
No private hosts, endpoints, user paths, credentials, or object names are present.
