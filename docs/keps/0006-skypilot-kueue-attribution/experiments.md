<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# SkyPilot and Kueue attribution experiments

This is a small functional comparison, not a load test or production qualification.
The [proposal](README.md) explains the choices. Experimental association metrics
came from separate Python collectors, not a modified DSX exporter.

## Setup

| Component | Version or selection |
| --- | --- |
| Kubernetes | 1.35.5 |
| SkyPilot chart and CLI | 0.14.2, source commit `39b151e11075f1d0029f8fc411a42334775fb3e7` |
| Kueue chart and controller | 0.20.5, Workload API `v1beta2` |
| DSX image | `0.0.0-main-643ba716`, digest `sha256:b28b32755dfc871e3e3b76d4de1be8c8964b8ad1d7b995a4feeb1b54dec9344c` |
| kube-state-metrics | 2.14.0 |
| Test Prometheus | 3.1.0; scrape and evaluation interval 2 seconds |

Kueue ran in `exporter-kueue-system`. Its webhook and controller selected only
namespaces labelled `exporter-experiment=kueue`. The controller namespace was
not selected. `manageJobsWithoutQueueName` was false. Only Pod and Job
integrations were enabled.

A separate SkyPilot API server used namespace `sky-kueue-lab`, LocalQueue
`research`, and the default Kubernetes scheduler. The existing SkyPilot and KAI
installations were not reconfigured. These test namespaces had no KAI queue
label. Namespace isolation does not isolate shared node capacity.

The test ClusterQueue initially had zero CPU quota. It was increased to 12 CPU
after recording the gated state. The two-node task requested 2 CPU and 4 GiB
per node. All additional fixtures were tiny CPU-only workloads.

| Exporter namespace | Additional component |
| --- | --- |
| `exporter-sky-labels` | Test Prometheus; SkyPilot label rules |
| `exporter-kueue-ksm` | KSM custom Workload conditions |
| `exporter-kueue-karta` | Required Workload kind and experimental Karta |
| `exporter-kueue-relation` | Read-only Workload-owner UID association collector |
| `exporter-sky-api` | Read-only SkyPilot API job association collector |
| `exporter-synthetic-root` | Experimental ClusterGroup and read-only membership collector |

All six DSX instances selected only `sky-kueue-lab`. Read permissions included
Pods, Jobs, Workloads and the SkyPilot API server's Deployment/ReplicaSet chain.
The association collector had a namespaced Role. The SkyPilot API collector
had no Kubernetes service-account token. No existing Pod owner reference changed.

## Results

| Input or operation | Observed result |
| --- | --- |
| Submit a real two-node SkyPilot task with zero quota | Both Pods stayed scheduling-gated. All six DSX instances still exposed both Pod records. |
| Increase test queue quota | Kueue removed its gates and both Pods ran. |
| Group SkyPilot labels | PromQL returned one cluster with two members. |
| Join KSM Admitted condition | Both Pod records acquired the same Kueue Workload UID. |
| Reverse owner-reference UID join | The prototype associated both current Pod UIDs with that Workload UID. |
| Remove only the prototype's Workload read permission | Association rows disappeared; collection validity became 0. Both normal DSX Pod rows remained. Restoring permission recovered the collector. |
| Replace a failed member | A new-name replacement joined the same Workload UID. The old Pod metric disappeared. Kueue removed the old owner reference in this run. |
| Delete and recreate the synthetic group | Associations disappeared and returned with a new group UID. Both Pod UIDs stayed unchanged. |
| Same Pod name and SkyPilot label in another namespace | Scoped DSX and the synthetic collector excluded the foreign Pod UID. |
| Two local jobs on the same SkyPilot cluster | API snapshot showed jobs 2 and 3 Running concurrently. Both later succeeded. |
| Join job records to cluster presence | Three job records joined to the same cluster; this is identity, not per-job usage. |
| Native Kubernetes Job | Existing Job Karta produced `dsx_workload_status{phase="Completed"}=1`. |
| One successful and two failed group Pods | Kueue reported Finished=True, reason=Succeeded, message="Pods succeeded: 1/3." |

The UID matcher also passed 16 fixture tests covering missing objects, stale
references, namespace separation, ambiguity, deletion and read failures.
These fixture tests are distinct from the live checks above.

<details>
<summary>What the comparison does not prove</summary>

- Virtual exporter roots are not implemented. Only equivalent query grouping was tested.
- The synthetic CR's member list was supplied by the experiment. No production
  controller maintains it, and DSX still reports Pod roots.
- The new API collector tested local jobs only. Earlier tests covered managed
  pipelines and SkyServe without Kueue. Their combined integration needs its own test.
- No real GPU was required. Identity joins do not prove per-job GPU accounting.
- No long-running soak, scale benchmark or production security review was performed.
- The two Kubernetes list calls in the prototype are separate snapshots. A production
  watcher must revalidate publication inputs and define a freshness bound.
- A two-second scrape interval is not a two-second end-to-end latency guarantee.

</details>

<details>
<summary>Failed attempts and corrections</summary>

The initial exporter RBAC omitted ReplicaSets and Deployments. This affected
owner discovery for the SkyPilot API Pod, not the bare compute Pods. Read-only
access for those two resource types was added to the six test exporters.

