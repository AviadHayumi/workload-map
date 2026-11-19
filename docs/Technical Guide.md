<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta technical guide

This guide describes the local `run.ai/v1alpha2` CEL PoC. These APIs are not in
released packages. Build from this checkout using the [PoC guide](design/cel-expressions/POC.md).

A Karta tells the SDK where a workload stores its pod specs, scale and status.
For example, a Job stores its template in `spec.template`. Given the Job as
`object`, `object.spec.template` returns that template. A write to `/spec/template`
changes that location in a private copy of the same Job.

## Components and ownership

Every definition has a root component with a full Kubernetes group, version and
kind, plus a `statusDefinition`. Core types use an empty group. Each child has a
unique name and an `ownerRef` naming its parent component. The owner graph must
reach the root without a cycle.

Components describe roles or repeated templates. Their value expressions always
read the original root workload. A child kind does not change that input into a
live child object. Use `additionalChildKinds` for generated resources needed by
an ownership walk but carrying no separate mapped values.

See [JobSet](catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml) for a generated Job
component and [PyTorchJob](catalog/kubeflow-org-pytorchjob-v1.yaml) for Pod roles.

## Read and write accessors

```yaml
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
```

`read.expression` returns a value. `write` declares where the caller may change
it. An accessor can have either side or both. Omit `write` for a projection,
computed total, immutable field or shared runtime value.

A write has exactly one of `path` and `pathExpression`. The fixed path is a JSON
Pointer. An empty path selects the workload root. A path expression returns one
pointer. Escape `~` as `~0` and `/` as `~1` inside each pointer segment.

Each side can have its own boolean `matchExpression`. Omission means true. A
false read match returns `resource.ErrValueNotFound`; whole-workload extraction
skips that field. A false write match rejects the batch before it changes the
object. Evaluation errors are errors, not false matches.

[KServe](catalog/serving-kserve-io-inferenceservice-v1beta1.yaml) uses different
matches: reading accepts a nonempty model selection, while writing requires
exactly one selected model. Its typed container read rejects multiple models.

## Expression inputs

| Expression | Inputs |
| --- | --- |
| Workload read, status and shared variable | `object`, `variables`, `references` |
| Write match and computed path | The read inputs, plus `instance` and `index` |
| Pod selector, filter and group key | One Pod as `object` |
| Reference lookup, label selector and eligibility match | Root workload as `object` |
| `fromReferences` target match | Root workload as `object`, referenced object as `target` |

The write value is never a CEL input. Expressions choose locations and read
values; they do not mutate objects or call Kubernetes.

Use optional access when a field can be missing:
`object.?status.?ready.orValue(0)`. A default applies to a missing field; it does
not automatically replace an explicitly stored null. Use a deliberate null
check when the workload schema allows null. For optional JSON values,
`orValue(dyn(null))` keeps the result dynamically typed.

Declare reusable workload expressions in `spec.variables` as `{name, expression}`
entries. The SDK validates references to variable names and rejects cycles.
Pod expressions cannot access workload variables or references.

## Pod specifications and instances

Choose one representation per component:

| Representation | Use |
| --- | --- |
| `podTemplateSpec` | A full template with metadata and spec |
| `podSpec`, with optional `metadata` | A bare Pod spec and separate metadata |
| `fragmentedPodSpecDefinition` | Separate accessors for resources, containers, labels or other fields |

For a repeated role, `instanceIds.read.expression` returns unique, nonempty IDs.
It is read-only. Every mapped field must return values in the same order and
with the same cardinality as the IDs. Do not filter one field independently.
A `componentInstanceSelector` reads the matching ID from each live Pod.

```yaml
instanceIds:
  read:
    expression: object.spec.replicatedJobs.map(job, job.name)
podSelector:
  componentInstanceSelector:
    read:
      expression: object.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"]
```

In the first expression, `object` is the JobSet. In the second, it is one Pod.
For an instance write, the SDK resolves its current `index` from the original
workload. JobSet uses that index in its template write pointer.

A `componentTypeSelector` distinguishes roles. Its `read` must produce a
nonempty selector value. When `value` is set, the extracted value must equal
it. A `replicaSelector` identifies repeated copies within a component.
Selectors classify Pods already associated with a workload; labels alone do
not prove workload ownership.

