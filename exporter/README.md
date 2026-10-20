<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter

The exporter resolves Pod ownership, evaluates selected Karta definitions and
publishes workload metadata through OpenTelemetry. It runs independently of the
Karta operator. Submitter admission is installed separately by Helm.

- [Implementation design](../docs/design/metrics-exporter/README.md): components,
  ownership examples, events and failure behavior.
- [Configuration](../docs/karta-metrics-exporter/configuration.md): Helm values
  and selection rules.
- [Behavior checks](../docs/design/metrics-exporter/behavior-checks.md): verification
  results and remaining limits.
- [Connected acceptance](test/connected/README.md): run the command against a
  disposable cluster.
- [Scale harness](test/scale/runtime-scale.md): measure the connected runtime
  against a modeled API.

Run `make build` or `make check` from this directory. The implementation is a PoC;
no release image is published by these commands.
