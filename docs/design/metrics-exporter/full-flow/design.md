<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Inside the metrics exporter

Historical baseline: this walkthrough describes `ee7d2ac6` before independent
Pod and workload publication. The [current consistency contract](../consistency.md) and
[controller guide](../../../../karta-metrics-exporter/pkg/controller/README.md)
describe the simplified implementation. The original walkthrough and diagrams
remain here to preserve the earlier design and review context.

A Pod belongs to a Job, and that Job belongs to a JobSet. Prometheus needs the JobSet identity, the Pod's component and replica group, and the JobSet's normalized status. A Pod name or a single owner reference cannot supply all of that. The exporter follows the complete controller-owner chain, evaluates the selected Karta against the full root object, and publishes an immutable result for scraping.

This document describes the simplified implementation on the branch containing this file. The concrete example uses the built-in JobSet definition. Its objects and event order are illustrative. This is not a captured cluster trace.

[Open the zoomable full sequence](./design.preview.html) |
[SVG](./01-full-story.svg) |
[PNG](./01-full-story.png) |
[PlantUML source](./diagrams/01-full-story.puml)

![Full story](./01-full-story.png)

The fixed columns are logical components. Application, Runtime, Discovery, Reader, Inventory, SDK, label resolver and Store run in one exporter process. Kubernetes and Prometheus are external. The exporter does not have a second inventory observer or a remote evaluation service.

The letters below match the single sequence. Source callbacks, different queue keys and scrapes may overlap. The diagram places them in a readable order without claiming a global execution order.

<details open>
<summary>A-I. Startup, sources and Pod-to-root discovery</summary>

## A-C. Configuration and process ownership

The command loads defaults, then the YAML file, then environment settings, then explicit flags. An explicit supported flag takes precedence even when empty. Unknown YAML keys and invalid configuration fail startup.

The defaults are a 45-second refresh interval, a 2-minute maximum observation age, a 30-second API request timeout, two workers per ownership-discovery and root-publication queue, API QPS 20, and burst 40. Maximum age must exceed the refresh interval. `leafWorkers` and `rootWorkers` default to zero, which inherits `workers`; each override resolves configuration precedence before inheritance. The cluster definition source is enabled by default. The catalog is disabled by default; its default exclusion contains the standalone Pod definition.

The example enables only the JobSet catalog entry. It leaves cluster definitions enabled to explain precedence and source failure. It assumes that no selected cluster Karta overrides JobSet. Namespace, root-object and Pod selectors are omitted, so they do not filter this example.

~~~yaml
kartaDefinitionSource:
  catalog:
    enabled: true
    include:
      - jobset-x-k8s-io-jobset-v1alpha2
  cluster:
    enabled: true
labelRules:
  - name: team
    metrics: [karta_workload_status]
    sources: [workload]
    expression: 'has(object.metadata.labels) && "team" in object.metadata.labels ? object.metadata.labels["team"] : ""'
  - name: team
    metrics: [karta_pod_workload_info]
    sources: [pod, workload]
    expression: 'has(object.metadata.labels) && "team" in object.metadata.labels ? object.metadata.labels["team"] : ""'
~~~

Application constructs one shared API rate limiter for dynamic, metadata and discovery clients. Reader receives those clients and a deferred REST mapper. A REST mapper translates a group/version/kind into the API resource path and its namespace scope. Application passes maximum age as Reader's metadata TTL.

Inventory selects the catalog and prepares cluster-definition selection. Discovery receives Reader and the ignored-owner configuration. The label resolver validates and compiles CEL rules once. Store holds publications. Runtime owns the source streams and coordinates those helpers.

Application registers the OTel observable collector before starting Runtime and HTTP serving. Runtime starts typed ownership-discovery workers, root-publication workers, and two maintenance workers. Discovery workers resolve Pod attribution and admit selected non-Pod candidates. Root workers only evaluate and publish. `leafWorkers` counts the discovery workers for both input kinds. At default configuration this is two workers in each queue. A control sync starts the source streams. A ticker later schedules source verification, owner metadata refresh and watch synchronization.

The HTTP server provides:

| Endpoint | Meaning |
| --- | --- |
| /healthz | The handler returns 200. It does not inspect workloads. |
| /readyz | Required exporter source streams are synchronized, healthy and recently verified. |
| /metrics | Format the currently captured publication view. Maximum one concurrent request. |

Readiness requires the Pod source. It also requires the Namespace source when namespace selection is configured, the Karta source when cluster definitions are enabled, and every configured required kind. An optional selected kind can fail without blocking readiness. Its resource-type observation health still reports that failure.

## D-E. One Runtime observer, several resource streams

