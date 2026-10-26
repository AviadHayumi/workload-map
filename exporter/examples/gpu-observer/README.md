<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Direct GPU activity observer example

This standalone example polls one node's DCGM endpoint and exposes device-level
gauges at `/metrics`. It does not change Karta's metrics, Helm values, or operator.
It does not use a Prometheus recording rule to compute device activity.

```sh
go run ./examples/gpu-observer \
  --dcgm-url=http://dcgm.example:9400/metrics \
  --node=example-gpu-node --source=dcgm
```

Use one verified node endpoint. Do not point this example at a Service balancing
multiple per-node exporters: each response would cover a different device set.
The configured node must identify that endpoint's actual node. DCGM `Hostname`
is not used because some synthetic exporters put their own Pod name there.

| Metric | Meaning |
| --- | --- |
| `gpu_observer_kernel_inactive_ratio` | `1 - GPU_UTIL / 100`, for a fresh valid sample. |
| `gpu_observer_low_activity` | One if every sample in the full window is at or below the threshold; zero otherwise. Omitted until the full window is observed. |
| `gpu_observer_observation_healthy` | One for a fresh valid device sample. Zero with a bounded reason if unknown. |
| `gpu_observer_source_healthy` | Fresh successful direct source response containing GPU samples. |
| `gpu_observer_window_seconds` | Configured window, default 300 seconds. |
| `gpu_observer_threshold_ratio` | Configured threshold, default 0.05. |

The default poll interval is 10 seconds and maximum age/gap is 25 seconds. An
explicit demonstration can use `--window=60s`; show this setting in the dashboard.
The observer does not extrapolate a partial window. Missing samples, source
failures, stale or repeated explicit timestamps, and reported assignment changes
reset continuity. Known missing devices expose unhealthy observations for at most
the larger of twice the window or five minutes, then expire. Source health remains
visible. Retention uses local receipt time, never a source-controlled timestamp.
State is capped at 1024 device identities per observer; overflow fails the source
closed instead of publishing a silently partial result. This is a bounded example,
not a fleet-scale discovery service. Invalid numbers, DCGM error sentinels, conflicting duplicates and ambiguous
device assignments produce no inferred activity. Identical duplicates collapse.

[NVIDIA documents the utilization field](https://docs.nvidia.com/deploy/nvml-api/latest/api/structnvmlUtilization__t.html)
as kernel activity over a sampling period. `GPU_UTIL` measures the fraction of its sampling period during which kernels
execute. Its complement does not measure queued work, application idleness,
useful work, available scheduling capacity, or reclaimable GPUs. The observation
window operates on periodically collected source samples, not continuous hardware
events. Fresh HTTP responses cannot prove the upstream hardware collector itself
is fresh when its metrics have no sample timestamp.

Device labels preserve `UUID`, `gpu`, `GPU_I_ID`, `GPU_CI_ID`, configured `node`,
and `source`. Partition identity is preserved, but this example deliberately
suppresses derived GPU_UTIL metrics for MIG rows because it cannot establish
the counter's instance scope. It does not claim MIG or time-sharing support.

Allocation labels are only source hints. `allocation=reported` means the source
provided a namespace, Pod or container; `none_reported` means none was provided;
`ambiguous` means conflicting samples were seen. None proves ownership or free
capacity. `reported_namespace`, `reported_pod`, and `reported_container` are
deliberately separate from trusted Karta identity labels. No Pod UID is invented.
Never join these hints to current workloads as proof of identity: name-only DCGM
data can outlive a deleted Pod and refer to a reused name. This example emits
device activity, not workload idleness. A source carrying trustworthy Pod UID or
an independent device allocation binding is required for that stronger claim.

For fake GPU sources use `--source=synthetic` and clearly label every chart.
Keep actual CPU/memory measurements separate from simulated GPU observations.

Tests cover window warmup, activity transitions, source failures, missing devices,
bounded retention, name changes, conflicting samples, invalid numbers, explicit
timestamps, partitions and concurrent collection:

```sh
go test -race ./examples/gpu-observer
```

Build from the repository root with `exporter/examples/gpu-observer/Dockerfile`.
The process requires no Kubernetes API permission or service-account token.
