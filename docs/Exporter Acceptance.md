<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Metrics exporter acceptance

The [Ginkgo operator matrix](../karta-metrics-exporter/test/e2e/README.md) checks
real controller-created workloads against the running exporter. It covers root
and Pod identity, component coordinates, all supported phases, label updates,
and deletion. The guide lists exact commands and coverage limits.

Use the repository's [e2e provisioner](../hack/e2e/README.md) to install the pinned
operators into an isolated kind cluster. The matrix installs its own exporter
release with the Karta operator and CRD upgrader disabled.

Checks without an operator cluster run from the repository root:

```sh
make check-exporter
make exporter-rules-test
make helm-lint helm-validate
```

`check-exporter` includes component tests and envtest integration with a real
Kubernetes API server. Envtest does not run workload controllers.

The [scale harness](../karta-metrics-exporter/test/scale/runtime-scale.md) runs
larger populations against a modeled API. Those measurements are separate from
operator acceptance and do not establish production Kubernetes capacity.

[Earlier PoC acceptance results](metrics-exporter-lab/historical-poc/e2e/acceptance-2026-10-14.json)
remain available as historical evidence.
