<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter component ownership and measurements

A workload with 1,000 Pods can use the same workload label expression for every
Pod. Repeating that expression wastes work. Keeping the result in a global cache
also requires identity keys, locking and eviction. Runtime already owns the
immutable workload input and serializes its root queue. A prepared label function
now shares the result within that input's lifetime. A Pod update reuses it; a
changed workload starts a new scope.

The same ownership rule applies to definitions and collection. Runtime owns
cluster observations. Inventory selects definitions from its supplied snapshot.
Store holds immutable results. Collection reads those results.

This report preserves the earlier comparison from `31be33e7`. The subsequent
[controller audit](controller-audit.md) records direct Reflector acceptance and
typed leaf/root queues from `cdcc8779`. Its measurements are separate.

## Ownership changes

| Area | Before | Current owner and operation |
| --- | --- | --- |
| Cluster Kartas | Inventory and Runtime each ran a LIST/WATCH lifecycle. | Runtime validates its Karta snapshot and calls `Inventory.ReplaceCluster`. |
| Discovery | Owner traversal also consulted inventory and returned coverage requests. | Discovery returns the owner path and root. Runtime selects the definition and watches that root. |
| Label rules | Compiler and evaluator factories plus a global 1,024-entry cache. | `Compile` creates an evaluator. Runtime retains a prepared resolver for one immutable workload. |
| Label results | A list of requested metrics produced a result slice. | One metric and optional Pod produce one result with labels and errors. |
| Root Pods | Five maps split each Pod's path, proof, errors, cached row and label failure. | One `podState` holds that data. A sparse dirty set still identifies pending rows. |
| Metadata requests | Separate maps tracked pending GETs, priming LISTs and accepted revisions. | One `metadataReadState` tracks active requests for an object. Metadata and failures retain separate lifetimes. |
| Pod pages | Public rows, a sorted flag and lazy dependency preparation. | Page construction fixes rows and dependency counts. Lookup uses sorted page bounds and rows. |
| Application | `Components`, `Run`, a registrar and duplicate Runtime references. | Application registers metrics directly and owns one Runtime and provider lifecycle. |
| Configuration | Separate label and runtime override callbacks and passes. | One setting table applies overrides to `Config`; `Validate` checks the result. |

These are internal changes. Metric families, labels, source precedence, configuration
syntax and validity rules keep their existing contracts. The
[implementation design](README.md) contains the component calls.

## Guards that remain

| Guard | Reason |
| --- | --- |
| UID, resourceVersion and API representation checks | A reused name or converted object must not validate an older input. ResourceVersions are compared for equality. |
| Root generations and final publication checks | An event accepted during JQ or CEL evaluation must prevent that calculation from restoring old results. |
| Read revisions and watch invalidation records | A delayed GET, LIST or informer callback must not restore proof contradicted by newer state. |
| Evidence deadlines and active proof keys | A quiet or failed source must not leave observations valid indefinitely. |
| Separate metadata and failure health | A failed read can produce health without a cached object. A later accepted observation has its own lifetime. |
| Store mutex, immutable pages and failure references | A scrape must see one stable view. Retiring a key must not clear a failure still used by a publication. |
| Root queue serialization | One worker owns a root's read/replace/publish sequence and prepared label function. Different roots can progress concurrently. |
| Label limits and cancellation checks | An expression must have bounded cost and output. Interrupted evaluations must not become cached results. |
| Source readiness and complete-snapshot checks | A failed or partial read cannot establish absence or enable a fallback definition. |

The [consistency contract](consistency.md) explains these checks and their failure
behavior. Fewer state owners make their relationships easier to inspect; source
size alone does not establish correctness.