Runtime creates a direct client-go Reflector for each desired resource and namespace scope. Its store adapter synchronously accepts add, update and delete observations into Reader, invalidates affected evidence and queues typed work. It performs no API requests or expression evaluation. Reader owns the persistent accepted full objects; no informer index or second full-object store remains. The source wrapper preserves read provenance outside Kubernetes fields and strips managed fields.

The initial source path uses LIST/WATCH behavior supplied through client-go. WatchList negotiation is optional. Unsupported initial-event negotiation falls back through the reflector's LIST path. It is not a second observer.

A full LIST records its start time, failure epoch and sequence. Accepted objects receive an expiry based on that read start. A late callback cannot gain a fresh lifetime from delivery time alone. Watch events also carry provenance before acceptance. Retired stream callbacks, obsolete deletes and contradicted older reads are fenced. `Replace` stages and validates the complete snapshot before applying present objects and UID-safe absence. It checks source incarnation, failure epoch, read age and object/alias revisions. Even an empty snapshot has a read stamp. Only an accepted replacement updates source readiness and the definition snapshot.

Runtime passes a complete accepted Karta snapshot to Inventory. Inventory applies name and label filters. It groups definitions by API group and kind, without using version as the election key. A selected cluster definition takes precedence over the built-in catalog. Among cluster candidates, the oldest creation timestamp wins; name breaks a timestamp tie.

The elected definition is validated. An invalid elected winner remains an error. Inventory does not choose a valid loser to hide it. Runtime also refuses to treat an unknown or unhealthy cluster source as an empty source and silently switch to the catalog.

The selected definition carries both the full Karta and its target GVK. That GVK determines which root representation is evaluated. Its origin records source, definition name, UID and resource version. Built-in catalog entries have no UID or resource version.

Runtime plans full watches for Pods, selected definition kinds and required kinds. It also retains full watches needed by discovered roots. If namespace selection is configured, Runtime observes Namespaces and creates namespaced streams only for matching namespaces. Without it, namespaced resources use all-namespace streams. Cluster-scoped resources normalize to an empty namespace and reuse one resource watch.

## F-G. The concrete objects

The root object used by the SDK in this example is the following JobSet. These are the relevant fields of an illustrative object, not an installation manifest.

~~~json
{
  "apiVersion": "jobset.x-k8s.io/v1alpha2",
  "kind": "JobSet",
  "metadata": {
    "namespace": "demo",
    "name": "train",
    "uid": "js-1",
    "resourceVersion": "s5",
    "creationTimestamp": "2026-01-01T00:00:00Z",
    "labels": {"team": "platform"}
  },
  "spec": {
    "replicatedJobs": [{
      "name": "worker",
      "replicas": 2,
      "template": {
        "spec": {
          "parallelism": 1,
          "template": {
            "spec": {
              "restartPolicy": "Never",
              "containers": [{
                "name": "worker",
                "image": "example.invalid/worker:v1"
              }]
            }
          }
        }
      }
    }]
  },
  "status": {
    "conditions": [],
    "replicatedJobsStatus": [
      {"name": "worker", "active": 2, "ready": 0, "failed": 0}
    ]
  }
}
~~~

The intermediate Job supplies ownership metadata. Its full status is not needed for this chain.

~~~json
{
  "apiVersion": "batch/v1",
  "kind": "Job",
  "metadata": {
    "namespace": "demo",
    "name": "train-worker-0",
    "uid": "job-1",
    "resourceVersion": "j7",
    "ownerReferences": [{
      "apiVersion": "jobset.x-k8s.io/v1alpha2",
      "kind": "JobSet",
      "name": "train",
      "uid": "js-1",
      "controller": true
    }]
  }
}
~~~

The accepted Pod provides both its controller owner and the fields used by Pod selectors and labels.

~~~json
{
  "apiVersion": "v1",
  "kind": "Pod",
  "metadata": {
    "namespace": "demo",
    "name": "train-worker-0-a",
    "uid": "pod-1",
    "resourceVersion": "p10",
    "labels": {
      "jobset.sigs.k8s.io/replicatedjob-name": "worker",
      "jobset.sigs.k8s.io/job-index": "0",
      "team": "batch"
    },
    "ownerReferences": [{
      "apiVersion": "batch/v1",
      "kind": "Job",
      "name": "train-worker-0",
      "uid": "job-1",
      "controller": true
    }]
  },
  "spec": {
    "restartPolicy": "Never",
    "containers": [{"name": "worker", "image": "example.invalid/worker:v1"}]
  }
}
~~~

Runtime accepts the Pod event and enqueues its object identity. The object worker reads the accepted Pod. If its labels no longer match the Pod selector, Runtime removes its previous attribution. Otherwise it calls Discovery with the Pod's GVK, namespace, name, UID and resource version.

Discovery reads the accepted full object first. For each hop it checks identity, scope and any requested resource version. It follows the controller owner only. Ordinary non-controller owner references do not extend the path.

