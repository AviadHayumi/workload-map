<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Original metrics exporter PoC

This directory restores the PoC removed in commit `6accdcf5`. It includes Pod
ownership discovery, Karta inventory, status and component mapping, submitter
CEL extraction, Prometheus and OTLP delivery, tests and lab examples.

The newer implementation remains in [karta-metrics-exporter](../karta-metrics-exporter/).
The repository's root Makefile builds that implementation. Build and test this
PoC from its own directory:

```sh
cd exporter
make build
make check
./bin/karta-exporter --help
```

The restored module uses the current local Karta SDK. No release image is
published by these commands.

- [PoC configuration](reference/docs/configuration.md) and
  [queries](reference/docs/README.md).
- [Original chart](reference/chart/): the PoC's Helm values and templates.
  The charts at the repository root configure the newer implementation.
- [Submitter extractor](pkg/submitterextractor/README.md).
- [Connected acceptance](test/connected/README.md): run against a disposable cluster.
- [Scale harness](test/scale/runtime-scale.md): measure the executable against a modeled API.

Recorded results describe their original runs. Restoring them does not repeat
those cluster or scale tests.
