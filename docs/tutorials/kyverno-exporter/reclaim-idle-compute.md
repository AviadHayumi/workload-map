<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Reclaim GPU, CPU and memory capacity with workload policies

A workload can hold four GPUs without making progress. Another can request
eight CPU cores and 16 GiB of memory while barely using either. A third can
stay in `Initializing` for hours. These are opportunities to return capacity
to the queue, enforce runtime budgets and stop manual cleanup.

DSX identifies the workload behind each Pod. Prometheus combines that identity
with status, utilization and resource requests. Kyverno applies the resulting
decision to the owning workload: suspend it, change a supported field, or
delete it after its retention period.

[Value](#the-value-turn-observations-into-actions) | [Prerequisites](#what-you-need-for-each-scenario) | [Examples](#recipe-1-reclaim-a-workloads-idle-gpus) | [Run the lab](#run-the-verified-kind-lab) | [Live results](#live-verification-results)

## The value: turn observations into actions

Thresholds below are examples. Choose them for the application and its owners.

| Opportunity | Example policy | What the team gets | Evidence |
| --- | --- | --- | --- |
| Reclaim idle GPU allocations | Every allocated GPU stays below 5% for an hour, the workload is Running, and no application work is queued. Suspend an opted-in Job that can restart safely. | GPU capacity becomes available after its Pods terminate. | Historical fake-GPU run; real-GPU recipe below. |
| Release unused CPU and memory requests | A Job requests 8 cores and 16 GiB, but uses under 0.1 core and a small memory footprint for an hour. Confirm application idleness, then notify its owner or suspend it. | Capacity reserved in scheduler accounting becomes available to other workloads. | Proposed cAdvisor recipe below. |
| Put a limit on time in status X | `Running` for 8 hours: suspend. `Initializing` for 90 minutes: investigate. `Degraded` for 10 minutes: notify and retain diagnostics. | Runtime budgets and stalled-work detection use workload state. | Running-Job suspension tested; other phases are extensions. |
| Retire completed work | A Job stays Completed beyond its retention window. Delete the Job and its owned Pods. | Fewer stale objects and less manual cleanup. | Job and Pod deletion tested in the live lab. |
| Warn before the limit | At 80% of the runtime or cost budget, notify the owner before a later action. | Owners can finish, checkpoint or request an exception. | Proposed; needs an alert delivery path. |

![Four ways workload policies return capacity and reduce manual work](images/value-overview.png)

Reclaimed requests create scheduling headroom. A lower cloud bill also needs
node consolidation, scale-down or release of paid capacity. This guide does
not claim measured financial savings.

The [original runtime-rules proposal](https://github.com/AviadHayumi/workload-map/blob/1a90457bbdc349bdc1fb5c7f0924198f291e6a04/keps/0003-runtime-rules/README.md#2-the-goals)
already described GPU idleness, time in a phase, completed-work retention and
warnings before action. This guide keeps those goals and uses the current
`dsx_*` metric contract. Its [CPU-only lab](#run-the-verified-kind-lab) proves the suspension and
cleanup mechanism; the GPU and utilization recipes need the inputs below.

## More policies to build from the same signals

These are proposed extensions. Their additional inputs and actions need their
own validation.

| Policy | Value | Additional input and action |
| --- | --- | --- |
| Stop a stalled training run | Avoid hours of compute without progress. | A progress counter and checkpoint confirmation; suspend an opted-in Job. |
| Prefer queued priority work | Return idle capacity when important work is waiting. | Scheduler backlog, priority and verified idleness; suspend eligible low-priority Jobs. |
| Scale an idle development service to zero | Release out-of-hours development requests. | No sessions or queued traffic, a restore owner, and coordination with any autoscaler; set Deployment replicas to zero. |
| Retain results before cleanup | Avoid deleting the only copy of a result. | Completed history plus an artifact-retention signal; delete only after both qualify. |
| Pause a repeatedly failing schedule | Stop producing another failed run every interval. | CronJob-to-Job history; set the CronJob's suspend field. Existing Jobs need separate handling. |
| Adjust one quiet component | Reclaim excess workers without treating every component alike. | Verified component attribution, demand and supported scaling; for example, investigate quiet decode workers separately from busy prefill workers. |
| Attribute cost to a team or workload | Make the owner and source of spend visible. | Join a cost source to DSX identity and configured team labels; alert on a defined budget before choosing an action. |

The writes follow Kubernetes' [Job suspension](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job),
[Deployment scaling](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#replicas)
and [CronJob suspension](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#schedule-suspension)
semantics. The signal and eligibility rules are the additional policy work.

## What you need for each scenario

All scenarios need DSX workload attribution, a supported workload definition,
Prometheus history, and Kyverno permissions for the selected target operation.
DSX supplies identity, normalized status, creation time and observation
diagnostics. Other sources supply utilization, requests, prices and application
progress.

| Scenario | Additional prerequisites | What to verify first |
| --- | --- | --- |
| Time in status X and completed-Job cleanup | No GPU or utilization exporter. The included kind lab is enough. | The definition's phase means what the policy expects. For example, a CronJob's Running state is not proof that a child Job is executing. |
| Real GPU idleness | A functional NVIDIA GPU on a supported Linux node, working driver and container access, device allocation, and DCGM Exporter. GPU Operator can manage this stack. | A real GPU workload runs. DCGM reports the expected device and Pod identity. Prometheus scrapes both DCGM and DSX. |
| CPU and memory use versus requests | Authorized kubelet `/metrics/cadvisor` scrapes and request metrics from kube-state-metrics or kube-scheduler. | Usage and requests refer to the same current Pods and containers. Duplicate scrapes do not inflate totals. |
| Confirm application idleness | Application progress, active sessions, queue depth or another owner-approved signal. | Low utilization is expected during neither useful waiting nor startup. A resume or restart path exists. |
| Cost budgets | A cost source such as OpenCost, workload attribution, a budget and its accounting period. | Price, currency and attribution are defined. A sampled policy is not an exact financial cap. |

The included `up.sh` installs DSX, Prometheus and Kyverno. It does not install
GPU drivers, DCGM, kube-state-metrics, cAdvisor scrapes or a notification service.
For real GPUs, follow the [DCGM installation requirements](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html).
For CPU and memory, configure the [Kubernetes metrics endpoints](https://kubernetes.io/docs/concepts/cluster-administration/system-metrics/).

## Recipe 1: reclaim a workload's idle GPUs

Example: `gpu-trainer` holds four dedicated GPUs. Every allocated device stays
below 5% utilization for one hour. The workload is Running, has no queued work,
and has a valid checkpoint. An opted-in policy can set `Job.spec.suspend=true`.
The Job controller terminates its Pods and releases their GPU allocations.
Suspension does not create a checkpoint; the application must support recovery.

Measure the workload, not an average across the cluster:

1. Join each DCGM device measurement to DSX by namespace and Pod UID.
2. Preserve device identity. Deduplicate measurements before aggregation.
3. Check the highest utilization across every allocated device and the entire
   observation window. A quiet device must not hide a busy one.
4. Require complete allocation coverage, valid Running history and application
   idleness before selecting the workload UID for a Kyverno action.

Enable Kubernetes mapping in DCGM Exporter and
`DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID=true`. Verify the actual `pod_uid`
label in its output. The [current GPU queries](../../dsx-workload-exporter/queries.md#3-gpu-measurements-by-pod-uid)
show the join and its prerequisites. Expected allocation counts and device IDs
must come from the allocation system; observing only three of four GPUs is
incomplete evidence. MIG, shared GPUs and DRA require their own attribution and
supported utilization metrics.

The original fork already demonstrated the policy loop with fake GPUs.
`gpu-trainer` stayed untouched at 90% simulated utilization. After the input
changed to 1% and the last busy sample left the two-minute window, Kyverno
suspended the Job and its Pod disappeared. The
[recorded run](https://github.com/AviadHayumi/workload-map/blob/f28da01bdf140355964fff5d450f867265371608/labs/exporter-kyverno-rr/captures/75-gpu-idle-fake-gpu.txt)
used DCGM metric names with simulated values. It did not measure a physical GPU.

## Recipe 2: act when a workload stays in status X too long

The rule is: the same workload UID has valid observations of phase X across
the full window, and the target still qualifies when Kyverno evaluates it.

| Phase and duration | Example response | Why |
| --- | --- | --- |
| `Running` for 8 hours | Suspend an opted-in Job. | Enforce a runtime budget. |
| `Initializing` for 90 minutes | Notify the owner or annotate for investigation. | Expose a stalled start; check scheduler backlog before treating it as waste. |
| `Degraded` for 10 minutes | Notify and retain diagnostic data. | Investigate persistent degradation. |
| `Completed` for 24 hours | Delete after outputs and required logs are retained. | Apply a retention policy. |

The [tested recording rules](manifests/prometheus-rules.yaml) implement the
Running and Completed cases with two-minute windows. They check sample count,
current and historical endpoints, creation age, scrape health and observation
validity. Changing the phase or window requires changing the associated count,
age and offset checks together. Workload age alone is not time in a phase.

The meaning of X comes from the workload's Karta definition. A different kind
also needs its own supported write operation and permissions. Shared status
queries do not make `Job.spec.suspend` valid on every Kubernetes resource.

For a Job with a fixed active deadline or simple completion TTL, use the native
`activeDeadlineSeconds` or `ttlSecondsAfterFinished` fields when they fit.
The metric-based policy becomes useful when phase history, application signals
or several conditions decide the action.

## Recipe 3: find workloads that reserve resources but do little work

Consider a simple Job with one regular container, no init containers and no Pod
overhead. The following numbers are illustrative, not a measurement from this
lab:

| Resource | Requested | Observed for one hour | Interpretation |
| --- | --- | --- | --- |
| CPU | 8 cores | Less than 0.1 core | Under 1.25% of the requested CPU is being used. |
| Memory | 16 GiB | Working set below 300 MiB | Small memory footprint compared with the request. This is not a measure of memory activity. |

Kubernetes schedules against requests even when actual use is low. Suspending
this Job releases its 8-core and 16-GiB requests from scheduling accounting
after its Pod terminates. It does not mean 16 GiB of physical RAM was in use.
See [Kubernetes resource requests](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/).

cAdvisor supplies the numerator:

```promql
rate(container_cpu_usage_seconds_total{
  namespace!="", pod!="", container!="", container!="POD"
}[5m])
```

This is CPU cores, not percent. For memory, use
`container_memory_working_set_bytes` with the same container filters; it is a
gauge in bytes, so do not apply `rate()` to it. The
[CPU and memory attribution examples](../../dsx-workload-exporter/queries.md#2-cpu-and-memory-by-component-instance-and-group)
join these measurements to DSX and sum them by workload or component.
See the [cAdvisor metric definitions](https://github.com/google/cadvisor/blob/master/docs/storage/prometheus.md)
for the counter and gauge units.

kube-state-metrics supplies declared container requests:

```promql
kube_pod_container_resource_requests{resource="cpu",unit="core"}
```

Use `resource="memory",unit="byte"` for memory. Its `uid` label is the Pod UID;
copy it to `pod_uid` before joining DSX. With the
[deduplicated DSX attribution rule](../../dsx-workload-exporter/queries.md#1-deduplicate-pod-attribution)
installed, the CPU-request join is:

```promql
label_replace(
  kube_pod_container_resource_requests{resource="cpu",unit="core"},
  "pod_uid", "$1", "uid", "(.+)"
)
* on (namespace, pod_uid)
  group_left (workload_name, workload_uid, workload_group, workload_kind)
  dsx:pod_attribution:unique{observation_valid="true"}
```

These are diagnostic query fragments, not installed action rules. Deduplicate
external scrapes and restrict both sides to the same current running Pods and
regular containers before summing by namespace and workload UID. Divide used
cores by requested cores only when the denominator is positive. Compare the
memory working set with the matching memory request in the same way.

For a one-hour decision, record the per-workload ratio at each evaluation and
require its maximum to stay below the threshold for that hour. Also require
complete, fresh observations throughout the window. A maximum preserves busy
periods that a long-window average can hide.

A container-request sum omits scheduling effects such as init containers and
Pod overhead. For effective Pod scheduling requests, use the scheduler's
`kube_pod_resource_request` metric with a validated Pod-identity join. The
[kube-state-metrics reference](https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/workload/pod-metrics.md)
documents the request fields and recommends that scheduler metric.

Policy choices differ by intent:

- Reclaim idle batch capacity: require sustained low CPU, complete telemetry,
  no application work and a safe restart path; then suspend an opted-in Job.
- Reduce oversized requests: flag a workload whose CPU use or memory footprint
  stays far below its request. Review peaks and startup behavior before changing
  the next workload submission or deployment template.
- Close abandoned development sessions: combine low activity with no active
  sessions and an expiry rule. Use a supported stop operation and a named owner
  for restoring it.

Low CPU can mean an application is waiting for useful I/O. Low or unchanged
memory does not prove inactivity. The current cAdvisor example joins Pod names,
so it is useful for diagnosis but is not sufficient for automatic suspension
across Pod replacement. An action policy needs stable identity and complete
history for every container it judges.

## Keep the owner in control

Use an explicit opt-in, namespace scope, a tested operation and an opt-out path.
The live lab verifies that unselected and failed Jobs remain untouched, a new
UID does not inherit an old Job's eligibility, and missing telemetry blocks
actions. Its [evidence-window checks](#5-understand-when-the-rule-waits)
are the starting point for additional signals; each source also needs its own
freshness and coverage checks. Prometheus observations and Kubernetes writes
are not one atomic transaction.

Owner notifications need an alert delivery service. Checkpointing needs
application support. Long-term action history needs retained audit records.
Those responsibilities are separate from the exporter and the two lab policies.

## Run the verified kind lab

This CPU-only kind lab demonstrates the two implemented Job policies. Follow
the steps below to see the API objects change and verify recovery.

| Input in `ai-lab` | Policy operation | Result |
| --- | --- | --- |
| `running-worker`, labelled `action: suspend`, with Running observations across a two-minute window | Set `Job.spec.suspend: true` | The Job controller removes its active Pods. |
| `completed-report`, labelled `action: delete`, with Completed observations across a two-minute window | Delete the Job | Kubernetes garbage collection removes its owned Pods. |

The full label key is `tutorial.karta.run.ai/action`. Jobs without an action's
label are left alone. Failed Jobs are retained for inspection.

The two-minute windows make the result visible without a long wait. The GPU
and cAdvisor recipes above require additional integrations.

![Exporter observations become Kyverno actions](images/policy-flow.png)

### 1. Create the lab

Run commands from this repository's root. Install Docker, kind, kubectl, Helm,
Bash, curl and Python 3 first. Docker must be running. No GPU is required.

```sh
bash docs/tutorials/kyverno-exporter/up.sh
export KUBECONFIG="$PWD/.artifacts/kyverno-exporter-tutorial/kubeconfig"
kubectl config current-context
```

The context must be `kind-kyverno-exporter-tutorial`. The script creates that
separate kind cluster and kubeconfig. The recorded run used these versions:

| Component | Version or source |
| --- | --- |
| kind | `0.30.0` |
| Docker | `29.0.1` |
| Helm | `4.0.1` |
| Kubernetes | `kindest/node:v1.34.3`, Linux arm64 |
| Kyverno | Helm chart `3.9.1`, application `1.19.1` |
| Prometheus | `quay.io/prometheus/prometheus:v3.14.0` |
| DSX workload exporter | Built from this checkout, loaded into kind |
| Example Job image | `busybox:1.37.0` |

The exporter uses the bundled `batch-job-v1` definition. It watches `ai-lab`
and discovers Jobs and their Pods. This installation does not need the Karta
operator or Karta CRD. The [exporter values](manifests/exporter-values.yaml)
show the selection and permissions.

Prometheus scrapes every five seconds and evaluates the
[recording rules](manifests/prometheus-rules.yaml) every five seconds. A recording
rule saves a query result as a metric that another query can read.
Kyverno's background scan runs every 30 seconds. Its cleanup schedule runs
once a minute. Actions therefore happen after the window qualifies and a
controller processes the Job, rather than at an exact two-minute deadline.

Rerunning `up.sh` rebuilds the exporter and restarts exporter and Prometheus
Pods. Prometheus uses an `emptyDir` volume, so this clears the lab's metric
history. The script does not apply the workload policies.

### 2. Observe a workload

```sh
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/workloads.yaml
kubectl -n ai-lab get jobs
kubectl -n ai-lab get job running-worker -o jsonpath='{.metadata.uid}{"\n"}'
```

The manifest also creates three controls: an unselected running Job, an
unselected completed Job, and a failed Job selected for cleanup. None should
be changed by these policies.

In a second terminal, with the same kubeconfig:

```sh
kubectl -n monitoring port-forward service/prometheus 19090:9090
```

Open [the local Prometheus query page](http://127.0.0.1:19090/query), or query
its API from the first terminal:

```sh
curl -fsSG http://127.0.0.1:19090/api/v1/query \
  --data-urlencode 'query=dsx_workload_status{namespace="ai-lab",workload_name="running-worker",phase="Running",observation_valid="true"}'
```

Once the Job has a ready Pod, the result has value `1`. Its `workload_uid`
label equals the Job's `metadata.uid`. The exporter reads the Job's active and
ready counts through its bundled definition. `observation_valid="true"`
means the exporter verified the inputs for that observation; it does not mean
the application is healthy.

The current exporter also publishes Pod attribution, creation timestamps and
observation diagnostics. The [metric reference](../../keps/0001-metrics-exporter/README.md#metrics)
describes these four families. Historical examples on the runtime-rules branch
use older `karta_*` names; this tutorial uses the current `dsx_*` contract.

### 3. Suspend an opted-in running Job

```sh
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/policies.yaml
kubectl -n ai-lab get jobs --watch
```

Wait until `running-worker` shows `Suspended`, then press Ctrl-C to stop the
watch. The manifest installs both policies. `tutorial-suspend-running` is a
`MutatingPolicy` with admission mutation disabled and mutation of existing
resources enabled. It selects Jobs in `ai-lab` with the suspend label, then
checks the current target's label, namespace, active Pods and suspension state.

The policy's CEL expression receives the Job as `object`. Its relevant input
fields look like this (`uid-A` is illustrative):

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: running-worker
  namespace: ai-lab
  uid: uid-A
  labels:
    tutorial.karta.run.ai/action: suspend
spec: {suspend: false}
status: {active: 1, ready: 1}
```

Kyverno calls Prometheus's `/api/v1/query` endpoint. The query selects
`tutorial:job_running:eligible` with a saved result less than 15 seconds old.
For this example, one row in the response's `data.result` array is:

```json
{"metric":{"namespace":"ai-lab","workload_name":"running-worker","workload_uid":"uid-A"},"value":[1794141000,"1"]}
```

The expression compares `object.metadata.uid` and
`object.metadata.namespace` with that row. Both match, so `exists(...)` returns
`true`. The JSON patch adds `/spec/suspend` with value `true`. The
[complete policy](manifests/policies.yaml) includes these checks and the patch.
The exporter performs no writes.

```sh
kubectl -n ai-lab get job running-worker -o jsonpath='{.spec.suspend}{"\n"}'
kubectl -n ai-lab get pods -l batch.kubernetes.io/job-name=running-worker
```

The first command should print `true`. After Job reconciliation, the second
shows no Pods. Inspect the actual Job to establish that a mutation happened;
a completed Kyverno work item alone is not proof of that result.

### 4. Delete a completed Job after the observation window

`tutorial-delete-completed` is a `DeletingPolicy`. Once a minute it selects
Jobs with the delete label and checks two conditions: the current Job has
`Complete=True`, and its UID is in `tutorial:job_completed:eligible` with a
fresh recording sample.

The policy sets `deletionPropagationPolicy: Background`. Kyverno deletes the
Job, then Kubernetes garbage collection removes its Pods. The explicit setting
prevents orphaned Pods; deleting only the Job did leave Pods behind in this lab.

```sh
kubectl -n ai-lab wait --for=delete job/completed-report --timeout=5m
kubectl -n ai-lab get pods -l batch.kubernetes.io/job-name=completed-report
kubectl -n ai-lab get jobs
```

`completed-report` and its Pods disappear. `unselected-report` and
`failed-report` remain.
The age check uses the object's creation time, while the phase window checks
its completion history. Creation age by itself does not establish how long a
Job has been completed.

The [namespaced roles](manifests/kyverno-rbac.yaml) grant the background
controller update and patch access to Jobs in `ai-lab`. The cleanup controller
gets delete access there. Its DeletingPolicy controller also needs a ClusterRole
to list Jobs before filtering namespaces. That grant is read-only; update and
delete permissions remain confined to `ai-lab`. Kyverno retains its chart's
baseline permissions.

### 5. Understand when the rule waits

![Evidence required before a policy acts](images/evidence-window.png)

Each recording rule requires all of these checks for the same workload UID:

- Every available valid phase sample in the two-minute range is `1`.
- At least 23 phase samples exist. A five-second interval normally gives 24;
  the floor allows one missing status sample.
- The phase is also `1` now and at the two-minute anchor. Both endpoint samples
  must be less than 15 seconds old relative to their evaluation time.
- The Job is older than 120 seconds.
- No invalid status sample or workload-error observation appears in the window.
- Exporter scrapes have enough samples, all available `up` values in the window
  are `1`, and the current scrape is fresh and successful.

Kyverno also checks the recording sample's freshness. Empty query results do
not match a target. An HTTP or evaluation error cannot supply a matching UID.
Samples cannot establish what happened between scrapes. The rules are designed
for this single exporter and single-cluster lab; they are not a distributed
history or transactional action system.

Deleting and recreating a Job with the same name gives it a new UID. Its old
history cannot qualify the replacement. A failed scrape prevents action until
it leaves the window and enough new observations exist.

### 6. Resume a Job and opt out

Opt out and resume in one update:

```sh
kubectl -n ai-lab patch job running-worker --type=merge -p \
  '{"metadata":{"labels":{"tutorial.karta.run.ai/action":"observe"}},"spec":{"suspend":false}}'
```

The Job controller creates a new Pod. The suspension policy skips this Job.
Changing only `spec.suspend` leaves the Job eligible for another suspension
once its Running history qualifies again. Re-enabling the label can use
existing history; this policy does not track a separate opt-in timestamp.

### 7. Run the live verification

```sh
python3 docs/tutorials/kyverno-exporter/verify.py
```

The script resets only its labelled Jobs and its two named policies in the
lab cluster. It checks effective permissions before creating fresh fixtures.
It checks initial waiting, suspension, Pod removal, cleanup,
negative controls, same-name replacement, exporter and Prometheus outages,
recovery, and opt-out before resume. It also opts a previously eligible Job in
while exporter telemetry is unavailable. Expect several minutes of real waiting.
During the outage tests it scales the lab exporter and Prometheus to zero, then restores
one replica of each. Do not run setup or edit policies while verification is running.

After the query-service outage, suspension recovers automatically. The test
explicitly requeues the cleanup policy using the recovery below. Its pass result
includes that operator-assisted recovery; it does not establish automatic
cleanup recovery.

Results are written to `.artifacts/kyverno-exporter-tutorial/verification.json`.
See [the recorded run](#live-verification-results) for the checked revision, verified outcomes
and limitations. The final run passed in 670 seconds, including dependent Pod
removal. The test checks API outcomes as well as Prometheus responses.

### Recover cleanup after a Prometheus outage

On the pinned Kyverno 1.19.1 release, repeated query errors can exhaust the
cleanup controller's ten retries. The DeletingPolicy can leave the queue and
stop being scheduled even after Prometheus recovers. The live outage test
reproduced this behavior.
Check `status.lastExecutionTime` if cleanup has stopped but Prometheus returns
eligible Jobs.

After Prometheus is healthy, requeue this policy by changing its spec:

```sh
kubectl patch deletingpolicy tutorial-delete-completed --type=merge \
  -p '{"spec":{"schedule":"* * * * *"}}'
kubectl patch deletingpolicy tutorial-delete-completed --type=merge \
  -p '{"spec":{"schedule":"*/1 * * * *"}}'
```

Both schedules mean once per minute. The spec changes requeue the policy and
restore its original configuration. An annotation change or reapplying an
unchanged manifest does not do that. Verify that `status.lastExecutionTime`
advances and that the eligible Job disappears. See the
[pinned scheduler](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/controllers/deleting/controller.go#L264)
and [retry handling](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/utils/controller/run.go#L66).

### Inspect a problem

```sh
kubectl get mutatingpolicy tutorial-suspend-running -o yaml
kubectl get deletingpolicy tutorial-delete-completed -o yaml
kubectl -n kyverno get updaterequests
kubectl -n kyverno logs deployment/kyverno-background-controller --tail=80
kubectl -n kyverno logs deployment/kyverno-cleanup-controller --tail=80
```

In Prometheus, inspect `up{job="dsx-exporter"}`, the raw status, and the two
`tutorial:job_*:eligible` recording rules in that order. Check `/api/v1/rules`
for evaluation errors. Observe the Job and its Pods to confirm the result.

### Operating limits

Prometheus here retains two hours of samples in an ephemeral volume. It is
not a durable action audit. Production deployment also needs suitable resource
limits, retained audit records, and decisions about user overrides and action
concurrency. Additional signals and workload kinds need separate validation.
The historical runtime-rules proposal is not a controller installed by this lab.

### Remove the lab

```sh
kind delete cluster --name kyverno-exporter-tutorial
```

Only the named lab cluster is removed. Stop the port-forward with Ctrl-C.
The ignored artifact directory remains for inspecting results.

## Live verification results

Recorded on 2026-11-08.

The complete live verifier passed in 670 seconds. It tested suspension,
completed-Job deletion, dependent Pod removal, UID isolation, outage handling
and recovery. Suspension recovered automatically. Cleanup recovered after an
explicit policy requeue. That operator-assisted step is part of the pass result.

### Test provenance

The recorded run used the `kyverno-exporter-tutorial` worktree, based on fork branch
`kep-runtime-rules` at `1a90457b`. Its 23 commits were rebased on upstream
`8370c0b5a720771dcb2e8c944d2e4bba39e7df3a`, producing base HEAD
`cba10f960b8d0dc8c938e23d8795579810e2c4c1`. Exporter code came from that
upstream revision. The fork PR contains the tutorial commits on the tested
upstream base; it omits the inherited KEP proposals.

The [lab setup table](#1-create-the-lab) lists the tested tool and component
versions.

After recovery from a shared Docker storage failure, the setup script completed
a full rerun with its recording rules and corrected cleanup permissions.
The exporter was built from the checkout and loaded locally. Its image ID and
deployed image names are in the ignored `inventory.json` artifact.

Associated branch references consulted:

| Reference | Purpose |
| --- | --- |
| `fork/kep-runtime-rules` | Runtime policy goals and historical examples. |
| `fork/exporter-integration-lab` | Earlier policy manifests and observations. |
| `fork/exporter-kind-e2e` | Current kind setup and exporter test conventions. |
| `fork/exporter-dsx-naming` | Exporter naming and installation defaults. |
| `fork/exporter-controller-runtime` | Exporter implementation context. |
| `fork/grafana-dashboard` | GPU integration context. |
| `fork/exporter-demo-release-9c14b074` | Historical image provenance. |

### Observed outcomes

The final run started at 13:50:44 UTC. Elapsed values are check completion times,
not exact controller action timestamps. The verifier polls every five seconds.

| Check | Elapsed | Observation |
| --- | --- | --- |
| Effective permissions | 0s | Background update and cleanup delete allowed in ai-lab and denied in default |
| Verified workload observation | 6s | Current exporter reports a verified Running Job |
| Fresh Jobs wait | 52s | Fresh Jobs survived 45 seconds after the first Running observation |
| Suspension and cascading cleanup | 189s | Kyverno suspended running-worker and deleted completed-report; both Jobs' Pods disappeared |
| Unselected and failed Jobs retained | 190s | Unselected Running/Completed Jobs and selected Failed Job remain untouched |
| Same-name replacement with a new UID | 266s | Replacement was observed under its new UID and survived 65 seconds of healthy telemetry without inheriting eligibility |
| Exporter outage | 433s | 145 seconds without exporter scrapes produced no eligible Jobs or policy actions |
| Previously eligible Job during exporter outage | 433s | An already observed Running Job was opted in during the outage and remained active |
| Prometheus outage | 506s | 65 seconds with Prometheus stopped left selected Jobs unchanged |
| Recovery waits for fresh history | 578s | A fresh minute of recovered telemetry was insufficient for action |
| Automatic suspension recovery | 639s | Suspension resumed after a new qualifying observation window |
| Explicit cleanup recovery | 670s | Equivalent schedule changes resumed cleanup; eligible completed Jobs and their Pods disappeared |
| Opt-out before manual resume | 670s | running-worker remained active beyond the policy window |
| Complete verifier | 670s | All checks passed; exporter and Prometheus restored to one replica |

### Recovery evidence

An earlier outage run left `lastExecutionTime` unchanged after Prometheus
recovered. The final verifier used the [explicit policy requeue](#recover-cleanup-after-a-prometheus-outage).
It confirmed that execution advanced and that both eligible completed Jobs and
their Pods disappeared. Automatic cleanup recovery is not part of the pass.

### Dependent Pod cleanup

A preceding 635-second run passed its Job-level assertions, but a separate Pod
check found orphaned completed Pods. Kubernetes defaults this Job deletion path
to orphaning dependents. See the
[pinned Job strategy](https://github.com/kubernetes/kubernetes/blob/v1.34.3/pkg/registry/batch/job/strategy.go#L60).

The policy now sets `deletionPropagationPolicy: Background`. The final run
checked Pod removal by the Job UID after both initial deletion and recovery.
This also detects orphaned Pods whose owner references have been removed.

### Other validation

- At 14:02:21 UTC, the API and all six deployments were healthy. The exporter
  scrape was successful and 0.872 seconds old. Both recording rules were healthy.
  A separate check confirmed the final Job and Pod outcomes.
- Exporter image build and Helm rendering passed.
- Bash syntax, ShellCheck, Python syntax, YAML parsing and Prometheus rule checks passed.
- All three PNG diagrams were rendered and visually inspected. Their editable sources
  are retained in the ignored local artifact directory.
- Effective controller permissions were checked on the live cluster: update and
  delete are allowed in `ai-lab` and denied in `default`. Cleanup additionally
  has read-only cluster-wide Job listing.

These checks do not establish physical-GPU idleness, cAdvisor-driven actions,
multi-cluster behavior, production scale, or atomicity between a Prometheus
query and a Kubernetes write. Invalid exporter observations were not
fault-injected. Samples do not prove what happened between scrapes.

### Local evidence

Artifacts are under `.artifacts/kyverno-exporter-tutorial/`:

- `up-recovered.log`: successful setup rerun after Docker recovery.
- `verification.json` and `verify-cascading-cleanup.log`: final successful run.
- `final-health.json`: nine final health and workload checks.
- `verification-before-cascading-cleanup.json` and `verify-recovered.log`:
  earlier Job-level pass before dependent Pod assertions were added.
- `verification-query-recovery-failure.json` and
  `verify-query-recovery-failure.log`: automatic cleanup recovery failure.
- `inventory.json`: revisions and deployed images.
- `pod-cleanup-finding.json`: orphaned Pod finding and correction.
- `infra-failure.json` and `recovery.json`: shared Docker storage failure and recovery.
- `diagrams/`: editable diagram sources.

## References

- [Kyverno MutatingPolicy](https://kyverno.io/docs/policy-types/mutating-policy/).
- [Kyverno DeletingPolicy](https://kyverno.io/docs/policy-types/deleting-policy/).
- [Prometheus range and timestamp functions](https://prometheus.io/docs/prometheus/latest/querying/functions/).
- [Kubernetes Job termination and cleanup](https://kubernetes.io/docs/concepts/workloads/controllers/job/).
