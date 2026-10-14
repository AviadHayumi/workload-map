<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Synthetic GPU workload scenarios

This example drives the fake GPU operator's existing per-Pod utilization control.
It does not measure activity, change Karta's API, or create an idleness recording rule.
Use it only in a disposable demo namespace with synthetic GPUs.

The pinned fake GPU operator v0.2.1 accepts
`run.ai/simulated-gpu-utilization: "min-max"` on running Pods. Its
[usage calculator](https://github.com/run-ai/fake-gpu-operator/blob/v0.2.1/internal/status-updater/handlers/pod/gpu_usage_calculator.go)
converts that annotation into a simulated utilization range.
The randomizer does not modify GPU fractions, memory allocation, Run:AI resources,
or the shared fake GPU installation.

## Behavior

A CronJob runs once per minute. Each ten-minute round randomly divides eligible
workload UIDs into quiet (0-3%) and active (60-95%) cohorts, alternating assignments
after a seeded shuffle. Two or more workloads guarantee both cohorts. Every GPU
Pod within a workload receives the same scenario. The assignment persists across
minute ticks, providing enough time for a separate 180-second low-activity window.

The ConfigMap stores the seed, round start/end, intended scenarios, workload UIDs,
current Pod UIDs, applied Pod UIDs and completion status.
Each assignment has `status=applying|applied|partial|blocked` and a bounded failure
reason. A missing or incomplete cohort is blocked and the round is partial. JSON logs record each
assignment. Scenario annotations are desired simulation settings, not measured
activity. Grafana must use observed GPU samples for its activity classification.

Only Pods labeled `karta.demo/gpu-randomize=true` are changed. Put the label in
workload Pod templates so replacements retain opt-in. Selection also requires a
positive `nvidia.com/gpu` limit, Running phase, unique current Pod-UID-to-root-UID
Karta mapping, and healthy root observation. A live GPU sibling that is pending,
deleting or opted out blocks the whole mapped cohort. An opted-in GPU Pod with
unknown identity blocks the run. CPU-only frontends are never given GPU metrics.

Updates test Pod UID, resource version and opt-in label before changing annotations.
This prevents name reuse or an intervening opt-out from receiving an old patch.
Other annotations are preserved. Pod updates are not atomic across a workload;
`status=applying|partial` and `applied_pod_uids` expose partial progress, and the
next tick retries the same round. Replacement Pods restart measured activity
history in the separate observer; desired round age is never measurement age.

A ConfigMap resource-version lease protects against overlapping manual Jobs as
well as scheduled runs. Failed workers release the lease after two minutes.
Source failures leave Pods and state unchanged. The namespace listing is bounded
at 512 Pods and selection at 32 workloads. This is a demo harness, not a controller
for arbitrary production workload scales. An empty eligible cohort is an error.

## Deploy and control

Build from the repository root with the example Dockerfile. Publish to an
accessible registry and use its immutable digest. Adapt `deploy.yaml` to a
preexisting demo namespace and the direct Karta exporter Service URL. Keep the
Role namespace scoped. Its Pod patch permission is limited by code to opted-in
fixtures; Kubernetes RBAC cannot express label-scoped patch permission.

The ServiceAccount can list and patch Pods and get/update one precreated state
ConfigMap. It cannot create workloads, delete resources, read Secrets, or change
other namespaces. Keep this account exclusive to the demo Job.

```sh
kubectl --context DEMO_CONTEXT apply -f deploy.yaml
kubectl --context DEMO_CONTEXT -n gpu-demo create job --from=cronjob/gpu-demo-randomizer gpu-demo-manual
kubectl --context DEMO_CONTEXT -n gpu-demo logs job/gpu-demo-manual
kubectl --context DEMO_CONTEXT -n gpu-demo get configmap karta-gpu-demo-round -o jsonpath='{.data.state\.json}'
kubectl --context DEMO_CONTEXT -n gpu-demo patch cronjob gpu-demo-randomizer --type=merge -p '{"spec":{"suspend":true}}'
kubectl --context DEMO_CONTEXT -n gpu-demo patch cronjob gpu-demo-randomizer --type=merge -p '{"spec":{"suspend":false}}'
```

Suspending prevents future scheduled Jobs; an already running Job can finish.
Resume retains the current round until it expires. Manual runs also respect the
current round and lease. New workload UIDs join the next round. Remove the opt-in
label to stop future changes; restore previous utilization annotations from the
fixture backup if needed. Deleting the CronJob does not restore annotation values.
Delete the example manifest to remove its RBAC and state after stopping its Jobs.

## Verify

```sh
cd exporter
go test -race ./examples/gpu-demo-randomizer
```

Tests cover conflicting identities, unhealthy roots, missing replacement UID
mappings, incomplete cohorts, deterministic mixed rounds, annotation preservation,
UID/resource-version guards, idempotent ticks, overlapping manual runs and source
failure. The fake Kubernetes client tests orchestration; actual API preconditions
must also be checked in a disposable cluster when deploying this example.