The example has no planned full Job watch because only the JobSet catalog entry is enabled. Reader can obtain the intermediate Job from its metadata cache or a metadata GET. Concurrent GETs for the same key share the pending read. If metadata content negotiation fails with 406 or 415, Reader falls back to a full GET and keeps the metadata. It does not start a full Job watch merely to traverse this edge.

Reader checks a read revision before accepting a response. A newer accepted observation or invalidation can supersede an in-flight read. TTL starts when the read starts. An over-age result is stale even if the network call eventually succeeds.

The resulting path is ordered from the input to the root:

~~~text
Pod demo/train-worker-0-a [pod-1, p10]
  -> Job demo/train-worker-0 [job-1, j7]
  -> JobSet demo/train [js-1, s5]
~~~

The terminal JobSet has no controller owner. Discovery returns it as Root and retains all three path elements. It rejects a changed UID, malformed controller reference, ownership cycle and paths beyond 1024 objects. A missing owner does not turn its child into a guessed root.

## H-I. Root confirmation, generic roots and zero Pods

After discovering ownership, Runtime looks up the selected Karta by the root's group and kind. A definition on an intermediate Job does not stop the ownership walk. The definition determines how the actual root is interpreted.

Runtime ensures that a full watch exists for the selected root GVK. Metadata alone is insufficient for root status or workload custom labels. If the accepted full root is not available yet, work waits for it and retries.

Before assigning the Pod, Runtime checks matching path identities, available evidence and the current input Pod resource version. Full-object evidence at this step must have a nonzero expiry; this is not yet a blanket check that every dependency is currently valid. Final commit guards and captured-view validity provide the later checks. It also checks that the root is still a terminal boundary across known representations. A same-UID observation in another known version that now has a controller owner contradicts an old root claim.

The Pod resolution path in an ownership-discovery worker stores the Pod-to-root association and indexes every object in the path back to affected Pods. It marks the Pod dirty and queues the root UID. The root worker alone publishes the complete workload. That reverse index lets an intermediate Job change invalidate the exact dependent paths. An owner change can detach a Pod from one root and attach it to another.

Selected definition watches can discover roots before Pods exist. A selected non-Pod object becomes a candidate in the ownership-discovery input queue. A terminal candidate has a one-element Discovery path. A nonterminal candidate walks upward, even when its ancestor has no selected definition and no Pods. Runtime establishes full-object coverage for the discovered ancestor and registers the candidate's path dependencies. Missing ancestors retain retry work; changed ancestry dirties the candidate. An admitted root can publish creation time and status with no Pod rows. The exporter does not infer Undefined merely because the workload has zero Pods.

Candidate admission can wait on owner GETs. It uses the discovery workers so those reads cannot occupy every publication worker. An accepted update to an already admitted healthy root can queue its UID directly. One blocked candidate can still delay another discovery input; it does not prevent independent root publication.

An unrecognized non-Pod root can still publish generic ownership information and creation time. It cannot publish Karta status without a definition. A standalone Pod without a Karta is explicitly excluded. Ignoring a configured next-owner GVK stops at the current object and records owner_resolution=ignored_owner.

</details>

<details open>
<summary>J-O. Root evaluation, component mapping, labels and emitted metrics</summary>

## J. What a publication contains

The following are relevant fields from the implementation. Omitted fields and synchronization details remain in the linked source.

~~~go
type ObjectRef struct {
    GVK             schema.GroupVersionKind
    Namespace       string
    Name            string
    UID             types.UID
    ResourceVersion string
}

type GenericWorkload struct {
    Root       ObjectRef
    Path       []ObjectRef
    Resolution OwnerResolution
}
~~~

Discovery uses these identity-bearing values. A resource name alone is insufficient because a deleted object can be recreated under the same name.

~~~go
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
~~~

Selection is the interpretation input. It is separate from the discovered ownership path.

~~~go
type Publication struct {
    Workload       WorkloadRecord
    Status         Evidence
    Creation       Evidence
    PodPages       []*PodPage
    FailureReasons []string
}

type Evidence struct {
    ExpiresAt    time.Time
    Reason       string
    Dependencies []string
}
~~~

Evidence says which observations justify a result and how long they remain usable. Dependencies are generation keys, not API object names. For example, a failed watch generation can invalidate every row that depends on it without modifying those immutable rows.

Creation depends on the root observation and root generation. Status additionally depends on definition selection and, when enabled, the cluster Karta source. Each Pod mapping also depends on the Pod proof and complete owner path. Merging evidence takes the earliest expiry.

This separation matters during a Karta source failure. The root creation timestamp can remain independently valid while normalized status and component mapping become invalid.

The root worker snapshots state under Runtime's lock, then evaluates outside it. It chooses a full refresh when the root or interpretation changed, when a source requires it, when no previous publication exists, or when dirty work exceeds one Pod page. Otherwise it copies only dirty Pod state. The current accepted root still supplies the status calculation.

