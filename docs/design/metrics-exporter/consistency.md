<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Verification and publication details

The [implementation design](./README.md) explains the components and workload flow.
This document describes how the controller handles overlapping events, API reads
and metric collection. [KEP-0001](../../keps/0001-metrics-exporter/README.md)
defines the public behavior.

<a id="definition-selection"></a>

## Definition selection

The catalog comes with the SDK version. Cluster definitions have a Kubernetes UID
and resourceVersion, recorded in `Origin`.

The inventory and controller watch cluster Kartas independently. The inventory
keeps definitions for lookup. The controller watches for changes that affect
workloads. The inventory sends no notifications.

These watches can receive the same change at different times. The controller
publishes results only when both agree on the selected definition:

1. A Karta callback records the candidate under the publication lock. It includes
   the target group/kind. Deletes use the UID, so a delayed delete cannot remove a
   new Karta with the same name.
2. If the selected definition changes, the controller removes its old status
   and dependent Pod mappings, then queues affected roots before releasing the lock. Changes that
   cannot affect selection leave the results in place.
3. A worker calls inventory `Lookup` outside the lock and prepares the definition.
   Errors or unfinished preparation do not mean that no definition exists.
4. The controller checks the returned definition against its own observations.
   This includes the source, filters, selection rules, target GVK and readiness
   of enabled sources. A matching definition version alone is insufficient if
   another candidate could win.
5. If either watch is ahead, the worker retries until both agree. UID and
   resourceVersion are compared for equality, never sorted.
6. Under the lock, the controller checks that these inputs still match and saves
   the selection. It also records confirmed absence when no definition applies.

Every enabled source must initialize before the inventory can confirm absence.
A failed source invalidates dependent results and prevents catalog fallback.
An invalid selected definition returns an error. Recovery needs fresh API checks.

The lock protects local state. It cannot turn separate caches or Kubernetes
objects into one atomic API snapshot.

### Definition changes and root watches

A first selected definition starts full-object watches and evaluation for its
root kind, including roots with no Pods. Selecting another root API version
requires a watch and fresh verification of the same root UID. The controller
replaces that lifetime's versioned output.

If a selected Karta changes its target group/kind or version, the controller
reconciles both the old and new selections and watches. When a definition is
removed, metadata watches keep known generic roots available. This does not
discover every unknown kind with no Pods. Restart rebuilds watches from selected
definitions and observed owner chains.

## API verification

Discovery evaluates cached objects. The controller checks those objects against
fresh paginated LISTs. Events queue a check immediately. Periodic checks also run
when no events arrive.

Object work, root work, verification and membership checks share one rate-limited
queue. Verification runs separately from evaluation. A root worker uses the API
results already available for its inputs.

For each check, the controller:

1. Records the current scope, watch, owners, selection and failures before the API call.
2. Requests metadata with UID, resourceVersion and owner references. It leaves
   resourceVersion unset for a most-recent read, rather than using `"0"`.
3. Compares the returned UID and resourceVersion with the calculation's inputs
   for the same group/version/resource (GVR). Matching metadata verifies the
   full object held by the informer.
4. Queues objects that differ. It leaves newer informer objects and valid checks
   for unchanged objects in place.
5. Saves the check only if the local state recorded before the call still matches.

The saved result and its deadline are the verification evidence used at collection.
Metadata requests fall back to full JSON only for HTTP 406 or 415. Other failures
remain errors. Both response formats use the same checks.

Owner checks are shared by namespace/resource or unresolved reference. Root and
cluster-definition checks are also shared. The fixed catalog needs no renewal.
Only one stream check runs at a time for each namespace/GVR. The worker pool
bounds concurrent work. Each LIST reads its pages sequentially.

Internal timings are:

| Timing | Value |
| --- | --- |
| Periodic refresh | Queue checks every 45-60 seconds, with jitter. |
| Evidence expiry | 120 seconds from the start of the fresh read. |
| LIST page deadline | 30 seconds. |
| WATCH lifetime | The controller's context. |

These internal defaults are not Helm settings. Queue delays can postpone checks.
A missed verification deadline makes the result invalid. A scrape, bookmark or successful delivery does not extend it.
A normal WATCH close reconnects and keeps current evidence. A short empty WATCH
or failed reconnect is a failure.

A source or resource failure immediately invalidates results that depend on it.
Checks started before that failure cannot clear it. Recovery starts a new round
of checks. Repeated failures of the same type and scope are combined. Rapid
object changes can prevent matching API reads and temporarily hide output.

## Cached objects and Karta evaluation

