<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Verification and publication details

These protocols support the [implementation design](./README.md).
[KEP-0001](../../../keps/0001-metrics-exporter/README.md) defines public behavior.
The local PoC implements these checks. [Verification results](./behavior-checks.md) separate tested behavior from remaining capacity limits.

<a id="definition-selection"></a>

## Definition selection

The catalog is fixed by the SDK version. Cluster definitions carry their
Kubernetes UID and resourceVersion in `Origin`.

The inventory owns its cluster cache. The controller separately watches Karta
objects to invalidate work. The inventory sends no notifications.

Selection is accepted in this order:

1. The controller accepts a Karta event under its publication lock. It records
   candidate facts needed to check selection, including changes to the target
   group/kind. Deletes identify the UID, so a delayed delete cannot remove a
   replacement.
2. A change that can replace or edit the winner revokes dependent output and
   queues affected roots before the lock is released. An edit that cannot change
   the winner leaves its output current.
3. Outside the lock, a worker calls inventory `Lookup` and prepares the returned
   definition. A lookup error is unavailable selection. Pending preparation is
   neither confirmed absence nor permission to fall back.
4. Before accepting the result, the controller checks that the selection agrees
   with its accepted candidate and source facts: provenance, filters, precedence,
   target GVK and enabled-source readiness. One matching definition version
   cannot rule out a competing winner.
5. If an event has arrived but the inventory still returns the old selection,
   retry until its cache catches up. If the inventory is ahead, wait for the
   controller's observations to catch up. Compare UID/resourceVersion for equality,
   never numerical ordering.
6. Under the publication lock, accept only a result whose captured facts remain
   current. The controller privately records accepted selection state, including
   confirmed absence.

All enabled sources must initialize before absence is accepted. Source failure
revokes dependent validity and prevents fallback. An invalid winner remains an
error. Recovery requires fresh verification.

The lock orders local acceptance and publication. It cannot make independent
caches, or Kubernetes objects, into one atomic API snapshot.

## Bounded verification

Discovery reads cached objects. The controller performs fresh paginated LISTs and
shares their results across dependents. Object events queue a check immediately.
Periodic refresh also renews evidence when no events arrive.

1. Capture local scope, watch, ownership, selection and failure state before I/O.
2. Read metadata containing UID, resourceVersion and owner references. Use
   resourceVersion unset for a most-recent read, not `"0"`.
3. Compare UID/resourceVersion with the calculation's inputs for the same GVR.
   A match verifies the corresponding full informer body. Karta still needs that
   full body for evaluation.
4. Queue mismatched objects. Do not overwrite newer informer state or reject
   unchanged objects' independent evidence.
5. Accept evidence only if its captured local state still matches.

Metadata negotiation falls back to full JSON only for HTTP 406 or 415.
Other failures remain errors. Both representations use the same acceptance rules.
Owner checks are shared by namespace/resource stream or unresolved reference.
Root and cluster-definition checks are also shared. The catalog needs no renewal.

One scheduler permits one in-flight check per namespace/GVR and bounds concurrent
requests and pages. Internal defaults are:

- Jitter refreshes across 45-60 seconds.
- Expire evidence 120 seconds after the fresh read starts.

Each LIST page has a 30-second deadline. WATCH requests use the controller
lifetime context. These internal constants add no Helm values. Missing a
verification deadline invalidates evidence. Reconnects, bookmarks, scrapes
and successful delivery never extend it. A normal WATCH close reconnects without
revoking current evidence. A short empty WATCH or failed reconnect is a failure.

A source or resource-type failure revokes dependent evidence immediately when
accepted. Work started before that failure cannot clear it. Recovery starts a
new verification batch. Repeated failures are coalesced by type and scope.

## Pod membership and pagination

Submitter agreement needs every associated Pod, including hidden rows.
Known-Pod GETs cannot prove that another Pod does not exist.

1. Read one complete paginated Pod LIST per namespace per verification round,
   without `podSelector`. Share it across roots.
2. Classify every listed Pod through verified controller-owner paths.
3. Compare each root's resulting UID set with its accepted membership.
4. Accept the shared namespace proof only while membership and every
   classification path remain current. Paths used to exclude Pods also count.

A status-only Pod change does not alter membership. Its object evidence still
needs resourceVersion equality. It can delay the next proof but does not revoke
the existing proof before its deadline.

Pod creation, deletion, owner changes and label or annotation changes invalidate
the shared namespace proof. Any unresolved Pod blocks renewal throughout that
namespace. Hidden Pods count too. This conservative boundary can temporarily
clear submitter agreement for unrelated workloads in a busy namespace.
Owner events record uncertainty under the publication lock before workers
resolve the new destination.

Reject incomplete lists, expired continuation chains and results from superseded
scope, watch or failure state. Partial pages prove neither deletion nor complete
membership. A complete list confirms absence only if the object's local lifetime
state has not changed since the read began.

The membership deadline is the earliest deadline of the LIST and every owner
proof used to classify or exclude a Pod. A new LIST cannot renew an older owner
proof. An accepted relevant event invalidates the proof immediately.
The LIST proves its snapshot, not absence of later unseen changes.

