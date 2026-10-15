<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Live workload discovery

`pkg/discovery` observes a terminal workload and its complete owned Pod set through
an injected reader. It supports built-in and custom Karta definitions, generic
ownership for unknown kinds, and an optional caller-fed index. Offline extraction
through `resource.ComponentFactory` remains available without a client.

## Inject a client

The `Reader` interface supplies exact GVK mappings, UID-checked object reads, and
complete Pod lists. The optional `pkg/discovery/clientgo` adapter accepts an
existing `dynamic.Interface` and `meta.RESTMapper`. The SDK does not discover a
kubeconfig, start informers, or depend on controller-runtime.

The caller owns credentials, permissions, REST mapping, and client lifetime.
`NewReader` and `discovery.New` do no network work. A mapper can perform discovery
when its `RESTMapping` method is subsequently called. Use a populated mapper when
mapping must remain local; the RESTMapper interface has no context parameter.

This complete example reads a Pod's current UID before resolving it:

```go
package example

import (
    "context"

    "k8s.io/apimachinery/pkg/api/meta"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/client-go/dynamic"

    "github.com/dsx-ai-factory/workload-map/pkg/discovery"
    sdkclient "github.com/dsx-ai-factory/workload-map/pkg/discovery/clientgo"
)

func WorkloadFromPod(ctx context.Context, client dynamic.Interface,
    mapper meta.RESTMapper, namespace, name string,
) (*discovery.Workload, error) {
    reader, err := sdkclient.NewReader(client, mapper)
    if err != nil {
        return nil, err
    }
    resolver, err := discovery.New(reader, discovery.Options{})
    if err != nil {
        return nil, err
    }
    pod, err := client.Resource(schema.GroupVersionResource{
        Version: "v1", Resource: "pods",
    }).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
    if err != nil {
        return nil, err
    }
    return resolver.WorkloadForPod(ctx, discovery.ObjectRef{
        Namespace: namespace,
        Name:      name,
        UID:       pod.GetUID(),
        GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
    })
}
```

Every entry reference requires GVK, name, and UID. Namespaced resources also need
a namespace. Name reuse cannot silently select a replacement lifetime.
`WorkloadForPod` follows controller references to the terminal owner, even through
known workload kinds. `Workload` accepts an explicit terminal root, including a
root with no Pods. An intermediate owner returns `ErrNotWorkloadRoot`.

`discovery.New(nil, options)` is valid for construction. Nil and typed-nil readers
make acquisition return `ErrNoClient`; they do not return a successful empty
workload. The client-go adapter rejects nil dependencies during construction.

## Optional metadata reads for owners

For direct discovery, `clientgo.NewReaderWithOwnerMetadata(dynamicClient,
metadataClient, mapper)` accepts an existing `k8s.io/client-go/metadata.Interface`
as well. Both clients and the mapper must refer to the same Kubernetes authority
and exact resource versions. The constructor performs no network work.

This opt-in reader uses `PartialObjectMetadata` for newly encountered owners
other than core v1 Pods, after the initial full seed-to-root trace. The root and
returned Pods stay complete. Initial ancestors, core v1 Pod owners, public `Collect` and indexed
capture keep their existing full-object paths. A server that supports metadata
responses can omit owner spec/status from those later GETs. A server returning
ordinary JSON may still transfer the full body in the same response. Large
annotations or managed fields remain part of metadata responses, so this option
primarily helps when owner spec/status bodies dominate the payload.

Choose this option when identity from the requested GVK and exact mapped resource
is sufficient. Metadata envelopes do not independently prove the original object
GVK; client-go's full-JSON fallback can discard it. The default `NewReader` keeps
checking the original response GVK. The metadata route also requires complete
identity fields on every owner reference, including non-controllers. It rejects
malformed entries that a full reader could otherwise ignore. Errors never trigger
an additional full GET.

Custom adapters can implement `discovery.OwnerMetadataReader`. They must preserve
the complete owner-reference list or return a decode error. The SDK validates the
envelope and every typed owner identity, then copies retained references. The
adapter returns detached identity, resourceVersion and owner references; other
metadata fields are unspecified. It does not start or own a cache or watch.

