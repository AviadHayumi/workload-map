<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# MIG metric validation notes

The initial full-GPU observer rejected MIG allocations. The observer was then
extended and tested against two real A100 3g.20gb partitions, with one compute
instance per GPU instance. Native GR-engine activity and Pod UID joins were
confirmed. See `README.md`, `acceptance.json`, and the corrected-interval
repeat under `interval-fixed/` for the current implementation and evidence.

DCGM exposes instance labels such as `GPU_I_ID` and `GPU_I_PROFILE` for MIG.
The parent GPU UUID alone is insufficient to identify each partition. Query
instance samples without adding parent aggregates to their totals. Preserve
Pod UID for the join to Karta. GPU instance IDs can be reused after geometry
changes, so observation windows must restart when allocation identity changes.

The verified per-instance compute activity query is:

```promql
DCGM_FI_PROF_GR_ENGINE_ACTIVE{job="karta-real-dcgm-native",GPU_I_ID!="",GPU_CI_ID!=""}
```

This is a ratio, not a percent. GPU_UTIL was absent for these MIG instances. Missing
or unsupported utilization must not be interpreted as zero. Temperature and
power repeated across both GPU instance labels in this cluster. Deduplicate
by node and physical UUID; these samples do not measure independent partition
power or temperature.

References:
- [NVIDIA exporter label and entity reference](https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html)
- [NVIDIA MIG profiling examples and normalization](https://docs.nvidia.com/datacenter/dcgm/latest/learn/modules/profiling.html)
- [NVIDIA A100 mode activation and reset prerequisites](https://docs.nvidia.com/datacenter/tesla/mig-user-guide/getting-started-with-mig.html)
