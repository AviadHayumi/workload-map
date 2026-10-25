<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter

The exporter maps Pods to their workload roots. Karta definitions add component,
instance, replica-group and status information. The exporter publishes identity,
status, creation time and observation health through Prometheus. Resource usage
comes from exporters such as DCGM and cAdvisor.

- [Metric contract](keps/0001-metrics-exporter/README.md)
- [Helm and runtime configuration](karta-metrics-exporter/configuration.md)
- [Prometheus queries](karta-metrics-exporter/README.md)
- [Implementation design](design/metrics-exporter/README.md)
- [Operator and scale testing](Exporter%20Acceptance.md)

Earlier GPU demonstrations and their results are preserved in the
[metrics lab](metrics-exporter-lab/README.md).
