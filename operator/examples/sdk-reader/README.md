<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Reuse an operator's Kubernetes reader

This example adapts controller-runtime to Karta's `discovery.Reader`.
Copy `reader.go` into the operator that owns the client dependency.
The Karta SDK itself does not import controller-runtime.

The example lives in the existing operator module, which pins
controller-runtime v0.24.1 and Kubernetes modules v0.36.3.
It adds no dependency to the SDK or exporter module.

## Read directly from the API server

Use this when the owner kinds are open-ended, or the existing cache excludes
Pods or namespaces that discovery needs.

```go
reader, err := sdkreader.NewAPIReader(mgr.GetAPIReader(), mgr.GetRESTMapper())
if err != nil {
    return err
}
discoverer, err := karta.NewDiscoverer(reader, discovery.Options{MaxDepth: 32})
if err != nil {
    return err
}
observed, err := discoverer.WorkloadForPod(ctx, discovery.PodReference(pod))
```

`WorkloadForPod` follows the Pod's owner references and finds all its workload's
Pods. The adapter exhausts API list pages and includes every Pod phase.
The consumer keeps its watch, queue, retry and output logic.

## Read from an existing complete cache

Use `mgr.GetCache()` explicitly. The manager's default client can send
unstructured reads directly to the API server.

Before starting the manager:

- Configure `ReaderFailOnMissingInformer: true` in its cache options.
- Register the typed Pod informer and an unstructured informer for each owner GVK.
- Keep every Pod in the declared namespace, including completed Pods.
- Keep the object fields used by Karta definitions and the full owner references.

```go
objects := []client.Object{&corev1.Pod{}}
for _, gvk := range ownerGVKs {
    object := &unstructured.Unstructured{}
    object.SetGroupVersionKind(gvk)
    objects = append(objects, object)
}

informers := make([]cache.Informer, 0, len(objects))
for _, object := range objects {
    informer, err := mgr.GetCache().GetInformer(
        ctx, object, cache.BlockUntilSynced(false),
    )
    if err != nil {
        return err
    }
    informers = append(informers, informer)
}
ready := func() bool {
    for _, informer := range informers {
        if !informer.HasSynced() {
            return false
        }
    }
    return true
}

reader, err := sdkreader.NewCacheReader(
    mgr.GetCache(), mgr.GetRESTMapper(), "demo", ready,
)
```

The caller then starts its manager as usual.
Before those informers synchronize, the adapter returns `discovery.ErrNotSynced`.
An unknown owner informer produces an error instead of silently falling back to
an API read. Register and synchronize it before retrying, or use the API reader.

The namespace argument declares coverage. `"demo"` rejects queries for another
namespace or all namespaces. Use `""` only when the cache covers every namespace.
A cluster-scoped workload can own Pods across namespaces, so its discovery
requires all-namespace coverage.
This declaration cannot detect a cache that was secretly filtered elsewhere.

A cache list has no page limit. controller-runtime truncates limited cache lists
and does not provide API-style pagination. The adapter accepts its known
`continue-not-supported` sentinel only in cache mode.

The REST mapper remains caller-owned. A discovery-backed mapper can still call
the API server; supply preloaded mappings when queries must stay offline.
Its lookup has no context argument, so cancellation is checked before and after.

Readiness describes initial synchronization, not a cross-resource Kubernetes
transaction or a guarantee that a disconnected informer is current.
If new informers are added dynamically, maintain the readiness set with
appropriate synchronization. Do not mutate the example's captured slice while
it is being read.

## What stays in the operator

- Starting and stopping watches.
- Registering new owner GVKs and refreshing REST mappings after CRD changes.
- Handling cache readiness, disconnections and access errors.
- Enqueueing affected workloads after Pod, owner or Karta-definition changes.
- Recovering a deleted Pod's previous workload from stored identity.
- Persisting results or publishing metrics.

The adapter verifies UID identity and returns detached objects.
It never treats a failed read as an empty workload.

## Verify the example

From the repository root:

```sh
cd operator
go test -race ./examples/sdk-reader
go vet ./examples/sdk-reader
```

The tests cover API pagination, completed Pods, cache completeness above 500
Pods, readiness, namespace coverage, stale UIDs, copy isolation and errors.
The same adapter and tests also compiled and passed with these resolved versions:

| controller-runtime | Kubernetes API, apimachinery and client-go | Validation |
| --- | --- | --- |
| v0.23.3 | v0.36.3 | Race tests and vet |
| v0.24.1 | v0.36.3 | Race tests and vet |
| v0.25.2 | v0.37.0 | Race tests and vet |

The tests include an in-process controller-runtime fake client feeding
`WorkloadForPod`. They do not start a real Kubernetes API server or informer cache.
The alternate versions were tested in isolated temporary modules; the example's
operator module remains pinned to v0.24.1.

This is an adapter example, not a promise that every controller-runtime and
Kubernetes dependency combination is compatible. Each consuming Go module
resolves and tests its own Kubernetes dependency versions.
