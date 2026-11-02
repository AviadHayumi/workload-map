<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Grafana dashboards

These JSON files are the dashboards used by the exporter runtime demo.
Import them through Grafana's dashboard import page.

- `workloads.json`: workload status, Pod attribution, CPU, memory and simulated GPU measurements.
- `workload-structure.json`: workload, component, instance, replica-group and Pod graph.
- `metric-samples.json`: raw samples from the current exporter metric contract.

The Prometheus datasource UID is `karta-prometheus`. The overview and structure
views use the demo's existing `karta_demo:pod_workload_info`,
`karta_demo:workload_status`, `karta_demo:observation_healthy` and
`karta:pod_attribution:unique` recording rules. Importing JSON alone does not
install those rules. The raw-samples view uses exporter metrics directly.

GPU panels display simulated values. They do not establish real GPU utilization
or application idleness. Grafana access settings and credentials are not included.
