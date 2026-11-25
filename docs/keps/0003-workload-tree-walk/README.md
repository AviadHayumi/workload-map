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
See [SDK usage by consumer](#sdk-usage-by-consumer) for reads, edits, actions and live Pod grouping.

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
`*unstructured.Unstructured`. The returned `Workload` supports reads, edits
during a walk, targeted edits and named actions.

```go
type Workload interface {
	Definition() Definition
	Status(ctx context.Context) (*Status, error)
	Read(ctx context.Context) (*Snapshot, error)
	Component(ctx context.Context, name string) (*Component, error)

	Walk(ctx context.Context, visit func(Node) error) (bool, error)
	MutatePodTemplates(ctx context.Context, component string, fn PodTemplateMutateFunc, opts ...MutateOption) (bool, error)
	Actions() []Action
	ApplyAction(ctx context.Context, name ActionName, args map[string]any) (bool, error)
	Modified() bool
	Object() *unstructured.Unstructured
}

type Node struct {
	Component  string
	InstanceID string
	Pod        *corev1.PodTemplateSpec
}

// CanWrite reports whether this instance has a writable route for a pod field.
func (n Node) CanWrite(field string) bool
```

`Definition` is the compiled Karta mapping. A `Snapshot` contains the root
status and the components with their instances. A targeted edit gets an
instance ID and its pod template in a `func(string, *corev1.PodTemplateSpec) error`
callback. [Targeted edit and options](#edit-an-instance-you-already-know)

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
with an empty ID. `Pod` is nil when the visit has no pod mapping or the optional
pod is absent. Otherwise `Pod` presents template metadata and spec as one
`corev1.PodTemplateSpec`. A full template supplies both. A bare pod spec supplies
the spec. A fragmented definition supplies only the fields it maps. `tree.Object()`
returns a deep copy of the current workload, including status and CR-specific fields.
Edits through the pod pointer count only until the callback returns.
[Normalized pod read proposal](https://github.com/dsx-ai-factory/workload-map/issues/344)

For this input Karta adds only `spec.predictor.schedulerName: batch`. The model
and replica count keep their original values.
[KServe mapping](../../catalog/serving-kserve-io-inferenceservice-v1beta1.yaml)

An empty Go value can mean the field is unset or the definition cannot supply it.
A policy can use `n.CanWrite("spec.schedulerName")` before editing. An attempted
edit to an unmapped field fails with the component, instance and field.
A no-op returns `false, nil`.

### Safe write-back

Adding one label to a core Pod currently can erase its `apiVersion`, `kind` and
`status`. Its catalog path is `.`, and the legacy writer replaces the workload
with a serialized template. A KServe transformer exposes the same bug at a
nested path. [Write-back issue](https://github.com/dsx-ai-factory/workload-map/issues/346)

1. Compare the typed pod before and after the visit. Resolve each changed field
   to a concrete address on the original instance. Bind keyed entries by their
   ID. Reject a leaf shared with an unselected instance. A computed read or an
   ambiguous destination needs an explicit write mapping. Without one, return
   an error. `CanWrite` uses the same check.
2. Apply just those edits to a private copy of the raw workload. Preserve fields
   outside the edit, unknown fields inside list entries, and explicit empty values.
   Reject a list edit if its entries cannot be matched safely.
3. Check that the changed fields read back as requested and that the raw changes
   are confined to the intended addresses. Publish the copy only after the whole
   walk succeeds. Any error or cancellation discards it.

Use the same writer for `Walk`, `MutatePodTemplates` and the existing resource
update methods. For a sparse fragment update, compare only the supplied fields.
A typed scalar that was already zero cannot express a new explicit zero through
assignment alone. The [open write-back PR](https://github.com/AviadHayumi/workload-map/pull/17)
is the starting point for the SDK fix. Enable walk writes after the shared writer
passes the [whole-object regression cases](#test-plan).

### Workload actions

`Actions` and `ApplyAction` replace KEP-0002's `CanSuspend`, `Suspend` and
`Resume`. An action runs once on the workload. `Actions()` lets a caller discover
the name, accepted arguments and allowed choices before it runs an action.

```go
type ActionName string
type ParameterType string // "string", "integer" or "boolean"

type Action struct {
	Name       ActionName
	Parameters []ActionParameter
}

type ActionParameter struct {
	Name     string
	Type     ParameterType
	Required bool
	Enum     []string // Optional choices for a string parameter.
}
```

Existing suspend and resume declarations contribute their ordered assignments to
one workload action per name, root first. They take no parameters. Call
`ApplyAction(ctx, "suspend", nil)`. New catalog actions can bind named parameters
to assignment values. At compile time Karta rejects duplicate names, collisions
with existing actions and references to undeclared parameters.

Before any write, Karta checks required arguments, names, types and choices.
Omitting an optional argument skips its assignment. Zero and false are supplied
values. An integer accepts signed Go integers. A string or boolean requires its
corresponding Go type. Each action is atomic and reports whether it changed the object.

Each `Walk` or `ApplyAction` call stages its own changes. If a walk succeeds
and a later action fails, the walk's edit is still in memory. A controller that
needs both changes to succeed discards the tree on error and calls `Update`
only after both succeed.
[Action definition and caller examples](#discover-and-run-an-action-with-arguments)

## SDK usage by consumer

Catalog lookup, definition validation and live Pod grouping exist in the SDK
today. The `karta.Workload` reads, edits and actions use the proposed interface.
In the KAI Scheduler example, the live Pod's role label identifies a worker.
The owning `CustomJob` supplies the subgroup minimum of two.

<details>
<summary>Validate and load a definition, read status, and calculate scale and CPU</summary>

### Validate, select and load a definition

`kli validate` and the operator webhook call
`v1alpha1.NewKartaValidator(def).Validate()` on a Karta manifest. A client can
list built-ins with `catalog.List()`. When it already has the workload, the
object's group, version and kind (GVK) identify the built-in definition.
[CLI validator](../../../cli/cmd/validate.go) and [webhook](../../../operator/pkg/webhook.go)

```go
func loadBuiltin(obj *unstructured.Unstructured) (karta.Workload, error) {
	def, err := catalog.Get(obj.GroupVersionKind())
	if err != nil {
		return nil, err
	}
	return karta.Load(def, obj)
}
```

For a JobSet this loads a private copy of that JobSet. An unknown GVK returns
`catalog.ErrNotFound` through `errors.Is`. Callers with a registered definition
can pass it directly to `karta.Load`. For example, NVIDIA Run:ai exposes
registration of a workload type with an administrator-supplied Karta. The CLI
resolves a cluster definition ahead of a built-in for the same GVK.
[Workload type registration](https://run-ai-docs.nvidia.com/self-hosted/workloads-in-nvidia-run-ai/workload-types/extending-workload-support) and [CLI definition resolver](../../../cli/pkg/definitions/resolver.go)

### Read workload health

NVCF reads the root component's status and ranks the matches when more than
one applies. The exporter records all matches. A controller using the proposed
interface can compile a definition once and reuse it for each workload event.
[NVCF cluster agent](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/compute-plane-services/nvca/internal/miniservice/status_karta.go#L217-L266) and [exporter status handling](../../../dsx-workload-exporter/pkg/controller/root.go)

```go
func reportPhases(ctx context.Context, def karta.Definition, obj *unstructured.Unstructured) ([]v1alpha1.ResourceStatus, error) {
	tree, err := def.Load(obj)
	if err != nil {
		return nil, err
	}
	status, err := tree.Status(ctx)
	if err != nil || status == nil {
		return nil, err
	}
	return status.MatchedStatuses, nil
}
```

Build `def` with `compiled, err := karta.Compile(kartaDef)` when the definition
changes. Call `reportPhases(ctx, compiled, jobSet)` after checking the error.
The quickstart JobSet maps to `Running`. A definition with no root status mapping
returns nil status. A definition inspector can use `tree.Definition().Karta()`
for the source manifest and `tree.Definition().Components()` for declared
component kinds, parents and writable fields before inspecting any instance.
[JobSet input](../../examples/quickstart/jobset.yaml) and [status mapping](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml)

### Inspect a tree or one component

`kli describe` displays components and instances. Headlamp asks its WebAssembly
bridge to build a tree from the definition JSON and workload JSON. A Go caller
can take one snapshot for the same display. On the quickstart JobSet,
`snapshot.Children("jobset")` returns the `replicatedjob` component. Its instance
counts are `leader: 1` and `workers: 8` (four replicated Jobs, each with
parallelism two). [CLI](../../../cli/pkg/workload/describe.go), [Headlamp bridge](../../../headlamp-plugin/src/lib/karta/kartaUtil.ts) and [JobSet mapping](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml)

```go
func jobSetScale(ctx context.Context, tree karta.Workload) (map[string]int32, error) {
	snapshot, err := tree.Read(ctx)
	if err != nil {
		return nil, err
	}
	jobs, ok := snapshot.Get("replicatedjob")
	if !ok {
		return nil, fmt.Errorf("replicatedjob component is missing")
	}
	counts := make(map[string]int32)
	for _, instance := range jobs.Instances {
		if instance.Present && instance.Scale != nil && instance.Scale.Replicas != nil {
			counts[instance.ID] = *instance.Scale.Replicas
		}
	}
	return counts, nil
}
```

In Headlamp the corresponding TypeScript call is
`const tree = await buildTree(definition, workload)`. For the same JobSet,
`tree.Status?.Phases` contains `"Running"`. This is the existing browser API.

### Calculate the CPU request for workers

Nova documents using custom workload resource requirements to choose a cluster
with enough capacity. A Go caller can calculate the CPU requested by the
`training` container across this JobSet's workers.
Read one component. Eight worker Pods at eight CPUs each request 64 CPUs for
that container. This example counts only the named container.
It imports Kubernetes resource quantities as `apiresource`.
[Nova supported resources](https://docs.elotl.co/nova/Appendix/api-resources/#custom-resource-objects) and [JobSet input](../../examples/quickstart/jobset.yaml)

```go
func workerCPU(ctx context.Context, tree karta.Workload) (apiresource.Quantity, error) {
	jobs, err := tree.Component(ctx, "replicatedjob")
	if err != nil {
		return apiresource.Quantity{}, err
	}
	for _, instance := range jobs.Instances {
		if instance.ID != "workers" || instance.Pod == nil || instance.Scale == nil || instance.Scale.Replicas == nil {
			continue
		}
		for _, c := range instance.Pod.Spec.Containers {
			if c.Name == "training" {
				cpu := c.Resources.Requests.Cpu().DeepCopy()
				cpu.Mul(int64(*instance.Scale.Replicas))
				return cpu, nil
			}
		}
	}
	return apiresource.Quantity{}, fmt.Errorf("workers training container or scale is missing")
}
```

</details>

<details>
<summary>Group a live Pod in KAI Scheduler or attribute it to an instance in the exporter</summary>

### How KAI Scheduler groups a live Pod

KAI's pod grouper can fall back to Karta when a workload has no native grouping
plugin. It selects a Karta for the top owner's GVK. Its test uses a `CustomJob`
with `spec.coordinator.replicas: 1` and `spec.worker.replicas: 2`. A worker
Pod carries `metadata.labels["e2e.example.com/role"]: worker`.
[Definition lookup](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/pkg/podgrouper/podgrouper/plugins/karta/hub.go#L81-L111) and [KAI fixture](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/test/e2e/suites/integrations/third_party/karta/customjob_karta.yaml#L22-L49)

KAI pins the SDK under its earlier `github.com/run-ai/karta` module path.
Here `resource` is Karta's resource package.
This example uses the same grouping calls on this branch for the fixture's
worker leaf. KAI's general matcher also searches descendants when a subgroup names a parent.
[Pinned module](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/go.mod#L31), [subgroup calculation](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/pkg/podgrouper/podgrouper/plugins/karta/grouper.go#L109-L150) and [subtree matching](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/pkg/podgrouper/podgrouper/plugins/karta/component_matching.go#L12-L47)

```go
func workerSubgroup(ctx context.Context, def *v1alpha1.Karta, job *unstructured.Unstructured, pod *corev1.Pod) (bool, int32, error) {
	summary, err := instructions.NewStructureSummary(def)
	if err != nil {
		return false, 0, err
	}
	factory := resource.NewComponentFactoryFromObject(def, job)
	worker, err := factory.GetComponent("worker")
	if err != nil {
		return false, 0, err
	}
	selector := worker.GetPodSelector()
	if selector == nil || selector.ComponentTypeSelector == nil {
		return false, 0, fmt.Errorf("worker component has no role selector")
	}
	matches, err := resource.NewPodQuerier(pod).MatchesComponentType(ctx, selector.ComponentTypeSelector)
	if err != nil {
		return false, 0, err
	}
	minimum, err := instructions.CalculateSubtreeScale(ctx, "worker", nil, factory, summary)
	return matches, minimum, err
}
```

With the worker Pod the result is `true, 2, nil`. With a coordinator Pod it is
`false, 2, nil`. KAI writes the PodGroup with `minSubGroup: 2` and subgroup
minimums of one coordinator and two workers. It marks the live Pods with their
group and subgroup. The owning CustomJob receives no write. With a named group
and no subgroups, KAI calculates the minimum from the root component's subtree.
[KAI test assertions](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/test/e2e/suites/integrations/third_party/karta/karta_test.go#L123-L151) and [root-scale fallback](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/pkg/podgrouper/podgrouper/plugins/karta/grouper.go#L93-L104)

### Attribute a live Pod to a workload instance

The exporter starts with a Pod and its resolved workload owner. For the
quickstart JobSet, a Pod whose `jobset.sigs.k8s.io/replicatedjob-name` label
is `workers` belongs to component `replicatedjob`, instance `workers`. The Pod
supplies the label. The JobSet supplies the valid instance names. These calls
exist today. The proposed walk starts from the workload in the other direction.
[Exporter attribution](../../../dsx-workload-exporter/pkg/controller/leaf.go) and [Pod selector](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml)

```go
func jobSetPodInstance(ctx context.Context, def *v1alpha1.Karta, job *unstructured.Unstructured, pod *corev1.Pod) (string, *string, error) {
	summary, err := instructions.NewStructureSummary(def)
	if err != nil {
		return "", nil, err
	}
	query := resource.NewPodQuerier(pod)
	component, err := instructions.InferPodComponent(ctx, query, summary)
	if err != nil {
		return "", nil, err
	}
	factory := resource.NewComponentFactoryFromObject(def, job)
	instance, err := instructions.InferPodComponentInstance(ctx, query, component, factory)
	return component, instance, err
}
```

For metrics the exporter also checks the match. If a component declares
instance IDs and none matches, it reports an attribution error. For a replica
group it follows the component's owners to the first replica selector and calls
`query.ExtractReplicaKey(ctx, selector.ReplicaSelector)`. For a LeaderWorkerSet
worker with group-index label `"2"`, the parent `group` supplies the selector
and the live Pod supplies the value `"2"`. [LeaderWorkerSet mapping](../../catalog/leaderworkerset-x-k8s-io-leaderworkerset-v1.yaml)

KAI's older `podGroups` format uses the same Pod and owner inputs to answer
a scheduling question. The quickstart JobSet and the worker Pod above give
these results with its catalog definition.
[JobSet group mapping](../../catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml) and [KAI grouping code](https://github.com/kai-scheduler/KAI-Scheduler/blob/8e0d52c26a117f3f4212a80a15cc82a10b1c1ea1/pkg/podgrouper/podgrouper/plugins/karta/grouper.go#L153-L199)

| SDK call | Result |
| --- | --- |
| `instructions.GetPodGroupingEffectiveComponent(ctx, query, "replicatedjob", summary)` | Member of group `job`, effective component `replicatedjob` |
| `instructions.InferPodComponentInstance(ctx, query, "replicatedjob", factory)` | A pointer to `"workers"` |
| `query.ExtractGroupKeys(ctx, member.MemberDefinition.GroupByKeyPaths)` | `["workers"]` from the live Pod's label |
| `instructions.CalculateSubtreeScale(ctx, member.EffectiveComponent, instance, factory, summary)` | `8` from the workload's worker entry |

</details>

<details>
<summary>Walk pod templates, bare pod specs, and computed fragments</summary>

### List the pod-bearing instances

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

### Use the same scheduler policy on four workloads

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

### Read a computed fragment

A [NIMCache](../../catalog/apps-nvidia-com-nimcache-v1alpha1.yaml) with
`spec.resources: {cpu: "1"}` uses the expression `{requests: .spec.resources}`.
Evaluating it against the NIMCache produces `{"requests":{"cpu":"1"}}` for the
pod view. A snapshot reader can inspect `instance.Fragments.Resources.Requests`
when both pointers are present. The expression constructs a new object. Editing
the CPU request to `"2"` must return an error until the definition supplies a writable location.
The original NIMCache still requests one CPU.

</details>

<details>
<summary>Edit a named JobSet, KServe, or Dynamo instance and handle rollback</summary>

### Change one container in one JobSet instance

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

### Edit an instance you already know

A queue controller already knows the JobSet instance ID `workers`. It can
call `MutatePodTemplates` directly and set a priority class in that template.
The selector is checked before the callback runs.

```go
func editKnownWorker(ctx context.Context, tree karta.Workload) (bool, error) {
	return tree.MutatePodTemplates(ctx, "replicatedjob", func(_ string, pod *corev1.PodTemplateSpec) error {
		pod.Spec.PriorityClassName = "training"
		return nil
	}, karta.OnInstances("workers"))
}
```

For a typo such as `"wokers"`, the caller can check
`errors.Is(err, karta.ErrInstanceNotFound)`. For an optional KServe component,
`tree.MutatePodTemplates(ctx, "transformer", edit, karta.SkipAbsent())` skips a
missing transformer. Here `edit` is the same kind of typed callback as above.
If an edit has no writable mapping, `var field *karta.FieldError` followed by
`errors.As(err, &field)` identifies the component, instance and field.
[Targeted mutation contract](../0002-sdk-workload-front-door/README.md#callback-and-options)

### Add one label to a core Pod or a KServe transformer

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

Existing callers select a getter and setter from the mapping. A core Pod
label uses `GetPodTemplateSpec` and `UpdatePodTemplateSpec`. A transformer
label uses `GetPodMetadata` and `UpdatePodMetadata`. Its scheduler uses
`GetPodSpec` and `UpdatePodSpec`. A predictor scheduler uses
`GetFragmentedPodSpec` and `UpdateFragmentedPodSpec`. The proposed walk routes
these edits from the same typed view. [Current component API](../../../karta/sdk/pkg/resource/component.go)

### Select a Dynamo service by map key

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

### Abort after an earlier visit made an edit

The leader and workers in the quickstart request different amounts of CPU.
Suppose this queue accepts at most four CPUs per container. The leader requests
four, so its scheduler edit is staged. The workers request eight, so the callback
returns an error. The caller gets `false, err` and the original JobSet, including
the leader's original scheduler. [JobSet requests](../../examples/quickstart/jobset.yaml)

```go
func assignSmallQueue(ctx context.Context, tree karta.Workload) (bool, error) {
	limit := apiresource.MustParse("4")
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

</details>

<details>
<summary>Discover action arguments, suspend or resume, and save edits with one update</summary>

### Suspend a workload that supports it

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
call to an unsupported action would return an error. To resume, call
`tree.ApplyAction(ctx, "resume", nil)`. This sets `spec.suspend` to false on
the JobSet and `spec.active` to true on the Kueue Workload.

### Discover and run an action with arguments

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

### Edit workers and suspend the JobSet in one API update

The controller stages the worker image and then applies the workload action once.
Each call has its own rollback boundary. If the action fails, the in-memory tree
still contains the successful image edit. The caller discards that tree and
issues no update. On the next reconcile it loads the latest JobSet. The two
booleans track these particular calls. A controller that owns the tree from
load to save can use `tree.Modified()` to check for any successful change.

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

</details>

## Compatibility and alternatives

Existing catalog definitions need no migration. Declaring parameterized actions
requires an additive CRD change. A mutable map per node would make the caller
learn every workload's layout. The Go pod view covers portable edits. Editing a
KServe model URI would require its own declared write target.

## Test plan

- Run the core Pod, KServe transformer and KServe predictor cases through the
  public SDK setters and the integrated `Walk`. Compare the complete raw object
  with the input plus the requested edit. A second walk must report no change.
  If a later visit fails, the complete input must survive.
- Apply `"suspend"` to JobSet and Kueue. Discover the arguments of a declared
  action, pass zero, omit an optional argument, and reject invalid input without
  changing the object.

### Write-back validation

In-memory replay covered 512 workload snapshots from 95 recordings. The
[candidate writer](https://github.com/AviadHayumi/workload-map/tree/cc64a09742780cfe411be142fe2dddde5fba7431)
returned the expected whole object for all 893 no-op writes and all 584
scheduler edits on present, mapped targets. Another 28 reads of optional
components failed before a write.

Four cases still block walk writes. The candidate dropped a sibling field,
silently skipped a KServe edit, edited the wrong JobSet instance, and rounded a
large integer. Cover these cases in the public setters and exercise each edit
the pod view exposes through the integrated walk.

<details>
<summary>Write-back regression inputs and expected results</summary>

| Case | Observed on the candidate | Required outcome |
| --- | --- | --- |
| Change only `tolerationSeconds` from 10 to 20 | A successful update dropped a neighboring extension field. A second test lost an explicit `value: ""`. | Preserve every sibling field or reject the edit and keep the input intact. |
| Edit a KServe model container when the predictor filter matches no model | The update returned success and wrote nothing. | Return an error naming the field and leave the predictor intact. |
| Target `workers` with a test JobSet definition that reverses only the instance IDs | The edit landed on `leader`. The stock mapping uses matching order. | Bind the edit to `workers` by ID or reject the mapping. The leader keeps its original template. |
| Load an object with `metadata.generation: 9007199254740993`, then set its scheduler | Reading the object already returned `9007199254740992`. The scheduler edit kept the rounded value. | Preserve the exact integer at load, after the edit and in the object returned to the caller. |

</details>

The integrated walk and parameterized actions still need end-to-end coverage.
The action argument examples ran against a test double.

## History

- 2026-11-25: Proposed.