## Status and suspension

`conditionsDefinition.read` reads the root's conditions. The field-name settings
identify type, status, reason and message. `phaseDefinition.read` reads a phase
string. Status mappings can use `byConditions`, `byPhase` and `byExpression`.

Within one matcher, all supplied tests must pass. Matchers under a status are
alternatives. `byExpression.expression` must return a boolean; there is no
`expectedResult`. Several statuses may match. No match yields `Undefined`.

Only map states supported by controller source and observed objects. Check
stale conditions, `observedGeneration`, scale-to-zero and suspension transitions.
A successful schema check does not prove a status mapping. Record real state
transitions and retain replay tests. Mark synthetic fixtures as synthetic.

Suspend and resume use ordered actions with `write` and a JSON-encoded `value`:

```yaml
suspendDefinition:
  suspendActions:
    - write: {path: /spec/suspend}
      value: "true"
  resumeActions:
    - write: {path: /spec/suspend}
      value: "false"
```

The application calls `Suspend` or `Resume`. The SDK edits its private workload;
the application owns the Kubernetes update and its conflict handling.

## References and Pod membership

`spec.structureDefinition.references` declares a lookup or label-selected list
by `apiVersion`, `kind` and name. Namespaced lookups stay in the workload's
namespace. A reader adapter uses API discovery to handle cluster-scoped types.
The SDK can use a supplied `ResourceReader` or manually supplied reference
objects. These sources are mutually exclusive. `resource.WithReferencesEnabled(false)`
disables both for that factory.

[Trainer v2](catalog/trainer-kubeflow-org-trainjob-v1alpha1.yaml) reads an override
from the TrainJob and falls back to its ClusterTrainingRuntime. It selects the
trainer job by its label and the container by name. Runtime values stay
read-only. The [design](design/cel-expressions/README.md#references) shows both
reader and manual-object calls, including the defaulting snapshot limits.

A value reference does not assign Pods to a workload. A component opts into
additional ownership through `podSelector.fromReferences`. The membership SDK
checks target identity and owner chains across all claimants in one namespace.
It returns per-component Pods and accepted targets. Consumers retain native
results when collection is incomplete and fold targets only when the result is
complete, the target is foldable and its parent is displayed. See the
[Milvus definition](catalog/milvus-io-milvus-v1beta1.yaml) and [PoC guide](design/cel-expressions/POC.md).

## Scheduling instructions

`optimizationInstructions.gangScheduling` describes grouping for a consuming
scheduler. It does not create scheduler resources. The existing `podGroups`
format has members with `componentName`, ordered `groupByExpressions` and
boolean `filters`. Every expression evaluates one Pod. Group keys must be
nonempty scalar values. Filters are ANDed.

The `podGroup` form maps components to subgroups and can add topology constraints.
Keep the format used by the definition being extended. A format migration is a
separate change. Do not create a gang that requires executor Pods before the
creator Pod can run.

## SDK writes

Use `tree.Open`, then `Mutate(ctx, tree.Write{...})`. Each write names a component,
instance, built-in field and raw JSON Merge Patch value. Maps preserve unspecified
fields. Null map members delete. Arrays replace. A top-level null is rejected.

The editor resolves all destinations against the original object, rejects
overlapping targets, applies the batch to a private copy and rebuilds the tree.
It publishes the result only if every step succeeds. `GetResource` returns the
edited object. It does not write to Kubernetes.

The [README example](../README.md#update-workload-specs) and
[offline quickstart](examples/quickstart/) show a template patch that preserves
existing containers. Avoid converting a partial typed view back into a whole
workload subtree; that can lose operator-specific fields.

## Validation

From this checkout, run `kli validate definition.yaml` or the
[verification tool](../karta/sdk/hack/karta-verify/README.md). Validation checks
structure, accessor shape and expression scope. Then exercise the definition
against a real root object and its Pods. Check multiple instances, absent
optional fields, ambiguous selections and denied references.

Use the [authoring tutorial](Authoring%20Your%20First%20Karta.md) for a complete
Job definition. Use the [catalog](catalog/) for other workload shapes.
