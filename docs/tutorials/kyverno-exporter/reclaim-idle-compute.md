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
`dsx_*` metric contract. Its [CPU-only lab](lab.md) proves the suspension and
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
actions. Its [evidence-window checks](lab.md#5-understand-when-the-rule-waits)
are the starting point for additional signals; each source also needs its own
freshness and coverage checks. Prometheus observations and Kubernetes writes
are not one atomic transaction.

Owner notifications need an alert delivery service. Checkpointing needs
application support. Long-term action history needs retained audit records.
Those responsibilities are separate from the exporter and the two lab policies.

## Try the verified policy loop

[Run the kind lab](lab.md) to observe a Job, suspend it after the Running
window, remove a completed Job and its Pods, and exercise outage recovery.
No GPU is required for that path. The [recorded results](results.md) passed in
670 seconds. Suspension recovered automatically after fresh history; cleanup
on the pinned Kyverno release required the documented explicit requeue.

The scripts currently validate those two Job policies. The real-GPU and
cAdvisor recipes above describe the next integrations to validate.
