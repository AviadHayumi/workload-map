<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Query tests

Run `make exporter-rules-test` from the repository root. These fixtures test the
[current query guide](../../../docs/karta-metrics-exporter/README.md) with promtool.
The recording rule is an example for monitoring configuration; the chart does
not install it.

Karta inputs use `pod_name`, `workload_name` and `observation_valid`. External CPU,
memory and GPU inputs retain `pod`. CPU and memory queries copy that label with
`label_replace`; GPU queries require a matching `pod_uid`. The configured `team`
label participates in attribution conflict detection and result aggregation.

The cases cover identical and conflicting replicas, stale mappings, invalid
observations, UID replacement, optional labels, CPU and memory units, missing GPU
telemetry, completed workloads, creation age and failure recovery windows.
Workload health rows are sparse failures. Their absence is not proof of complete
health history, and the low-activity query is not an idleness guarantee.
