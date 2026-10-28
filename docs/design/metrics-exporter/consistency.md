<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Collection and publication

For `Pod p -> ReplicaSet r -> Deployment A`, the Pod worker publishes p's
mapping to A. The workload worker publishes A's status. A change to r's owner
schedules a scope sweep. The same Pod can then resolve to Deployment B without
a Pod update.

## Independent results

Store holds separate immutable Pod and workload results keyed by UID. A Pod
result includes its root name, UID, GVK and Karta provenance. It does not borrow
those fields from the current workload result. Either result can arrive first.

A scrape captures both result maps and source health under one store lock. It
then formats the saved values without API requests or expression evaluation.
This is a stable local view, not a transaction across Kubernetes resources. Pod
attribution and workload status may represent different revisions.

An upsert replaces one result, including its errors and source keys. Published
maps and slices are immutable. A failed evaluation retains the last successful
value with its original root and Karta provenance. It marks that value invalid.
The failed attempt must not put a new definition's identity on an old value.

A first discovery failure has no confirmed root to publish. It appears in
`karta_exporter_observation_valid{scope="discovery"}` with the input GVK and
namespace and empty workload identity. Known-root failures aggregate under
`scope="workload"`. Source health uses `scope="resource_type"`. One object's
success cannot clear another object's error.

## Source health and validity

A source key identifies the actual GVR and collection scope. An all-namespace
watch has one shared health state. Sources record initial synchronization and
observed LIST/WATCH failures, including stream errors and closure.

Results record the sources used for their object and owner chain. Enabled
cluster-Karta and namespace-selection sources also affect applicable results.
A result is valid only when it has no relevant error and every recorded source
is present, synchronized and healthy. Reconnection clears source failure; it
does not clear an object evaluation error.

Creation-time output depends on root collection and discovery. Karta-source and
status-expression errors affect status independently. A failed status expression
alone does not suppress a known creation timestamp.

Quiet objects do not expire. There is no per-object age deadline, periodic proof
refresh or global Kubernetes revision guarantee. A silent stalled connection may
retain its values until a watch timeout, reconnect or observed error. Scrape
freshness remains a separate monitoring concern. The removed `refreshInterval`
and `maxAge` settings return migration errors when supplied.

## Shared reads

Selected evaluated resources, Pods and Kartas use full-object sources. Custom
Pod expressions can read spec and status. Traversal-only ancestors use metadata
sources. Full coverage also supplies ownership metadata, so a resource does not
need simultaneous full and metadata sources during ordinary operation.

When discovery encounters a new ancestor kind, it requests a shared source and
retries until that source synchronizes. It does not issue a root GET for every
Pod. A metadata source promoted to full coverage keeps its accepted data until
the replacement list synchronizes. The replacement becomes the accepted source
before the old source is retired. Old source callbacks cannot mutate the new one.

Standard reflectors reconnect and relist. A complete accepted list removes
objects absent from that collection. A failed or partial list does not establish
absence. Metadata representation fallback is limited to APIs that reject that
representation with HTTP 406 or 415; other failures stay visible.

## Queue and membership ordering

The Pod queue owns Pod mapping and removal. It also contains coalesced scope keys.
A scope key scans cached Pods and selected candidates, then enqueues object keys.
Coalescing happens before the scan, not after scanning for every owner event.

The workload queue handles candidate admission and canonical root UIDs. A
selected child with no Pods can admit a generic ancestor. Candidate handlers own
membership and discovery errors; only canonical root handlers publish workload
values. Multiple candidates do not become independent writers for one root.

Membership records only the final root association. It does not retain a graph
of every intermediate edge. A Pod registers its root membership before reading
the current root body. Root event fanout uses the same controller lock. An event
before registration is visible to the subsequent read. An event after registration
re-adds the Pod key, including when that key is already being processed.

Root full-object changes enqueue the root and associated Pods. Workload spec or
status may supply Pod labels, so metadata-only filtering is insufficient here.
Intermediate create, deletion, UID replacement or controller-reference changes
schedule a scope sweep. Ordinary intermediate status changes do not change
ownership. Ordinary Pod creation and deletion only schedule that Pod. A small UID
set records Pods seen as ancestors, so a leader Pod changing owner still schedules
a sweep without treating every Pod as a possible intermediate.

Karta changes refresh definition selection and schedule affected work. Namespace
withdrawal retires sources and queues cleanup. A Pod can be both a leaf and a root
when its Pod Karta is selected. Adoption removes its root role while keeping the
leaf role.

## API versions

A generic Pod mapping retains the API version verified in its own owner chain.
The workload row has one canonical representation per UID, chosen independently
from the admitted resource rules. A Karta can select a different evaluated version.
That full source must synchronize before its owner boundary becomes authoritative.
A failed, retired alias cannot permanently veto a healthy elected representation.
A Pod still referencing that failed alias remains invalid through its own sources.

## Deletion and failures

Names locate objects; UIDs distinguish object lifetimes. A delayed deletion for
UID A must not remove replacement UID B with the same name. Queue workers check
current accepted objects. Events arriving during reconciliation dirty the key
for another pass.

Moving or removing membership queues the previous root. A root with no remaining
selected candidate or Pod admission is removed. Root deletion or demotion queues
members before removing derived output. Source retirement explicitly queues old
UID cleanup even though those objects are no longer in the cache.

Discovery retains UID mismatch, missing owner, invalid controller reference and
cycle checks. Missing or unavailable owners never become confirmed roots through
a success fallback. Retryable failures use the queue's rate limiter. Each label
reconciliation creates its own resolver closure; mutable closure caches are not
shared between worker goroutines.

## Validation

The source comparison is in the [controller decision](../simple-exporter-controller-decision.md).
The current branch's [implementation validation](simple-controller-validation.md)
records executed tests and their limits. Earlier proof-based results in the
[controller audit](controller-audit.md) describe the previous implementation.
