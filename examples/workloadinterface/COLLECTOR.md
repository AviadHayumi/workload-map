<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Collect workload metrics with the SDK

The exporter computes attribution when Kubernetes objects change.
Prometheus scrapes the resulting records.

The SDK branch shares the attribution and owner graph with the Karta library.
It does not replace the exporter controller with `NewDiscoverer`.

This comparison uses PoC revision `11847572` and SDK revision `630fbf78`.
The PoC also has newer changes that have not been integrated into the SDK branch.

## Follow one Pod into a metric

```text
Kubernetes watch event
  -> confirm the workload owner
  -> resolve component, instance and replica group
  -> store a small PodRecord

Prometheus scrape
  -> read one Store.Snapshot()
  -> emit OTel observations
  -> render the existing Prometheus metric
```

The scrape callback does not query Kubernetes or rerun selectors.
The controller owns that work.

For a JobSet Pod, the existing attribution test resolves `decode` and group `1`:

```prometheus
karta_pod_workload_info{namespace="example",workload="training-set",workload_group="jobset.x-k8s.io",workload_version="v1alpha2",workload_kind="JobSet",workload_uid="example-workload-uid",owner_resolution="resolved",pod="decode-1-pod",pod_uid="example-pod-uid",component="replicatedjob",component_instance="decode",replica_group_id="1"} 1
```

This is an illustrative sample using the actual metric name and complete label set.
Object names and UIDs are placeholders, not captured cluster output.

- `workload` identifies the confirmed JobSet.
- `component` identifies the definition's Pod-producing component.
- `component_instance` selects the named replicated job.
- `replica_group_id` selects one repeated group within that instance.
- The value `1` records attribution. It does not measure GPU activity.

