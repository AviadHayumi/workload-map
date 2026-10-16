<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# kube-state-metrics: caller-owned watches and reusable workload interpretation

## TL;DR

- The custom-resource pipeline compiles configuration into object-to-metric functions. Discovery, list/watch, storage, and HTTP exposition are separate steps. The exporter reuses the compiled functions when watch events arrive, rather than evaluating object fields during every scrape. (`pkg/customresourcestate/custom_resource_metrics.go`, `pkg/metrics_store/metrics_store.go`.)
- `RegistryFactory` accepts pluggable resource clients and list/watch implementations, but the default builder creates and runs its own reflectors. This is a client-go integration example, not an existing caller-owned object-reader contract. (`pkg/customresource/registry_factory.go`, `internal/store/builder.go`.)
- CRD discovery tracks served GVKs and plural resource names, supports version/kind wildcards, and explicitly removes unavailable registrations from the enabled set. Revision tracking distinguishes a delete/re-add from an unchanged set of names. (`internal/discovery/discovery.go`, `internal/discovery/types.go`.)
- Metric configuration changes and CRD changes have different lifecycles. A watched configuration file restarts the application; CRD events reconcile stores inside the running application. There is no hot-swappable definition-provider interface in this path. (`internal/wrapper.go`, `pkg/customresourcestate/config.go`.)
- Transfer the compilation and lifecycle boundaries to Karta. Keep definition snapshots and workload interpretation reusable by an exporter, while the embedding controller owns readers, cache synchronization, event sources, and shutdown. This is a recommendation based on the boundaries above, not an API already provided by kube-state-metrics.

## How it works

The source checkout was verified with `git rev-parse HEAD` and `git remote -v`:

- Repository: `kubernetes/kube-state-metrics`.
- Origin: `https://github.com/kubernetes/kube-state-metrics.git`.
- Commit: `ca3c2e3ded4f9845ff9b025cacd1c17331c07496`.
- Local reference: `/Users/ahayumi/.greenlight/reference-worktrees/kubernetes/kube-state-metrics/ca3c2e3ded4f`.
- Method: local source inspection only. No network access or execution of upstream tests.

`pkg/customresourcestate/config.go` defines resources by GVK, metric paths, labels, and optional resource plural. `FromConfig` decodes the configuration once, then returns a factory-generating closure. Each invocation resolves the captured configuration against the current CRD discovery cache. The resolver supplies the actual CRD plural; the closure passes each concrete resource to `NewCustomResourceMetrics`. Unresolved configured GVKs produce no factories. Resolution errors are logged and skipped, while metric compilation errors fail factory generation. This distinction matters for separating an unavailable resource type from an invalid definition. [E1, E3]

`NewCustomResourceMetrics` compiles metric families before constructing a factory. `pkg/customresourcestate/registry_factory.go` compiles field paths and labels and produces `FamilyGenerator.GenerateFunc` functions that accept an unstructured object. Those functions do not perform Kubernetes reads. `pkg/customresource/registry_factory.go` packages these functions together with `Name`, `ExpectedType`, `CreateClient`, and `ListWatch`. Thus the transformation can be called independently, although the complete factory interface also includes transport responsibilities. [E2]

For configured custom resources, `CreateClient` builds a client-go dynamic resource client for the resolved GVR. `ListWatch` uses that client for namespaced list/watch calls. The public builder exposes `WithCustomResourceClients` and hooks for replacing store construction. Its default implementation constructs a metrics store for all namespaces or one per configured namespace, wraps the lister/watcher with instrumentation and sharding, and starts a client-go reflector. Injecting clients does not transfer ownership of the reflector or its store to the caller. [E2, E4]

CRD discovery is a separate dynamic informer over `apiextensions.k8s.io/v1/customresourcedefinitions`. It indexes group, served version, kind, and plural. The group must be explicit; kind and version can be omitted or `*`. Explicit GVKs must also exist in the discovery cache. Discovery is CRD-based, so this configuration path does not select native Kubernetes resources. The documentation warns that observing multiple served versions can expose the same underlying objects in each version's representation. It does not choose one preferred version or deduplicate logical workloads across versions. [E3, E9]

