<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter: implementation design

- Status: implementation design for the current PoC
- Author: @AviadHayumi
- Public contract: [KEP-0001][kep] and [Helm configuration][configuration]

## From one Pod to workload metrics

JobSet `train` in namespace `team-a` declares `leader` with one replica and `workers` with two.
Each Job creates one Pod in this example. Following a Pod's owner references
through its Job leads to the root JobSet.

![JobSet train owns three Jobs and their Pods. The highlighted Pod's labels determine its Karta mapping, while the JobSet condition determines workload status.](./jobset-pod-attribution.png)

The exporter passes the root JobSet and the highlighted Pod to the Karta SDK.
This fragment is from `spec.structureDefinition` in the JobSet Karta:

```yaml
childComponents:
  - name: replicatedjob
    specDefinition:
      podTemplateSpecPath: .spec.replicatedJobs[].template.spec.template
    instanceIdPath: .spec.replicatedJobs[].name
    podSelector:
      componentInstanceSelector:
        idPath: '.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"]'
      replicaSelector:
        keyPath: '.metadata.labels["jobset.sigs.k8s.io/job-index"]'
```

The Karta field determines which object the expression reads.
`podTemplateSpecPath` and `instanceIdPath` read the root JobSet.
Paths under `podSelector` read the Pod being mapped.

For Pod `train-workers-1-0`:

1. Component: `replicatedjob` is the only component with a Pod template, so the SDK selects it. No component label is needed on the Pod.

   <details>
   <summary>When a Karta has several child components</summary>

   The Karta defines which Pod field to read and what value to match. These are the relevant selectors from the PyTorchJob Karta:

   ```yaml
   childComponents:
     - name: master
       podSelector:
         componentTypeSelector:
           keyPath: '.metadata.labels["training.kubeflow.org/replica-type"]'
           value: master
     - name: worker
       podSelector:
         componentTypeSelector:
           keyPath: '.metadata.labels["training.kubeflow.org/replica-type"]'
           value: worker
   ```

   For a Pod whose `training.kubeflow.org/replica-type` label is `worker`:

   - The `master` selector does not match: `worker != master`.
   - The `worker` selector matches: `worker == worker`.
   - The SDK returns the matching component's `name`: `component="worker"`.

   Matching uses the selector's `value`, not the component name. The Karta author selects the exact label or annotation in `keyPath`, which the SDK reads from the Pod using JQ.

   If selectors overlap, the first match wins. If none match, mapping fails.

   </details>

2. Instance: the SDK compares the value read from the Pod with the instance names read from the JobSet:

   ```text
   JobSet instance names:                 ["leader", "workers"]
   Pod's replicatedjob-name label:        "workers"
   Exact match:                          "workers" == "workers"
   Result:                               component_instance="workers"
   ```

   A Pod labeled `leader` matches `leader` instead. A value outside the JobSet's instance names fails the mapping.
3. Replica group: `replicaSelector.keyPath` reads `1` from the Pod's `job-index` label. That becomes replica group `1`.

Status is evaluated on the root JobSet. Its `Completed=True` condition matches
Karta's `Completed` status rule.

These results produce two metrics. Selected labels and the matching phase are shown:

```prometheus
karta_pod_workload_info{workload_name="train",pod_name="train-workers-1-0",component="replicatedjob",component_instance="workers",replica_group_id="1"} 1
karta_workload_status{workload_name="train",phase="Completed"} 1
```
## Controller and startup

One process runs the controller, inventory and telemetry. `controller.Runtime` is the Go struct that implements the exporter controller.

| Component | Responsibility |
| --- | --- |
| `Runtime` | Watches resources, queues changes, verifies API objects and publishes results. Owns scope, Pod associations and the workqueue. |
| `Discovery` | Follows the UID ownership graph and evaluates the selected Karta. Caches calculations. |
| Definition inventory | Selects a catalog or cluster Karta. Owns a separate cluster-Karta watch. |
| Submitter extractor | Evaluates one object's metadata with compiled CEL. Keeps a bounded cache of successful results. |
| Publication store | Keeps immutable results per root UID, including Pod rows and verification state. |
| Collector and telemetry | Turns one store snapshot into samples through OTel instruments and a metric reader. |

![Controller, discovery, submitter and OpenTelemetry responsibilities.](./exporter-component-flow.png)

### Construct and run

[`exporter/cmd/main.go`](../../../exporter/cmd/main.go) constructs dynamic and metadata clients and a REST mapper from GVK to LIST/WATCH resource.

The components receive these settings:

- `definitionOptions` selects Kartas. The inventory and `runtimeOptions.Definitions` use the same options.
- `ignoredOwners` lists the owner GVKs where discovery should stop.
- `submittedByExpression` is the configured CEL expression. When extraction is disabled, `submitter` is nil.
- `provider` sends metrics through the selected OTel reader. `metricOptions` selects phases and the optional identity label.

```go
inventory, err := kartadefinitioninventory.New(client, definitionOptions)
if err != nil {
    return err
}
submitter, err := submitterextractor.New(submittedByExpression)
if err != nil {
    return err
}
reader := controller.NewInformerReader()
discovery := workloaddiscovery.New(reader, inventory, ignoredOwners...)
metricsStore := store.New()

runtime, err := controller.NewRuntime(
    client, metadataClient, mapper,
    reader, discovery, inventory, submitter, metricsStore,
    runtimeOptions,
)
if err != nil {
    return err
}
registration, err := collector.New(
    provider.Meter("karta-exporter"), metricsStore, metricOptions,
)
if err != nil {
    return err
}

results := make(chan error, 2)
go func() { results <- inventory.Run(ctx) }()
go func() { results <- runtime.Run(ctx) }()
```

`InformerReader` lets discovery read cached Kubernetes objects. The controller starts Pod and configured resource watches, then adds missing owner kinds as discovery finds them.

The exporter exits before starting watches if its configuration or enabled submitter CEL is invalid. It shuts down gracefully.

### From Pod to workload

This example follows `Pod -> Job -> JobSet`. Only the JobSet Karta is selected.

| Watch | Starts when | Cached data |
| --- | --- | --- |
| Pods | Exporter starts | Full objects |
| JobSets | JobSet Karta is selected | Full objects |
| Jobs | A Pod names a Job owner | Metadata only |

Complete objects include metadata, spec and status. Kinds with a selected Karta are cached in full.

Status is evaluated only on a resolved root with a selected Karta. For `Pod -> ReplicaSet -> Deployment`, that is the Deployment. A ReplicaSet Karta is not applied while the ReplicaSet is an intermediate owner.

![The Job callback allows ownership resolution before the whole watch finishes loading.](./owner-arrival-resolution.png)

An owner watch is added through this flow:

1. The Pod informer sends an add or update event. The controller calls `Discovery.Upsert` with the Pod's reference and owners.
2. The Pod names a Job as its owner, but Jobs are not watched yet. Discovery still records `Job UID -> Pod UID` in `children`. It returns the Job's GVK and namespace in `ChangeSet.MissingCoverage` so the controller can start that watch.
3. The controller queues that request. When it processes the request, it starts or reuses a shared Job metadata informer for the namespace.
4. The initial Job list delivers `train-workers-1`. The controller calls `Discovery.Upsert`. Discovery reads `children[job-uid]` and includes Pod `train-workers-1-0` in `ChangeSet.Objects`. The controller queues that Pod again.
5. The controller retries `Resolve`. Once every owner in the Pod's path has arrived on its current watch, resolution returns the JobSet root. Other Jobs can still be loading.
6. The controller waits until the initial objects are cached and their add callbacks have run. It checks `informer.HasSynced` and `registration.HasSynced`, then calls `Discovery.CoverageChanged` to retry subtree traversal and Pod mapping. Pod metrics publish after verification.

During startup, one Pod may arrive before another in the same workload. Waiting avoids treating that partial set as the whole workload. The current PoC also delays individual Pod mapping, even when that Pod's inputs are already present.

The exporter is still eventually consistent. Related watches can deliver changes at different times. Initial synchronization does not replace API verification.

If Kubernetes cannot serve or read the required kind, ownership remains unresolved.

### Scope and readiness

The [configuration guide][configuration] defines all settings. Defaults are:

| Setting | Default and effect |
| --- | --- |
| `namespaceSelector` | All namespaces. Limits namespaced watches. |
| `objectSelector`, `resourceRules`, `excludeResourceRules` | No root filter. Exclusions win after ownership resolution. |
| `podSelector` | All Pods. Controls emitted Pod rows. Hidden Pods remain observed. |
| Definition sources | Cluster enabled, catalog disabled. Catalog excludes `core-pod-v1`. |
| `ignoredOwnerKinds`, `requiredKinds` | Empty. Set traversal boundaries and resource kinds required for readiness. |

Workload filters apply after ownership resolution, so intermediate kinds still need read permissions without an explicit discovery entry. Excluding a root removes its output. Its children do not become replacement roots.

Resource rules use the selected Karta's API version, or each observed version separately when no Karta exists. Namespace and workload scope exits remove rows. A failed scope refresh cannot add workloads.

`/healthz` reports process health. `/readyz` requires current configuration and source verification and synchronized Pod and required-kind watches, without waiting for every workload calculation. A failed kind invalidates dependent results. Required kinds also fail readiness, even when traversal ignores them. Optional kinds leave unrelated workloads running.

## How the controller handles a Pod

After `Resolve` returns the root, the controller associates the Pod UID with that root UID and queues the root.

The root worker applies workload filters, evaluates status and maps its Pods using the selected Karta. It also runs the submitter extractor when enabled.

Before publishing, the controller checks that the objects, owners, definition and scope still match the calculation. Status, creation time and each Pod mapping publish after their own API checks succeed.

Evaluation and verification run separately on one rate-limited queue, alongside object and membership work. Repeated keys combine, and workers read current state when they start. Events queue verification immediately, while periodic checks keep it running between events.

Verification shares complete API LISTs across workloads in a namespace and resource. Inputs must match UID and resourceVersion in the same API version. These reads detect stale cached objects. The local input checks reject work overtaken during calculation. Membership also needs complete Pod LISTs and verified owner paths, because checking only known Pods could miss a new member.

Every accepted Job update with a new `resourceVersion` queues its descendant Pods for `Resolve`, even when its owners are unchanged. Duplicate callbacks for the same object version and watch run are ignored. A root change queues status and dependent Pod mappings. Deletion removes the UID and retries surviving dependents. When a Pod moves to another root, the controller removes the old association before publishing the new one.

## Resolve ownership, then interpret the root

Watch logic, scope and readiness stay in the controller. Discovery receives an `InformerReader` and uses it to read cached objects.

The controller sends object changes to one `Discovery` through `Upsert` and `Delete`. Watch and definition changes use `CoverageChanged` and `DefinitionChanged`. Each returns a `ChangeSet` with affected objects, roots to reconcile and missing watches.

`Resolve(ctx, podRef)` reads the current cached state and returns a workload or an error. It does not start watches or wait for missing owners. If a required owner has not arrived while its watch is loading, it returns `ErrPending`. The exporter controller retries as watch events arrive.

A selected Karta supplies `Status` and `PodMapping`. These operations share Discovery's references and ownership graph:

```go
type Discovery struct {
    mu        sync.RWMutex
    reader    CachedReader
    inventory kartadefinitioninventory.Interface
    ownership *OwnershipIndex
}

type CachedReader interface {
    Get(context.Context, ObjectRef) (*unstructured.Unstructured, error)
}

type ObjectRef struct {
    GVK             schema.GroupVersionKind
    Namespace       string
    Name            string
    UID             types.UID
    ResourceVersion string
}

type OwnershipIndex struct {
    nodes    map[types.UID]*OwnerNode
    children map[types.UID]map[types.UID]struct{}
    byKind   map[schema.GroupKind]map[types.UID]struct{}
}

type OwnerNode struct {
    objects map[schema.GroupVersionKind]observation
    parents map[types.UID]struct{}
}
```

`nodes` indexes objects by UID, so siblings share their ancestors. `children` finds descendants affected by an owner change, including children received before their parent. `byKind` finds objects affected by Karta changes. Pods can be parents too.

An `observation` records the reference, controller owners and source watch while full objects stay in informers. Each resolution walks the shared graph without caching a complete path, so siblings still traverse their common ancestors. Conflicting owners for one UID across API versions fail resolution.

### Follow controller owners

Discovery follows controller owners and checks parent UIDs. After the required watch initializes, a missing parent returns `ErrUnresolvedOwner`. Wrong UIDs, cycles and multiple controller owners return the same error. An unreadable parent cannot become a root boundary. Cache absence alone cannot confirm deletion.

![A Pod waits for parent coverage, then shares the ownership graph with a sibling.](./ownership-traversal.png)

Traversal continues past an owner that has a Karta:

```text
worker Pod -> worker StatefulSet -> leader Pod -> leader StatefulSet -> LeaderWorkerSet
```

LeaderWorkerSet is the root even if only StatefulSet has a Karta. Discovery does not evaluate intermediate definitions.

With `leaderworkerset.x-k8s.io/v1/LeaderWorkerSet` in `ignoredOwnerKinds`, Discovery stops before reading that owner. The leader StatefulSet becomes the root and gets `owner_resolution="ignored_owner"`. The path below that boundary must still resolve.

Ignoring this ownership edge does not filter out the LWS object itself. If it has a selected Karta, the exporter can still report its status and creation time.

### Return the root and its interpretation

`Resolve` returns a `GenericWorkload` with the root and the path from the starting object. If the root has a selected Karta, `KartaWorkload()` returns a handle that evaluates the same root using that definition.

```go
type GenericWorkload struct {
    root       ObjectRef
    Path       []ObjectRef
    Resolution OwnerResolution
    karta      *KartaWorkload
    state      CapturedState
}

type KartaWorkload struct {
    root      ObjectRef
    selection kartadefinitioninventory.Selection
    discovery *Discovery
    state     CapturedState
}
```

For the JobSet example, the result is:

```text
GenericWorkload.Root(): JobSet team-a/train, UID jobset-uid
GenericWorkload.Path: [Pod train-workers-1-0, Job train-workers-1, JobSet train]
GenericWorkload.Resolution: resolved
GenericWorkload.KartaWorkload(): selected JobSet interpretation
```

`OwnerResolution` is a string enum with successful values `resolved` and `ignored_owner`. `CapturedState` records the inputs so `Discovery.IsCurrent` can detect changes during calculation. The handle keeps a definition snapshot, so definition changes require resolution again. `Walk` visits the root and observed descendants, but complete Pod membership still requires the controller's check.

| `Resolve` result | Runtime behavior |
| --- | --- |
| Success with Karta | Use root identity and selected interpretation. |
| Success without Karta | Definition absence is confirmed. Use generic identity and verified creation time, with `<unknown>` component and instance and an empty group. Emit no status. |
| Error with root UID | Ownership resolved, but definition selection failed. Verified creation time may publish. The controller derives no new mapping or status. |
| Error without root UID | Ownership did not resolve. The controller retries or retains an applicable previous result. |

Pod roots require a selected Pod Karta to emit Pod-info, status or creation rows. This also applies when an ignored owner makes a Pod the root. Catalog `core-pod-v1` gives component `pod`, empty instance and group, and Pod status. Resource-type health does not depend on this choice.

## Definition selection and changes

Discovery asks the inventory which Karta applies and where it came from:

```go
type Interface interface {
    Lookup(schema.GroupVersionKind) (Selection, error)
}

type Selection struct {
    Definition *v1alpha1.Karta
    GVK        schema.GroupVersionKind
    Origin     Origin
}

type Origin struct {
    Source          Source
    Name            string
    UID             types.UID
    ResourceVersion string
}

type Source string
const (
    SourceCatalog Source = "catalog"
    SourceCluster Source = "cluster"
)
```

Catalog-only lookup is ready after construction. Cluster lookup needs the watch initialized by `Run`. `Lookup` returns a copy from the cache. Confirming absence requires every enabled source to initialize. A failed source returns an error even with a catalog candidate, so a cluster-source failure cannot switch dependent output to the catalog.

Lookup selects by group/kind across versions. `Selection.GVK` gives the selected root version. `Origin` provides the definition name and source for status labels, plus UID and resourceVersion for cluster definitions.

Each source includes definitions matching its include list or label selector, or all definitions when both are empty. Exclusions always win.

Cluster definitions win over catalog definitions. The oldest cluster definition wins, with name order breaking ties. Catalog selection uses name order. Invalid selected definitions return an error without trying another candidate.

The inventory watches Kartas independently so it can serve lookups without the exporter controller. The controller has its own watch to withdraw affected output and queue recalculation. This avoids notification callbacks between the two components, but their caches can receive a change at different times. Publication waits until both select the same definition.

Selecting another API version starts a full-object watch and requires verification of the same root UID.

| Karta change | Runtime behavior |
| --- | --- |
| First selection | Watch full roots and evaluate them, including roots without Pods. |
| Winner edited or replaced | Remove old status and dependent mappings immediately. Evaluate the new snapshot. Failure cannot restore the old interpretation. |
| Winner deleted | Choose the next candidate or confirm absence. Absence restores generic coordinates and removes status. A Pod root stops publishing without its Pod Karta. |
| Target group/kind or version changed | Reconcile old and new selections and watches. Replace versioned output for the same lifetime. |
| Source failed | Invalidate dependent output. Do not treat failure as absence. |

After definition removal, metadata watches preserve known generic roots. They do not discover every unknown kind with no Pods. Restart rebuilds watches from definitions and observed owner chains. [Definition selection](./consistency.md#definition-selection) specifies cache agreement.

## Evaluate components and status

The controller resolves `podRef`, then evaluates status and maps the full `pod` object through the Karta handle. The reference and Pod must have the same UID and resourceVersion:

```go
workload, err := discovery.Resolve(ctx, podRef)
if err != nil {
    return err
}
interpretation := workload.KartaWorkload()
if interpretation == nil {
    // Runtime handles the generic-root case separately.
    return nil
}
batch := discovery.NewEvaluationBatch()
status, statusErr := interpretation.Status(ctx, batch)
mapping, mappingErr := interpretation.PodMapping(ctx, pod, batch)
```

An evaluation batch shares root calculations across status and sibling Pods, while status and mapping can succeed independently.

```go
type StatusResult struct {
    Defined bool
    Matched []v1alpha1.ResourceStatus
    state   CapturedState
}
type PodMapping struct {
    Component string
    Instance  string
    Group     string
    state     CapturedState
}
```

Both results expose `State()` for publication checks. Periodic API checks queue root reconciliation, which creates a new evaluation batch. This reevaluates JQ even when objects are unchanged, so time-dependent expressions can produce new results.

### JobSet mapping in the SDK

For the [opening JobSet example](#from-one-pod-to-workload-metrics), `NewComponentFactoryFromObject(karta, jobSet)` receives the full JobSet. `NewPodQuerier(pod)` receives the full Pod.

| SDK call | Produces |
| --- | --- |
| `InferPodComponent` | Component name. |
| `InferPodComponentInstance` | Named instance. |
| `ExtractReplicaKey` | Replica group key. |

The result is `{Component: "replicatedjob", Instance: "workers", Group: "1"}`. Mapping fails when a required instance is missing or not declared on the root. The group is only a key: the SDK does not compare it with the replica count or treat it as a Pod index.

### LWS: inherit the group expression

LWS defines a logical `group` containing Pod-producing `leader` and `worker` components. The group has no Kubernetes object or Pod template. Consider this worker:

```yaml
metadata:
  labels:
    leaderworkerset.sigs.k8s.io/worker-index: "1"
    leaderworkerset.sigs.k8s.io/group-index: "2"
  annotations:
    leaderworkerset.sigs.k8s.io/leader-name: leader-2
```

Its `leader-name` annotation matches the worker selector. A nonzero worker index alone does not select it. The leader selector matches `worker-index="0"`.

The LWS definition has no named instances. Its `worker` component belongs to the logical `group`, which defines the replica key expression. Discovery uses the nearest such expression along the Karta's `ownerRef` links. These links describe Karta components, separately from Kubernetes ownership.

The expression runs on the same Pod and reads `group-index="2"`. The result is `{Component: "worker", Instance: "", Group: "2"}`. A missing expression gives an empty group. An extraction error fails mapping.

### Workload status

`GetStatus(ctx)` evaluates the root component against the full workload object. For LWS, it matches `Running` when `.status.replicas` is positive and the ready and updated counts both match it.

Without a status definition, the exporter emits no status rows. Otherwise, successful evaluation returns every matching phase, or `Undefined` when none match. Expression errors fail the evaluation rather than producing `Undefined`.

## Submitter identity

The submitter extractor returns an identity from one object's metadata and GVK:

```go
type Interface interface {
    Evaluate(context.Context, metav1.Object, schema.GroupVersionKind) (Submitter, error)
}
type Submitter struct { Identity string }
```

`submitterextractor.New(expression)` compiles CEL with a string result. Invalid CEL rejects startup. The extractor receives metadata as `labels`, `annotations` and `resource`. Resource contains namespace, name, group, version and kind. CEL cannot access spec, status or Kubernetes APIs.

```cel
"team.example/submitted-by" in annotations
  ? annotations["team.example/submitted-by"] : ""
```

For JobSet `train` with this annotation set to `alice`, `Evaluate` returns `Submitter{Identity: "alice"}`. Each associated Pod is evaluated separately. A missing annotation returns empty identity. Successful metadata results are cached across status-only changes. Cancellation, evaluation limits and invalid output return errors.

When enabled, API-server admission policies record the authenticated CREATE caller in `karta.run.ai/submitted-by`. UPDATE protection stays in place even if the recording policy is later narrowed or disabled. The CEL example reads a separate, custom annotation. CEL can extract either value, but it does not authenticate the caller. See [policy details](./consistency.md#submitter-update-protection).

The controller compares root and Pod identities, including Pods hidden by `podSelector`. When all nonempty values agree, Pod-info and status share that `submitted_by` value. Empty values do not conflict with a nonempty one. All-empty results, conflicts, extraction errors and unverified membership leave the label empty, as do invalid mapping and status rows. Disabling extraction removes the label. A submitter error alone leaves other verified results available.

Agreement needs a complete Pod list because an unseen Pod could carry a different identity. The controller shares one namespace list across roots and verifies every owner path, including paths outside a workload. One unresolved Pod therefore blocks renewal and can temporarily clear other workloads' identities. Pod creation, deletion, owner changes, and label or annotation changes invalidate the check. [Membership checks](./consistency.md#pod-membership-and-pagination) describe expiry and renewal.

An empty `submitted_by` means the identity is unknown. Changing its value creates a different metric series.

## Updates and failures

Pod-info and status carry `observation_valid`. A workload can be `Failed` while its observation is valid. The label describes whether the exporter verified its result, not whether the workload is healthy.

After verification fails or expires, the exporter may retain a previous result with `observation_valid="false"`. Consumers must filter that label. Creation time has no validity label, so an unverified timestamp is omitted. A status-expression error alone does not remove a verified timestamp.

For a Pod last verified as instance `workers`:

| Change | Pod-info result |
| --- | --- |
| Pod resourceVersion changes, including status-only updates | Hide its mapping until reevaluation. |
| Evaluation and verification succeed | Publish the new mapping as valid. |
| Evaluation fails | Retain an applicable previous mapping as invalid, with empty submitter. |
| Association moved, deletion or scope exit | Remove superseded output permanently. Failure cannot restore it. |

Retention requires the same Pod UID, root UID and definition snapshot.

A root resourceVersion change hides status, dependent Pod mappings and creation time while the controller recalculates them. Each result returns independently. A Pod change leaves unchanged root status, creation time and sibling mappings available. It can still clear their shared submitter value by invalidating membership.

| Error | Result |
| --- | --- |
| `ErrPending` | Watch/source initialization produces no new result or failure reason. Unchanged existing results remain. |
| `ErrPermissionDenied` | Mark dependent observations invalid with `permission_denied`. |
| `ErrUnavailable` | Read/source failure or expiry reports `api_unavailable`. |
| `ErrUnresolvedOwner` | Missing owner, wrong UID or invalid chain reports `unresolved_owner`. Retained ownership becomes `unknown`. |
| `ErrInvalidDefinition` | Remove invalid interpretation and report `invalid_definition`. |
| `ErrEvaluation` | Retain applicable affected results as invalid with `evaluation_failed`. |
| `ErrStale` | Discard superseded work and retry. Preserve newer output. |

The controller checks these wrapped sentinel errors with `errors.Is`. Cancellation remains a context error. Detailed error messages are not metric labels. Without an applicable earlier result, the exporter omits the affected identity, mapping or phase.

Failed reads or watches immediately invalidate dependent results. A normal WATCH close reconnects without discarding current verification. Failed reconnects and short empty watches count as failures. Only fresh API checks restore validity, so scrapes cannot renew it. Rapid changes can prevent matching reads and temporarily hide output.

Replacement watches must synchronize. An object missing from a replacement cache is removed only after a fresh complete LIST confirms its absence. [Consistency details](./consistency.md) cover delayed callbacks, UID deletions, verification expiry, locking and independent field deadlines.

## Publication and delivery

The store keeps immutable records per root, with separate verification for status, creation time, membership and each Pod mapping. Pod rows use small pages, so updating one Pod does not copy every sibling. [Memory and collection](./consistency.md#memory-and-collection) describes storage and cache lifetime.

The OTel callback calls `Store.Capture()` once and formats all families from the same records and collection time. It performs no API reads, ownership walks or expression evaluation. This is a consistent view within the exporter, not an atomic Kubernetes snapshot.

| Family | Output |
| --- | --- |
| `karta_pod_workload_info` | Value `1` with workload and Pod coordinates. |
| `karta_workload_status` | Selected phases as `1` when matched, otherwise `0`. |
| `karta_workload_created_timestamp_seconds` | Verified root creation timestamp. |
| `karta_exporter_observation_valid` | Resource-type health as 1/0. Known-workload failures as 0 only. |

The KEP defines the labels and failure reasons. Healthy workloads have no separate health row.

Prometheus delivery is the default. Its OTel reader serves `/metrics`. Alternatively, OTLP uses a periodic HTTP/protobuf or gRPC reader, with no `/metrics` or ServiceMonitor. Only one mode runs. Both retain health endpoints. Omitted OTLP samples depend on backend expiry.

Prometheus translation, scope info and target info are disabled to preserve names and labels. SDK overflow aggregation is disabled to avoid merging identities.

Replicas with overlapping namespaces duplicate watch work. Separate namespace sets divide it.

## Current gaps

- Discovery can resolve cluster-scoped roots, but the controller does not publish their Pod-info, status or creation rows. Resource-type health is separate. Cluster-scoped workload output still needs a KEP decision.
- Detailed reconciliation logging is not implemented in the PoC.

[kep]: ../../../keps/0001-metrics-exporter/README.md
[configuration]: ../../karta-metrics-exporter/configuration.md