[Collector source](https://github.com/AviadHayumi/workload-map/blob/11847572cdb2de87d07ad6915129f4a7958af774/exporter/pkg/collector/collector.go#L38) and [JobSet attribution assertions](https://github.com/AviadHayumi/workload-map/blob/11847572cdb2de87d07ad6915129f4a7958af774/exporter/pkg/attribute/attributor_test.go).

## What changes internally

| Responsibility | PoC implementation | SDK branch |
| --- | --- | --- |
| Owner edges and descendant lookup | Exporter-local index. | Adapter to `ownership.Index`. |
| Pod classification | Exporter-local attribution. | Calls `attribution.AttributeLegacy`. |
| Declared instances | Exporter-local factory traversal. | Calls `attribution.InstanceIDs(..., false)`. |
| Watches, retries and health verification | Exporter controller. | Remain with the exporter. |
| Root status | Exporter state builder. | Remains with the exporter. |
| Scraping and metric labels | Store snapshot and OTel gauges. | Same collection path. |
| Full Pods for other consumers | No general discovery API. | `WorkloadForPod` and component, instance and replica scopes. |

The controller's attribution call remains:

```go
result := attribute.Attribute(ctx, pod, entry, instanceIDs)
```

The SDK branch delegates that call to:

```go
return Result(attribution.AttributeLegacy(
    ctx, pod, entry.Summary, entry.Definitions, instanceIDs,
))
```

This compatibility path preserves first-match selection and existing failure values:

- `<unknown>` for an unresolved field.
- `unknown_instance` for an undeclared instance.
- `jq_error` for an expression failure.

The newer discovery API uses strict component selection.
The exporter does not silently switch to that policy.

Relative to the shared base, the SDK change leaves the collector, state builder,
controller, telemetry provider and Helm chart unchanged.
That source comparison does not establish parity with newer PoC commits.

[Compatibility implementation](https://github.com/AviadHayumi/workload-map/blob/630fbf78672161cfc3ac624aaef4e046518cd52a/pkg/attribution/legacy.go), [attribution adapter](https://github.com/AviadHayumi/workload-map/blob/630fbf78672161cfc3ac624aaef4e046518cd52a/exporter/pkg/attribute/attributor.go) and [owner adapter](https://github.com/AviadHayumi/workload-map/blob/630fbf78672161cfc3ac624aaef4e046518cd52a/exporter/pkg/owner/index.go).

## If a new collector needs full Pods

Acquire one workload observation and pass it to the example consumer:

```go
observed, err := discoverer.Workload(ctx, rootRef)
if err != nil {
    return err
}
counts := workloadinterface.CountPods(observed)
```

The runnable fixture contains a terminating running Pod, a completed Pod and an unscheduled pending Pod.
Its expected output is:

```text
total=3 running=1 completed=1 pending=1
terminating=1 unscheduled=1
```

Termination and scheduling are separate from phase.
A terminating running Pod contributes to both counts.

`CountPods` is an example helper, not an addition to the exporter metric contract.
The [SDK usage guide](README.md) shows how to select a component, instance or group and obtain its actual Pods.

[Consumer implementation](pods.go) and [runnable metric-consumer example](example_test.go).

## What a complete exporter integration still needs

A future integration could feed `discovery.Index` from synchronized object caches.
It could acquire observations after changes and project them into the existing metric records.
Scrapes would continue reading the metric store.

The SDK starts no watches and exposes no Prometheus endpoint.
Its optional cache reuses attribution; the caller still supplies current objects.

These differences need explicit handling before replacing the current controller:

| Case | Existing exporter | Discovery SDK |
| --- | --- | --- |
| Workload boundary | Outermost selected Karta root; supports ignored owners. | Terminal controller owner; no ignored-owner option. |
| Unknown kind | Not automatically exported as a workload. | Generic workload with a `pods` component. |
| Definition selection | Registry chooses per group/kind; bare Pods are opt-in. | Constructor selects by exact GVK, including the built-in catalog. |
| Classification failure | Keeps the Pod metric with degraded fields and failed observation health. | Keeps ownership-confirmed Pods; an affected scope query reports incomplete attribution. |
| Root status | Evaluated separately from scale. | `DesiredTree` evaluates spec, scale and status. |
| Cached data | Trimmed Pods and intermediate-owner metadata by default. | Full Pods and roots for full-object output. |

Keep root status evaluation separate when metrics do not need scale or templates.
Otherwise, an unrelated expression failure could start suppressing useful status.

The exporter must retain its watch lifecycle, zero-Pod reporting, freshness checks,
metric projection, Prometheus delivery and Helm configuration.

Discovery benchmark results do not prove an exporter improvement.
The exporter evaluates events and stores small records; full-object discovery has different costs.

[Exporter policy](https://github.com/AviadHayumi/workload-map/blob/11847572cdb2de87d07ad6915129f4a7958af774/exporter/pkg/registry/registry.go#L60), [status evaluation](https://github.com/AviadHayumi/workload-map/blob/11847572cdb2de87d07ad6915129f4a7958af774/exporter/pkg/state/workload.go#L23) and [discovery contract](https://github.com/AviadHayumi/workload-map/blob/630fbf78672161cfc3ac624aaef4e046518cd52a/pkg/discovery/resolver.go).

<details>
<summary>Newer PoC changes to preserve</summary>

The shared base is `5d0661ab`.
The PoC later added:

- `a0aa04c9`: optional CEL submitter attribution and its metrics.
- `5e700636`: cluster-scoped ownership through the exporter lifecycle.
- `e05d1f3e`: health verification using the metadata watch's API representation.

Those changes are absent from the inspected SDK branch.
The shared legacy owner adapter still needs the newer cluster-scope fix before replacing the latest PoC implementation.

Integrate the SDK into the current PoC rather than replacing that branch wholesale.
The optional submitter functionality remains part of that newer PoC scope.

This comparison does not claim a deployed migration or identical behavior between the two branch tips.
See [validation](VALIDATION.md) for the recorded checks and their scope.

</details>
