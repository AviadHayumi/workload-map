<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Warm owner-walk measurements

These are medians of three 300 ms samples in serial fresh Go test processes,
with GOMAXPROCS=4 on darwin/arm64 Apple M4 Pro. Go versions and source/module
hashes are in each manifest. Operator setup continued on the host, so these
are modest development measurements, not an isolated-host capacity study.

| Roots | Implementation | Job->Pod, ns/op | Job->ReplicaSet->Pod, ns/op | Allocations, direct/intermediate |
| --- | --- | --- | --- | --- |
| 1,000 | Current exporter | 746.7 | 1,192 | 7 / 12 |
| 1,000 | Old PoC | 697.8 | 1,155 | 6 / 9 |
| 1,000 | k8s-workload-discovery | 278.9 | 535.7 | 9 / 14 |
| 10,000 | Current exporter | 788.9 | 1,288 | 7 / 12 |
| 10,000 | Old PoC | 815.2 | 1,319 | 6 / 9 |
| 10,000 | k8s-workload-discovery | 294.5 | 720.7 | 9 / 14 |

The current exporter and old PoC had similar timings in this narrow warm case.
The workload-discovery walker was faster in these samples. Cache representations
and returned data structures differ. This does not compare full workload
publication, API behavior, missing-owner correctness or exporter capacity.
See [the comparison scope](../../ownership/README.md) before using these numbers.

Raw benchmem output is retained for every repetition. The 1,000-root manifest's
overlay hash records assembled source before gofmt. The 10,000-root manifest
records both assembled and formatted overlay hashes. Comparator checkouts stayed
clean at their pinned revisions. The current checkout was under active development;
its benchmark package hash identifies the measured source independently of HEAD.

## After the Discovery allocation change

Commit `dd407f5d` keys cycle checks by confirmed UID, preallocates the common
three-object path and keeps controller references as values. It does not cache
parents between calls. Integration tests with the race detector preserve cycle,
UID, long-chain and root-only coverage behavior.

| Roots | Implementation | Direct ns/op | Intermediate ns/op | Direct bytes / allocs | Intermediate bytes / allocs |
| --- | --- | --- | --- | --- | --- |
| 1000 | current | 542.4 | 834.3 | 594 / 5 | 963 / 8 |
| 1000 | baseline | 703.1 | 1174.0 | 1168 / 6 | 2608 / 9 |
| 1000 | workload_discovery | 289.2 | 526.3 | 624 / 9 | 1040 / 14 |
| 10000 | current | 540.5 | 813.9 | 594 / 5 | 963 / 8 |
| 10000 | baseline | 743.1 | 1345.0 | 1168 / 6 | 2608 / 9 |
| 10000 | workload_discovery | 293.2 | 640.3 | 624 / 9 | 1040 / 14 |

The current walker now allocates five times for direct ownership and eight times
for one intermediate, compared with seven and twelve before. The comparison
library remains faster in these warm samples. Timing varies across these short
active-host runs; allocation counts are the clearest effect of this local change.
The fixed three-entry capacity trades spare space on direct paths for avoiding
path growth on the common intermediate path. Longer chains still grow normally.
