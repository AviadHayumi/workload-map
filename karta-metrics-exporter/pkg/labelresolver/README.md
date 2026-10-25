<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Label resolver

`Config.Rules` is the `labelRules` list. Each rule names a label, its metrics,
ordered sources and expression. `Dependencies.Compiler` supplies the compiler.
`Config.Limits` supplies its evaluation bounds. `New(Config, Dependencies)` validates and compiles each rule once
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
configuration. Resolve creates fresh result maps on every call. Missing source
objects are per-label errors and permit fallback. Unknown or duplicate requested
metrics are invalid input. A canceled context stops resolution immediately.
The resolver supports concurrent calls with immutable input objects.

Workload evaluation results use a 1024-entry FIFO cache keyed by workload API version,
kind, UID, resourceVersion and compiled rule. The Kubernetes object contents must
be unchanged for the same API representation, UID and resourceVersion. Missing identities or versions
bypass the cache. Deterministic evaluation errors remain available for fallback;
cancellation errors are never cached. Pod results are evaluated separately.
Concurrent cache misses can evaluate independently, without holding the cache
lock during CEL execution.

The OTel scope label names `otel_scope_name`, `otel_scope_version` and
`otel_scope_schema_url` are reserved along with the metric's built-in labels.
