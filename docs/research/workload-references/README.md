<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Workload references for OSMO and SkyPilot

OSMO's workflow identity is available on its Pods. For the completed test run,
Pod `df8c436a2ee64248-275e9079d6834334` had no owner references and carried
`osmo.workflow_uuid=df8c436a2ee642488e061aef2fc5e1f4`. The exporter kept the Pod
as its Kubernetes root. A live KSM join added the workflow UUID separately.

SkyPilot also creates bare compute Pods, but its jobs live in platform state and
can share a Pod concurrently. Kubernetes ownership stops at the Pod. It cannot
identify one exclusive SkyPilot job or allocate shared usage between jobs.

## Current evidence

The isolated exporter is Running and Ready in `test-ref`. Real exporter/KSM
samples and periodic Prometheus rules have been checked for OSMO and SkyPilot.
The OSMO single task and two-task group completed. Their real Pod, PodGroup and
ConfigMap UIDs were captured, and the group resources disappeared afterward.
The SkyPilot shared runtime request failed during API connection refusal and
a Ray startup timeout. Runtime and concurrent-job checks remain unvalidated. See the [audit](audit.md) for case results and validation limits.

Production exporter code and the Karta API are unchanged. The generic
association code is an isolated research spike.

## Preserve the Kubernetes root

For `Pod -> JobSet -> Workflow`, the Workflow remains the root even when only
JobSet has a Karta definition. Only an explicit `ignoredOwnerKinds` entry can
change that traversal boundary. An ownerless Pod remains its own Kubernetes
root. An external workflow UUID must not replace `workload_uid` or become an
invented Kubernetes UID.

CEL references solve a different problem. Given an input workload object, they
fetch related real Kubernetes objects by name or selector. For example, a Pod's
`pod-group-name` annotation can name a same-namespace PodGroup. The result has a
real GVK and UID. A reference does not discover a parent or change ownership.
The implemented reference branch and the proposed reference KEP have different
APIs; [the Karta report](karta/README.md) records both.

## Three options

| Option | What it provides | What remains separate |
| --- | --- | --- |
| Kube-state-metrics joins | Copy approved Pod labels onto recorded associations, joined by namespace and Pod UID. | Existing root metrics retain their identity. No target Kubernetes object is implied. |
| Explicit object associations | Resolve a configured reference to a current real object, such as a PodGroup. Publish its actual UID separately. | Resolution does not promote the target into the ownership chain. |
| Platform object projection | A bridge maintains one real Kubernetes object for each external workflow or cluster incarnation. Generic references can then read it. | The bridge must define lifecycle, completeness, tenancy and deletion behavior. |

Label grouping adds external identity, but it does not create workflow-level
Karta status, component/instance/replica-group semantics, or exclusive per-job
usage. Those need a real definition and the appropriate object or state mapping.

The quickest useful option is the first. The saved
[PromQL rules and tests](spike/promql/README.md) validate UID joins, namespace
isolation, name reuse, duplicate KSM observations, conflicting labels and invalid
observations. The same rules have also been replayed against real captures and evaluated by
a local Prometheus scraping the live exporter and KSM. The [live evidence](spike/live/README.md)
includes both OSMO group Pods and their real PodGroup. Multi-cluster storage
still needs a common cluster label.

A label allowlist selects metadata to export. It does not authenticate whoever
wrote the label. These joins provide grouping; trusted accounting also needs
platform-controlled metadata or a separately verified platform identity.

The [generic spike](spike/README.md) represents metadata observations and resolved
objects separately. It checks absent, unique, ambiguous and failed reads without
changing root metrics. Its snapshot input does not implement production watches,
cache invalidation or an exporter configuration API.

OSMO already supplies run, group, task and attempt identity on each Pod. Its
optional group template can create a real per-group ConfigMap. That object is a
group projection, not a workflow root. A complete workflow projection needs a
component that follows the external workflow lifecycle. See the
[OSMO report](osmo/README.md) for the identity and cleanup rules.

SkyPilot cluster labels group compute Pods. Creation-time managed-job annotations
cannot describe later jobs sharing those Pods. A state join can expose the set
of active jobs, but Pod CPU or GPU measurements cannot allocate usage to one
concurrent job. See the [SkyPilot report](skypilot/README.md).

## Two-week scope

Two weeks is an implementation estimate after reliable cluster access and metric
collection are available. It covers the following bounded result:

1. Make the completed OSMO cases and captured SkyPilot compute-Pod/name-reuse
   cases repeatable. Finish the pending SkyPilot concurrent-job checks.
2. Deploy the tested KSM allowlist and recording rules. Verify every association
   against the captured Pod UID and preserve `karta_pod_workload_info` unchanged.
3. Document unsupported attribution and add operational checks for missing,
   conflicting or stale observations. Review cardinality and multi-cluster scope.

Success means an OSMO run can be grouped by its full UUID, a SkyPilot compute
cluster can be grouped by its scoped label, and neither result claims a new
Kubernetes owner or exclusive shared-job usage. Retry testing requires the
existing OSMO policy to permit it; this lab's limit is zero.

A production reference reader or workflow bridge is a separate follow-up. It
needs target watches, reverse dependencies, deletion handling, new-UID checks,
RBAC and an explicit public association contract. The existing CEL library alone
does not supply those exporter responsibilities.
