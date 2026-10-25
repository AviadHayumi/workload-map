<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Legacy exporter reference

The active exporter module is `karta-metrics-exporter/` at the repository root.
The older `exporter/` implementation, examples, fixtures and recorded results
remain available while its behavior is ported to the component contracts.
The root Makefile builds and tests the active module.

This directory preserves superseded integration files:

- `chart/` contains the earlier exporter and submitter-admission templates,
  helper definitions and values. Helm does not render these files.
- `operator/` contains the removed submitter webhook and its tests. Go source
  files use a `.go.txt` suffix so they are not compiled.
- `docs/` contains the earlier query guide, configuration and KEP text. Their
  relative links describe the original locations. Current contracts live under
  `docs/keps/` and `docs/karta-metrics-exporter/` at the repository root.

Dedicated submitter identity, admission capture, configuration migration and
phase selection are absent from the active scaffold contract. These references
are historical evidence rather than supported configuration.