The controller records each object version when its callback runs. `InformerReader`
returns a full object only when its UID and resourceVersion match that recorded
version. It also rejects an informer object that has advanced ahead of its callback.
This keeps a calculation from mixing an ownership path with a newer object body.

The SDK chooses among Pod-producing components. It visits the root component,
then children in definition order. A single candidate is selected directly.
With several candidates, the first matching type selector wins. No candidate or
no matching selector fails mapping.

`Status` evaluates the root component without extracting scale. One evaluation
batch shares root calculations across status and sibling Pod mappings. Periodic API
checks queue root reconciliation and create a new evaluation batch. JQ runs
again even on unchanged objects, including expressions that depend on time.

### SDK calls and inherited group expressions

Discovery calls `resource.NewComponentFactory(definition, accessor)` with the
selected Karta. The accessor runs JQ against the full root object and caches
extracted instance IDs within the batch. `NewPodQuerier(pod)` receives the full
Pod. `InferPodComponent` returns the component name, `InferPodComponentInstance`
returns its named instance, and `ExtractReplicaKey` returns the group key.
Missing or undeclared required instances fail mapping.

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

For LWS status, `GetStatus(ctx)` matches `Running` when `.status.replicas` is
positive and the ready and updated counts both match it.

## Pod membership and pagination

Submitter agreement needs every Pod belonging to the workload, including Pods
hidden by `podSelector`. Reading only known Pods cannot detect a newly created Pod.

The controller lists all Pods in a namespace once per verification round and
shares that result across roots. It follows verified owner paths for every Pod,
then compares each root's Pod UIDs with its observed membership. Paths that place
a Pod outside a workload also need verification.

Pod creation, deletion, owner changes, and label or annotation changes invalidate
this namespace check. Owner callbacks record the change under the publication lock,
before workers resolve the new root. An unresolved Pod prevents renewal for the
whole namespace. This can temporarily clear `submitted_by` for other workloads.

A status-only Pod change leaves membership unchanged. Its object verification
still needs a matching resourceVersion. It can delay the next membership check
without revoking the current one before expiry.

Incomplete lists and expired continuation chains are rejected. Results from an
old scope, watch or failure state are rejected too. Partial pages cannot prove
membership or deletion. A complete list confirms absence only if the object's
local state has not changed since the read began.

Membership expires at the earliest deadline among the Pod LIST and all owner
checks used to classify the Pods. A new LIST cannot renew an older owner check.
The result describes the Pod LIST snapshot. It does not prove that no later
change exists. Relevant events invalidate it when the controller accepts them.

Uncertain membership clears `submitted_by`. Separately verified mapping, status
and creation time remain available. A missing owner can also invalidate the
affected Pod's mapping.

## Controller errors and watch failures

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

## Deletion and delayed events

Confirmed deletion removes the UID and its verification evidence. Failed or
partial reads cannot confirm deletion. A new UID at the same name is a different
object.

The controller remembers a deleted UID while a watch could still deliver an old
callback for it. It keeps this guard until each relevant watch has processed the
deletion or been replaced.

Replacing a watch invalidates its old verification evidence. The controller
stops the old generation, waits for the new store and initial callbacks to
synchronize, and rejects callbacks from the old generation. An object missing from the new store is removed only after a
complete most-recent LIST confirms its absence and its local state still matches.

This applies to both the ownership API version and the version selected by the
Karta. Once all relevant watches have caught up, the deletion guard can be removed.
Earlier calculations remain invalid without retaining deleted UIDs forever.

## Publishing a calculation

`NewRuntime` binds the informer reader and publication store to one mutex.
Object events, scope changes, definition changes and failures use this short
publication lock. Discovery's lock is acquired after it. Discovery never calls
back into the controller. Inventory lookups run outside the publication lock.

A worker records its inputs under the lock, then reads matching cache objects
and evaluates JQ/CEL outside it. Verification comes from separate queued work.
Before publishing, it takes the lock again and checks the objects, owner links,
definition, scope, watch, membership and failures.

If they still match, it replaces the result and its verification evidence in the
same locked operation. Otherwise it discards the calculation and retries.
`Discovery.IsCurrent` checks the discovery inputs recorded in `CapturedState`.
A zero state value is invalid. The controller checks its own state as well. Checking and saving under separate locks would allow an update
between them.

### Retaining results after failure

