<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Label evaluator

The compiler turns one CEL expression into an `Evaluator`. Pass a full Pod or
workload object to `Evaluate` and get a string back. It reads the supplied object
without changing it or calling Kubernetes.

```go
compiler := labelevaluator.NewCompiler()
evaluator, err := compiler.Compile(labelevaluator.Config{
    Expression: `object.spec.schedulerName`,
})
// Handle err before calling Evaluate.
value, err := evaluator.Evaluate(ctx, pod)
// A Pod with spec.schedulerName: example-scheduler returns "example-scheduler".
```

CEL sees `object`, including apiVersion, kind, metadata, spec and status. Guard
optional fields with `has(...)`. Known non-string expressions fail compilation.
Dynamic results, such as `object.spec.schedulerName`, are checked during evaluation.
Empty results mean no label value. An empty expression compiles to an evaluator
that returns an empty string; configured label rules require an expression.

The compiled evaluator is safe for concurrent calls. Callers must not change the
input object during evaluation.

## Configuration

```yaml
exporter:
  labelRules:
    - name: team
      metrics: [karta_pod_workload_info, karta_workload_status]
      sources: [workload]
      expression: |
        has(object.metadata.labels) && "example.com/team" in object.metadata.labels
          ? object.metadata.labels["example.com/team"] : ""
  labelLimits:
    costLimit: 10000
    maxExpressionBytes: 4096
    maxValueBytes: 1024
    interruptCheckFrequency: 100
```

`labelRules` defaults to `[]`. Rules target Pod info or workload status. A Pod
source is allowed only for Pod info. Sources run in listed order until a nonempty
value is found. The resolver will retain per-source evaluation errors, including
when a fallback succeeds. Built-in labels cannot be replaced.

The evaluator and startup rule validation work today. Resolver execution,
Runtime evaluation and metric delivery remain scaffold work.

Helm mounts these two sections as a config file. Outside Helm, use the same file
without the `exporter:` wrapper and pass `--config path.yaml`.

| Config key | Flag | Environment variable |
| --- | --- | --- |
| Config path | `--config` | `KARTA_EXPORTER_CONFIG` |
| `labelLimits.costLimit` | `--label-cost-limit` | `KARTA_EXPORTER_LABEL_COST_LIMIT` |
| `labelLimits.maxExpressionBytes` | `--label-max-expression-bytes` | `KARTA_EXPORTER_LABEL_MAX_EXPRESSION_BYTES` |
| `labelLimits.maxValueBytes` | `--label-max-value-bytes` | `KARTA_EXPORTER_LABEL_MAX_VALUE_BYTES` |
| `labelLimits.interruptCheckFrequency` | `--label-interrupt-check-frequency` | `KARTA_EXPORTER_LABEL_INTERRUPT_CHECK_FREQUENCY` |

Explicit flags override environment variables, then YAML, then defaults. An empty
numeric override is an error. `--config=""` skips a path set through the environment.
Zero limits use compiler defaults, except an explicit zero interrupt frequency
in configuration is rejected. Negative limits are rejected. Rules stay in YAML.

## Migrating from submitter extraction

The dedicated `submitterextractor` package, `exporter.attribution` values,
`--submitter-*` and `--submitted-by-expression` flags are removed. Replace their
environment variables with the label limit variables above.

A submitter can be an ordinary configured label:

```yaml
exporter:
  labelRules:
    - name: submitted_by
      metrics: [karta_pod_workload_info, karta_workload_status]
      sources: [workload]
      expression: |
        has(object.metadata.annotations) &&
        "karta.run.ai/submitted-by" in object.metadata.annotations
          ? object.metadata.annotations["karta.run.ai/submitted-by"]
          : (has(object.metadata.labels) &&
             "example.com/submitted-by" in object.metadata.labels
               ? object.metadata.labels["example.com/submitted-by"] : "")
```

The old variables `labels`, `annotations` and `resource` are replaced by fields
under `object`. GVK is available as `object.apiVersion` and `object.kind`.
There is no special identity agreement, authentication or inheritance behavior.
