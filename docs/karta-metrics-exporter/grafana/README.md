<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Grafana dashboards

Import these JSON files into Grafana with Prometheus datasource UID `karta-prometheus`.

- `workloads.json`: workload phases, Pods, CPU, memory, GPU measurements and exporter health. All saved sections are retained, including expandable device diagnostics and PromQL.
- `workload-structure.json`: workload, component, instance, replica-group and Pod graph, alongside GPU activity and simulator panels.
- `metric-samples.json`: raw samples from the current exporter metric contract.

The layouts come from the [saved playground dashboards](https://github.com/AviadHayumi/workload-map/blob/5d0661ab19a6830625f26f77faf51cbe3ea5964f/docs/metrics-exporter-lab/playground-dev/recreate/assets/dashboard.json).
Queries use the current runtime demo's `karta_demo:pod_workload_info`,
`karta_demo:workload_status`, `karta_demo:observation_healthy` and
`karta:pod_attribution:unique` recording rules. Importing JSON does not install these rules.

Workload, Pod, CPU, memory and simulated GPU panels use the running demo's data.
GPU requests are requested capacity. The normalized DCGM-to-Pod join uses Pod names;
name reuse remains a limitation. Simulated readings do not prove physical GPU work
or application idleness.

The demo GPU randomizer runs every minute and chooses new workload settings every
10 minutes: 0-3% or 60-95% simulated utilization. The workload observer verifies
Pod UIDs and complete device allocations. Low activity requires every assigned
GPU to remain at or below 5% for 180 seconds. Missing evidence stays Unknown.

The overview includes a utilization graph and activity timeline for each workload.
Click a workload to open its structure, utilization history and simulation settings.
The structure page places utilization and activity history directly below the graph.
These panels require the separate GPU observers and randomizer; importing JSON
alone does not install them. The workload creation CronJob runs independently.

Grafana authentication settings and credentials are not included.