The [KEP's validity contract](../../keps/0001-metrics-exporter/README.md#reading-observation-validity)
allows the exporter to retain earlier Pod mappings and status with
`observation_valid="false"`. They must still belong to the same objects and
selected definition.

| Change | Stored result |
| --- | --- |
| Temporary failure | Keep the applicable last verified result as invalid. Ownership failure also sets `owner_resolution="unknown"`. |
| Pod moves to another root | Remove the old association and clear submitter agreement for both roots. The Pod cannot remain valid under both. |
| Selected definition is replaced or removed | Remove its coordinates and status. Independently verified root identity and creation time remain usable. |
| Ordinary input update | Hide affected fields while recalculation is pending. Waiting in the queue is not a failure. |
| Deletion or scope exit | Remove current series. |

A later failure cannot restore a removed association or an old definition's
results. Without an applicable earlier result, the exporter has no mapping or
phase to retain. Invalid rows have an empty submitter value.

Mapping and status have separate validity. The first collection after expiry
marks retained rows invalid. Creation timestamps are omitted when unverified.
A status-expression error alone does not remove a verified creation timestamp.

## Memory and collection

The store indexes records by root UID. Records are immutable. Updates replace
the affected record without copying the global map.

Pod rows are held in pages of at most 256 rows. A one-Pod change copies its page
and the page-pointer array. Other pages stay shared. The record header holds
status, submitter agreement and shared verification results. A definition change
can require rebuilding every page.

Row lookup scans a record's pages. Inserts, deletes and root moves use the same
checked publication step. Nested maps and slices are immutable, so a collection
can keep reading an older record while an update replaces it. Shared failure
state can invalidate results without copying every record.

Collection takes the publication lock to capture record pointers, verification
evidence and one timestamp. It releases the lock before formatting metrics.
All four metric families use that captured view for expiry and membership checks.
Capture takes O(workloads + dependency-evidence nodes). The view is consistent
within the exporter, not an atomic snapshot of Kubernetes.

Discovery calculation caches and successful submitter evaluations may be evicted
and recalculated. Active ownership links and unresolved references remain while
their objects are in scope. Queue items are removed after successful processing
and retried after errors. Informers hold full roots and Pods, with metadata for
intermediate owners. Queues hold keys. Verification
copies the needed metadata and releases each LIST page before reading the next.
It does not retain pointers into the page.

## Scope and readiness

[Configuration](../../karta-metrics-exporter/configuration.md) defines the full
settings. Namespace and Pod selectors default to all. Root filters default to
none. Exclusions win. Cluster definitions are enabled by default; the catalog is
disabled and excludes `core-pod-v1`. `ignoredOwnerKinds` and `requiredKinds` are
empty by default.

Root labels cannot filter child watches. Intermediate kinds need read
permissions without explicit discovery entries. Resource rules use the selected
Karta's API version, or check each observed root version separately when no
Karta exists. Namespace and workload scope exits remove current rows. A failed
scope refresh cannot add workloads.

A failed kind invalidates only dependent results. Required kinds also affect
readiness even when traversal ignores them. Optional-kind failures leave
unrelated workloads running.

## Submitter evaluation

`submitterextractor.New(expression)` compiles CEL with a string result. Invalid
expressions prevent startup. The extractor keeps a bounded cache of successful
metadata results. Cancellation, evaluation limits and invalid output return
errors. A status-only change can reuse a cached result because the input metadata
has not changed.

<a id="submitter-update-protection"></a>

## Submitter update protection

CREATE capture follows `submitterAdmission.matchConstraints` and
`matchConditions`. A separate VAP and binding protect already captured identities
on UPDATE:

```yaml
# UPDATE protection policy: matching fragment.
matchConstraints:
  resourceRules:
    - operations: ["UPDATE"]
      apiGroups: ["*"]
      apiVersions: ["*"]
      resources: ["*/*"] # Main resources and subresources.
      scope: Namespaced
matchConditions:
  - name: already-captured
    expression: >-
      has(oldObject.metadata.annotations) &&
      "karta.run.ai/submitted-by" in oldObject.metadata.annotations
validations:
  - expression: >-
      has(object.metadata.annotations) &&
      "karta.run.ai/submitted-by" in object.metadata.annotations &&
      object.metadata.annotations["karta.run.ai/submitted-by"] ==
      oldObject.metadata.annotations["karta.run.ai/submitted-by"]
    message: "The recorded submitter cannot be changed or removed."
```

The UPDATE binding has no namespace selector, object selector or CREATE capture
condition. It also covers subresources that carry the annotation. The chart keeps
this protection whenever VAP is served, even when capture is disabled. Removing
it requires a separate administrator action.

Policies use `failurePolicy: Fail`. Validation bindings use `Deny`. Capture
configuration is rejected when the required APIs are unavailable.
[Configuration](../../karta-metrics-exporter/configuration.md) lists supported versions.

## Metric reader configuration

OTLP headers come from a Secret-backed environment variable. TLS files are
mounted read-only. Secret changes require an exporter restart.
