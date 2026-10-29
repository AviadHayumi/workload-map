<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter component integration

The controller must consume the merged component APIs without taking ownership
of their work. For example, changing a Job's Karta definition must update that
Job and its Pod mappings even when the Pods have not changed. Inventory observes
the Karta update and signals Runtime. Runtime reads the new selection snapshot
and queues the affected Job candidates, roots and Pods. Settled Deployment
results remain untouched.

## Source baseline

The current base is upstream `karta-metrics-exporter-impl` at
`50719d65c52a5a0fb88502576175fff0e3f6691a`. Inventory and discovery are already
merged there. The resolver branch is an incoming component, not part of that
upstream revision.

| Component | Source used |
| --- | --- |
| Upstream | [50719d65](https://github.com/dsx-ai-factory/karta/tree/50719d65c52a5a0fb88502576175fff0e3f6691a) |
| Discovery | [Merged upstream implementation](https://github.com/dsx-ai-factory/karta/tree/50719d65c52a5a0fb88502576175fff0e3f6691a/karta-metrics-exporter/pkg/discovery) |
| Inventory | [Merged upstream implementation](https://github.com/dsx-ai-factory/karta/tree/50719d65c52a5a0fb88502576175fff0e3f6691a/karta-metrics-exporter/pkg/kartadefinitioninventory) |
| Resolver and evaluator | [acdfe9c4](https://github.com/rogirun/workload-map/tree/acdfe9c4acdb7b1d3e0adccd4a2224c405085efe), branch `roeeg/exporter-label-resolver` |

Discovery's owner-walking algorithm and public contracts are preserved. Resolver
integration adds the optional raw Pod input described below. Validation retains
the reserved labels `otel_scope_name`, `otel_scope_version` and
`otel_scope_schema_url`, which the public Prometheus collector can emit.

## Ownership and API changes

[Inventory](../../../karta-metrics-exporter/pkg/kartadefinitioninventory/inventory.go)
owns Karta LIST/WATCH, decoding, validation, selection and source health. Its
existing `New(dynamic.Interface, Config)`, `Run(context.Context) error` and
`Reader.Lookup(schema.GroupVersionKind) (Selection, error)` contracts remain.
It has no dependency on controller or Store. The upstream synchronization fix is
preserved: the initial LIST application time is recorded after synchronization
logging. A blocked log sink therefore cannot make a short-lived WATCH appear
healthy.

The integration adds these extensions to the pinned component APIs:

| Extension | Contract | Why it is needed |
| --- | --- | --- |
| `Observer` | `List() ([]SelectionResult, error)` and `Changes() <-chan struct{}` | Runtime needs a complete selection snapshot to choose workload watches and a signal when definitions or source health change. Repeated lookups cannot provide an atomic snapshot. |
| `SourceConfig.LabelSelector` | `*metav1.LabelSelector` | Preserves the existing cluster-definition selector. A definition that loses its label match leaves the inventory. Catalog selectors are rejected. |
| `Config.RequestTimeout` | `time.Duration`, excluded from JSON and YAML | Preserves the application's existing per-LIST-page deadline after inventory takes over Karta watches. Zero retains caller-controlled lifetime; negative values are rejected. WATCH keeps its original context. |
| Resolver `Input.PodObject` | `*unstructured.Unstructured`, mutually exclusive with `Pod *corev1.Pod` | Lets the controller pass its full cached Pod directly to label evaluation without converting it to a typed Pod and back. Setting both returns `ErrConflictingPodInputs`. |

`SelectionResult` embeds `Selection` and adds `Err error`. `Selection` contains
`Definition *v1alpha1.Karta`, `GVK schema.GroupVersionKind` and `Origin`. Origin
records the source, name, UID and resource version. Invalid selected definitions
retain this identity and their validation error. List returns sorted, owned
copies from one snapshot. A top-level error reports source failure, not absence.
Changes is a buffered, coalesced signal for one observer to read List again.
The consumer reads the initial snapshot and stops on context cancellation.

[Runtime](../../../karta-metrics-exporter/pkg/controller/runtime.go) receives
`inventory.Observer`, `discovery.Discoverer` and `labelresolver.Resolver` directly.
It no longer watches or decodes Karta objects. On definition changes it scans
cached objects and memberships for affected GroupKinds, including unresolved
Pods and zero-Pod candidates. Health transitions trigger a coalesced scope sweep.
Duplicate snapshots do not requeue unrelated work or invalidate in-flight
evaluations. Actual selection or health changes advance the generation checked
before publishing values and errors.

Discovery resolves ownership through Runtime's public `ObjectReader` adapter.
The reader shares metadata LIST/WATCH sources for ancestors. Runtime requires
full Pod and root objects before evaluation, including generic roots without a
Karta. UID, resource-version and selected-definition checks reject stale work.
The [controller README](../../../karta-metrics-exporter/pkg/controller/README.md)
maps these responsibilities to its five production files.

The Pod worker passes the full root workload, `PodObject: object` and
`MetricPodWorkloadInfo` to the resolver. It converts the Pod to `corev1.Pod` only
when SDK component mapping needs it. Generic roots need no typed Pod conversion.
The raw input preserves actual field presence and fields unknown to the compiled
Pod type. The existing typed input retains Kubernetes serialization and TypeMeta
normalization. These representations do not promise identical absent-field
behavior. Both are read-only during resolution. This extension adds no cache,
synchronization or controller file.

The root worker supplies the full root and `MetricWorkloadStatus`. Each successful
call returns exactly one result, so the workers read it directly after checking
the top-level error. Per-label errors remain visible even when a later configured
source supplies a value.

## Application and publication

[pkg/config](../../../karta-metrics-exporter/pkg/config/config.go) parses and
validates configuration. [bootstrap.New](../../../karta-metrics-exporter/pkg/bootstrap/components.go)
constructs Kubernetes clients, inventory, discovery, compiler, resolver, Store
and Runtime. It supplies the global `requestTimeout` to inventory and the workload
reader, then returns `app.Components`.

[app.New](../../../karta-metrics-exporter/pkg/app/application.go) accepts those
components and prepares HTTP and OTel collection. `Application.Serve` runs
inventory, Runtime and HTTP together. HTTP can serve while sources load;
readiness reflects Runtime's required sources. Cancellation or a worker exit
stops the application. Shutdown joins workers before unregistering collection
and shutting down the OTel provider.

Store and collector retain separate Pod and workload results. Failed definition
reads retain Runtime's previous selections and mark their health unavailable. Evaluation failures retain existing successful
records with their provenance and explicit errors. A definition failure does not
invalidate verified root creation time. Confirmed deletion can select the catalog;
source failure or an invalid cluster winner cannot silently do so.

Configuration keeps the upstream names
`genericLabelEvaluationLimits.costLimit`, `maxExpressionBytes` and
`maxResultBytes`, with `--generic-label-eval-*` flags. Cancellation checks are
always enabled inside the evaluator. Cluster definitions are enabled by default;
catalog definitions are disabled. The
[configuration reference](../../karta-metrics-exporter/configuration.md) lists
settings and precedence.

## Dependency compatibility

The Prometheus adapter adds `go.opentelemetry.io/otel/exporters/prometheus v0.66.0`
under Apache-2.0. It provides the constructor, registry, translation and scope
options used by `app.New` while preserving upstream OTel, Prometheus, OAuth2 and
Protobuf versions. Version `v0.69.0` would also require OAuth2 and Protobuf upgrades;
those upgrades are unnecessary for this integration.

The `google.golang.org/genproto/googleapis/api` and `rpc` requirements align with
`v0.0.0-20260526163538-3dc84a4a5aaa`, already required by upstream controller-runtime
and apiextensions-apiserver dependencies. Dependency changes are confined to the
exporter and its integration-test module. Other repository modules are unchanged.

## Validation status

The rebased implementation passed full presubmit, nine actual-main kind checks,
all 27 operator cases and twelve matched performance runs with 2,400 acknowledged
events. The [acceptance report](../../../karta-metrics-exporter/test/e2e/results/2026-10-29-component-alignment.md)
records source identities, the test-harness repair and measurement limits. An
isolated first-use startup delay remains unexplained; the short performance runs
do not establish production capacity.