Root selection applies the root-object selector and API resource rules. Exclusion rules win. With no inclusion rules, non-excluded resources pass. A Pod selector applies to Pods; it does not replace the root selector.

## K. Exact status input, expression and result

The JobSet catalog definition has a root component named jobset. Runtime creates a ComponentFactory from that Karta and the full JobSet JSON above. It calls GetRootComponent().GetStatus(ctx).

The call chain is:

~~~text
Runtime.reconcile
  -> prepareWorkload(selected Karta, full JobSet)
  -> resource.NewComponentFactoryFromObject
  -> factory.GetRootComponent
  -> Component.GetStatus
  -> Accessor.ExtractStatus
  -> condition extraction and status matchers
  -> expression runner on the same JobSet object
~~~

The root accessor reads .status.conditions. JobSet has no phase-path definition. Its Initializing expression is:

~~~jq
(.status.replicatedJobsStatus // [])
| any(.active > 0 and (.ready // 0) == 0) and all(.failed == 0)
~~~

With active=2, ready=0 and failed=0, the result is true. Its Running expression is:

~~~jq
(.status.replicatedJobsStatus // [])
| any(.active > 0 or .ready > 0) and all((.failed // 0) == 0)
~~~

The same input also produces true. The matcher converts an expression result to a string and compares the first result with the configured expected value, "true". These are the actual expressions in the catalog, not suggested replacements.

Completed, Failed and Suspended use matching conditions with status "True". The example's empty conditions match none of them. The successful result is MatchedStatuses=[Running, Initializing], in the SDK's mapping iteration order.

The SDK checks every mapped status. Several statuses can match. Within a status, alternative matchers are ORed. Within one matcher, its configured phase, conditions and expression requirements are ANDed. A successful evaluation with no matching mapped status produces Undefined.

The exporter does not calculate JobSet status by aggregating Pod or child Job phases. The JobSet controller has already written the JobSet's counts and conditions. The exporter evaluates that root observation. SDK StructureSummary is used for component inference, not status aggregation.

These outcomes differ:

| Root interpretation outcome | Exported behavior |
| --- | --- |
| Both example expressions match | Running=1 and Initializing=1. Other phase gauges are 0. |
| Valid status definition, successful evaluation, no match | Undefined=1. Other phase gauges are 0. |
| No Karta, or no status definition | No workload-status series is fabricated. |
| JQ or condition extraction error | evaluation_failed. A compatible previous status can remain with invalid evidence. |
| Catalog Failed matcher succeeds | Application phase Failed=1. This is distinct from an exporter error. |

## M. Component, instance and replica group use two different objects

All ComponentFactory accessors are bound to the full root object. Naming a child component Kind=Job does not cause that component's spec accessor to fetch a Job.

The JobSet definition declares the child replicatedjob, owned by jobset. It is the only Pod-bearing leaf in this definition, so InferPodComponent selects it directly. The full Pod is separately converted into a PodQuerier.

| Operation | Input object | Actual selector | Result |
| --- | --- | --- | --- |
| Allowed component instance IDs | JobSet | .spec.replicatedJobs[].name | worker |
| Pod instance | Pod | .metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] | worker |
| Pod replica group | Pod | .metadata.labels["jobset.sigs.k8s.io/job-index"] | "0" |

The inferred instance must belong to the component's allowed IDs from the root object. The result is component=replicatedjob, component_instance=worker and replica_group_id=0.

The replica group comes from PodReplicaSelector. It is not the JobSet catalog's gang-scheduling group-by value. If the selected component lacks a replica selector, mapPod walks its declared component-owner links to look for one.

Missing required instance information or an invalid selector result is an evaluation error. When a changed Pod needs a new calculation, failure produces a new row with unknown component and instance, even if the previous row had known coordinates. The cache can retain this evaluated result and its error reason when ownership, definition and root sources are otherwise usable. A compatible previous row can instead remain invalid when its input object becomes unavailable.

## L-M. CEL custom labels

Runtime resolves workload-status labels after root status evaluation and before iterating Pods. Pod labels are resolved after each needed Pod mapping calculation. The label rule receives exactly one selected object as CEL variable object. The example team rule uses the JobSet for workload status, yielding platform. For Pod attribution it tries the Pod first, yielding batch, so it does not need the fallback JobSet value.

Source order is part of the configuration. An empty value tries the next source. An error is recorded for that label and also allows the next source. A later successful source can provide the label while the earlier error remains recorded as an evaluation_failed health reason.

The resolver compiles rules once during construction. ForWorkload creates a resolution function for an immutable root object. Workload results are evaluated lazily and cached across metrics and Pods. A root object change creates a new function. A Pod-only change preserves it.

The function has this contract:

~~~go
type ResolveFunc func(
    context.Context,
    string,
    *unstructured.Unstructured,
) (Result, error)

type Result struct {
    Labels map[string]string
    Errors map[string]error
}
~~~

The string argument is the metric name. The object argument is an optional Pod; the workload object is already captured by ForWorkload. Calls to one returned function must be serialized. Runtime's per-root queue supplies that serialization.

CEL results must be strings. Expression length, evaluation cost, output length and cancellation checks are bounded. Defaults are 4096 expression bytes, 10000 cost units and 1024 value bytes. Cancellation returns a top-level error and is not cached as a normal workload result. A single custom-label failure does not turn root status into Failed.

## N. Final publication guards

Before committing, Runtime reacquires its lock and rejects work if any of the following changed:

1. The root state pointer or its generation.
2. The global or per-kind definition proof key.
3. Definition lookup success, selected origin, or selected GVK.
4. The accepted selected root resource version.
5. The root's terminal ownership boundary, unless a root failure is already being represented.
6. Any active watch, metadata or ownership dependency used by otherwise valid calculated evidence.

The origin comparison includes the cluster definition's resource version. A resource-version-only Karta update does not require the same semantic invalidation as a spec change, but an in-flight calculation still checks its exact origin.

A rejected calculation restores drained dirty work and retries. It cannot publish an old success into a newer source generation.

Store.Publish swaps one root publication atomically under Store's lock. Published maps, slices and Pod pages must never be mutated. Full rebuilds use BuildPodPages. Sparse updates use WithPod and share unchanged pages. A page holds at most 256 rows. Sparse updates still copy the page-pointer list; they are not constant-cost regardless of root size.

## O. What scraping actually does

The OTel callback captures Store once. The captured view contains immutable publication references, copied mutable failure state and one timestamp. Valid checks explicit failure, failed dependencies and expiry at that timestamp. An invalidation after Capture affects the next captured view.

The callback performs no Kubernetes reads, JQ evaluation or CEL evaluation.

For this example:

| Metric | Example output |
| --- | --- |
| karta_pod_workload_info | Value 1 for pod-1 -> js-1, component replicatedjob, instance worker, replica group 0, team batch. |
| karta_workload_status | Nine phase gauges. Running and Initializing are 1. Completed, Failed, Degraded, Suspended, Suspending, Resuming and Undefined are 0. Team is platform. |
| karta_workload_created_timestamp | Root creation time in Unix seconds, only while independent creation evidence is valid. |
| karta_exporter_observation_valid | Resource-type collection validity and known workload failure reasons. It is not application readiness. |

Pod rows and workload-status rows carry observation_valid. A retained invalid row can still have value 1 or a previous phase value. Consumers must use the validity label when they require trusted attribution or status. The creation timestamp metric has no such label and is omitted when its creation evidence is invalid.

A known workload failure emits a workload-scoped validity value of 0. Healthy workload-scoped values of 1 are not emitted by this collector. Resource-type health emits its current 0/1 validity. When several reasons apply, the collector prioritizes permission_denied, api_unavailable, invalid_definition, unresolved_owner, then evaluation_failed.

</details>

<details>
<summary>P-U. Updates, errors, freshness, recovery and shutdown</summary>

## P. Changes invalidate the smallest relevant input

A Pod update invalidates its old mapping proof before replacement work completes. Discovery rechecks ownership, then marks that Pod dirty. The root label scope can be reused when the root object did not change.

A root spec, status or label update invalidates the old root-generation proof and schedules a full root refresh. A changed Karta snapshot is re-elected. Runtime compares identity, selected GVK and spec, invalidates the affected group/kind, updates watches and reevaluates affected roots. A cluster-source failure can affect all definition-dependent results.

Each root key is serialized by the work queue. Source acceptance and commit use Runtime's lock. Root evaluation runs outside that lock so Kubernetes events and other roots can continue.

## Q. Errors and the visible result

| Failure | Owner of the decision | Result and recovery |
| --- | --- | --- |
| Invalid startup configuration or CEL compile | Application / resolver | Return error before serving. Correct configuration and restart. |
| Owner GET denied | Reader / Discovery | permission_denied; invalidate affected proof. Retry through queued work or later source refresh. |
| Owner missing, UID replaced, cycle, malformed owner | Discovery / Runtime | An unresolved or stale chain. Recheck identities and invalidate affected prior evidence; do not invent a new root. |
| API unavailable or proof stale | Reader / Runtime / captured view | api_unavailable. Return retryable work or expose expired evidence as invalid. |
| Invalid elected Karta | Inventory / Runtime | invalid_definition. Preserve the winning error rather than select a losing definition. |
| Root status expression fails | SDK / Runtime | evaluation_failed. Retain compatible previous status as invalid, if one exists. |
| Pod component/instance/replica selector fails | SDK / Runtime | Failed mapping evidence; a new mapping uses unknown component/instance. |
| One CEL label fails | Label resolver / Runtime | Continue other labels and configured fallback sources; keep the error in workload health. |
| Calculation superseded during evaluation | Runtime commit guard | Discard it, restore dirty work and return ErrStale. |
| Context canceled | Owning worker / helper | Stop. Do not turn shutdown into a retry loop. |

A returned queue error receives exponential retry from 10 milliseconds up to 10 seconds. Successful work calls Forget. Some evaluation failures are deliberately published as failed evidence and do not return a queue error. Their recovery follows later input changes or refresh work.

Known retained values are not evidence that the system is currently healthy. Their dependencies and expiry determine validity independently. A first unresolved Pod has no confirmed root publication to fabricate.

## R. Verification, metadata refresh and expiry

Runtime periodically verifies each full-object source with paginated metadata LISTs. Each page requests a limit of 500. The round is bounded by maximum age, and each page is also bounded by request timeout. The code rejects repeated continuation tokens, inconsistent list resource versions, superseded failure epochs and expired rounds.

The complete verification result is compared with accepted full objects:

- Same UID and resource version can renew evidence for the existing full object.
- A missing UID removes the accepted observation and dependent state.
- A changed resource version invalidates the old full object's evidence. Metadata verification does not replace the full spec/status object.
- A newer callback or invalidation that arrived during verification is protected by sequence checks.

Intermediate owners have a different refresh path. Reader groups retained metadata by resource and namespace, lists the group and retains only requested names. It falls back to individual GETs when collection LIST permission is denied. A 406/415 metadata representation failure can fall back to the dynamic client.

Pod-source synchronization also triggers optional owner-metadata priming. It derives demand from referenced owners, performs grouped reads and can continue upward as new owners appear. Priming is bounded and coalesced. Ordinary Discovery GETs continue making progress. A failed priming optimization does not become the only discovery path.

The reverse path index checks affected ownership edges and wakes Pod leaves or non-Pod candidates. Metadata refresh remains necessary when an intermediate has no full-object watch. The configured interval is a schedule under healthy sources and available worker/API capacity, not a guaranteed convergence deadline.

The root worker records the earliest evidence deadline while building publication. A sparse replacement can lower that deadline but cannot extend unchanged rows. Removing the earliest row may leave an early deadline until the next full pass. Healthy roots stagger renewal before that deadline; changed inputs bypass delayed renewal. This avoids scanning all published Pod rows just to choose a renewal time.

The captured view checks deadlines at scrape time. If refresh falls behind, expired data becomes invalid without requiring a new Kubernetes event. A quiet watch therefore cannot keep a publication valid forever.

## S. Failure and recovery generations

A source failure revokes its current proof key immediately. Dependent immutable publications become invalid through Store's failure map. The old generation remains invalid even if a later read succeeds.

A recovered source receives a new watch proof key. Its newly verified inputs rebuild publications under the new evidence. Store retires abandoned failure keys only when no current publication or health record refers to them.

If a current-source callback is rejected and accepted full state is not already proven equivalent, Runtime invalidates the source and schedules one coalesced restart. client-go can advance its watch cursor after a store callback error, so returning an error alone cannot repair the missed body. The maintenance lane restarts with a new Reflector and an authoritative full snapshot. Retired-source callbacks are harmless. Metadata verification alone cannot reconstruct a missed spec or status.

A canceled or retired LIST is rejected before it records a replacement stamp. The first LIST of authoritative recovery uses an empty resourceVersion to request current state, rather than resourceVersion `0`, which permits a cached snapshot.

An expired watch cursor schedules verification and allows Reflector recovery. A Karta stream closing is treated more strictly because the definition source must remain known. While cluster definitions are enabled but unavailable, Runtime does not silently interpret an incomplete snapshot as absence and fall back to catalog entries.

## T. Deletion and reparenting

Pod deletion removes its association and reverse path entries. The next root publication removes its row. Root deletion removes the entire root publication. A recreated object with the same name and a different UID is a different workload.

An intermediate-owner change invalidates the old path first. Rediscovery then finds the new complete chain and moves the Pod attachment. A root that gains a controller owner is no longer a confirmed terminal root.

When a Namespace stops matching its selector or is deleted, Runtime removes its roots, prunes metadata and resynchronizes desired streams. Obsolete watch generations are retired. These state transitions do not imply that every source allocation is freed at the exact same instant.

## U. Shutdown

An interrupt, SIGTERM, Runtime completion or HTTP server failure ends serving. Application cancels the shared child context. Runtime shuts down its queues, cancels resource streams and waits for workers. API calls use those contexts.

Application performs HTTP shutdown with a 10-second bound and waits for the serving goroutines. It unregisters the collector before shutting down the meter provider, also with a 10-second bound. Shutdown errors are joined and returned. Publications are in memory and are rebuilt on process restart.

</details>

<details>
<summary>Why these pieces exist, and what can be simpler</summary>

## Responsibilities and tradeoffs

| Piece | Why it is needed | Simpler direction and its limit |
| --- | --- | --- |
| One Runtime source owner | Source failure and definition availability must agree with published validity. | Keep Inventory as a pure snapshot selector. A second inventory watch would add another source state to reconcile. |
| Discovery with complete path | A Pod's direct owner may be only an intermediate controller. | Keep traversal generic. Stopping at the first supported kind would change the workload identity. |
| Shared Reader | Roots need full objects; intermediate owners usually need only metadata. | Reuse accepted full observations first. Removing metadata grouping is possible but needs cold-start/API-load measurements. |
| UID and scope checks | Names can be reused; owners can be cluster-scoped. | Keep these checks even in a proof of concept. Names alone are an unsafe identity. |
| Karta selection | Root fields and status conventions differ between APIs. | Centralize selection, validation and precedence. Avoid another interpretation path in the collector. |
| SDK status and Pod mapping | The existing Karta contract describes fields and component relationships. | Reuse the SDK. Reimplementing expressions in the exporter duplicates behavior and error rules. |
| Compiled CEL rules | Custom labels need controlled source order and bounded work. | Cache workload results for the current immutable root. Do not evaluate the same workload rule for every Pod. |
| Immutable publication Store | Scrapes should not wait for Kubernetes, traverse owners or run expressions. | Keep formatting in the collector and computation in workers. Sharing unchanged pages reduces repeated copies. |
| Dependency generations and deadlines | An event can invalidate data while another worker is computing its replacement. | Consolidate code around common invariants, but retain immediate invalidation and final commit guards. |
| Root/Pod dirty work | One Pod label update should not rebuild every Pod mapping. | Preserve sparse updates; document that page-pointer copying and full refreshes still have size-dependent cost. |

The achieved behavior is a generic Pod-to-root map, definition-based root status, zero-Pod root visibility, separate validity and application status, and scrapes that consume prepared memory. This document explains the implementation. It does not establish that every edge case is bug-free or that production-scale performance is acceptable.

Further proposed changes, unresolved problems and release validation belong in the [open issue and fix ledger](../open-issues.md). That keeps proposed work separate from the source behavior shown here.

</details>

<details>
<summary>Source evidence and verification limits</summary>

## Evidence map

Links are relative to this branch. Symbol names identify the implementation behind the important arrows.

| Stage | Source and symbol | Claim |
| --- | --- | --- |
| A | [command](../../../../karta-metrics-exporter/cmd/main.go), run | Flags, signal context, Kubernetes client configuration and process exit. |
| A | [configuration](../../../../karta-metrics-exporter/internal/config/config.go), Loader.Load, DefaultConfig, Validate | Precedence, defaults and startup validation. |
| B-C, O, U | [application](../../../../karta-metrics-exporter/internal/app/application.go), New, ServeListener | Shared limiter, construction, routes, collector registration and shutdown. |
| C, O | [runtime](../../../../karta-metrics-exporter/pkg/controller/runtime.go), Run, runWorker, Ready, requiredWatch | Worker counts, queues, retries, periodic work and readiness. |
| D-F | [source streams](../../../../karta-metrics-exporter/pkg/controller/sources.go), ensureWatch, listWatch, monitorSource, accept | Direct Reflectors, complete replacement, provenance, accepted observations and immediate invalidation. |
| E | [inventory](../../../../karta-metrics-exporter/pkg/kartadefinitioninventory/inventory.go), New, ReplaceCluster, Lookup | Snapshot selection, precedence, election and invalid-winner behavior. |
| E, P | [definition lifecycle](../../../../karta-metrics-exporter/pkg/controller/runtime.go), refreshDefinitionsLocked, definitionChanged | Source availability, semantic change detection and definition proof generations. |
| G | [ownership traversal](../../../../karta-metrics-exporter/pkg/discovery/resolve.go), Get, ControllerOwner | Controller-only traversal, complete path, identity validation and termination. |
| G | [Reader](../../../../karta-metrics-exporter/pkg/controller/reader.go), Cached, GetMetadata, fetchMetadata, getMetadata | Cache preference, GET coalescing, metadata fallback and read revisions. |
| F-I | [input dispatch and Pod work](../../../../karta-metrics-exporter/pkg/controller/leaf.go), resolveInput, resolveLeaf | Ownership-discovery queue dispatches Pods and candidates; Pod path commits attribution and queues publication. |
| H-I, J-N | [root work](../../../../karta-metrics-exporter/pkg/controller/root.go), admitRoot, discoverPath, reconcile | Root attachment, selected representation, proof construction, evaluation and commit guards. |
| H | [root boundary](../../../../karta-metrics-exporter/pkg/controller/reader.go), rootBoundaryLocked, terminalOwnerBoundary | Cross-representation ownership contradiction and terminal root checks. |
| H, P, T | [path dependencies](../../../../karta-metrics-exporter/pkg/controller/owners.go), setPodPathLocked, invalidatePathDependentsLocked | Reverse path index and targeted invalidation. |
| J | [root selection](../../../../karta-metrics-exporter/pkg/controller/root.go), selectedRoot, selectedResource | Root labels, resource rules and exclusion precedence. |
| J, N-O | [publication types](../../../../karta-metrics-exporter/pkg/store/store.go) and [Store](../../../../karta-metrics-exporter/pkg/store/publication.go), Publish, Capture, Valid, WithPod | Separate evidence, immutable capture, validity and bounded Pod pages. |
| K-L | [JobSet catalog](../../../../karta/sdk/pkg/catalog/kartas/jobset.go) | Actual GVK, components, root status expressions, instance IDs and replica selector. |
| K-L | [factory](../../../../karta/sdk/pkg/resource/component_factory.go), NewComponentFactoryFromObject | All component accessors bind to the full root object. |
| K | [component](../../../../karta/sdk/pkg/resource/component.go), GetStatus | Root component delegates status extraction. |
| K | [accessor](../../../../karta/sdk/pkg/resource/accessor.go), ExtractStatus, matchStatus, matchByExpression | Conditions, matcher composition, expression output and Undefined fallback. |
| K | [status entries](../../../../karta/sdk/pkg/api/runai/v1alpha1/structure.go), StatusMappings.Entries | Status mapping iteration order. |
| L | [structure summary](../../../../karta/sdk/pkg/instructions/summary.go), NewStructureSummary | Declared component and leaf relationships. |
| L | [Pod inference](../../../../karta/sdk/pkg/instructions/pod.go), InferPodComponent, InferPodComponentInstance | Component choice and allowed instance matching. |
| L | [Pod querier](../../../../karta/sdk/pkg/resource/pod_querier.go), ExtractReplicaKey, GetMatchingInstanceId | Selectors evaluate the full Pod and validate results. |
| M | [label resolver](../../../../karta-metrics-exporter/pkg/labelresolver/resolve.go), New, ForWorkload | Compile-once rules, ordered source fallback, workload cache and per-label errors. |
| M | [CEL evaluator](../../../../karta-metrics-exporter/pkg/labelevaluator/cel.go), Compile, Evaluate | object binding, strings, cost/length limits and cancellation. |
| O, Q | [collector](../../../../karta-metrics-exporter/pkg/adapters/otel/collector.go), collect, failureReason | One capture, nine phase gauges, retained invalid rows and reason priority. |
| R | [watch verification](../../../../karta-metrics-exporter/pkg/controller/sources.go), verifyWatch | Paged UID/RV verification, freshness renewal and missing-object handling. |
| R | [metadata refresh](../../../../karta-metrics-exporter/pkg/controller/owners.go), refreshMetadataGroup, readMetadataPages, refreshMetadataByGet | Grouped owner refresh, bounded pages and GET-only permission fallback. |
| R | [metadata priming](../../../../karta-metrics-exporter/pkg/controller/owners.go), primeMetadata, primeMetadataGroup | Initial grouped owner reads without blocking ordinary GET discovery. |
| R | [renewal](../../../../karta-metrics-exporter/pkg/controller/root.go), scheduleRenewalLocked; [path refresh](../../../../karta-metrics-exporter/pkg/controller/owners.go), scheduleMetadataPathsLocked | Deadline-aware renewal and targeted path scheduling. |
| S-T | [source recovery](../../../../karta-metrics-exporter/pkg/controller/sources.go), failWatchLocked, recoverWatchLocked, deleteObjectLocked, retireWatchObjectsLocked | Failure generations, recovery, deletion and watch retirement. |
| T | [root removal](../../../../karta-metrics-exporter/pkg/controller/runtime.go), removeRoot | Remove root associations and publication state. |

## Verification limits

The arrows and examples were checked against implementation source. The diagram is an explanatory artifact, not a runtime recording. Evaluated example values above are derived from the displayed catalog expressions and input objects; they are not claimed as a new cluster or end-to-end test.

The PNG, SVG and HTML are generated from the same editable PlantUML source. The HTML embeds its SVG and has Fit, 100% and 150% controls. It needs no server or external assets. Source links remain branch-relative in this Markdown.

The [controller audit](../controller-audit.md) records this change and operator source limits. Earlier runtime test and performance evidence is maintained separately in [verification](../verification.md) and the [open issue and fix ledger](../open-issues.md). Rendering the diagram does not validate Kubernetes behavior, race safety or production-scale performance.

</details>
