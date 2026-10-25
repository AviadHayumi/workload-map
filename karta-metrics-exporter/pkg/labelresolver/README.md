<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Label resolver

`Config.Rules` is the `labelRules` list. Each rule names a label, its metrics,
ordered sources and expression. `Dependencies.Compiler` supplies the compiler.
`Config.Limits` supplies its evaluation bounds. The resolver will compile rules
at construction.

`Resolve` receives metric names, a workload object and an optional Pod object.
It returns one `ResolvedMetricLabels` per metric, with values and per-label errors.
Empty or failed values try the next source. Errors remain visible even when a
fallback succeeds. A top-level error reports cancellation or invalid input.
The resolver neither fetches objects nor owns a Kubernetes cache.

For a Pod metric, sources may be `workload`, `pod`, or both in that order.
A workload-only metric must reject a rule containing `pod`: there is no single
Pod to choose for the workload. Empty and failed values must not reuse old labels.

Runtime calls the resolver for relevant changes and saves the results in Store.
Collector reads those saved labels. Evaluation modes are not part of this
configuration.

`ValidateConfig` checks the rules at startup. Helm and the command accept the
configuration. Source selection, fallback and caching are not implemented yet.
