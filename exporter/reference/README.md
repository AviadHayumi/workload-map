<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Original PoC integration files

The restored [exporter](../README.md) uses a different configuration from the
newer [karta-metrics-exporter](../../karta-metrics-exporter/).

- `chart/` is the original chart snapshot, including exporter and native
  submitter-admission templates. The restored chart tests render this copy.
  The repository's current charts are unchanged.
- `operator/` preserves the older submitter webhook and tests as `.go.txt`
  files. These files are not compiled.
- `docs/` contains the original configuration, query guide and KEP.

The repository's root Makefile builds and tests the newer exporter. Use
`make -C exporter check` to check the restored PoC.
