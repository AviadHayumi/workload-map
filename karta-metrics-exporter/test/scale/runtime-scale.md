<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter executable scale checks

The harness launches the supplied exporter binary against a loopback HTTP fixture.
The fixture serves Kubernetes discovery, paginated LISTs, GETs and streaming
WATCH events. The binary runs its own cache, discovery, SDK evaluation, label
resolution, publication and OTel collection. No Store records are injected.
This models API responses. It does not measure etcd or production API capacity.

Each root is a Job. Each Job owns a ReplicaSet that owns its Pods. Intermediate
ReplicaSets are available through GET; only root and Pod watches are expected.
The bundled Job definition supplies the single `job` component. The independent
oracle checks every root and Pod UID, component coordinates, creation timestamp,
all nine phase values, custom `team` labels and observation health. Root phases
change from Running to Failed. Pod label updates change a visible public value.
Healthy WATCH delivery does not require a subsequent LIST to pass.

Stages are cold start, steady scrapes, a single Pod label update, root/Pod churn,
partial deletion, same-name recreation with new UIDs, delete-all and optional
settling. The oracle rejects old UIDs after recreation and any workload rows
after deletion. A steady or continuity scrape failure remains a failed run even
after recovery. `--quiet-seconds` and `--continuity-seconds` check every periodic
quiet scrape. Sustained churn first primes stable public phase values, then
changes root resource versions while an independent periodic oracle checks all
rows. `--refresh-seconds` sets an explicit refresh interval for bounded TTL
regression runs. `--watch-faults` injects Pod WATCH EOF and Job WATCH 410 before
checking reconnection and a fresh visible Pod update.

```sh
(cd karta-metrics-exporter && go build -o /tmp/karta-exporter ./cmd)
python3 karta-metrics-exporter/test/scale/runtime_scale.py \
  --binary /tmp/karta-exporter --roots 10 --pods 1 \
  --settle-seconds 0 --poll-seconds 0.2 \
  --output /tmp/karta-scale-smoke-10-1
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s karta-metrics-exporter/test/scale -p runtime_scale_test.py -v
```

Runs use fresh child processes and `GOMAXPROCS=4`. Start with 10 and 1,000 roots.
Stop other benchmark processes and local kind clusters before larger runs.
Run 10k, then 100k roots with 0, 1 and 10 Pods each. Separately run one root
with 100k Pods. The default RSS cap is 8 GiB. The 100k/10 scenario needs an
explicit `--max-rss-mib 16384`; its predeclared cold-start target is 300 seconds.
The monitor samples once per second and kills the child on RSS or wall limits.
This is a sampled limit, not an operating-system memory limit.

## Measurements and gates

`result.json` includes binary/harness hashes, configuration, request counts by
resource/operation/representation, WATCH delivery, response sizes, sampled RSS
and CPU time, and per-stage convergence. Pure HTTP request/transfer time appears
as `http_seconds`. The offline streaming oracle appears as `oracle_seconds`.
`scrape_seconds` includes both. Temporary response files avoid retaining a full
large exposition in Python memory.

Process CPU comes from `ps`, outside the exporter process. Go heap and allocation
samples are recorded only when the executable exposes the corresponding runtime
metrics. Their absence is not zero. Sampled RSS is not live heap. Polling-stage
convergence includes transfer/oracle cost and is not per-event publication latency.
Queue backlog and drain time require the test diagnostics wrapper below.

The following targets are predeclared engineering gates on the 48 GiB, 14-CPU
host. A missing measurement leaves its gate unverified.

| Measurement | 100k roots, 0/1 Pod | 100k roots, 10 Pods |
| --- | --- | --- |
| Sampled RSS | <=8 GiB | <=16 GiB |
| Cold start | <=180 s | <=300 s |
| Pure HTTP scrape | <=25 s | <=25 s |
| Oracle deadline | 60 s | 120 s |
| Publication p99 at 100 events/s for five minutes | <=5 s | <=10 s |
| Queue drain | <=30 s | <=60 s |
| Final three-minute live heap growth | <=5% | <=5% |
| Delete-all live heap | baseline + max(256 MiB, 10% peak) | same |

`complete` reports correctness of the staged oracle. `measured_gates` separately
reports observed performance limits. A short smoke run cannot satisfy the
five-minute churn duration gate.

## Test diagnostics wrapper

`test/scale/cmd` calls production `app.New` and `Application.Serve`. It adds a
separate loopback-only diagnostics listener. The normal command has no diagnostic
endpoints. Run a small case through the normal command as a separate composition
check before using the wrapper for measurement.

```sh
(cd karta-metrics-exporter && go build -o /tmp/karta-exporter-scale ./test/scale/cmd)
python3 karta-metrics-exporter/test/scale/runtime_scale.py \
  --binary /tmp/karta-exporter-scale --diagnostics --roots 1000 --pods 1 \
  --churn-seconds 300 --churn-rate 100 --max-seconds 900 \
  --output /tmp/karta-scale-instrumented-1000-1
```

The wrapper records `runtime.ReadMemStats` and named client-go queue metrics.
Queue metrics include depth, additions, retries, work/queue duration count, sum
and maximum. Drain requires two diagnostic samples with zero queued and active
work. Active work is observed queue starts minus completed durations. Publication
polls read exact root resource versions from `Store.Published`; they do not scan
or format the complete metric output.

Sustained churn rotates through up to 100 independent roots. Each root has at
most one pending event, so a later generation cannot falsely acknowledge an
older one. A slow publisher reduces achieved event rate, which is reported and
checked within 1% of the requested rate. Reported publication latency is an upper
bound from just before event emission to the first matching diagnostic response.
It includes polling and diagnostic transport. It is not an exact publication
timestamp. Diagnostic traffic and CPU are part of the measured wrapper overhead.

During the final three minutes, explicit test GC requests sample retained heap
at 30-second intervals. These pauses are included in observed latency. A final
GC after delete-all compares retained heap with the pre-worker GC baseline.
Total allocation, heap objects and GC counts are sampled too. The production
command does not force these collections. The wrapper is an instrumented
production composition, not an unmodified production binary.

Large scenario gates remain unverified until their full runs complete.

## Historical evidence

The historical runtime at `63d74fc` passed 100k roots with one Pod at 7.072 GiB
sampled RSS and 127 seconds cold start. Its 37-38 second scrape timing included
the Python oracle. The 100k roots/10 Pods run failed above its 8 GiB cap before
complete output. That failure remains part of the evidence.

The exact old baseline at `01352db5` is retained separately. Its old configuration
and submitter semantics differ from this harness. A small baseline smoke or an
isolated Store benchmark is not a matched full-runtime performance comparison.
The separate k8s-workload-discovery project is suitable for an ownership-walk
comparison only; its full command has different publication semantics.
