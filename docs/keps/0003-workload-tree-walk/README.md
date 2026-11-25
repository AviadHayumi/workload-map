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
Karta applies the changed fields to an in-memory copy of the workload.
The caller decides when to send that object to Kubernetes.

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

KEP-0002 lets the controller read the tree, then pass a component name and instance
IDs to a second call. A visitor already has the instance it wants to change.
The visitor can make the edit there, as it can in Go's
[syntax tree walker](https://pkg.go.dev/golang.org/x/tools/go/ast/astutil#Apply).

## Proposal

Keep `definition.Load(obj)` from KEP-0002. It accepts a Kubernetes `runtime.Object`
such as `*unstructured.Unstructured` and returns a `Workload`.
The interface excerpt below shows the methods used for edits.

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

The walk visits the root first, then children in definition order. Instances of
each component are visited in ID order. A node without a pod has a nil `Pod`.
Full templates, standalone pod specs and fragments all expose the same Go type.
For this input Karta adds only `spec.predictor.schedulerName: batch`. The model
and replica count keep their original values.
[KServe mapping](../../catalog/serving-kserve-io-inferenceservice-v1beta1.yaml)

Each changed field must map to one writable location for the visited instance.
An unmapped edit, such as adding predictor tolerations, returns an error naming
the component, instance and field. Any error or cancellation discards the whole
walk. A walk with no changes returns `false, nil`.

### Workload actions

`Actions` lists the names the definition supports. Call `ApplyAction` once on the
workload after a successful walk. Existing suspend and resume declarations supply
the names `"suspend"` and `"resume"`. Suspending a
[JobSet](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml) sets `spec.suspend` to
`true`. Suspending a [Kueue Workload](../../catalog/kueue-x-k8s-io-workload-v1beta2.yaml)
sets `spec.active` to `false`. An unsupported action fails. Each call is atomic
in memory. The caller can save both changes in one update after both calls succeed.

## Compatibility and alternatives

Existing Karta definitions and SDK clients continue to work. This changes the
proposed Go front door only. A mutable map per node would require callers to know
each workload's fields. Some fragments even come from different parents.
Workload-specific fields such as the model URI and custom action declarations
can be proposed separately when a caller needs them.

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

- 2026-11-25: Proposed. The walk and named actions are proposed SDK interfaces.