A PromQL join initially omitted `()` after `group_left` before a parenthesized
expression. Correcting the syntax produced the three expected job associations.

Deleting a failed Kueue member did not immediately free its name: Kueue retained
a finalizer while waiting for replacement. The successful replacement used a new
Pod name with the same template. No finalizer was forced away.

The existing platform agent deleted the experimental Karta on its roughly
ten-second control-plane sync. Its logs explicitly said the definition did not
exist in the control plane. That agent was left unchanged. The admission-phase
assertion did not pass, so the Karta status test remains blocked by the environment.
The separate Workload record and unchanged Pod roots were observed; they do not
count as a successful Karta status test.

</details>

## Local Kyverno CEL probe

The additional CEL experiment used the real Kyverno SDK resource library with
an in-memory provider. It did not install Kyverno or change the exporter or lab.
The [named lookup and predicate](README.md#9-configurable-relationships-with-cel)
compiled and ran with Go 1.27.3, cel-go 0.29.2 and apimachinery 0.37.2, matching
the exporter's direct versions. The SDK was pinned to
`v0.0.0-20260825082632-68d74afcb07a`; its library compatibility argument was 1.18.
`go test -mod=readonly -count=1 -v ./...` passed in the isolated probe module.

| Input or operation | Observed result |
| --- | --- |
| Workload `w1` has an ordinary owner reference to Pod `p1` | Named lookup plus match returned true. No controller owner was required. |
| Wrong namespace, stale Pod UID or missing owner references | Named match returned false. |
| Missing named Workload | Get returned null; the guarded predicate returned false. |
| Provider denies Get or List | CEL returned an error. This simulated denial did not test cluster RBAC. |
| Two Workloads reference the Pod | The List expression returned both. The caller must reject ambiguity if it requires one target. |
| Target appears, disappears, then is recreated with UID `w2` | Explicit evaluations returned the new state. No evaluation was triggered automatically. |
| Pod is recreated with UID `p2` | The old reference to `p1` did not match. |
| Expression calls `post` or `Post` | Both compiled and reached the provider, which rejected writes. |

The List capability check needed `dyn(resource.list(...).items)` before its CEL
filter. Direct `.items.filter(...)` did not compile with the inspected binding.
The recommended named-Get rule avoids that list scan.

These checks prove expression and provider behavior, not exporter integration.
No real HTTP call, watch/index implementation, publication race test, scale test
or full exporter dependency integration was run. Missing group-label handling
remains part of the proposed Pod filter. The production test must also cover an
initially unsynced cache and target creation without any Pod update.

Source research used Kyverno commit `3bb926936656bfb14ae1a6306a6d9208a4b77613`
and SDK commit `68d74afcb07a4ad8c941e8bc43e9da31d9f703b0`.
The proposal links the inspected provider, cache and polling implementations.

## Lab state after testing

All ten scrape targets were healthy at the final check. The SkyPilot compute
cluster and named test fixtures were removed. Their current Pod metrics disappeared.
Only the separate SkyPilot API Pod remained in the workload namespace.

Kueue, the separate SkyPilot API server, test queues, six exporters, association
prototypes, KSM and test Prometheus remain installed for reuse. Existing platform
workloads and configuration were left unchanged.

## Reproduce the useful parts

Use an isolated namespace and explicit kubectl context. Install pinned Kueue
0.20.5 with the Pod and Job integrations. Scope both its manager and webhooks to
the test namespace label, then create a ResourceFlavor, ClusterQueue and LocalQueue.
The Kueue system namespace must not match that label.

Configure the separate SkyPilot API server:

```yaml
kubernetes:
  namespace: sky-kueue-lab
  kueue:
    local_queue_name: research
    admission_timeout: 900
  pod_config:
    spec:
      schedulerName: default-scheduler
```

This is SkyPilot configuration, not exporter Helm configuration. Do not combine
KAI and Kueue settings for the same SkyPilot cluster. Use the existing
[SkyPilot label rule](README.md#alternatives-and-what-was-tested) in the exporter.
Submit a short CPU-only two-node task and compare the Pods, Workload and metrics
before and after releasing quota.

For KSM, mount this configuration and set
`--custom-resource-state-only=true`,
`--custom-resource-state-config-file=/etc/state/config.yaml`, and
`--namespaces=sky-kueue-lab`. Grant list/watch on Workloads and CRD discovery.

```yaml
kind: CustomResourceStateMetrics
spec:
  resources:
    - groupVersionKind:
        group: kueue.x-k8s.io
        version: v1beta2
        kind: Workload
      labelsFromPath:
        namespace: [metadata, namespace]
        kueue_group: [metadata, name]
        kueue_workload_uid: [metadata, uid]
      metrics:
        - name: kueue_workload_condition
          help: Kueue controller condition, not SkyPilot job status.
          each:
            type: Gauge
            gauge:
              path: [status, conditions]
              labelsFromPath:
                condition: [type]
                condition_status: [status]
                reason: [reason]
              valueFrom: [status]
```

Configure exactly one served Workload version. Missing conditions remain absent.
KSM maps True to 1 and False/Unknown to 0; keep the condition-status label to
preserve that distinction. Use the join in the proposal and compare its UID
against the Workload object before interpreting any measurement.