## Ownership and observation boundaries

Discovery follows controller references, validates every owner UID and identity,
and rejects multiple controllers, cycles, malformed references, invalid scope,
and excessive depth. The default limit is 32 owner hops. A noncontroller owner
reference does not establish membership.

Direct acquisition lists all Pods in the terminal root's namespace. A
cluster-scoped root requires an all-namespace list. There are no label, node,
phase, or readiness filters: completed, failed, terminating, and unscheduled Pods
remain eligible. A standalone Pod is its own root. Labels used for semantic
attribution never replace the ownership check.

Each acquisition resolves the full relevant Pod list and memoizes owner reads for
that acquisition. The client-go adapter exhausts pagination. An expired page,
changed seed, missing owner, denied read, or unresolved unrelated ownership chain
fails acquisition; no partial workload is returned. Direct reads therefore cost
a complete namespace Pod list, or a cluster-wide list, plus the required owner
GETs. They are not optimized for repeatedly querying many roots in one namespace.

`Observation` records source, namespace, start and completion times, Pod list
resourceVersion, and an index generation when applicable. These are observation
boundaries. A Pod LIST and subsequent owner GETs are not a Kubernetes transaction.
The interval does not prove that all objects existed in one simultaneous cluster
state. Indexed capture is coherent within one published local generation; it
does not establish how recently the producer observed the cluster.

## Pod list ownership

Ordinary `Reader` results are copied before owner reads so later reader activity
cannot change the captured Pods. A reader can optionally implement
`PodTransferReader.ListPodsForTransfer` to transfer the complete list without
copying every nested field again. Collection keeps a compact array of Pod values
so a small result cannot retain hidden portions of the reader's backing array.
The list, its backing array, and all reachable mutable data must be exclusive to
the caller. The reader must not retain, mutate or reuse them, and
Pods must not share mutable data with other Pods or earlier or later results.

The client-go adapter implements this capability using its freshly decoded Pod
list. Existing callers need no configuration change. Collection applies the same
coverage, identity and ownership checks to either path; a transfer error never
falls back to another list request. A partial workload result still detaches its
selected Pods from the full list allocation. Public result accessors return
copies, and public `Collect` retains complete owner bodies.

## Definitions and generic workloads

`Options.Definitions` overrides the built-in catalog by exact group, version,
and kind. Definitions are validated and copied during construction. Duplicate
explicit GVKs are rejected. Construct another resolver to replace definitions;
previous results keep their captured definition. An override for one served
version does not authorize evaluation against another version's schema.

The jq execution layer reuses compiled simple selectors through a small shared
cache. It keys programs by the exact expression and ordered variable names, with
entry and source-size limits. Complex expressions follow the original compilation
path. Every invocation still evaluates the current input with its own execution
context and limits; workload data and evaluation results are never cached there.

Only an absent definition enables generic ownership. A broken definition or
failed read is an error. If an unknown Bundle owns two Deployments, discovery
returns the Bundle and both branches' Pods. It does not stop at either Deployment.
A generic result preserves the actual root object, has `Generic() == true`, and
exposes one generic component, `pods`, with anonymous instance `""` and no
replica axis. It makes no claim about the unknown kind's templates or scale.

`Snapshot(ctx, factory)` bridges an existing component factory to live discovery.
Its root UID, nonempty resourceVersion, and exact definition must agree with the
acquired root. A changed lifetime, resourceVersion, or definition returns
`ErrStaleInput`. It does not silently reinterpret a stale factory as the current
object with the same name.

## Immutable results and semantic scopes

A `Workload` is a detached observation. `Root`, `Object`, `Definition`, `Generic`,
`Observation`, `Pods`, `MembershipIssues`, and scope navigation issue no requests.
Returned objects, Pods, definitions, slices, and diagnostics belong to the caller.
Later client changes or index publication do not change earlier results. Acquire
a new workload to refresh it.

`Workload.Pods()` retains every ownership-confirmed Pod, including Pods with
unresolved semantic attribution. `MembershipIssues()` reports the Pod UID,
component, axis, and cause. The axes are `component`, `instance`, and `replica`.
An empty issue component means any component could be affected.

