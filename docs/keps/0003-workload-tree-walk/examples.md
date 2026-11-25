<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Workload tree examples

These examples use the [proposed Go interface](README.md#proposal). Each
`tree` is a workload loaded from one Kubernetes object. Only the last example
sends an update to a Kubernetes API server.

## List the pod-bearing instances

The quickstart JobSet has two replicated jobs, `leader` and `workers`.
The root has no pod. This read returns
`["replicatedjob/leader", "replicatedjob/workers"]`. The walk reports no change.
[JobSet input](../../examples/quickstart/jobset.yaml)

```go
func podNames(ctx context.Context, tree karta.Workload) ([]string, error) {
	var names []string
	_, err := tree.Walk(ctx, func(n karta.Node) error {
		if n.Pod != nil {
			names = append(names, n.Component+"/"+n.InstanceID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}
```

## Use the same scheduler policy on four workloads

Give every pod-bearing instance a scheduler if its definition can write the field
and the owner left it unset. A missing optional transformer gets no scheduler.
A second walk over the resulting object returns `false, nil`.

```go
func fillScheduler(ctx context.Context, tree karta.Workload) (bool, error) {
	return tree.Walk(ctx, func(n karta.Node) error {
		if n.Pod != nil && n.CanWrite("spec.schedulerName") && n.Pod.Spec.SchedulerName == "" {
			n.Pod.Spec.SchedulerName = "batch"
		}
		return nil
	})
}
```

| Input and visit | What the catalog supplies | Field that receives `"batch"` |
| --- | --- | --- |
| [Deployment](../../catalog/apps-deployment-v1.yaml), root | Full template | `spec.template.spec.schedulerName` |
| [LLMInferenceService v1alpha2](../../catalog/serving-kserve-io-llminferenceservice-v1alpha2.yaml), present `worker` | Pod spec without metadata | `spec.worker.schedulerName` |
| [InferenceService](../../catalog/serving-kserve-io-inferenceservice-v1beta1.yaml), present `predictor` | Individually mapped pod fields | `spec.predictor.schedulerName` |
| [NIMService](../../catalog/apps-nvidia-com-nimservice-v1alpha1.yaml), root | Individually mapped pod fields | `spec.schedulerName` |

## Read a computed fragment

A [NIMCache](../../catalog/apps-nvidia-com-nimcache-v1alpha1.yaml) with
`spec.resources: {cpu: "1"}` uses the expression `{requests: .spec.resources}`.
Evaluating it against the NIMCache produces `{"requests":{"cpu":"1"}}` for the
pod view. That expression constructs a new object. Editing the CPU request to
`"2"` must return an error until the definition supplies a writable location.
The original NIMCache still requests one CPU.

## Change one container in one JobSet instance

Both jobs in the quickstart have a container named `training`. Roll out a new
image to `workers`. Look up the instance by its ID and the container by its name.
Reordering the jobs or adding a sidecar does not change the target.
[JobSet input](../../examples/quickstart/jobset.yaml) and [mapping](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml)

```go
func updateWorkers(ctx context.Context, tree karta.Workload) (bool, error) {
	found := false
	changed, err := tree.Walk(ctx, func(n karta.Node) error {
		if n.Component != "replicatedjob" || n.InstanceID != "workers" || n.Pod == nil {
			return nil
		}
		for i := range n.Pod.Spec.Containers {
			c := &n.Pod.Spec.Containers[i]
			if c.Name == "training" {
				c.Image = "ghcr.io/example/training:v2"
				found = true
			}
		}
		return nil
	})
	if err == nil && !found {
		return false, fmt.Errorf("workers training container is missing")
	}
	return changed, err
}
```

Only the selected container's `image` changes. Its resource limits and the
leader's image survive. An already updated worker is a successful no-op.

## Add one label to a core Pod or a KServe transformer

```go
func labelPods(ctx context.Context, tree karta.Workload, component string) (bool, error) {
	return tree.Walk(ctx, func(n karta.Node) error {
		if n.Component == component && n.Pod != nil {
			if n.Pod.Labels == nil {
				n.Pod.Labels = make(map[string]string)
			}
			n.Pod.Labels["team"] = "ml"
		}
		return nil
	})
}
```

| Input | Call with component | Expected change to the raw workload |
| --- | --- | --- |
| Running [core Pod](../../catalog/core-pod-v1.yaml) | `"pod"` | Add `metadata.labels.team: ml`. Keep `apiVersion`, `kind`, `status`, and existing labels. The template read path is `.`. |
| [InferenceService](../../catalog/serving-kserve-io-inferenceservice-v1beta1.yaml) with a transformer | `"transformer"` | Add `spec.transformer.labels.team: ml`. Keep the transformer's `minReplicas`, containers and root workload labels. Spec and metadata share the same parent. |
| [LLMInferenceService v1alpha2](../../catalog/serving-kserve-io-llminferenceservice-v1alpha2.yaml) with a worker | `"worker"` | Return an unmapped-field error. The worker maps only a pod spec. Its workload's root labels are a different field. |

The first two rows are whole-object regression cases for the writer. For the
transformer, also change only its scheduler and check that existing labels and
`minReplicas` survive and that no `resources: {}` appears in the container.
[Write-back issue](https://github.com/dsx-ai-factory/workload-map/issues/346)

## Select a Dynamo service by map key

In this DynamoGraphDeployment, `spec.services` is a map. The catalog exposes each
key as an instance ID. This edits `spec.services.Frontend.extraPodSpec.schedulerName`.
Other services keep their own settings.
[Recorded input](../../../test/e2e/recorded_data/dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/running.yaml) and [mapping](../../catalog/nvidia-com-dynamographdeployment-v1alpha1.yaml)

```go
func setServiceScheduler(ctx context.Context, tree karta.Workload) (bool, error) {
	return tree.Walk(ctx, func(n karta.Node) error {
		if n.Component == "service" && n.InstanceID == "Frontend" && n.Pod != nil {
			n.Pod.Spec.SchedulerName = "frontend"
		}
		return nil
	})
}
```

## Abort after an earlier visit made an edit

The leader and workers in the quickstart request different amounts of CPU.
Suppose this queue accepts at most four CPUs per container. The leader requests
four, so its scheduler edit is staged. The workers request eight, so the callback
returns an error. The caller gets `false, err` and the original JobSet, including
the leader's original scheduler. [JobSet requests](../../examples/quickstart/jobset.yaml)

```go
func assignSmallQueue(ctx context.Context, tree karta.Workload) (bool, error) {
	limit := resource.MustParse("4")
	return tree.Walk(ctx, func(n karta.Node) error {
		if n.Pod == nil {
			return nil
		}
		for _, c := range n.Pod.Spec.Containers {
			if c.Resources.Requests.Cpu().Cmp(limit) > 0 {
				return fmt.Errorf("%s/%s: %s requests more than four CPUs", n.Component, n.InstanceID, c.Name)
			}
		}
		n.Pod.Spec.SchedulerName = "small"
		return nil
	})
}
```

## Suspend a workload that supports it

```go
func suspendIfSupported(ctx context.Context, tree karta.Workload) (bool, error) {
	for _, action := range tree.Actions() {
		if action.Name == "suspend" {
			return tree.ApplyAction(ctx, action.Name, nil)
		}
	}
	return false, nil
}
```

On a [JobSet](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml) this sets
`spec.suspend: true`. On a [Kueue Workload](../../catalog/kueue-x-k8s-io-workload-v1beta2.yaml)
it sets `spec.active: false`. Both take zero arguments. The KServe
InferenceService declares neither action, so this policy skips it. A direct
call to an unsupported action would return an error.

## Discover and run an action with arguments

Suppose a Deployment owner declares `set-capacity`. It sets the replica count
and can label the workload with a capacity tier. This is proposed catalog YAML.
The current Deployment definition has no such action.

```yaml
spec:
  structureDefinition:
    actions:
      - name: set-capacity
        parameters:
          - {name: replicas, type: integer, required: true}
          - {name: tier, type: string, enum: [standard, burst]}
        assignments:
          - path: .spec.replicas
            valueFrom: {parameter: replicas}
          - path: '.metadata.labels["capacity-tier"]'
            valueFrom: {parameter: tier}
```

A CLI can build its prompts from `tree.Actions()`. For `set-capacity` it sees a
required integer named `replicas` and an optional string named `tier` with two
choices. For `suspend` on JobSet, this lookup succeeds with no parameters. The
boolean distinguishes that result from an unknown action.

```go
func parametersFor(tree karta.Workload, name karta.ActionName) ([]karta.ActionParameter, bool) {
	for _, action := range tree.Actions() {
		if action.Name == name {
			return action.Parameters, true
		}
	}
	return nil, false
}
```

A parameter supplies a value to its declared path. A caller can invoke an action
directly when it already knows the arguments.

```go
func setCapacity(ctx context.Context, tree karta.Workload, replicas int64, tier string) (bool, error) {
	args := map[string]any{"replicas": replicas}
	if tier != "" {
		args["tier"] = tier
	}
	return tree.ApplyAction(ctx, "set-capacity", args)
}
```

| Arguments passed to `ApplyAction` | Result on a Deployment with two replicas and tier `standard` |
| --- | --- |
| `{"replicas": 3, "tier": "burst"}` | Set `spec.replicas` to 3 and `metadata.labels["capacity-tier"]` to `burst`. |
| `{"replicas": 0}` | Scale to zero. The tier stays `standard`. |
| `{"tier": "burst"}` | Error naming the missing `replicas` parameter. |
| `{"replicas": "3"}` | Error naming the expected integer type. |
| `{"replicas": 3, "tier": "fast"}` | Error listing the allowed tiers. Replicas stay at two. |
| `{"replicas": 3, "region": "east"}` | Error naming the unknown parameter `region`. |

## Edit workers and suspend the JobSet in one API update

The controller stages the worker image and then applies the workload action once.
Each call has its own rollback boundary. If the action fails, the in-memory tree
still contains the successful image edit. The caller discards that tree and
issues no update. On the next reconcile it loads the latest JobSet.

```go
func prepareAndSave(ctx context.Context, tree karta.Workload, c client.Client) error {
	edited, err := updateWorkers(ctx, tree)
	if err != nil {
		return err
	}
	suspended, err := tree.ApplyAction(ctx, "suspend", nil)
	if err != nil {
		return err
	}
	if edited || suspended {
		return c.Update(ctx, tree.Object())
	}
	return nil
}
```
