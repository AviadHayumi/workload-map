<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Label resolver

`New(Config)` validates and compiles `labelRules` once. Each rule names a label,
its metrics, ordered sources and expression. `Config.Limits` bounds evaluation.

`ForWorkload` receives an immutable full workload object and returns a function
that resolves one metric and an optional full Pod object:

```go
resolve := resolver.ForWorkload(workload)
result, err := resolve(ctx, "karta_pod_workload_info", pod)
```

`Result.Labels` contains values and `Result.Errors` contains per-label errors.
Empty or failed values try the next source. Errors remain visible when a fallback
succeeds. Missing source objects are per-label errors and permit fallback.
Cancellation or an unknown metric returns a top-level error.

For Pod metrics, sources may be `workload`, `pod`, or both in either order.
Workload status rejects a rule containing `pod` because no single Pod represents
the workload. Built-in metric labels and the OTel scope labels are reserved.

The controller retains the prepared function with its cached workload object and
replaces both when the workload changes. Each workload expression runs only when
its source is reached. Its result is reused across metrics and Pods in that scope.
Deterministic errors and empty values are reused; cancellation is never cached.
Pod expressions run separately for each call. There is no global workload cache.

Calls to a prepared function must be serialized. Separate workload scopes may run
concurrently. Each call returns fresh result maps. The workload object must remain
unchanged for the lifetime of its scope, and the Pod must remain unchanged during
evaluation. The resolver does not fetch Kubernetes objects.

The controller stores resolved labels with observations. The collector reads those
saved labels without evaluating expressions.

## Verify

From the exporter module directory:

```sh
go test -race ./pkg/labelresolver
go test -run '^$' -bench BenchmarkWorkloadLabels -benchmem ./pkg/labelresolver
```

Tests cover ordered fallback, retained errors, fresh result maps, cancellation,
independent scopes and one workload evaluation shared across 1000 Pods. The
benchmark exercises a workload expression over a list for unchanged workloads and
new workload scopes. No Kubernetes API server is required.
