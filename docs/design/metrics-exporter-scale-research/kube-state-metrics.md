<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# kube-state-metrics scaling research

## TLDR

kube-state-metrics (KSM) makes repeated scrapes cheap by converting each changed object into serialized metric bytes once, retaining those bytes by object UID, and streaming them on demand. It does not retain full workload objects in its metrics store. This is a useful design for read-heavy derived views, but moves generation cost onto every watch update. [E1, E2]

Sharding has two different effects. UID sharding partitions retained metrics but each instance still receives and decodes the whole upstream stream. Pod node sharding uses an API field selector and can reduce the objects delivered to each instance. At the inspected commit, implementation uses FNV-1a plus jump consistent hashing; the README's MD5/modulo description is stale. [E3, E4]

This checkout does not establish capacity for 100,000 workloads. Its full-server benchmark seeds 1,000 Pods, 1,000 Services, and 1,000 ConfigMaps through a fake client; the writer benchmark uses 500 synthetic objects. The README's latency numbers describe a 100-node test without enough workload detail to extrapolate. [E10, E11]

Research scope: only the local `kubernetes/kube-state-metrics` checkout at verified HEAD `ca3c2e3ded4f9845ff9b025cacd1c17331c07496` was inspected. No network, source edits, benchmark execution, or Karta source inspection was performed.

## How it works

### Cache and serialization

1. The builder filters enabled metric families before constructing generators and stores. For all namespaces it uses one store and reflector per resource; an explicit namespace list creates a store and reflector for each selected namespace. Each reflector uses `ResyncPeriod: 0` and runs in its own goroutine. [E2]
2. Reflector add/update events call the metrics generator, render the families, and replace a UID-keyed `sync.Map` entry containing `[][]byte`. Delete removes that UID. There is no workload-object cache behind this store: `List`, `Get`, and `GetByKey` return no objects. This is projection at the store boundary, rather than an informer `SetTransform` optimization. A repository-wide Go search found no `SetTransform`, `TransformFunc`, or managed-fields pruning implementation. Full upstream decoding still occurs. [E1, E2]
3. Rendering uses one contiguous byte buffer for an object's families, with per-family slice views. Size hints reduce buffer growth, excessive spare capacity is trimmed, and full slice expressions prevent appends from corrupting neighboring families. Headers are precomputed for both exposition formats. These reduce retained memory and allocation overhead; they do not eliminate metric generation on updates. `Update` still calls `Add`, with a TODO about checking resource-version changes. [E1]
4. A scrape snapshots the writer list under a short read lock, releases it before serving a potentially slow client, and streams each cached family. Header sanitization shares the backing metric maps rather than cloning object data and pools the deduplication map. `WriteAll` traverses stores once per metric family, so the response still costs roughly O(objects times enabled families plus bytes written). It is not a cached whole-response blob. [E5]

### Sharding and upstream API work

- UID partitioning wraps `List` and `Watch` after the underlying calls. It keeps objects selected by jump consistent hash over FNV-1a of UID. Therefore it partitions generation and retained output, while all shards pay receive/decode/filter costs. More UID shards can increase aggregate API network and client decoding work. [E3]
- The list wrapper preserves resource version, continuation token, and remaining item count. It uses `ExtractListWithAlloc` so retained shard items do not pin the source list's item backing array. The watch wrapper passes bookmark/error events through and has cancellation-aware forwarding to avoid blocked-send goroutine leaks. These are correctness and peak-memory concerns at large list sizes, not just implementation details. [E3]
- Node-based pod sharding sends `spec.nodeName` to the API server for both list and watch. An additional deployment tracks unscheduled pods using `spec.nodeName=`. This is an upstream selection mechanism, but it depends on resource-specific selectable fields and cannot be assumed for arbitrary workload CRDs. [E4]
- `--use-apiserver-cache` sets `ResourceVersion="0"` in the `List` wrapper. Although some documentation describes ListWatch requests broadly, the actual `Watch` wrapper does not rewrite resource version. Do not claim this flag removes watch traffic or treat the README's etcd explanation as a verified statement about every Kubernetes version. [E6]
- WatchList support is delegated through wrappers; an object limit disables WatchList because limiting is implemented only in `List`. E2E code explicitly treats list-request metrics as optional under WatchList. `--object-limit` clears continuation tokens after limiting a list, deliberately dropping part of the initial population. Its `Watch` method does not cap future adds; it is neither complete coverage nor a hard resident-object bound. [E6]
- Built-in client configuration prefers Kubernetes protobuf and accepts JSON as fallback. That is separate from scrape serialization and is not evidence that arbitrary custom resources use protobuf. [E7]

### Concurrency and cardinality

The design has concurrent reflectors and concurrent scrapes, with thread-safe per-object byte replacement. It does not create one goroutine per object. A single resource stream still invokes its store synchronously; increasing scrape concurrency does not increase event-generation capacity. The README warns that too little CPU can grow internal queues and memory. Shard reconfiguration cancels the old context and rebuilds stores, so capacity planning must include warm-up and relist behavior. [E1, E2, E5, E8]

