<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Synthetic exporter scale scenarios

These scenarios test an in-memory store and the real Prometheus HTTP handler.
They do not represent a 100,000-workload production Kubernetes cluster. They
exclude informer ingestion, real API latency, Prometheus ingestion, network I/O,
and compression. The independent streaming oracle verifies all workload and Pod
identities, all nine phase values, health values, exact label sets, and absence of
extra metric families or overflow rows.

Run from `exporter/`. The workload selection is required for tests and benchmarks:

```sh
KARTA_SCALE_WORKLOADS=1000 KARTA_SCALE_PODS_PER_WORKLOAD=1 \
  go test ./pkg/collector -run '^TestScalePrometheusExposition$' -count=1 -v

KARTA_SCALE_WORKLOADS=all KARTA_SCALE_PODS_PER_WORKLOAD=all \
  go test ./pkg/collector ./pkg/store -run '^$' -bench '^BenchmarkScale' \
  -benchtime=1x -benchmem -count=1 -timeout=30m

go test -race ./pkg/collector ./pkg/store -run '^TestScaleConcurrentHTTPOracle$|^TestScaleStore' -count=1

KARTA_SCALE_WORKLOADS=100000 KARTA_SCALE_PODS_PER_WORKLOAD=1 \
  go test ./pkg/controller -run '^TestScaleHealthVerification$' -count=1 -v
```

The full matrix is 1,000, 10,000 and 100,000 roots, each with 0, 1 and 10 Pods.
Every root has nine status samples and one healthy workload observation. At
100,000 roots this is 1,000,000 samples plus Pod mappings. The test deletes 10%
of roots and replaces another 10% under the same names with new UIDs, then
verifies that old identities have disappeared.

Run one case per process for a useful peak RSS measurement:

```sh
go build -o /tmp/karta-exporter-scale ./test/scale/cmd
/tmp/karta-exporter-scale -workloads=100000 -pods=1 -scrapes=3
```

The command validates a complete exposition before timing scrapes. Timed scrapes
include snapshot creation, OTel callbacks and aggregation, Prometheus gathering,
and text encoding. They write to a byte-counting response writer. The oracle and
fixture generation are outside the timed scrapes. Exposition byte counts must
remain equal on every measured scrape.

JSON reports samples, bytes, each scrape duration, allocation bytes and count per
scrape, GC cycles, retained heap after GC, and process peak RSS on Linux/macOS.
Peak RSS includes fixture generation and oracle verification; it is a process
high-water mark, not an incremental scrape allocation. Repeated cases in one
benchmark process cannot isolate peak RSS. `-race` runs are correctness checks,
not performance measurements.

Optional `-profile-dir=/tmp/karta-scale-profiles` records a CPU profile only during
timed scrapes and allocation profiles before and after them. Compare allocation
profiles to remove fixture construction and oracle costs:

```sh
go tool pprof -top -alloc_space -base /tmp/karta-scale-profiles/before.pb.gz \
  /tmp/karta-scale-profiles/after.pb.gz
go tool pprof -top /tmp/karta-scale-profiles/cpu.pb.gz
```

The controller scenario uses paginated loopback HTTP API responses, populated
informers, and real status/attribution evaluation. It checks every healthy root,
three collection-health rows, a page size of 1,000, and exact LIST counts. With
100,000 roots and 100,000 Pods, one pass issues 301 LISTs: 100 Job pages, 200 Pod
pages across two reads, and one Karta collection. No fallback owner GETs are
expected. It uses QPS 50, burst 100, and the default verification deadline. It
does not model a real API server, authorization, etcd, watch traffic, or network
latency. `BenchmarkScaleHealthVerification` uses the same opt-in selection.

Scrape benchmarks call the Prometheus handler directly and issue serial requests.
The small concurrent oracle test also calls this handler directly to stress SDK
and store synchronization. It bypasses the production server's admission limit
of one in-flight scrape, where overlapping requests receive HTTP 503. Benchmark
peak memory therefore describes serial scrapes, not unconstrained production
request concurrency.
