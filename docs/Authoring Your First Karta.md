<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Authoring your first Karta

This tutorial uses the local `run.ai/v1alpha2` CEL PoC. Build `kli` from this
checkout using the [PoC guide](design/cel-expressions/POC.md). Released packages
and `@latest` do not contain these APIs.

A Job stores its pod template in `spec.template` and its requested concurrency
in `spec.parallelism`. A Karta exposes those values through the same SDK used
for other workload types. This example Job requests two Pods:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: example
spec:
  parallelism: 2
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: worker
          image: ghcr.io/example/worker:v1
```

The evaluator receives this whole Job as `object`.
`object.?spec.?parallelism.orValue(1)` returns `2`.
`object.spec.template` returns the template, including its container.

## Write the definition

Create `job.yaml` with this definition:

```yaml
apiVersion: run.ai/v1alpha2
kind: Karta
metadata:
  name: example-batch-job
spec:
  structureDefinition:
    rootComponent:
      name: job
      kind:
        group: batch
        version: v1
        kind: Job
      specDefinition:
        podTemplateSpec:
          read:
            expression: object.spec.template
          write:
            path: /spec/template
      scaleDefinition:
        replicas:
          read:
            expression: object.?spec.?parallelism.orValue(1)
          write:
            path: /spec/parallelism
      statusDefinition:
        conditionsDefinition:
          read:
            expression: object.?status.?conditions.orValue([])
          typeFieldName: type
          statusFieldName: status
          reasonFieldName: reason
          messageFieldName: message
        statusMappings:
          initializing:
            - byExpression:
                expression: >-
                  object.?status.?active.orValue(0) > 0 &&
                  object.?status.?ready.orValue(0) == 0
          running:
            - byExpression:
                expression: >-
                  object.?status.?active.orValue(0) > 0 &&
                  object.?status.?ready.orValue(0) > 0
          completed:
            - byConditions:
                - type: Complete
                  status: "True"
          failed:
            - byConditions:
                - type: Failed
                  status: "True"
```

`run.ai/v1alpha2` is the Karta API. The root's `batch/v1` identifies the workload
it describes. Every root needs a full group, version and kind, plus a
`statusDefinition`. Core kinds such as Pod use an empty group.

`read.expression` reads a value with CEL. `write.path` is a JSON Pointer into the
same Job. The read and write declarations are independent. Removing `write`
makes the accessor read-only. For example, a computed replica total should
usually remain read-only.

The optional access syntax handles missing fields. A Job without `status.active`
reads as zero in these status expressions. This differs from an explicitly
stored null; choose defaults that match the workload's schema and controller.

A status expression returns a boolean. `byConditions` checks real conditions
written by the controller. Within a matcher, tests are ANDed. Matchers under a
status are alternatives. No match yields `Undefined`.

This small example does not map suspension. The shipped
[Job definition](catalog/batch-job-v1.yaml) also maps suspension and declares
suspend/resume writes. Copy supported behavior from the catalog, then verify
that it matches the operator version being added.

## Read and write separately

A field can have different `read.matchExpression` and `write.matchExpression`
predicates. A false read match skips that field during whole-workload extraction.
A false write match rejects the batch. An omitted match means true.

For a write, the SDK applies a raw JSON Merge Patch to the declared location.
For example, writing `{"spec":{"schedulerName":"example-scheduler"}}` to the
`podTemplateSpec` accessor changes only that template field. Existing containers
remain present. Arrays replace whole arrays; null map members delete fields.
A top-level null patch is rejected.

The application uses `tree.Open`, `Mutate` and `GetResource`, as shown in the
[README](../README.md#update-workload-specs). The SDK changes a private object.
The application owns any Kubernetes update.

## Add structure when needed

A Job needs only its root. For a workload with several roles, add named children
and set each `ownerRef` to its parent component. A child's value expressions
still read the root workload. They do not read a generated child resource.

Choose one pod representation per component:

- `podTemplateSpec` for a full template.
- `podSpec`, with optional `metadata`, for a bare Pod spec.
- `fragmentedPodSpecDefinition` for fields stored in separate places.

Repeated roles use `instanceIds.read.expression` and a
`componentInstanceSelector.read.expression`. IDs and extracted values must keep
the same order and length. Test at least two instances and one missing optional
field. [JobSet](catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml) is a complete example.

Pod selectors and `groupByExpressions` evaluate one Pod as `object`. They cannot
read workload variables or references. Use actual controller labels and make
role selectors mutually exclusive. The [technical guide](Technical%20Guide.md)
explains grouping, external references and optional Pod membership.

## Validate and prove the behavior

Run the local CLI:

```bash
kli validate job.yaml
```

Validation checks schema shape, required fields and CEL expressions. It also
rejects unknown fields and duplicate YAML keys. A valid definition can still
read the wrong object field, so test extraction separately.

Save the example Job as `workload.yaml`. From `karta/sdk/`, run:

```bash
go run ./hack/karta-verify \
  --karta /path/to/job.yaml \
  --workload /path/to/workload.yaml
```

The verification tool reports child extraction and root status. This definition
has no children. Check its root pod template and scale through the Component
API, as in the [offline quickstart](examples/quickstart/). Do not interpret an
empty child list as proof that the root scale was tested.

Before adding a catalog definition, check real controller source and record
its state transitions. Test creation, updates, stale status, suspension and
terminal states when the operator supports them. Keep source-based predictions,
synthetic tests and live recordings distinct. Do not invent condition names or
broaden an expected result to hide a failed mapping.

The offline quickstart exercises reads and raw writes without a cluster:

```bash
cd docs/examples/quickstart
go run . --print-mutated
```

For a definition that requires explicit opt-in, set `spec.experimental: true`.
It remains available through the SDK. Consumers such as the CLI, Headlamp and
exporter exclude it until their experimental option is enabled.

See the [catalog](catalog/) for KServe, Trainer v2, Milvus and other workload
shapes. See the [technical guide](Technical%20Guide.md) for the full input scopes
and write rules.
