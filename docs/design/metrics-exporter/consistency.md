<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Verification and publication

The [implementation design](README.md) follows one Pod through the components.
This document explains what happens when an input changes during that work.
The [KEP validity contract](../../keps/0001-metrics-exporter/README.md#reading-observation-validity)
defines the metric meaning.

For example, Runtime starts mapping a Pod to JobSet `train`. An accepted Pod
update changes its owner before the calculation finishes. The old calculation
must not restore a valid row under `train`. Runtime invalidates the old Pod
generation, resolves the new path and publishes only after checking its inputs.

## Observations and evidence

Runtime's reader keeps full watched Pods and workload objects. It shares immutable
informer objects after removing managed fields. Intermediate owners fetched for
traversal are cached as metadata. Discovery checks names, UIDs and resourceVersions
before using an object. A matching name with a different UID is a replacement.

Every published result has evidence:

```go
type Evidence struct {
    ExpiresAt    time.Time
    Reason       string
    Dependencies []string
}
```

Dependencies identify watch streams, metadata reads, definitions and local root
or Pod generations. `CapturedView.Valid` checks an explicit reason first, then
the captured dependency failures, then expiry. Zero expiry and the expiry instant
are invalid. A mapping uses the earliest expiry among its required inputs.
Root creation and status do not depend on unrelated Pod paths.

A WATCH event gives Runtime a new observed object version. Periodic verification
renews unchanged observations. Scrapes and watch bookmarks never renew evidence.
Local generations detect changes already accepted by this exporter. They do not
prove Kubernetes has no newer change.

## Reads and refresh

A cold intermediate-owner lookup performs a metadata GET. Concurrent callers
share the request. The reader resolves API scope, so a cluster-scoped owner uses
an empty namespace. It accepts the response only if its recorded local state is
still current. If the API rejects metadata representation with HTTP 406 or 415,
the reader retries the GET or LIST page as a full-object request and extracts its
metadata. The same timeout, identity checks and expiry apply. Other errors do not
trigger representation fallback.

After the initial Pod watch synchronizes, Runtime groups missing owners by API
resource and namespace. A background metadata LIST seeds only the owners still
needed by those Pods. GET workers continue while the LIST runs. Newer accepted
reads, object changes or namespace withdrawal prevent an older batch from
replacing current data. A failed batch leaves the ordinary GET path in charge.
Later misses use GETs. No intermediate-owner watch is added.

Periodic refresh groups retained intermediate owners by API resource and
namespace. Each group uses a paginated metadata LIST. Unrelated returned objects
are discarded. A complete result can confirm that a retained owner disappeared;
a failed or partial result cannot. If LIST is forbidden or unauthorized, the
reader refreshes retained owners with individual GETs, preserving GET-only owner
permissions. Other LIST failures remain failures. There is no count threshold
for switching between these paths.

Watched resources have separate periodic metadata verification. Runtime compares
the returned UID and resourceVersion with the accepted informer observation for
the same API representation. Matching metadata renews the full object's evidence.
A mismatch invalidates the old result and queues work. A complete LIST can remove
an object absent from that snapshot if no newer local event superseded it.

LIST pages use a limit of 500. A page-chain resourceVersion change or repeated
continuation token rejects the round. A request timeout bounds each GET or page.
`ExpiresAt = readStart + maxAge`. An already over-age response cannot renew it.
Reads started before a newer event, failure or scope change cannot replace that
newer state.

Healthy WATCH delivery processes ordinary changes without a new LIST per event.
Periodic verification still performs LISTs. Intermediate owner changes can remain
unnoticed until refresh because traversal does not create intermediate watches.
Queue and API delays can exceed the evidence deadline, which makes affected rows
invalid until fresh verification succeeds.

The command defaults to a 45-second refresh interval, two-minute maximum age and
30-second request timeout. The object and root worker counts and shared API rate
limit bound concurrent work; see [runtime settings](../../karta-metrics-exporter/configuration.md#runtime-settings).

## Definition agreement

The inventory and Runtime observe cluster Kartas independently. Inventory owns
selection. Runtime uses its Karta watch to invalidate affected results and check
that both observations agree. Their UIDs and resourceVersions are compared for
equality, never sorted.

An accepted Karta change invalidates the affected group/kind interpretation and
queues its roots. A worker obtains an inventory selection outside the Runtime
guard. Before publication it checks the selected origin, GVK and relevant local
generation again. Selection and absence use the [inventory contract](README.md#select-the-definition).

A changed target group/kind affects both the old and new selections. A changed
API version needs coverage for that representation. Known generic roots retain
coverage even without a definition.

## Concurrent publication

Runtime has its own guard for accepted events and final publication checks.
Store owns a separate mutex. Store never takes Runtime's guard. Collector takes
only Store's lock while capturing; it formats metrics after releasing that lock.

A root generation is Runtime's local change counter, not `metadata.generation`.
A root worker snapshots that counter, the selected definition and relevant
Pod paths. It evaluates JQ and CEL outside Runtime's guard. Before saving, it
checks that the root still exists, its generation and definition agree, the root
object version matches and required evidence keys remain active. A superseded
result is discarded and retried.

Each root queue key has one worker at a time. This also serializes use of that
root's calculation cache and the read/replace/publish sequence. Store
makes individual methods atomic, not a caller's multi-call operation. Other
roots can progress concurrently.

A small Pod update rebuilds affected rows. Initial publication, root changes and
full refresh use bulk page construction. Full expiry refresh still visits all
rows and may allocate replacement pages. Unchanged immutable pages can be reused;
this does not make a full refresh constant-time.

The stored types include:

```go
type Publication struct {
    Workload       WorkloadRecord
    Status         Evidence
    Creation       Evidence
    PodPages       []*PodPage
    FailureReasons []string
}

type CapturedView struct {
    At       time.Time
    Records  []*Publication
    Health   []HealthRecord
    failures map[string]string
}

type HealthRecord struct {
    GVK       schema.GroupVersionKind
    Namespace string
    Evidence  Evidence
}

func (v CapturedView) Valid(e Evidence) (bool, string)
func (p *Publication) WithPod(uid types.UID, replacement *PublishedPod) *Publication
```

`WorkloadRecord` holds root identity, selected Karta, matching phases, creation
time and status labels. `BuildPodPages` groups at most 256 `PublishedPod` rows
per page. Each row pairs a `PodRecord` with mapping evidence. The Pod record holds
its own namespace, UID, name, coordinates, owner resolution and labels. It also
keeps the verified owner API version, which may differ from the version selected
for Karta evaluation. These pages are immutable
chunks that can be shared between publications.

`Publish` transfers immutable ownership. Callers must replace nested maps and
slices rather than mutate them. Captured publications can outlive their current
Store entries. Health input slices and their dependencies are copied at the Store
boundary. The [Store README](../../../karta-metrics-exporter/pkg/store/) defines the
helper invariants and their measured benchmark commands.

### Failure-key lifetime

For example, a Pod update fails its old local evidence key and creates a new
one. The old row immediately becomes invalid. The new calculation uses the new
key. Root and Pod generation keys retire after the same-root worker has committed
or discarded its in-flight result. Metadata and definition keys can retire sooner
because final active-key and selection checks reject obsolete work. Keys are
opaque internal strings, not Kubernetes generations or metric labels.
`RetirePublicationFailure` removes a key only when no current publication or
health record refers to it.

Store counts those references and retires a failed key when its last reference
is removed. Active failing streams keep a health reference. A failure installed
before any publication remains until explicitly cleared or retired. Replacing a
publication adds new references before removing old ones. A captured view keeps
its own failure map, so retirement cannot change an ongoing collection.

## Updates, deletion and failure

| Event | Result |
| --- | --- |
| Pod or root changes | Invalidate affected old evidence and queue recalculation. |
| Pod moves to another root | Remove its old association and queue affected roots. |
| Definition changes | Invalidate its previous interpretation and rebuild affected results. |
| Owner read or root watch fails | Mark dependent evidence invalid and retry. |
| Pod, root or selected namespace disappears | Remove its published rows and retire unused state. |
| Same name returns with a new UID | Treat it as a new object; old work cannot restore the deleted UID. |

An accepted newer object makes the previous evaluation unverified until Runtime
recomputes it. The old row can remain with `observation_valid="false"` during
that interval, even when the API is healthy. Final publication replaces it
atomically. Evidence failures and expiry also mark retained rows invalid. An old
row is kept only for the same root UID, Pod UID and compatible definition.
Deleting those objects removes their rows. No prior result means there is no
status or mapping to retain. Invalid creation evidence
omits the timestamp and contributes a workload-health failure.

An intermediate-owner GET returning NotFound fails that Pod's owner path. A
compatible old mapping can remain invalid with unknown owner resolution until
recovery or removal of its Pod/root. It is not treated as a confirmed Pod or root
delete. An accepted delete of the published Pod or root removes its rows.

Status, mapping and creation validity are separate. A status-expression error
does not invalidate an independently verified creation time. A label-expression
error removes the unavailable custom value or permits the next configured source;
it does not erase base identity. Resolver errors remain visible to workload health
even when a fallback supplies a value.

Replacing a watch keeps the existing publication invalid under the retired
watch evidence until a replacement observation is accepted. Old callbacks cannot
delete a replacement UID. A fresh complete LIST can confirm that a known UID is
absent; starting the replacement watch alone cannot. Complete relists reconcile
missed deletions. Failed or incomplete reads cannot establish absence. Scope
withdrawal removes affected rows, including Pods beneath a cluster-scoped root
whose own namespace is empty.

Failures appear in the `reason` label of `karta_exporter_observation_valid`.
Resource health uses `scope="resource_type"` and emits 0 or 1. Workload health uses
`scope="workload"` and emits failures with value 0. Retained Pod-info and status
rows carry `observation_valid="false"`; they do not gain a reason label.
The error categories are bounded:

| Error | Action |
| --- | --- |
| `ErrPending` | Wait for source or watch initialization and retry. |
| `ErrStale` | Discard superseded work and retry. |
| `ErrPermissionDenied` | Report `permission_denied` for dependent evidence. |
| `ErrUnavailable` | Report `api_unavailable`; expiry uses the same reason. |
| `ErrUnresolvedOwner` | Report `unresolved_owner` for a missing or invalid owner path. |
| `ErrInvalidDefinition` | Report `invalid_definition`; do not use that interpretation. |
| `ErrEvaluation` | Report `evaluation_failed` for the affected calculation. |

Callers use `errors.Is` on wrapped errors. Cancellation stays a context error.
Detailed messages belong in diagnostics, not metric labels.

## Readiness and collection

`/healthz` reports process health. `/readyz` requires initialized definition
sources, Pod coverage, namespace-selection coverage when configured, and every
explicitly required kind. A supplemental root or ordinary selected-kind failure
changes its resource health and dependent observations without blocking readiness
for unrelated workloads.

One scrape calls `Capture` once. Store fixes the time, immutable publication
references, copied health and failure state under its own lock. The callback uses
that view for all four families. It may finish an older view after a write; the
next capture sees that write.

Capture costs O(roots + health records + failure keys). Collection visits emitted
series and their evidence dependencies. Removing a row removes it from the next
snapshot, while backend history remains. These guarantees concern the exporter's
local state, not an atomic snapshot of the Kubernetes API.

[Component tests](../../../karta-metrics-exporter/README.md#verify), the
[real-operator matrix](../../../karta-metrics-exporter/test/e2e/) and
[modeled API measurements](../../../karta-metrics-exporter/test/scale/) provide
separate evidence. Recorded results identify the tested revisions.
