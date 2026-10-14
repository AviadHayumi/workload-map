<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Final lifecycle scale validation

Measured on 2026-10-14 against production commit
`02a613462995f11ac3825c6b3db7a7f51c485e0c`, using Go 1.26.3 on darwin/arm64
and GOMAXPROCS 14. The coordinating session reported an Apple M4 Pro with
14 logical CPUs and 48 GiB memory. Every scenario ran in a fresh process,
serially after live acceptance released the host. No build, race suite, image
build, or acceptance load ran concurrently. The dedicated acceptance cluster
remained idle; the host was not otherwise empty.

All nine scrape scenarios, the 100,000-root/100,000-Pod churn oracle, and both
fresh-health scenarios passed. The two health scenarios initially failed before
fixture creation because the sandbox denied a loopback listener. Those exit-2
startup failures are preserved separately; both health cases then passed with
loopback access. The production verification deadline remained 60 seconds.

## Evidence

- `scrape-N-P.json`: full independent identity/value oracle, then three timed
  uncompressed scrapes with N roots and P Pods per root.
- `scrape-summary.json`: those raw measurements plus derived medians.
- `churn-100000-1.log` and `churn-summary.json`: exact identities and values
  before and after 10% root deletion and 10% UID replacement.
- `health-100000-P.log` and `health-summary.json`: actual verified rows,
  request counts, duration, and deadline headroom for each successful health run.
- `health-100000-P-sandbox-startup.*`: initial startup failures. No verification
  duration or coverage can be inferred from these attempts.
- `*.stderr` and `*.exit-status`: stderr and exit status for every attempt.
- `runs.jsonl`: serial order, UTC start times, total process wall times,
  binary names, arguments, and performance environment. Process wall time
  includes fixture setup and differs from health verification duration.
- `source-build.json`: clean-tree source manifest used to prepare the binaries.
- `source-before.json`, `source-before-health-retry.json`, and `source-after.json`:
  123 identical source/module hashes at the same commit, with `dirty: true`
  during measurement. Concurrent documentation and acceptance fixture edits
  were outside the source manifest.
- `binary-hashes.json`: SHA-256 for the three frozen executables.
- `run-metadata.json`: isolation, host provenance, limitations, and sanitization.

Source paths are repository-relative. Published startup stack traces replace
absolute workspace/toolchain paths and the local listen address. Original raw
startup output is retained locally outside the repository.

The largest scrape emitted 2,000,000 samples and 558,033,869 bytes. Peak RSS was
9.325 GB, above the previous 7.812 GB sample. The largest health verification
completed in 47.229 seconds with all 100,003 rows healthy, exactly 2,101 LISTs,
and no fallback GETs. Its 12.771-second deadline headroom is one synthetic
observation, not a production guarantee. The historical 62.188-second failed
coverage run and the 58.568-second narrow-margin pass remain in the parent
results directory.

These results supersede the older matrix and health measurements for this
source revision. They do not replace historical optimization experiments or
establish production capacity with default chart resources. See the
[scale decision](../../../../../docs/design/metrics-exporter-scale-decision.md).
