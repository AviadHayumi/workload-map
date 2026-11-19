<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Pod membership from references

A Milvus Pod can belong to an internal MinIO Deployment that has no owner edge
to the Milvus object. A reference reads that Deployment. Explicit membership
also lets its owned Pods appear under the Milvus storage component.

```yaml
podSelector:
  componentTypeSelector:
    read:
      expression: '"storage"'
    value: storage
  fromReferences:
  - name: minioDeployments
    matchExpression: >-
      target.?metadata.?annotations[?"meta.helm.sh/release-name"].orValue("")
      == object.metadata.name + "-minio"
```

The complete Milvus example also checks release namespace, Helm management,
external-storage eligibility and hooks. A constant component selector is safe
only after the resolver proves membership through the selected target's UID.
It is never an independent workload selector.

`Reference.matchExpression` runs on the original workload before the lookup or
list expression. False skips that reference. Each `fromReferences` match sees
the original workload as `object` and one resolved resource as `target`.
The component selector then sees one Pod as `object`.

## Calling the SDK

```go
input := membership.Input{
    Workloads: []membership.WorkloadInput{
        {Object: milvus, Definition: milvusKarta},
        // Include every other potential parent in this namespace.
    },
    Pods: pods,
    ScopeComplete: true,
}
result, err := membership.Resolve(ctx, input, membership.WithReader(reader))
```

The caller collects the original workload population and Pods. The reader gets
reference targets and intermediate owners through the caller's credentials.
Set `ScopeComplete` only after all required workload and Pod lists succeed.
One call covers one namespace; an all-namespaces caller makes separate calls.

For an offline observation, pass `WithSnapshot(membership.Snapshot{...})`.
Its `References` map is keyed by workload UID and contains
`references.ResolvedReferences`. `Owners` holds the intermediate objects.
`Complete` certifies that supplied target and owner inventory is complete.
There is no reader fallback. Reader and snapshot options are mutually exclusive.

References default to enabled. `WithReferencesEnabled(false)` disables extra
membership and reference reads. Ordinary native components retain their owned
Pods. Reference-only components cannot take native Pods through their constant
selectors. Pass the same option to a separate value factory when needed.

## Ownership and results

The resolver checks target GVK, namespace, name or list selector, and UID.
Membership targets must be namespaced non-Pod objects. Value references may be
cluster-scoped and may reference Pods.

The owner walk follows controller edges and validates each intermediate UID.
The last direct edge to a selected target may have `controller: false`, as in
Strimzi's PodSet-to-pool relationship. Traversal has a depth bound and cycle
checks. Labels never replace this owner chain.

`Result.Pods` is keyed by workload UID and component name. Passing a union of
these sets back through broad component selectors would lose the boundary.
`NativePods` retains ordinary attribution. `Targets` contains accepted target
identities, their parent, component names and a `Foldable` flag. Diagnostics
explain omitted evidence or conflicts.

Two valid claims for one Pod or target reject all involved target associations.
Unrelated targets remain usable. An independent native workload is a boundary,
including when the target has zero Pods. The target's own native subtree can
move beneath its accepted parent. Reference chains do not recursively group
independent workloads.

Any membership expression, target validation or required read failure disables
additional attribution and folding for that namespace. Native results remain.
An invalid definition fails validation; cancellation aborts. Value-only reference
failures remain separate from membership.

## CLI behavior and operator cases

`kli describe` collects potential claimants before rendering the requested root.
It uses per-component Pod sets while retaining the original object for value and
status reads. `kli get` without a type forms claims before display filters. It
folds only an exact accepted target with a displayed parent and complete observed
descendant coverage. A successful empty Pod list can satisfy coverage; a failed
list cannot. Explicit typed queries keep dependency rows visible.

| Operator | Membership behavior |
| --- | --- |
| Milvus | Internal Helm dependencies become explicit targets. External, disabled, unselected and hook resources are excluded. |
| Strimzi | Current namespace/cluster-labeled pools are targets. PodSet owner UIDs connect Pods to pools; status names are not used. |
| Flink | A shared session cluster is value-only and keeps its own row. |
| Trainer | A shared runtime is value-only. Separate TrainJob -> JobSet -> Job chains keep job Pods separate. |
| KServe | Ordinary ownership is proved before predictor/transformer labels divide Pods. |
| Deployment | Optional health references do not broaden native Pod membership. |

The monitoring addon exercises synthetic Prometheus, PrometheusAgent,
Alertmanager and ThanosRuler ownership paths. It needs recorded operator evidence
before being advertised as a supported builtin integration.
