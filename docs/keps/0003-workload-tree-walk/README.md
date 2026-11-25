<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# KEP-0003: Edit a workload during a tree walk

- Status: provisional
- Authors: @AviadHayumi
- Issue: https://github.com/dsx-ai-factory/workload-map/issues/341
- Depends on: [Workload front door](../0002-sdk-workload-front-door/README.md)
- Created: 2026-11-25
- Last updated: 2026-11-25

## Summary

![A visitor sets the KServe predictor scheduler. Karta stages the mapped field. The caller saves a successful edit. A failed walk discards its edits.](walk.png)

A controller can inspect a workload instance and edit its pod template in the same visit.
Karta applies the changed fields in memory. The caller saves the workload.

## Motivation

A controller wants to give a KServe predictor a scheduler when its owner has
left that field empty. These are the relevant fields of the `InferenceService`.

```yaml
spec:
  predictor:
    minReplicas: 2
    model:
      modelFormat: {name: sklearn}
      storageUri: s3://example/model
```

KEP-0002 lets a controller edit instances it names up front. Here the controller
decides what to edit from the data it sees during the visit, as in Go's
[syntax tree walker](https://pkg.go.dev/golang.org/x/tools/go/ast/astutil#Apply).

## Proposal

`definition.Load(obj)` accepts a Kubernetes `runtime.Object` such as
`*unstructured.Unstructured`. Its `Workload` gains `Walk` alongside the targeted
`MutatePodTemplates` call. The excerpt shows the methods used here.

```go
type Workload interface {
	Walk(ctx context.Context, visit func(Node) error) (bool, error)
	Actions() []ActionName
	ApplyAction(ctx context.Context, name ActionName) (bool, error)
	Object() *unstructured.Unstructured
}

type Node struct {
	Component  string
	InstanceID string
	Pod        *corev1.PodTemplateSpec
}

type ActionName string
```

```go
func setScheduler(ctx context.Context, tree karta.Workload, c client.Client) error {
	changed, err := tree.Walk(ctx, func(n karta.Node) error {
		if n.Component == "predictor" && n.Pod != nil && n.Pod.Spec.SchedulerName == "" {
			n.Pod.Spec.SchedulerName = "batch"
		}
		return nil
	})
	if err != nil || !changed {
		return err
	}
	return c.Update(ctx, tree.Object())
}
```

Walk starts at the root. It visits a component's instances in ID order, then
its children in definition order. A component with no instances gets one visit
with an empty ID. `Pod` is nil when the visit has no pod. Otherwise it presents
a full template, a standalone spec or mapped fragments as a `PodTemplateSpec`.
Edits through the pointer count only until the callback returns.

For this input Karta adds only `spec.predictor.schedulerName: batch`. The model
and replica count keep their original values.
[KServe mapping](../../catalog/serving-kserve-io-inferenceservice-v1beta1.yaml)

Each edit needs one writable location on this instance. Adding predictor
tolerations, for example, fails with the component, instance and unmapped field.
Any error or cancellation discards the walk's edits. A no-op returns `false, nil`.

### Workload actions

`Actions` and `ApplyAction` replace KEP-0002's `CanSuspend`, `Suspend` and
`Resume`. `Actions` lists supported names. An action runs once on the workload
and can run independently of a walk. Existing declarations supply `"suspend"`
and `"resume"`. Suspending a
[JobSet](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml) sets `spec.suspend` to
`true`. Suspending a [Kueue Workload](../../catalog/kueue-x-k8s-io-workload-v1beta2.yaml)
sets `spec.active` to `false`. `ApplyAction` reports whether the object changed.
An unsupported action fails. Each call is atomic in memory. A caller combining
a walk and an action saves the object after both succeed.

## Compatibility and alternatives

Only the proposed Go API changes. A mutable map per node would make the caller
learn every workload's layout. Editing KServe's model URI or declaring custom
actions would need a separate proposal.

## Test plan

- Compare the entire KServe object before and after the scheduler edit. A second
  walk must report no change. Reading all fragments and writing them back through
  the [current SDK](../../../karta/sdk/pkg/resource/accessor.go) can erase
  `storageUri` and `modelFormat`.
- Edit one of two JobSet instances and check its sibling. Make a later visit
  fail and check that the original object survives.
- Apply `"suspend"` to JobSet and Kueue. Verify each declared field and reject an
  unknown action without changing the object.

## History

- 2026-11-25: Proposed.
