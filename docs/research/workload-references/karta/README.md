<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Kubernetes roots and logical associations

A Pod can belong to an external workflow without having that workflow as a
Kubernetes owner. The exporter must preserve the distinction. Given
`Pod -> JobSet -> Workflow`, the Workflow remains the root even when only the
JobSet has a Karta. Given an ownerless OSMO Pod with a workflow UUID label, the
Pod remains the Kubernetes root. The UUID is a separate logical association.

## Source baseline

The fork branch heads were checked with read-only `git ls-remote` on 2026-10-22:

| Branch | Commit | Purpose |
| --- | --- | --- |
| `karta-metrics-exporter-poc-impl` | `01352db5c4be54b0b3eb6d53a0a04fb5faa34520` | Exporter and its design |
| `cel-references` | `cd91d670a9b6613e4d1c21e066b37a8446eb9fb1` | Implemented resource references |
| `kep-references` | `4d9534b9fdfa46d337c5d5c250dbad5db282be3e` | Proposed reference API expansion |

Tests used local `cel-references` commit `0fff67e9`. A direct Git diff found no
changes from the fork reference commit in `pkg/references`, the CEL runner,
the reference factory options, API types or reader example.

## Existing exporter contract

Discovery follows controller owners before looking up the root's Karta.
An unknown root keeps its identity. A missing owner, wrong UID, invalid chain
or denied read cannot promote an intermediate object into a root.
An exact configured `ignoredOwnerKinds` GVK is the only deliberate boundary.