Cardinality controls apply at different stages. Resource selection avoids unneeded collectors; family filtering prevents generation and storage; label and annotation allowlists limit rendered labels. Request-level resource filters only change the current scrape response and do not shrink the cache. Sparse optional reasons are emitted only when present, with unknown values folded into `Other`; true partitions still use state sets. KSM's metrics guidance also separates 1:n properties from stable object identity and bounds arbitrary error labels. [E2, E5, E9]

## Relevance to Karta

These are general design inferences, not findings about Karta's current implementation:

- For a read-heavy normalized workload view, compare retaining full unstructured objects against a compact extracted representation. If identical representations are serialized repeatedly, cache the serialized result per object and invalidate on relevant changes. Benchmark whether update churn makes eager serialization more expensive than lazy serialization.
- Select kinds and namespaces before starting watches. Distinguish partitioning after decode from server-side selection when estimating API load. A workload hash shard is a memory partition, not automatically an API-load partition.
- Keep full-object access for operations that need it. KSM's metrics-only store cannot support arbitrary object lookup, mutation, or controller indexes.
- Treat stored-object count, enabled fields, generated series per object, update rate, and response bytes as separate capacity dimensions. "100k workloads" alone does not identify the bottleneck.
- Define snapshot requirements explicitly. Cached per-object replacement is useful for observational views; it does not establish a consistent cross-object snapshot suitable for decisions requiring one.

## Evidence

All links are pinned to the inspected commit.

- E1: [MetricsStore, rendering, update, replacement, and missing object lookup](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_store.go#L27-L239).
- E2: [Store construction, family filtering, namespace fan-out, direct reflectors](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/store/builder.go#L637-L762).
- E3: [Sharded watch cancellation, list extraction, pagination, control events, and hash](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/sharding/listwatch.go#L38-L215); [README's documented whole-stream cost and stale hash description](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/README.md#L245-L281).
- E4: [Pod list/watch field selector](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/store/pod.go#L2050-L2060); [node and unscheduled selector setup](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/app/server.go#L231-L245); [DaemonSet partition documentation](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/README.md#L283-L313).
- E5: [Writer streaming loop](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_writer.go#L74-L136); [header sanitization shares maps](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_writer.go#L207-L260); [HTTP writer snapshot and request-level filtering](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metricshandler/metrics_handler.go#L224-L299).
- E6: [Instrumented List/Watch, resource version, limits, and WatchList capability](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/watch/watch.go#L97-L174); [WatchList-aware E2E telemetry assertion](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/tests/e2e/main_test.go#L214-L226).
- E7: [Client encoding preference](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/util/utils.go#L38-L52).
- E8: [CPU/queue warning](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/README.md#L219-L229); [cancel and rebuild on reconfiguration](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metricshandler/metrics_handler.go#L88-L133).
- E9: [Cardinality and sparse-state guidance](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/docs/design/metrics-best-practices.md#L31-L74); [label allowlist implementation](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/store/utils.go#L276-L300); [CLI labels and annotation controls](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/docs/developer/cli-arguments.md#L64-L66).
- E10: [Server benchmark and timing caveat](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/app/server_test.go#L64-L147); [three fixture kinds](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/app/server_test.go#L791-L809); [500-object, 10-family writer benchmark](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_writer_test.go#L692-L745); [single-object, 50-family add benchmark](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_store_test.go#L139-L175).
- E11: [100-node latency figures](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/README.md#L231-L239); [benchmark comparison script with benchmem and benchstat](https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/tests/compare_benchmarks.sh#L18-L48).

## Lessons

1. Optimize retained representation before adding replicas. Cached output can remove repeated extraction and serialization work, while contiguous storage and trimmed capacity reduce per-object overhead.
2. Partition at the earliest feasible boundary. Resource/namespace/API selectors can reduce delivered work; client-side UID filtering cannot.
3. Preserve continuation tokens and watch control events through wrappers. Large-scale correctness depends on pagination and cancellation details.
4. Benchmark separately: cold initialization and relist peak RSS; steady retained bytes per object; watch events per second and lag; scrape latency and bytes; concurrent slow scrapes; reshards and reconnects. Sweep object count to at least 100,000, but also vary nested-object size, churn, labels, enabled fields, kinds, and namespaces. This is a proposed validation matrix, not an upstream result.
5. Track list/watch requests and errors alongside object counts, response sizes, GC, and update lag. WatchList can make an absent list counter normal; instrument the path actually used.

## What not to copy

- Do not copy the README's hash algorithm or resource recommendation as a verified 100k capacity design. Source and benchmark scope disagree with that level of certainty.
- Do not treat object truncation as a scaling solution when complete state is required. It omits initial objects and does not bound subsequent watch growth.
- Do not reuse this `cache.Store` as an indexed controller cache: its read methods intentionally return no objects.
- Do not promise atomic snapshots. `Replace` clears then refills a live map, and scrapes range each family separately while updates continue. An observational scrape can tolerate semantics that a mutation planner may not.
- Do not assume serialization caching helps equally under all workloads. Every update regenerates all enabled families; high-churn objects still allocate and consume CPU even when their exported values barely change.
- Do not assume replica scaling lowers API load, or node selectors generalize to arbitrary CRDs. State the upstream selection mechanism and its coverage, including unscheduled workloads.
