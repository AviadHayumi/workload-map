<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter scale checks

[Runtime scale checks](runtime-scale.md) run the actual exporter command against a modeled Kubernetes API. They exercise informer traffic, workqueues, verification and metrics. They remain separate from real-cluster acceptance.

The tests below measure immutable publications and the Prometheus HTTP handler. They exclude informer ingestion, API latency and Prometheus ingestion. Their results do not establish cluster capacity.

Run from `exporter/`:

```sh
KARTA_SCALE_WORKLOADS=1000 KARTA_SCALE_PODS_PER_WORKLOAD=0,1,10 \
  go test ./pkg/collector -run '^TestScalePrometheusExposition$' -count=1 -v

go test -race ./pkg/collector ./pkg/store -count=1

KARTA_SCALE_WORKLOADS=10000 KARTA_SCALE_PODS_PER_WORKLOAD=1 \
  go test ./pkg/collector ./pkg/store -run '^$' -bench '^BenchmarkScale' \
  -benchtime=1x -benchmem -count=1
```

Each root contributes nine status samples and one creation timestamp. Each Pod contributes one mapping. Healthy workloads have no workload-health row. The fixture has one resource-type health row.

The collector tests check identities, phases and coordinates before and after deletion and same-name UID replacement. The store tests check publication lifetime and atomic capture under concurrent writes. Race runs check correctness, not performance.

For an isolated exposition measurement:

```sh
go build -o /tmp/karta-exporter-scale ./test/scale/cmd
/tmp/karta-exporter-scale -workloads=10000 -pods=1 -scrapes=3
```

The command verifies every sample through an independent streaming oracle before timing scrapes. Timings include capture, OTel aggregation, Prometheus gathering and encoding. Fixture construction and oracle parsing are reported separately. One case per process gives a meaningful peak RSS.

Use `-profile-dir=/tmp/karta-scale-profiles` to record CPU and allocation profiles. Peak RSS includes setup and validation. It is not retained controller memory.

Historical results under `results/` belong to their recorded source revisions. They do not validate the current runtime or publication implementation.
