<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Controller review guide

The exporter must map a Pod to its workload and evaluate that workload's status.
For example, input Pod `web-7` names ReplicaSet `web-rs` as its controller
owner. That ReplicaSet names Deployment `web`. Discovery returns `web` as
the root. The Pod result records `web-7 -> web`; a separate workload result
records the Deployment's status using its Karta definition.

The Pod is the leaf. The workload returned by Discovery is the root.
The controller keeps Kubernetes objects in a cache, processes changes through
queues, and saves results for `/metrics`.

## 1. Startup: who starts what?

![Startup: construct components, then run Inventory, Runtime and HTTP together](images/startup.png)

1. [`cmd/main`](../../cmd/main.go) reads configuration.
2. [`bootstrap`](../bootstrap/components.go) constructs clients and components. It does not start watches.
3. [`Application.ServeListener`](../app/application.go#L80) registers the collector and starts three things together:
   - Inventory watches Karta definitions for changes, when cluster definitions are enabled.
   - [`Runtime.Run`](runtime.go#L130) starts workers and queues `"sources"` to start object watches.
   - HTTP serves `/metrics` and health endpoints.

HTTP can start before the caches are ready. `Runtime.Ready()` checks required
sources and definition health. On cancellation, `Runtime.Run` shuts down the
three queues, cancels active and replacement watches, and waits for workers
and watch goroutines.

## 2. Runtime: from an object change to a metric

![Runtime: watches update the cache, queues wake workers, and results reach metrics](images/runtime.png)

There are two different stores:

| Store | Contains | Read by |
| --- | --- | --- |
| Reader's object cache | Kubernetes objects and watch health | Discovery and workers |
| Metric Store | Calculated Pod/workload results and errors | Collector |

A scrape reads the Metric Store. It does not call Kubernetes or evaluate Karta expressions.

### The three queues

| Queue | Key shape | Handler |
| --- | --- | --- |
| `exporter-sources` | String `"sources"` | [`syncWatches`](sources.go#L38) refreshes definitions and namespace scopes, then starts shared watches |
| `exporter-pods` | `inputKey{Object: objectKey{GVK, Namespace, Name}, UID}` | [`resolveInput`](leaf.go#L24) calls `resolveLeaf` for one Pod |
| `exporter-pods` | `inputKey{Object: objectKey{Namespace: scope}, Rescan: true}` | `resolveInput` calls [`rescan`](state.go#L236) to requeue cached objects and previous candidates |
| `exporter-workloads` | `rootKey{Candidate: inputKey{Object, UID}}` | [`resolveWorkload`](root.go#L26) calls `admitRoot` to discover a candidate |
| `exporter-workloads` | `rootKey{UID: rootUID}` | `resolveWorkload` calls `reconcile` to evaluate an admitted root |

GVK means group, version and kind. UID is Kubernetes' object identity, distinct
from its name. Queue keys contain neither resource versions nor object bodies.
The empty scope means all namespaces. Without a namespace selector, watches
use this scope. With one, `syncWatches` derives admitted scopes from the
Namespace cache. Cluster-scoped resources still use the empty scope. A rescan
makes no API request.

The sources queue has one worker. `New` defaults `Workers` to two.
`LeafWorkers` and `RootWorkers` inherit that value unless set explicitly.

Event routing skips leaf work for Pods that do not match the Pod selector.
Those Pods remain in the cache because another Pod may reference them as an owner.
Previously selected or tracked Pods still get queued for cleanup. Rescans use the
same rule. The worker rechecks current labels because they may change while a
key is waiting in the queue.

### Follow one Pod

The main path is [`resolveLeaf`](leaf.go#L31):

1. A selected Pod's event updates the object cache and queues the Pod's identity.
2. A Pod worker reads the latest full Pod object.
3. Discovery follows controller owner references through the cache:
   `Pod -> ReplicaSet -> Deployment`.
4. [`admitPod`](state.go#L185) records membership and queues the root before the
   worker reads the full Deployment. This lets root events find the Pod while
   its evaluation is in flight.
5. The Deployment's Karta definition maps the Pod to a component and instance.
   The label resolver receives both the full Pod and the full Deployment.
6. [`publishPod`](state.go#L216) checks current inputs and saves the Pod result.
   [`reconcile`](root.go#L99) separately evaluates the full Deployment's status
   and labels, then calls `publishWorkload`.

A non-Pod root without a Karta definition can still provide creation information
and Pod ownership. Karta adds workload status and component mapping. A bare Pod
needs a Pod Karta definition to be included.

### How does a workload appear before it has Pods?

Objects whose GVK matches a selected Karta definition enter the workload queue
as candidates. This means an instance of the selected kind, not the Karta
custom resource itself. A candidate is an object to discover, not necessarily
the root. For example, a selected Job owned by a Deployment admits the
Deployment, even with no Pods and no Deployment definition.

[`admitRoot`](root.go#L33) discovers the candidate's owner chain.
[`admitCandidate`](state.go#L275) records its membership and queues the resulting
root UID. Only `reconcile` on that UID publishes workload results. Candidate
keys admit or withdraw membership; they never publish a workload directly.

## 3. Reflectors: how does the cache stay current?

![Reflector: LIST and WATCH feed the object cache and queue changed identities](images/reflector.png)

A client-go Reflector maintains a shared watch for a resource collection and
namespace scope. For example, one watch serves all Deployments in that scope.
There is no separate watch for each Deployment.
A collection's group, version and resource form its GVR, such as `apps/v1/deployments`.

- `LIST` supplies a complete snapshot.
- `WATCH` supplies later additions, updates and deletions.
- The controller's [`reflectorStore.Replace`](sources.go#L342) accepts snapshots;
  `accept` handles individual events. They update the Reader cache and
  queue work. They do not calculate metrics.
- The Reflector reconnects and relists when needed.

A `watch.ProxyWatcher` exposes the forwarded event channel to the Reflector.
[`watchResource`](sources.go#L250) owns the forwarding goroutine. It converts
source objects and records watch failures before forwarding events. It stops
the upstream watch and closes the channel on exit. Both Reflector stop
requests and context cancellation unblock it.

### Full objects or metadata?

| Object | What is cached | Why |
| --- | --- | --- |
| Pods | Full object | Pod mapping and label evaluation |
| Selected Karta kinds and roots being evaluated | Full object | Workload status and label evaluation |
| Other ancestors encountered by Discovery | Metadata, unless already watched in full | Follow names, UIDs and owner references |

A full object includes all fields returned by Kubernetes, including metadata,
spec and status where present. `sourceObject` removes managed fields. If an
API rejects metadata requests, the source retries with the dynamic client but
still strips the result to metadata for a metadata-only watch.

Discovery calls [`Reader.GetMetadata`](reader.go#L75) to start missing ancestor
watches on demand. It does not issue an individual API GET for every owner.

If a metadata-only kind becomes an evaluated root, Runtime starts a full watch.
[`ensureWatch`](sources.go#L113) keeps the metadata watch active while a replacement
full watch starts. `reflectorStore.Replace` accepts the complete full snapshot
before canceling the metadata watch. Workers wait and retry instead of
evaluating metadata as if it were a full object.

[`readLocked`](state.go#L530) rejects unhealthy or unsynchronized sources before
checking object identity. It returns `ErrPending` when a full read has only
metadata, `ErrStale` for a changed resource version, and NotFound for a
mismatched UID. Absence is NotFound only when a matching source is healthy
and synchronized; otherwise the worker waits or reports the source error.

## 4. Workqueues: what does a worker actually do?

![Workqueue: take a key, read current inputs, process it, and finish or retry](images/workqueue.png)

Queues hold identities, not copies of Kubernetes objects. Repeated adds of the
same pending key are merged. One queue does not give the same key to two workers at
once. Different keys can run concurrently. For example, two Pod workers can
update the same Deployment's membership.

`controllerState` and Reader share one read/write lock. It protects the object
cache, source health, selected definitions and Pod/root membership.
State methods take the lock when they read or change shared state. The handlers
in `leaf.go` and `root.go` call those methods instead of managing locks themselves.
The Metric Store has its own lock. Reader uses a separate mutex for mapper resets.

### State maps and snapshots

[`controllerState`](state.go#L28) owns these maps under the shared lock:

| Map | Meaning |
| --- | --- |
| `objects`, `sources` | Accepted objects by GVK/namespace/name, and their collection health/fullness |
| `watches`, `replacements`, `watchFailures` | Active watches, full-watch replacements and failed watch requests |
| `scopes`, `selections` | Admitted namespaces and selected definitions by group/kind |
| `podRoots` | Pod UID to root UID |
| `candidates` | Candidate key to root UID; empty UID means tracked but not yet admitted |
| `roots` | Root UID to reference, generation, owner resolution, Pod members and candidate members |
| `ancestorPods` | Pod UIDs seen as owners, so their ownership changes trigger rescans |

`rootState.candidates` stores each candidate's sources and discovery error.
[`snapshotRoot`](state.go#L302) combines those into an owned snapshot. One candidate's
success cannot clear another candidate's error. The snapshot also carries the
root reference, owner resolution and generation; it exposes no membership maps.

### Evaluation and publication

Workers follow three steps:

1. Read cached objects and take the state snapshots needed for this evaluation.
2. Run discovery, mapping, status and label evaluation after the state methods
   have released the lock.
3. [`publishPod`](state.go#L216) or [`publishWorkload`](state.go#L337) locks state again.
   It checks cached UID/resource versions, current definition selection and
   whether the workload is still a root before saving. A mismatch returns
   `ErrStale` for retry. Pod publication checks the whole discovered path and
   the full root version. Workload publication checks the root version and
   the snapshot generation.

For example, a worker starts evaluating Pod version `10`. A watch event replaces
it in the cache with version `11` while evaluation runs. The publication check
sees the changed version and rejects the old result. The next attempt reads
version `11`.

A root snapshot also carries a generation number. It prevents an old evaluation
from saving into a root entry that was removed and recreated. Workers do not
copy the whole object cache.

## 5. What causes another evaluation?

| Change | What gets queued |
| --- | --- |
| Pod changes | That Pod, if selected, previously selected, or needing cleanup |
| Known root changes | The root and its associated Pods |
| An intermediate owner changes ownership | One scope-rescan item; it requeues cached Pods and candidates |
| Inventory reports a definition change | Refresh sources; requeue affected candidates, roots, their Pods and unresolved Pods |
| A source recovers | A scope rescan and root evaluations |

For example, a ReplicaSet can gain a different Deployment owner while its Pods
stay unchanged. The scope rescan lets those Pods discover their new root.

[`refreshDefinitionsLocked`](runtime.go#L218) compares Inventory selections and
increments `definitionVersion` when selection or definition-source health changes. A
failed Inventory read keeps the previous selections but records the source
error. A health change triggers a rescan; a definition-only change targets
affected kinds, members and unresolved Pods.

Removing a definition triggers reevaluation using Inventory's new selection,
including any fallback. If no definition remains, objects of that kind stop
admitting roots as candidates. A non-Pod root can still remain through its
Pods, with ownership and creation information but no Karta-derived status or
component mapping.

If a definition selects a different API version, [`replaceCandidate`](state.go#L261)
queues its candidate and waits for admission before removing the old
membership. Confirmed absence in the new version removes the old candidate.

## 6. Errors, retries and cleanup

[`runWorker`](runtime.go#L200) takes a key with `Get`, calls its handler, then calls
`Done`. A returned error schedules exponential backoff from 10 ms to 10 s,
unless the context is canceled. A nil error calls `Forget` to clear retry
history. Pending watches, stale inputs and unresolved owners retry this way.
Pod evaluation and invalid-definition failures are saved by `failPodLocked`
without a timed retry. Workload status failures are saved by `publishWorkload`.
Changed inputs cause another evaluation.

[`failPodLocked`](state.go#L461) and `failWorkload` retain any previous successful
record and attach the current error. Publication also preserves previous values
when label or status evaluation fails. The collector combines saved errors
with current source health, so retained values are not reported as fresh success.

### Startup without a resolved owner

A Pod whose owner has never resolved has no successful mapping to retain.
`failPodLocked` saves an error result with `HasValue=false` and no workload UID.
[`collector.collect`](../adapters/otel/collector.go#L85) emits discovery health for
that input's kind and namespace, but no Pod mapping row. It does not invent a
workload identity. If a root is later confirmed but mapping still fails, the
error can be attributed to that workload without emitting a mapping value.

### Deletion is different from a failed read

- Confirmed Pod deletion calls [`removePod`](state.go#L121), removing its mapping
  and membership. Leaving the Pod selector or admitted namespace also removes it.
- Confirmed root deletion calls [`withdrawWorkload`](state.go#L322), removing the
  workload result and requeuing its Pods and candidates. It does not delete
  still-live Pods' mapping results. Their next failed discovery retains the
  last successful mapping with an unresolved-owner error. A new root with the
  same name cannot satisfy an owner reference to the old UID.
- A root that gains a non-ignored controller owner is also withdrawn. Its
  members rediscover the new root. Removing the last member lets `snapshotRoot`
  remove the old root entry and result.
- Failed or malformed lists do not erase accepted objects. `Replace` validates
  the whole snapshot before treating absence as deletion. `accept` ignores
  deletion events whose UID does not match the cached object.
- Namespace removal makes `syncWatches` retire that scope's watches and failed
  requests. Retirement routes cached removals through the same queues. Late
  callbacks from retired watches cannot repopulate the cache.

See [consistency](../../../docs/design/metrics-exporter/consistency.md) for the
detailed result and source-health rules.

## Where to read the code

| File | Responsibility |
| --- | --- |
| [runtime.go](runtime.go) | Start workers, handle definition notifications and shut down |
| [state.go](state.go) | Manage Pod/root membership, return snapshots and guard publication |
| [sources.go](sources.go) | Start Reflectors, accept LIST/WATCH results and route changes to queues |
| [reader.go](reader.go) | Read cached objects and check their identities and source health |
| [leaf.go](leaf.go) | Discover a Pod's root and save its mapping; handle scope rescans |
| [root.go](root.go) | Discover candidate roots and save workload status and labels |

Diagram sources: [startup](images/startup.excalidraw),
[runtime](images/runtime.excalidraw), [reflector](images/reflector.excalidraw),
[workqueue](images/workqueue.excalidraw).