Every explicit Pod-producing component selector is evaluated, including a sole
selector. A sole producer without a type selector is implicit. Matching ancestors
can be superseded by one matching descendant; unrelated matching producers are
ambiguous. Instance and replica selectors are evaluated independently for every
ancestor. At the attribution layer, a pointer to `""` denotes a known anonymous
instance; nil denotes an absent or unresolved axis. A replica expression returning
`empty` declares that no replica grouping applies to that Pod.

Scope navigation preserves all previously bound ancestor coordinates:

| Operation | Meaning |
| --- | --- |
| `workload.Component(name)` | That component and all its logical descendants. |
| `component.Instance(id)` | Bind the component's declared instance ID. Use `""` for anonymous instances. |
| `component.Replica(key)` or `instance.Replica(key)` | Bind the nearest component or ancestor that defines a replica selector. |
| `instance.Component(name)` or `replica.Component(name)` | Descend to a named logical component while retaining existing bindings. |
| `scope.Pods()` | Return detached Pods satisfying all component and axis constraints. |

A declared instance with no observed Pods returns an empty set. An undeclared
instance returns `ErrInstanceNotFound`. An unobserved replica key returns an empty
set if the axis exists; an absent axis returns `ErrNoReplicaAxis`. Rebinding an
already constrained inherited replica axis to a different key returns
`ErrInvalidInput`. Descendant navigation cannot escape to an ancestor or sibling.

A scope returns `ErrIncompleteAttribution` when an issue could change its members.
An unrelated component issue or an unused axis does not invalidate the query.
Ambiguous producer selection conservatively blocks affected component scopes,
including the logical root scope. `Workload.Pods()` remains the complete owned set.

These functions select a JobSet instance/replica and the workers inside one LWS
group. Neither performs an API request:

```go
package example

import (
    corev1 "k8s.io/api/core/v1"

    "github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

func JobSetReplica(workload *discovery.Workload, instanceID, replicaKey string,
) ([]*corev1.Pod, error) {
    jobs, err := workload.Component("replicatedjob")
    if err != nil {
        return nil, err
    }
    instance, err := jobs.Instance(instanceID)
    if err != nil {
        return nil, err
    }
    replica, err := instance.Replica(replicaKey)
    if err != nil {
        return nil, err
    }
    return replica.Pods()
}

func LWSWorkers(workload *discovery.Workload, groupKey string) ([]*corev1.Pod, error) {
    groups, err := workload.Component("group")
    if err != nil {
        return nil, err
    }
    group, err := groups.Replica(groupKey)
    if err != nil {
        return nil, err
    }
    workers, err := group.Component("worker")
    if err != nil {
        return nil, err
    }
    return workers.Pods()
}
```

`DesiredTree(ctx)` explicitly evaluates captured spec, scale, and status data.
Discovery and scope queries never call it. It may fail even when observed Pod
membership is complete, and it returns `ErrUnsupported` for a generic workload.
It uses captured copies and issues no cluster requests.

## Initialize a caller-fed index

`Index` implements both `Reader` and `IndexedReader`. It owns object copies,
identity bindings, reverse owner edges, and root-to-Pod membership. Resolver takes
an atomic snapshot without falling back to direct requests. An uninitialized or
invalidated index returns `ErrNotSynced`.

The following initializes a complete observation. Pass explicit zero-Pod roots
through `extraRoots`; Pod enumeration alone cannot discover arbitrary empty CRs.
Use namespace `""` for cluster-root coverage.

```go
package example

import (
    "context"

    "github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

func IndexedResolver(ctx context.Context, reader discovery.Reader, namespace string,
    extraRoots ...discovery.ObjectRef,
) (*discovery.Resolver, *discovery.Index, uint64, error) {
    dataset, err := discovery.Collect(ctx, reader, namespace, extraRoots...)
    if err != nil {
        return nil, nil, 0, err
    }
    index := discovery.NewIndex()
    generation, err := index.Replace(ctx, dataset)
    if err != nil {
        return nil, nil, 0, err
    }
    resolver, err := discovery.New(index, discovery.Options{})
    if err != nil {
        return nil, nil, 0, err
    }
    return resolver, index, generation, nil
}
```