Kubernetes uses a related separation: one
[`GraphBuilder`](https://github.com/kubernetes/kubernetes/blob/v1.34.1/pkg/controller/garbagecollector/graph_builder.go#L72)
updates the ownership graph and enqueues work. Its informer handlers unwrap
delete tombstones, and graph entries use UIDs. The
[client-go workqueue](https://github.com/kubernetes/client-go/blob/v0.34.1/util/workqueue/queue.go#L198)
keeps processing and dirty keys separate, so an update during processing schedules
another pass. The exporter uses these ownership and queue patterns. It does not
need garbage-collection virtual nodes, finalizers or monitors for every resource.
Concrete data types and narrow dependency interfaces also follow
[Effective Go](https://go.dev/doc/effective_go#interfaces_and_types).

## Source size

The baseline is `31be33e7`. Counts include comments and blank lines in non-test Go
files. Generated mocks and test files are excluded. The command and scale driver
are included so the same directory set is counted on both sides.

| Area | Before lines | After lines |
| --- | ---: | ---: |
| Controller and reader | 3,604 | 3,605 |
| Definition inventory | 335 | 202 |
| Discovery | 211 | 179 |
| Label resolver | 277 | 199 |
| Label evaluator | 167 | 135 |
| Store | 422 | 397 |
| OTel adapter | 230 | 212 |
| Application | 248 | 170 |
| Configuration | 347 | 383 |
| Command and scale driver | 331 | 331 |
| Total | 6,172 | 5,813 |

This removes 359 lines, or 5.8%, and five production source files: 28 become 23.
The inventory loses its independent watcher lifecycle. Label resolution loses the
global cache, lock, identity tuple and eviction ring. Metadata request tracking
uses one map instead of three. Three unused factory mocks are also removed;
their generated lines are excluded above. Configuration gained lines when compact
callbacks were formatted across several lines. These counts accompany the
ownership changes; they do not measure their benefit by themselves.

## Performance evidence

These are local microbenchmarks on an Apple M4 Pro, Darwin arm64, with
`GOMAXPROCS=4`. Each comparison uses the median of five 500 ms samples. They
measure component operations, not API convergence or production capacity.

| Operation | Before | After | Time change |
| --- | ---: | ---: | ---: |
| Find last Pod among 100,000 | 1,657 ns | 94.51 ns | 94.3% less |
| Replace last Pod and publish, 100,000 Pods | 15,865 ns | 13,485 ns | 15.0% less |
| Refresh all rows and publish, 100,000 Pods | 6.521 ms | 6.220 ms | 4.6% less |
| Build pages only, 100,000 Pods | 4.654 ms | 5.366 ms | 15.3% more |
| Labels for 1,000 Pods, unchanged workload | 235,327 ns | 97,823 ns | 58.4% less |
| Labels for 1,000 Pods, changed workload | 317,315 ns | 187,955 ns | 40.8% less |

Page construction now prepares dependency counts, so build-only work costs more.
The complete refresh-and-publish measurement includes the corresponding reduction
in Store work. It is the relevant comparison for full publication refreshes.

The label fixture evaluates a filter and map over 100 workload entries. Both
versions receive a full object with the same GVK, UID and resourceVersion. The
unchanged case reuses it across calls. The changed case clones it and advances
resourceVersion before each batch. Allocated bytes per 1,000-Pod batch fall from
432,000 to 384,009 for unchanged workloads and from 520,514 to 472,484 for changed
workloads. The baseline uses an API adapter for the same fixture.

The source benchmarks are [Store mutations](../../../karta-metrics-exporter/pkg/store/benchmark_test.go)
and [workload labels](../../../karta-metrics-exporter/pkg/labelresolver/resolve_test.go).

### Owner traversal

A matched Discovery benchmark resolves either Pod -> ReplicaSet -> Job or a
32-object chain. Both versions use complete object identities and validate the
root and path. The cached case reads every object from a fake cache. The metadata
case caches only the Pod and returns a fresh metadata copy for each owner.

| Chain and reads | Before | After | Time change | Bytes per operation, before -> after |
| --- | ---: | ---: | ---: | ---: |
| 3 objects, cached | 1,278 ns | 764.3 ns | 40.2% less | 2,556 -> 676 |
| 3 objects, metadata owners | 1,288 ns | 750.8 ns | 41.7% less | 3,195 -> 1,251 |
| 32 objects, cached | 11,688 ns | 10,867 ns | 7.0% less | 19,470 -> 17,590 |
| 32 objects, metadata owners | 11,213 ns | 10,284 ns | 8.3% less | 28,432 -> 26,488 |

Reader call counts are identical: one cache attempt per object, with 2 or 31
metadata reads in the metadata cases. The baseline includes the inventory lookup
that Discovery previously performed. Runtime still selects definitions in the
candidate. These timings exclude network latency, Runtime queues, Reader locking,
publication and collection. Temporary benchmark overlays and their complete
outputs are retained with the local evidence.

### Modeled API runtime

The [scale harness](../../../karta-metrics-exporter/test/scale/) runs fresh
processes with matched arguments against a modeled Kubernetes API. These are
single runs per profile on the same host, with `GOMAXPROCS=4`. They describe this
fixture, not a statistical estimate or production capacity. Cold time includes
initial metric convergence. HTTP values are medians of two steady scrapes.
RSS is the sampled process peak; MiB means 1,048,576 bytes.
CPU and allocated bytes cover the whole run, including startup scrapes. The
10,000-root/1-Pod run produced fewer incomplete startup scrape bytes, 145.5 MB
before and 62.3 MB after. Its lower CPU and allocations do not isolate
reconciliation costs.

| Roots / Pods per root | Cold seconds, before -> after | HTTP seconds, before -> after | Peak RSS MiB, before -> after |
| --- | ---: | ---: | ---: |
| 1,000 / 1, sustained updates | 5.192 -> 5.207 | 0.0797 -> 0.0758 | 140.5 -> 141.3 |
| 10,000 / 0 | 3.868 -> 3.659 | 0.8272 -> 0.8235 | 650.1 -> 672.6 |
| 10,000 / 1 | 12.269 -> 10.803 | 0.9109 -> 0.8898 | 1,087.5 -> 1,004.9 |
| 10,000 / 10 | 32.759 -> 31.723 | 1.8227 -> 1.8668 | 3,282.4 -> 3,189.8 |
| 1 / 100,000, sustained updates | 25.452 -> 25.036 | 0.9811 -> 0.9828 | 2,828.6 -> 2,940.8 |

| Roots / Pods per root | Process CPU seconds, before -> after | Allocated bytes change | API requests, before -> after |
| --- | ---: | ---: | ---: |
| 1,000 / 1, sustained updates | 44.13 -> 48.72 | 0.7% less | 136 -> 136 |
| 10,000 / 0 | 13.78 -> 13.56 | 1.0% less | 77 -> 77 |
| 10,000 / 1 | 26.35 -> 21.31 | 17.7% less | 244 -> 209 |
| 10,000 / 10 | 66.89 -> 67.51 | 1.7% less | 981 -> 982 |
| 1 / 100,000, sustained updates | 180.03 -> 178.44 | 1.1% more | 2,883 -> 2,883 |

All measured gates pass for both versions in the first four profiles. The
1,000-root sustained run delivers 30,000 updates over five minutes. Its publication
p99 upper bound is 0.2626 -> 0.2628 seconds and its final queue drain is
0.2649 -> 0.2662 seconds.
Process CPU increases by 10.4% in that run despite slightly lower allocations.
The 10,000-root/10-Pod scrape median increases by 2.4%, while cold convergence
improves by 3.2%. These increases remain visible in the comparison.
The single extra request in the 10,000-root/10-Pod run is a cold ReplicaSet GET.
Paged LIST and object counts match by completion.

The giant-root pair completes its correctness stages and passes every measured
gate except post-delete live heap. Both versions fail that gate: 649,147,912 ->
655,640,192 bytes remain after forced GC, a 1.0% increase. The cause of this
existing retention is not established. The run delivers 15 workload updates over
five minutes. Its p99 upper bound is therefore the maximum observed value,
2.5748 -> 2.5378 seconds. Final queue drain is 0.2874 -> 0.3105 seconds. API counts
and response bytes match exactly.

Across the tested shapes, these measurements show no material performance
regression. The sustained small-root CPU increase remains unexplained, and the
giant-root deletion-heap failure remains unresolved in both versions. Single runs
cannot establish general capacity or absence of bugs.
[Verification evidence](verification.md) records completed checks, independent
review and retained raw outputs.
