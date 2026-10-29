<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Warm ownership API comparison

This benchmark compares one narrow common operation: resolve a healthy Pod to
its known root through an acyclic, fully cached controller-owner chain. Cases
use Job-to-Pod and Job-to-ReplicaSet-to-Pod paths. Each iteration checks the
expected root UID and path length. Setup, cache filling, watches, network reads,
Karta evaluation, labels, publication and metrics are outside the timed region.

The adapters call the actual implementations through temporary Go overlays.
They do not edit the comparator checkouts or replace the owner-walk algorithms.
The runner checks these clean comparator revisions before compiling:

| Implementation | Source | Timed API |
| --- | --- | --- |
| Current exporter | caller-supplied checkout, hash recorded | `Discovery.Get` |
| Old PoC | `01352db5c4be54b0b3eb6d53a0a04fb5faa34520` | `Discovery.Resolve` |
| k8s-workload-discovery | `1f56e5bf50a33c943ce5c7d45a19a7ff003c5bbf` | `OwnerWalker.WalkPod` |

The current adapter supplies immutable cached Pod/root objects under a read
lock and deep-copied cached intermediate metadata, matching the reader contract.
The old PoC uses its preloaded ownership index and synchronized coverage. Both
inventories return confirmed absence, so no Karta evaluation is measured. The
workload-discovery adapter preloads its native LRU/TTL owner cache. Any backing
read in that warm adapter fails. These cache representations are intentionally
reported, not presented as equivalent internal operations.

Return values differ. The old PoC includes captured input state. The current
exporter returns references and missing coverage. Workload discovery builds
nodes with child maps. Its missing-owner behavior can stop at an intermediate,
and its maximum depth truncates paths. These semantics are not suitable for a
correctness comparison on missing owners, cycles, or arbitrary depth. Such cases
are excluded instead of relabeled as equivalent work.

```sh
python3 karta-metrics-exporter/test/scale/owner_bench.py \
  --current /path/to/current/karta \
  --baseline /path/to/clean/old/karta \
  --workload-discovery /path/to/k8s-workload-discovery \
  --roots 1000 --benchtime 300ms --count 3 \
  --output /tmp/karta-owner-walk
```

Runs are serial fresh Go test processes with `GOMAXPROCS=4` and `-mod=readonly`.
The manifest records source revisions, dirty state, package and go.mod hashes,
overlay hashes, Go versions and arguments. The repositories retain their own
dependency versions; do not interpret results as a dependency-normalized study.
Use `--benchtime 1x --count 1` only to verify the adapters. A single iteration is
not performance evidence. Do not run 100k-root cases alongside operator setup or
other benchmarks. Full command capacity is measured by the separate HTTP harness.
