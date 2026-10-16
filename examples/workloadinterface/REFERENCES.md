<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Decide using a referenced resource

A workload may depend on an object outside its ownership tree.
For example, a training workload should wait until a separate preparation Job completes.
Reuse the same `kube` dynamic client supplied to discovery to read that Job:

```go
jobs := kube.Resource(schema.GroupVersionResource{
    Group: "batch", Version: "v1", Resource: "jobs",
}).Namespace(namespace)
external, err := jobs.Get(ctx, preparationJobName, metav1.GetOptions{})
if err != nil { return err }
definition, err := catalog.Get(external.GroupVersionKind())
if err != nil { return err }
dependency, err := karta.New(definition, external)
if err != nil { return err }
summary, err := workloadinterface.SummarizeDesired(ctx, dependency)
if err != nil { return err }
if !slices.Contains(summary.Phases, "Completed") {
    return fmt.Errorf("preparation Job %s has not completed", external.GetName())
}
// The caller can now evaluate its remaining policy before resuming training.
```

`external` is the `*unstructured.Unstructured` returned by the dynamic client.
Import `catalog` from `github.com/dsx-ai-factory/workload-map/pkg/catalog`.
The other SDK and client setup is shown in the [main guide](README.md#create-the-sdk-once).

Here the caller supplies `namespace` and `preparationJobName` from its dependency
configuration. This is an explicit read followed by local Karta interpretation.
It does not automatically follow arbitrary fields in the training object.
If the relationship pins a UID, also reject a same-name replacement before using it.

Karta supplies the extracted phases. The consumer owns the decision, requeue and
any write. A failed read is an error, not permission to continue.
To inspect the dependency's Pods too, pass its exact GVK, namespace, name and
fetched UID to `sdk.Workload`, provided it is a terminal workload root.
Reading a dependency does not merge its Pods into the training workload.

<details>
<summary>Design discussion: bring the resource-references prototype into the SDK</summary>

The [CEL references prototype](https://github.com/AviadHayumi/workload-map/blob/cd91d670a9b6613e4d1c21e066b37a8446eb9fb1/pkg/references/reader.go)
lets a definition declare a lookup by name or a list by label selector.
Expressions receive the resulting objects as `references.<name>`.
This integration is not present in this SDK checkout, which still uses jq.

The motivating example is a TrainJob whose `spec.runtimeRef.name` points to a
ClusterTrainingRuntime. The runtime provides the base template; the TrainJob
provides overrides. A definition can then extract the effective image or resources,
and a consumer can decide whether they satisfy its policy.
Other uses include inspecting a preparation workload or aggregating referenced
Pod states when a root does not report enough status.

The reusable boundary is the data provider:

- The prototype's `ResourceReader` has generic `Get` and `List` methods.
  Adapt the Kubernetes client the application already owns; no second client is needed.
- The [factory options](https://github.com/AviadHayumi/workload-map/blob/cd91d670a9b6613e4d1c21e066b37a8446eb9fb1/pkg/resource/component_factory.go)
  accept either `WithReferenceReader` or `WithReferences` for already supplied objects.
  These are prototype factory options, not options on the current `karta.New`.
- The current `discovery.Reader` is a different contract: its `Get` requires a UID,
  and its list operation is Pod-specific. Reuse its underlying client through a
  reference adapter, rather than treating the two reader interfaces as interchangeable.

Before adding SDK options, agree on these behaviors:

- Keep object-only inspection available without cluster access. Allow a reader
  or supplied objects when a definition needs external inputs.
- Preserve the prototype's lazy reads: the first reference-dependent evaluation
  resolves all declared references. Successful resolution is reused for that
  factory; a fresh observation needs a fresh reference snapshot.
- Keep namespaced targets in the workload's namespace. Resolve cluster-scoped
  targets without a namespace. Do not turn an empty namespace into a cluster-wide list.
- Distinguish a missing lookup from a denied or failed read. A missing object stays
  unbound; an empty list binds `[]`. An expression must explicitly tolerate absence.
- Keep reference reads separate from ownership discovery and mutation targets.
  A label-selected Pod list is not proof of workload ownership.

Client reuse does not make the reads atomic or fresh. The consumer chooses direct
or cached reads and owns watches and invalidation. The SDK's existing `CacheOptions`
only reuses attribution; it does not cache or refresh external references.

The [resource-references KEP](https://github.com/AviadHayumi/workload-map/blob/4d9534b9fdfa46d337c5d5c250dbad5db282be3e/keps/0002-resource-references/README.md)
describes the wider proposal. Its adapter module, request-plan API and validation
extensions are separate proposed work, not delivered SDK features.

</details>

