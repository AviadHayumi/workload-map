<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Performance and reuse

Reuse one discoverer for repeated queries. Choose the smallest operation that
returns the data the consumer needs.

- `OwnershipPath` reads one controller-owner chain.
- `WorkloadForPod` discovers the terminal root and its complete Pod membership.
- `PodMemberships` returns already computed classification from an observation.
- Component, instance and replica scopes use that observation's lookup tables.

Public results are detached copies. Repeated access does not refresh an observation.

## What avoids repeated work

| Mechanism | Work avoided | Boundary |
| --- | --- | --- |
| Direct lookup memoization | Repeated reads of shared owners within one acquisition | Does not cache owner reads across acquisitions |
| Optional owner metadata reader | Full bodies for newly encountered membership-only owners | Initial reads from the starting object to its root remain full; metadata fields can still be large |
| Caller-fed `discovery.Index` | Namespace-wide collection on every query | Caller supplies complete coverage, ordered changes and invalidation |
| Affected-descendant updates | Re-resolving unrelated ownership after a change | Publication still validates and stores changed records |
| Immutable internal Pod records | Copying every indexed Pod during acquisition | Public Pod accessors still copy their output |
| Compiled plans and selector cache | Repeated definition validation and compilation of eligible jq programs | Complex expressions can bypass program reuse |
| Optional attribution cache | Rebuilding successful membership and scope tables for unchanged indexed records | Requires a concrete `*discovery.Index`; nil disables reuse |

The attribution cache is bounded by workload count and total retained Pod
membership records. These are cardinality limits, not byte limits.
Changed root or Pod records cause recomputation. Unsupported expressions and
attribution failures do not populate the cache.

These are implemented SDK mechanisms, not claims that another controller was
copied or migrated. The microbenchmarks below measure selected mechanisms; they
do not measure every optimization in this table.

The index owns copies of published objects. Feeding an existing application cache
into it reuses the application's watch lifecycle, but adds index storage.
The SDK starts no watchers and does not make a stale caller cache fresh.

Implementation and correctness checks:

- [Ownership-path queries](../../pkg/discovery/resolver.go) and
  [detached membership output tests](../../pkg/discovery/pod_memberships_test.go).
- [Collection](../../pkg/discovery/collect.go) and
  [shared-owner lookup tests](../../pkg/discovery/collection_lookup_test.go).
- [Metadata reader](../../pkg/discovery/clientgo/owner_metadata_reader.go) and
  [metadata validation tests](../../pkg/discovery/owner_metadata_validation_test.go).
- [Incremental index](../../pkg/discovery/index.go) and
  [snapshot isolation tests](../../pkg/discovery/index_snapshot_test.go).
- [Pod ownership transfer](../../pkg/discovery/reader.go) and
  [transfer tests](../../pkg/discovery/transfer_test.go).
- [Compiled selector cache](../../pkg/jq/execution/program_cache.go) and
  [program-cache tests](../../pkg/jq/execution/program_cache_test.go).
- [Attribution reuse](../../pkg/discovery/attribution_cache.go) and
  [invalidation, bounds and copy tests](../../pkg/discovery/attribution_cache_test.go).

## Keep an existing efficient path

An ownership inventory can adapt its current metadata cache to `ownership.Lookup`
and call `ownership.Trace`. It does not need a second cache merely to share traversal.

A consumer that already has a confirmed root and selected full Pods can reuse
`attribution.Compile`, `Prepare` and `Evaluate`. It does not need to rediscover
membership. After full discovery, use `PodMemberships` instead of evaluating again.

An external `IndexedReader` can supply a coherent workload capture from its own
index. The optional attribution cache currently uses the built-in index's
immutable revision tokens and does not accept arbitrary `IndexedReader` values.

See [integration choices](INTEGRATION.md) and the
[reader, index and cache contract](../../docs/Live%20Workload%20Discovery.md).

## Recorded microbenchmark results

On 2026-10-16, the command below completed on Darwin/arm64, Apple M4 Pro,
with Go 1.26.3. Each case has three reported samples with a 100 ms target.
These samples come from one invocation, not three independent process runs.

