<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Warm owner-walk measurements

## Final frozen-source comparison

Three fresh serial Go test processes per implementation and shape used
500 ms per benchmark, GOMAXPROCS=4, Go 1.27.3, and the same 48 GiB Apple M4 Pro
host. All four kind nodes were stopped. No other benchmark, test or build ran
concurrently; desktop background work remained. Raw benchmem and manifests
are in `final-1000/` and `final-10000/`.

The current checkout was clean at `1fa92592` (evidence commits above production
`f868589a`). Before the runs, `git diff --exit-code f868589a --
karta-metrics-exporter/pkg/discovery karta-metrics-exporter/go.mod
karta-metrics-exporter/go.sum` confirmed the measured package and module files
were unchanged from that production checkpoint. Manifests preserve package,
module and benchmark-overlay hashes. Baseline and comparison checkouts were
clean at `01352db5c4be54b0b3eb6d53a0a04fb5faa34520` and
`1f56e5bf50a33c943ce5c7d45a19a7ff003c5bbf`, respectively.

Timing columns show median ns/op and the observed minimum-maximum across three
repetitions. Allocation counts are stable across these repetitions.

| Roots | Implementation | Direct ns/op (range) | Intermediate ns/op (range) | Direct bytes / allocs | Intermediate bytes / allocs |
| --- | --- | --- | --- | --- | --- |
| 1,000 | current | 475.1 (416.0-492.7) | 627.5 (602.5-628.8) | 594 / 5 | 963 / 8 |
| 1,000 | baseline | 664.0 (626.9-679.4) | 1148.0 (1032.0-1163.0) | 1168 / 6 | 2608 / 9 |
| 1,000 | workload_discovery | 271.4 (252.0-279.4) | 468.1 (450.2-506.9) | 624 / 9 | 1040 / 14 |
| 10,000 | current | 494.9 (466.0-506.1) | 753.3 (697.1-830.1) | 594 / 5 | 963 / 8 |
| 10,000 | baseline | 658.7 (653.8-703.2) | 1306.0 (1121.0-1331.0) | 1168 / 6 | 2608 / 9 |
| 10,000 | workload_discovery | 264.4 (262.6-274.0) | 544.7 (484.3-566.4) | 624 / 9 | 1040 / 14 |

The current walker is faster than the old PoC in these samples and allocates
less. The k8s-workload-discovery walker remains faster than the current walker,
while allocating more objects. This healthy, acyclic, fully cached owner walk
excludes API transport, missing-owner recovery, Karta evaluation, publication
and metrics collection. Native cache and return-value representations differ.
It is not a full exporter capacity comparison. Read the
[comparison scope](../../ownership/README.md). Three samples describe observed
variation, not a statistically established performance difference.

## Historical development-host measurements

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