CRD add/update/delete handlers modify this index and maintain stop channels per GVK. Updates compare the meaningful GVK-plus-plural set, so status-only changes preserve active channels. Removed GVKs close their channels. A valid empty served-version set removes existing entries; an unreadable replacement is not treated as an empty set. A cache revision advances for meaningful changes, including a delete/re-add that ends with the same GVR strings. Tests exercise these cases. [E3, E10]

`PollForCacheUpdates` checks changes every three seconds. It rebuilds factories, creates clients, replaces the enabled custom-resource set, and asks `MetricsHandler` to rebuild writers. It retains the dirty flag on failures and backs retries off to five minutes. It clears the flag only if the applied revision still matches discovery. This avoids acknowledging newer events that arrived during a rebuild. [E3]

Registration and activation are distinct. `internal/store/builder.go` keeps constructors in a package-global map keyed by GVR string. Registering the same key replaces its constructor. `ReplaceEnabledCustomResources` replaces the active custom-resource names while preserving built-in names, so a removed custom resource is no longer built. The constructor map itself is not pruned by that method. `MetricsHandler.BuildWriters` cancels the previous build context, creates a new one, and builds a fresh writer list. Consequently a real discovery change rebuilds all enabled stores, including otherwise unchanged stores. The code publishes the resulting writers without waiting for those resource reflectors to finish their initial lists. [E4, E5]

Metric definition edits use a broader mechanism. `internal/wrapper.go` watches the custom-resource configuration file with Viper. Its callback cancels the running application context, waits three seconds for ports, and starts a new application run. `pkg/app/server.go` reads the file again, creates another discoverer, and calls `FromConfig` again. The configuration captured by an existing closure is not updated by CRD discovery. Removing a definition from the file therefore takes effect through application restart and reconstruction. [E1, E7]

Finally, a metrics store implements client-go's store update surface but stores rendered metric bytes by object UID. `Add` evaluates the generators; `Update` delegates to `Add`; `Delete` removes the UID; `Replace` clears and repopulates the store. Its object lookup methods return no objects. HTTP scrapes snapshot the current writer list, select requested resources, normalize headers, and write stored bytes. The writer-list snapshot does not freeze concurrent object updates inside the underlying stores. This is an exporter projection, not a reusable Kubernetes object cache. [E5, E6]

## Relevance to Karta

The strongest analogy is a shared transformation layer: a definition compiles once, then evaluates a caller-provided object. kube-state-metrics demonstrates this through `NewCustomResourceMetrics` and `FamilyGenerator.GenerateFunc`. Karta can use the same boundary for controller integration and exporter reuse: the exporter supplies observed workloads to the same interpretation code that controllers call. The interpretation package need not start discovery or watch loops. [E2]

The watch implementation also proves that controller-runtime is not necessary for this pipeline. Discovery and store updates use client-go directly. However, this checkout is not a controller-runtime-free module: `go.mod` requires it, and `pkg/app/server.go` imports its metrics authorization filters. Karta should distinguish an independent SDK import graph from removing every controller-runtime use across the repository. [E3, E4, E8]

The missing part is the requested externally maintained definition provider. `FromConfig` takes a concrete internal discoverer and retains a decoded configuration; `RegistryFactory` combines interpretation with client construction and list/watch methods. Neither expresses a small read-only definition snapshot supplied by an embedding application. These are useful decomposition examples but unsuitable public interfaces to copy directly. [E1, E2]

## Evidence