The measured checkout had parent commit
`b3591bc093eb311dc1b1b41341800bf277092e28` plus the working source changes
delivered with this guide. This is not a measurement of that unmodified commit.
[Raw output](benchmarks/microbench.txt) retains every sample and iteration count.

Each table cell is the median of its three reported values.
Bytes and allocations are per operation; their medians are calculated separately
from elapsed time.

| Operation and fixture | Median time/op | Allocated bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| Two-edge walk, retain full path | 200.9 ns | 512 | 1 |
| Two-edge walk, return root reference | 161.8 ns | 0 | 0 |
| One descendant, 1,000 tracked Pods | 121.4 ns | 96 | 1 |
| One descendant, 100,000 tracked Pods | 120.3 ns | 96 | 1 |
| Republish one Pod, 1,000-Pod index | 8.069 us | 4,172 | 41 |
| Republish one Pod, 10,000-Pod index | 8.186 us | 4,172 | 41 |
| Acquire from 1,000 listed Pods, transfer | 61.584 ms | 42,026,428 | 659,246 |
| Acquire from 1,000 listed Pods, defensive copy | 62.799 ms | 46,621,032 | 690,240 |
| Acquire from 10,000 listed Pods, transfer | 607.368 ms | 437,718,408 | 6,583,359 |
| Acquire from 10,000 listed Pods, defensive copy | 620.674 ms | 483,586,112 | 6,893,375 |

The walk uses a toy two-edge chain and an in-memory lookup.
Its root-only path avoids retaining the full traversal slice.
The descendant fixture returns one Pod among many unrelated edges.
The update fixture repeatedly publishes the same Pod body after index setup.
These results do not measure a large affected subtree or prove capacity for
100,000 workloads.

The transfer fixture uses JSON responses through an in-process HTTP transport.
It has ten Pods per root and acquires the selected ten-Pod workload.
The 1,000-Pod case makes 101 GETs and two LIST-page requests per operation;
the 10,000-Pod case makes 1,001 GETs and 20 LIST-page requests.
Both modes perform the same reads. Output-validation copies occur after timing.

Transfer avoids the defensive copy of the acquired Pod list and reports fewer
allocated bytes in these samples. Timing ranges overlap between modes.
The 1,000-Pod samples contain only two iterations each and the 10,000-Pod samples
only one, so the median timing differences do not establish a latency win.

Allocated bytes measure cumulative allocation traffic per operation, including
decoding and transport work. They are not retained memory or process peaks.
This run does not measure attribution-cache reuse, production API latency,
watch feeds, persistence or controller startup.

## Reproduce the public microbenchmarks

Run from the repository root:

```sh
go test ./pkg/ownership ./pkg/discovery ./pkg/discovery/clientgo \
  -run '^$' \
  -bench '^(BenchmarkTraceResult|BenchmarkAffectedDescendants|BenchmarkIndexApplyOnePod|BenchmarkReaderWorkloadPodTransfer)$' \
  -benchmem -benchtime=100ms -count=3
```

| Benchmark | Measured operation |
| --- | --- |
| [TraceResult](../../pkg/ownership/trace_root_test.go) | Root-only traversal versus retaining the path |
| [AffectedDescendants](../../pkg/ownership/descendants_test.go) | Reverse ownership lookup in a populated graph |
| [IndexApplyOnePod](../../pkg/discovery/index_test.go) | Publishing one Pod update into a populated index |
| [ReaderWorkloadPodTransfer](../../pkg/discovery/clientgo/reader_transfer_test.go) | Direct acquisition with and without exclusive Pod-list transfer |

These microbenchmarks do not measure production API latency, watch feeds,
persistence or complete controller startup. Record the source revision, Go version,
platform and benchmark output before comparing runs.

There is no universal speedup claim here. Compare equivalent outputs, cold and
warm reads separately, update costs, output copying and retained memory.
The [validation guide](VALIDATION.md) describes correctness coverage.