Source: [resolution](../../../../exporter/pkg/discovery/resolve.go),
[true-root and ignored-owner tests](../../../../exporter/pkg/discovery/lifecycle_test.go),
[configuration](../../../karta-metrics-exporter/configuration.md#definition-sources-and-owner-boundaries).

The metric is named `karta_pod_workload_info`. Its `workload_uid` is a Kubernetes
UID. It is not an external workflow UUID or a cluster name. A verified root with
no Karta emits generic identity and creation time, with no workload status.
Pod roots require the Pod Karta. Cluster-scoped roots resolve in Discovery, but
the current controller omits their workload rows. These two limitations must
remain visible when comparing platform integration choices.

Source: [metric contract](../../../../keps/0001-metrics-exporter/README.md#metrics),
[Pod-root exception and current limits](../../../design/metrics-exporter/README.md).

## What CEL references implement

A definition declares `structureDefinition.references`. Each entry has `name`,
a fixed `gvk`, and one of `lookup.nameExpression` or `list`. Lookup expressions
and selector values read only the input workload object. They cannot read another
reference, so reference chaining is unsupported. Namespaced targets resolve in
the workload's own namespace; the API has no namespace override. Cluster-scoped
targets have no namespace. The consumer fetches the objects. Expressions read
the result as `references.<name>`.

The reader supplies `Get(ctx, gvk, namespace, name)` and
`List(ctx, gvk, ListQuery{Namespace, Selector})`. Consumers pass it through
`WithReferenceReader`, or supply completed bindings through `WithReferences`.
A reference does not add an ownership edge or choose a different root.

Source: [reference types](https://github.com/AviadHayumi/workload-map/blob/cd91d670a9b6613e4d1c21e066b37a8446eb9fb1/pkg/api/runai/v1alpha1/types.go#L65),
[reader](https://github.com/AviadHayumi/workload-map/blob/cd91d670a9b6613e4d1c21e066b37a8446eb9fb1/pkg/references/reader.go#L28),
[factory](https://github.com/AviadHayumi/workload-map/blob/cd91d670a9b6613e4d1c21e066b37a8446eb9fb1/pkg/resource/component_factory.go#L80).

The first dependent expression resolves all declared references. Success is
cached for the factory lifetime. A failed resolution is retried. A lookup miss
is unbound; optional access may supply a default. An empty list binds `[]`.
Permission denial and read failures remain errors. Expressions that do not use
references make no reference reads.

Source: [resolver](https://github.com/AviadHayumi/workload-map/blob/cd91d670a9b6613e4d1c21e066b37a8446eb9fb1/pkg/references/resolve.go#L21),
[CEL binding cache](https://github.com/AviadHayumi/workload-map/blob/cd91d670a9b6613e4d1c21e066b37a8446eb9fb1/pkg/cel/runner.go#L143).

The separate KEP proposes flat `apiVersion` and `kind`, `Plan`, `Fulfill`,
`ReferencedKinds`, an objects reader and a packaged controller-runtime adapter.
Those are not implemented APIs on the inspected reference branch. The KEP also
leaves watches, retries and cache lifecycle to the consumer.

Source: [reference KEP](https://github.com/AviadHayumi/workload-map/blob/4d9534b9fdfa46d337c5d5c250dbad5db282be3e/keps/0002-resource-references/README.md).

## Integration cost and cache requirements

The current exporter creates a JQ runner from the root object and a component
factory without reference bindings. Its cached reader only supports `Get`.
Adding the reference reader alone is insufficient: a referenced object can
change while the root stays unchanged.

Source: [evaluation](../../../../exporter/pkg/discovery/evaluation.go),
[cached reader and publication token](../../../../exporter/pkg/discovery/types.go).

Reference integration would need full-object watches, reverse dependencies from
references to roots, and reevaluation on updates or deletion. Missing lookups
must be indexed so later creation wakes consumers. Lists need membership
invalidation when an object starts or stops matching. New bindings require a
new factory. Publication must verify the reference versions, complete list or
absence observations, watch generation and freshness deadlines. A failed read
must not look like an empty set. These dependencies belong outside the ownership
graph.

## Options for a two-week increment

| Option | Behavior | Main gap |
| --- | --- | --- |
| Pod labels through kube-state-metrics and a downstream join | Adds workflow or cluster dimensions while keeping Karta's root labels | Does not verify an external entity or divide shared Pod usage between jobs |
| Existing Kubernetes group target | Resolves a Pod label or annotation to a real namespaced PodGroup and its UID | PodGroup may cover only one execution group; it is not necessarily the workflow or the Pod's owner |
| Explicit reference edge | Makes a non-owner association queryable | Needs a new relation output contract plus reference cache and validity work |
| Mirror custom resource | Gives an external entity a real Kubernetes object and UID | Needs a controller, lifecycle rules and explicit association; creating it alone does not make it a controller owner |
| Virtual root | Replaces Kubernetes root identity with an external entity | Conflicts with the current UID/GVK contract; not part of this spike |

The smallest candidate is the downstream join when only usage grouping is
needed. kube-state-metrics already exposes Pod labels and annotations with
namespace, Pod name and UID, controlled by allowlists. A new sidecar is not
required for those metadata dimensions.

Source: [Pod metrics](https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/workload/pod-metrics.md),
[allowlist arguments](https://github.com/kubernetes/kube-state-metrics/blob/main/docs/developer/cli-arguments.md).

OSMO's workflow UUID can group its Pods. A real PodGroup, where present, describes
a group rather than necessarily the complete workflow. SkyPilot's cluster label
can group shared cluster Pods. It cannot assign their CPU or GPU samples to one
of several concurrent jobs. The [platform research](../osmo/README.md) and
[SkyPilot research](../skypilot/README.md) establish the platform-specific facts.

The [standalone spike](../spike/README.md) tests these association semantics with
configurable metadata keys. Its CLI emits JSON and changes no exporter API.
The [separate live comparison](../spike/live/README.md) runs the unchanged
exporter, isolated KSM and local Prometheus. The CLI does not implement a cache
or claim API freshness.

## Validation evidence

- Seven focused ownership tests passed on the exporter baseline, including an
  unknown true root, exact ignore matching, pending versus missing owners,
  reparenting, replacement, namespace rules and concurrent owner publication.
- Twenty-one reference specs passed: six resolver, eight CEL binding and seven
  factory specs. They cover lookup/list, absence, errors, lazy reads and caching.
- The isolated spike tests exercise cardinality, namespace isolation, failures,
  Pod and target replacement, metadata changes and concurrent-job ambiguity.
- Ten PromQL checks passed with promtool 3.14.0. The UID-normalized KSM join
  preserves the root and rejects namespace mismatches, replacement Pod UIDs,
  invalid Karta observations and conflicting metadata. Duplicate KSM scrapes
  collapse to one metadata row. See [the tested rules](../spike/promql/README.md).

- Actual periodic Prometheus queries preserved the roots for an OSMO task, a
  running two-Pod OSMO group and a Sky shared Pod. Paired raw-scrape replays also
  covered Sky multinode Pods. The generic snapshot evaluator resolved actual
  OSMO PodGroup UIDs without replacing those roots.
- The disposable live Pod passed label removal/replacement, deletion and
  same-name recreation. Six PromQL checks passed, including old-root/new-UID
  exclusion. The live rule returned only the recreated UID, then disappeared
  after cleanup.

Commands and unit output are in [evidence](./evidence/). Tests use a temporary
Go cache. [Live evidence](../spike/live/README.md) separates actual scrapes,
periodic Prometheus queries and captured-input evaluation. It does not claim
exclusive attribution of shared Pod usage to concurrent platform jobs.