- E1: `pkg/customresourcestate/config.go`, `FromConfig`: decode once, resolve GVKs on repeated calls, compile factories, and propagate compilation failures.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/customresourcestate/config.go
- E2: `pkg/customresource/registry_factory.go`: the factory contract. `pkg/customresourcestate/custom_resource_metrics.go`: implementation, dynamic clients, and list/watch. `pkg/customresourcestate/registry_factory.go`: compilation and object-only metric generation. `pkg/metric_generator/generator.go`: the public generator function contract.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/customresource/registry_factory.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/customresourcestate/custom_resource_metrics.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/customresourcestate/registry_factory.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metric_generator/generator.go
- E3: `internal/discovery/discovery.go`: CRD event handlers, served versions, GVK resolution, periodic rebuilds, revision acknowledgments, and retry backoff. `internal/discovery/types.go`: protected maps, GVK stop channels, semantic diffs, and revision changes.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/discovery/discovery.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/discovery/types.go
- E4: `pkg/builder/builder.go`, `pkg/builder/types/interfaces.go`: public injection hooks. `internal/store/builder.go`: global constructor registration, enabled-set replacement, metrics-store creation, and reflector ownership. `pkg/util/utils.go`: client construction and GVR keys.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/builder/builder.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/builder/types/interfaces.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/store/builder.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/util/utils.go
- E5: `pkg/metricshandler/metrics_handler.go`: cancel-and-rebuild lifecycle and HTTP writer snapshots. `pkg/metricshandler/configure_store_test.go`: configuration followed by rebuild; rejected configuration skips rebuilding.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metricshandler/metrics_handler.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metricshandler/configure_store_test.go
- E6: `pkg/metrics_store/metrics_store.go`: UID-keyed rendered metrics, update/delete/replace, and unsupported object lookups. `pkg/metrics_store/metrics_writer.go`: exposition of stored bytes.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_store.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/metrics_store/metrics_writer.go
- E7: `internal/wrapper.go`: file watchers restart the application. `pkg/app/server.go`: file decoding and discovery-to-store wiring.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/wrapper.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/app/server.go
- E8: `go.mod`: explicit controller-runtime dependency. `pkg/app/server.go`: metrics authorization filter import; the only controller-runtime import found by searching the checkout's Go source.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/go.mod
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/pkg/app/server.go
- E9: `docs/metrics/extend/customresourcestate-metrics.md`, wildcard matching: served-version selection, multiple-version duplication, and native-resource exclusion.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/docs/metrics/extend/customresourcestate-metrics.md
- E10: `internal/discovery/gvkp_update_test.go`: no-op updates, unreadable updates, added versions, delete/re-add, and plural changes. `internal/discovery/discovery_test.go`: all-unserved teardown and status-only updates. `internal/store/builder_test.go`: active custom-resource removal preserves built-ins.
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/discovery/gvkp_update_test.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/discovery/discovery_test.go
  https://github.com/kubernetes/kube-state-metrics/blob/ca3c2e3ded4f9845ff9b025cacd1c17331c07496/internal/store/builder_test.go

## Lessons for Karta

1. Prefer an object-and-definition evaluation boundary. Keep interpretation callable without client construction, goroutines, metric stores, or HTTP setup. Put optional Kubernetes integration around it. This transfers the useful separation visible in the generator pipeline. [E2, E6]
2. Make definition updates explicit. A caller-owned watcher should validate a candidate definition set, publish a new immutable registry snapshot, and remove deleted definitions from active lookup. Keep a generation or revision if consumers must detect replacement even when resource names remain identical. This recommendation adapts the discovery revision and enabled-set logic. [E3, E4, E10]
3. Keep definitions and API availability separate. A definition can remain configured while its GVK is temporarily absent. Select supported served versions deliberately, and avoid treating every version as a distinct logical workload. [E1, E3, E9]
4. Let the embedding application own its reader and cache lifecycle. Client injection alone is insufficient if an SDK silently starts additional reflectors. Exporter-specific code can own its own watch-to-metric projection while reusing the evaluation functions. [E4, E6]
5. Specify removal, invalid replacement, readiness, and in-flight operation behavior. The source offers concrete tests for no-op updates and delete/re-add cycles, but its full-store rebuild publishes newly empty stores immediately. Karta should define its own snapshot and readiness guarantees rather than inherit these exporter tradeoffs accidentally. [E3, E5, E10]

## What NOT to copy

- Do not copy the process-global constructor map into a multi-consumer SDK. Use an instance-owned registry with clear collision and removal semantics. The source's global map replaces constructors by GVR and retains disabled entries. [E4]
- Do not make a definition-provider interface construct Kubernetes clients or return list/watch factories. The source combines those responsibilities in `RegistryFactory`; they are unnecessary for callers that already own cached readers. [E2]
- Do not restart the embedding application when definitions change, or rebuild every unchanged workload watch on one definition update. Those are exporter lifecycle choices in the source. [E4, E5, E7]
- Do not expose the metrics store as a workload cache. It stores rendered bytes and intentionally cannot return Kubernetes objects. [E6]
- Do not equate wildcard GVK expansion with workload identity or complete Kubernetes discovery. This path is based on CRDs and can observe the same logical object through several served versions. [E3, E9]