`Dataset` declares complete, unfiltered Pod coverage for one namespace or all
namespaces, full owner closure, exact mappings, and optional explicit empty roots.
`Replace` validates and atomically publishes it. A namespace-only index cannot
answer a cluster-root query; that returns `ErrInsufficientScope`. Successful
replacement establishes local readiness, not cluster freshness. A producer's
`HasSynced` signal alone is not evidence that its declared coverage is complete.

For incremental updates, the producer calls
`Apply(ctx, expectedGeneration, discovery.Changes{...})` and retains the returned
generation. Follow this publication contract:

1. Serialize the initial collection/replacement with subsequent feed publication.
   An older collected list must not overwrite a newer published feed state.
2. Supply ordered current objects from the producer's canonical cache. An event
   callback is a cue to read that cache, not authority to publish an arbitrary
   delayed payload. Include Pods as `v1/Pod` unstructured objects in `Upserts`.
3. On `ErrGenerationConflict`, reread the canonical producer state before retrying.
   Reusing the old payload with a new generation can undo a newer update.
4. Include exact mappings for newly observed kinds. Publish every required served
   version alias for a changed UID together. Aliases must agree on identity,
   resourceVersion, and owner references. Other aliases for that UID are dropped;
   the index cannot convert an object body between API versions.
5. Put UID-bearing references in `Deletes`. Deletes address one lifetime and cannot
   remove a same-name replacement. Retired UIDs stay rejected with `ErrRetiredUID`
   until an authoritative `Replace` resets that history.
6. Call `Invalidate(cause)` after watch failure, lost ordering or coverage, or a
   key-only tombstone whose UID is unavailable. A fresh complete `Replace` restores
   readiness. `Apply` does not make an invalidated index ready again.

An update can publish while owner closure is temporarily incomplete. Dependent
queries then fail explicitly with `ErrIncompleteOwnership`; they do not return an
apparently complete empty set. Changing a mapping's resource identity or scope
requires replacement. Indexed publication recomputes affected descendants, and
capture copies the selected generation without holding the lock during all
output copying.

For the built-in `Index`, the resolver reuses immutable Pod records from the
selected generation internally. It copies the root before interpretation and
keeps a private ordered membership slice. Public getters still return detached
copies. Updates publish new records, so retained workloads keep their original
values. Cached ownership depths enforce the resolver's depth limit, including
siblings deeper than the requested Pod. External `IndexedReader` implementations
continue through defensive capture copying and validation.

The SDK owns no watch fleet, resync loop, or background refresh. Existing informer
consumers can publish their existing canonical cache. Direct-only callers can
explicitly collect and replace when needed. Index memory, initial collection,
feed acquisition, publication, and output copies are costs of this mode; a warm
indexed lookup does not measure the cost of maintaining its source.

Plan for workload turnover as well as live population. Retired UIDs and observed
GVK mappings remain until a successful `Replace`; a continuous `Apply` feed can
therefore grow memory even when the live object count stays constant.
`Invalidate` retains this state. Producers that need a memory bound must arrange
complete authoritative replacements while preserving feed ordering. A rebuild
needs room for the old index, replacement input and new index at the same time.
Retaining old workload observations also retains their selected Pod data; release
observations when the consumer no longer needs them.

## Errors and validation

Use `errors.Is` for SDK and ownership sentinels. Kubernetes errors remain wrapped,
so `apierrors.IsForbidden` and `apierrors.IsNotFound` remain useful. An
`*ownership.Error` includes expected and actual identities, the verified path,
and an underlying cause. Relevant ownership sentinels include `ErrMissing`,
`ErrUIDMismatch`, `ErrCycle`, `ErrDepthExceeded`, `ErrScope`,
`ErrMultipleControllers`, and `ErrInvalidReference`.

Cancellation is fatal to acquisition and evaluation. Membership expression
failures are scoped diagnostics when ownership is complete. The exporter keeps
its existing first-match selector and metric degradation behavior through the
shared legacy attribution adapter; SDK selector strictness does not change that
metric contract.

See [the live test guide](../test/discovery/README.md) for independent exact-UID
checks with JobSet, LWS, a generic Bundle, and ownership churn in kind.
