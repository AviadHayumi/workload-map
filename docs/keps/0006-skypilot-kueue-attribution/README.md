<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# KEP-0006: SkyPilot workload attribution with optional Kueue

- Status: provisional
- Authors: @AviadHayumi
- Issue: [Label-based workload roots](https://github.com/dsx-ai-factory/workload-map/issues/463)
- Created: 2026-11-07
- Last updated: 2026-11-07

## Summary

A SkyPilot head Pod and worker Pod can have no Kubernetes owner. The exporter
reports two Pod roots instead of one cluster. Start with labels and Prometheus
grouping. Kueue can add admission-group identity when installed. Job status
needs SkyPilot data: two jobs can share one running Pod.

## How Kueue connects the objects

![Optional Kueue admission flow and actual ownership direction](kueue-pod-group.png)

SkyPilot optionally adds these fields to both Pods in cluster `train-a`:

| Pod field | Value | Purpose |
| --- | --- | --- |
| Label `kueue.x-k8s.io/queue-name` | `research` | Select the namespaced LocalQueue. |
| Label `kueue.x-k8s.io/pod-group-name` | `train-a` | Identify the cluster's Pod group. |
| Annotation `kueue.x-k8s.io/pod-group-total-count` | `"2"` | Declare the expected Pod count. |

With Kueue's Pod integration enabled, its webhook holds Pods with a scheduling
gate. Its controller creates the group's Workload. After admission, Kueue removes
its gate so scheduling can proceed. Admission does not mean Running.
[SkyPilot template][sky-template], [Kueue Pod integration][pod-guide].

The Workload has ordinary owner references to the Pods, not the reverse. Our
[discovery walk](../../../dsx-workload-exporter/pkg/discovery/resolve.go) follows
controller owners upward. A Karta definition alone cannot connect them.
For a native Job, both its Pods and Kueue Workload belong to the Job.
[Group references][group-refs], [Job references][job-refs].

## Options

### 1. Add labels to Pod metrics now

Existing Helm configuration, with the full Pod passed as `object`:

```yaml
exporter:
  labelRules:
    - name: skypilot_cluster
      metrics: [dsx_workload_pod_info]
      sources: [pod]
      expression: |
        has(object.metadata.labels) && "skypilot-cluster-name" in object.metadata.labels
          ? object.metadata.labels["skypilot-cluster-name"] : ""
```

The rule returns `train-a` for that cluster. Join resource measurements to Pod
info, then group by Kubernetes cluster, namespace and `skypilot_cluster`.
Kueue queue/group labels work the same way. This changes query grouping, not
`workload_uid` or status. Names are reusable, not immutable IDs.
See [label configuration][labels] and [joins][queries].

### 2. Add an optional Kueue association

Proposed: watch Kueue Workloads. Index their owner Pod UIDs to Workload UIDs
within the namespace. Publish the resolved relationship in a separate metric;
keep the existing root and status.

Update on Workload changes; remove on deletion. Missing, unreadable or ambiguous
links must not hide Pod metrics. Owner references can include failed, replaced
Pods, so their count is not the live replica count. [Replacement behavior][replacement].

This gives the group a real UID and admission information. Making it the actual
workload root also requires a new discovery rule and agreed phase semantics.
It is more work than a catalog definition.

### 3. Add SkyPilot job and service associations

Proposed: read SkyPilot's job/service API and join its cluster records to Pods.
Scope IDs to the API instance; local job IDs also need their cluster. This adds
API access and polling. Use it only when cluster grouping is insufficient.
[Job requests][sky-jobs], [service records][sky-serve].

| Workload shape | What Kueue would group | What still needs SkyPilot data |
| --- | --- | --- |
| Two-node task | Head and worker in one cluster | Task completion, distinct from Pod phase. |
| Two jobs on a shared cluster | Both jobs share one Pod group | Each job's ID and status. Pod GPU usage cannot separate concurrent jobs. |
| Pipeline with separate task clusters | One group per task cluster | The parent managed job and task relationships. |
| SkyServe | One group per replica cluster | Service, replica and version relationships. |

## Compatibility and limits

Only option 1 works today. Options 2 and 3 need implementation and metric-schema
agreement. None changes owner references or installs Kueue.

Do not map Kueue completion directly to SkyPilot success. The inspected revision
tests `Finished=True`, reason `Succeeded`, with only one of three Pods successful.
[Completion test][finished-test].

## Validation and next test

SkyPilot 0.14.2 tests covered shared jobs, a two-node task, a pipeline and SkyServe.
All six compute Pods lacked owners. Shared jobs completed while their Pod stayed
Running. The playground had KAI Scheduler, but no Kueue CRDs or release.

Kueue behavior was source-checked, not tested live. Before option 2, test delayed
admission, Pod replacement, Workload recreation, denied reads and identical names
across namespaces. Original Pod metrics must remain available.

## History

- 2026-11-07: Proposed labels first, with optional Kueue and SkyPilot associations.

[sky-template]: https://github.com/skypilot-org/skypilot/blob/39b151e11075f1d0029f8fc411a42334775fb3e7/sky/templates/kubernetes-ray.yml.j2#L306-L330
[pod-guide]: https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/
[group-refs]: https://github.com/kubernetes-sigs/kueue/blob/bee9304a8aa9a83412b5d91516fdd3779ff1b331/pkg/controller/jobs/pod/pod_controller.go#L1140-L1164
[job-refs]: https://github.com/kubernetes-sigs/kueue/blob/bee9304a8aa9a83412b5d91516fdd3779ff1b331/pkg/controller/jobframework/reconciler.go#L1428-L1456
[replacement]: https://github.com/kubernetes-sigs/kueue/blob/bee9304a8aa9a83412b5d91516fdd3779ff1b331/pkg/controller/jobs/pod/pod_controller.go#L1275-L1293
[finished-test]: https://github.com/kubernetes-sigs/kueue/blob/bee9304a8aa9a83412b5d91516fdd3779ff1b331/pkg/controller/jobs/pod/pod_controller_test.go#L2930-L3041
[sky-jobs]: https://github.com/skypilot-org/skypilot/blob/39b151e11075f1d0029f8fc411a42334775fb3e7/sky/server/requests/payloads.py#L479
[sky-serve]: https://github.com/skypilot-org/skypilot/blob/39b151e11075f1d0029f8fc411a42334775fb3e7/sky/serve/server/core.py#L161
[labels]: ../../dsx-workload-exporter/configuration.md#configured-labels
[queries]: ../../dsx-workload-exporter/queries.md