Membership uncertainty clears enabled `submitted_by`. Independently verified
mapping, status and creation time remain available. An unresolved owner can
separately invalidate the affected Pod's mapping.

## Deletion and delayed events

Confirmed deletion retires a UID and revokes its evidence. Failed or partial
reads do not establish deletion. A new UID at the same name is a new lifetime.

A deletion guard remains until every stream that could deliver the retired UID
has processed its deletion or been replaced. Replacing a stream has three steps:

1. Stop the old watch generation.
2. Synchronize the replacement store and initial event handling.
3. Reject late callbacks from the old generation.

Initial informer synchronization is not fresh absence evidence. Old observations
missing from the replacement are retired only after a complete, most-recent LIST
confirms absence and their captured lifetime facts still match.

The guard covers both the ownership representation and the version selected by
the definition. It is removed only after these conditions hold. Calculations
captured before deletion remain invalid. This avoids both resourceVersion
ordering and a permanent set of deleted UIDs.

## Acceptance, retention and publication

Every controller-accepted object event, scope change, definition state change and
failure transition uses one short publication lock. Discovery's subordinate lock
is acquired after it. Discovery never calls back into the controller. Inventory
lookups run outside the publication lock.

1. Capture input state and dependencies under the lock.
2. Read matching cache bodies, evaluate JQ/CEL and verify inputs outside it.
3. Reacquire the lock. Check object, owner-edge, definition, scope, watch,
   membership and failure state.
4. In that same critical section, install the immutable replacement and evidence.
   Otherwise discard the result and keep the work queued.

`Discovery.IsCurrent` covers only part of step 3. The controller checks its own
evidence in the same commit. Checking and publishing separately would race.

The [KEP's validity contract](../../../keps/0001-metrics-exporter/README.md#reading-observation-validity)
retains applicable Pod/status observations after failure or expiry as
`observation_valid="false"`. Deletion and scope exit remove current series.
The retention rules make "applicable" explicit:

- A transient failure can retain the last verified observation for the same
  object lifetime and still-applicable association. Ownership failure also
  changes `owner_resolution` to `unknown`.
- Confirmed reparenting removes the old Pod-to-root association. A later failure
  must never revive it. The acceptance updates old/new membership and invalidates
  both submitter aggregates. Publishing the replacement cannot leave the Pod
  valid under two roots.
- A confirmed replacement or removal of the selected definition supersedes its
  interpretation. Later failure cannot revive those old coordinates or status.
  Independently valid root identity and creation metadata remain usable.
- An ordinary input change suppresses affected fields while recomputation is
  pending. Queue delay alone does not create a failure reason.
- Without a previous applicable result, emit no invented identity, coordinates
  or phase. Invalid emitted rows clear their submitter value.

Mapping and status have independent validity. The first collection after expiry
marks applicable retained rows invalid.
Unverified creation metadata omits the timestamp. A status-expression failure
alone cannot suppress a separately verified creation timestamp.

## Memory and collection views

Publication stores immutable records behind a mutable root index. Events replace
affected records without copying the global map.

- Pod rows live in small immutable pages. A one-Pod change copies its page and
  the page-pointer array. Other pages remain shared.
- The record header holds root status, submitter agreement and shared evidence.
  A definition change can require rebuilding all pages before replacement.
- Row lookup scans the record's pages. Inserts, deletes and root moves use
  the same checked commit and preserve retained views.
- Nested slices and maps remain immutable. Each page holds at most 256 rows.
- Shared failure state revokes validity without eagerly copying every record.

Collection captures one time, record pointers and required evidence under the
publication lock. Formatting happens after release. All four metric families use
that same view, including expiry and membership decisions. Capture is
O(workloads + dependency-evidence nodes). This guarantees a consistent local
view, not a transaction across Kubernetes objects.

Optional derived caches may be evicted. Active ownership edges, unresolved
references and dirty work remain until deletion or scope exit. Informer stores
retain full roots and Pods. Intermediate ownership needs only metadata.
Queues retain keys. Verification copies compact facts from each LIST page and
releases the page before continuing. Retained pointers must not pin whole pages.

<a id="submitter-update-protection"></a>

## Submitter update protection

CREATE uses `submitterAdmission.matchConstraints` and `matchConditions`.
UPDATE protection has its own VAP and binding, outside capture scope:

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

The protection binding has no escapable namespace/object selector or capture
condition. The chart retains protection whenever VAP is served, even when capture
is disabled. Removing protection requires a separate administrator action.
The old-object guard also covers subresource representations carrying the annotation.

Policies use `failurePolicy: Fail`. Validation bindings use `Deny`. Capture
rejects configuration when required APIs are unavailable.
[Configuration](../../karta-metrics-exporter/configuration.md) defines supported
versions.

## Verification results

[Behavior checks](./behavior-checks.md) records component tests, connected cluster
checks and capacity measurements. It also lists the remaining limits. Those
results are separate from the consistency rules described here.
