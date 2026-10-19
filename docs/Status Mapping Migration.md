<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Pending and Progressing migration

The built-in catalog now reports independent observations instead of grouping
all startup and update activity into Initializing. Pending means waiting for
required readiness. Progressing means an evidenced, unfinished operation and
can remain set while that operation is blocked. Neither label alone establishes
Pod scheduling. Missing evidence is not evidence of progress.

The SDK keeps legacy Initializing mappings and their output during a deprecation
window. Admission and CLI validation warn, but do not rewrite them. Install the
new CRD and update consumers before applying new definitions.

## Per-kind evidence

Each row describes predicates, not a guaranteed ordered lifecycle. Watch events
may skip intermediate observations. Pending and Progressing can overlap with
other statuses; the evaluator has no global precedence rule.

| Kind and served version | Pending evidence | Progressing evidence |
| --- | --- | --- |
| Pod v1 | Native phase Pending | Pending and PodScheduled=True |
| Deployment apps/v1 | Fresh desired availability deficit | Fresh incomplete replica reconciliation or unfinished native rollout, including a deadline-exceeded rollout |
| StatefulSet apps/v1 | Fresh ready-replica deficit | Replica reconciliation or an unfinished update consistent with its update strategy and partition |
| Job batch/v1 | Active Pods with an explicit ready-count deficit | No parent-only signal used; active includes Pending Pods |
| CronJob batch/v1 | Unsuspended, observed status, first schedule not yet recorded | No parent rollout signal used |
| JobSet v1alpha2 | Observed replicated Jobs below their ready-or-succeeded target | InOrder startup policy in progress, or RestartingJobSet=True |
| LeaderWorkerSet v1 | No ready groups during native progress | Progressing=True or UpdateInProgress=True |
| Grove PodCliqueSet v1alpha1 | Observed available-replica deficit | Controller-reported unfinished UpdateInProgress=True, or Unknown/ProgressDeadlineExceeded; not attributed to the latest desired generation |
| PyTorchJob v1 | Created=True without running, terminal, or suspended state | That startup state plus active replica components |
| MPIJob v2beta1 | Created=True without running, terminal, or suspended state | That startup state plus active replica components |
| RayCluster v1 | Not provisioned and head not ready, including a current observed resume after suspension conditions clear | Specific head container startup or backoff reasons during initial provisioning or that observed resume |
| RayJob v1 | Application PENDING, deployment Initializing, interactive Waiting, or deployment Running awaiting its first application status | Deployment Initializing while the application is not running or terminal |
| RayService v1 | Ready=False with Initializing reason | Allocated pending cluster during initialization, UpgradeInProgress=True, or RollbackInProgress=True |
| DynamoGraphDeployment v1alpha1 and v1beta1 | Native pending state | Explicit rollingUpdate phase Pending or InProgress; schema default initializing alone is Undefined |
| Knative Service v1 | Ready=Unknown | Specific revision or traffic transition evidence during unresolved readiness |
| Milvus v1beta1 | Native Pending phase | MilvusUpdated=False with an explicit image/component update reason |
| NIMService v1alpha1 | Pending, NotReady, or PVC-Created with positive desired replicas | PVC-Created provisioning milestone; generic NotReady is not progress evidence |
| NIMCache v1alpha1 | Pending, PVC-Created, or Started, excluding completion | PVC/Job creation within that unfinished startup; retained creation history after completion is ignored |
| KServe InferenceService v1beta1 | Explicit unresolved readiness, or model Loading waiting for predictor availability | modelStatus.states.targetModelState=Loading |
| LLMInferenceService v1alpha1 and v1alpha2 | Unknown readiness or known readiness-wait reasons, excluding failed/stopped presets | Ready=False with reason Progressing; gateway or autoscaler wait names alone do not prove a rollout |
| Sandbox v1beta1 | Running intent, Ready=False/DependenciesNotReady, not suspended or finished | That startup state plus current PodScheduled=True |

The 21 rows cover all 23 catalog definitions. Each definition has fresh live
recordings in the dated validation section below. Those recordings establish
observed status combinations; they do not exercise every predicate branch or
promise an ordered sequence. Constructed regression inputs cover additional
edge cases.

## Scheduling and health limits

PodScheduled on a Pod directly reports its scheduling condition. Sandbox mirrors
that condition. A False condition is distinct from a missing or Unknown one.
The remaining parent mappings require child Pod observations to answer whether
all requested Pods are scheduled. Optional version-specific parent counters are
not treated as a universal scheduling API.

Pending can coexist with Running when some replicas are ready and others are
still needed. Progressing is not a heartbeat: image pull backoff, a blocked
rollout, or a Deployment progress deadline can coexist with it. Keep reasons,
messages, and Failed/Degraded observations visible when deciding whether to act.

NIMService NotReady includes blocked deployments and does not identify the cause.
NIMCache NotReady is a reconcile error, so it does not become normal startup
progress. Existing RayCluster ready state can be sticky, NIMCache InProgress
uses active-Job evidence, and CronJob Running still describes an established
schedule including idle intervals. This change does not turn those existing
Running mappings into proof of live container execution. KServe's existing
Running rule remains specific to its serverless readiness conditions. NIMCache
Failed can appear after one failed Pod, before its Job exhausts retries.

A few existing matches change to make the new combinations truthful: training
Created history no longer adds startup to terminal or running jobs; JobSet
active alone no longer means Running; LWS fallback counts are compared with
desired groups; Deployment Running uses current availability. StatefulSet
serving capacity can coexist with a rollout. Predicates with generation guards
reject observations known to be stale. These guards are not universal: some
existing Running and Failed mappings still use native state alone, and some
guards permit an absent observed generation. Deployment, StatefulSet, and LWS at
zero desired replicas do not claim active execution; other native-ready mappings
retain their operator's readiness semantics.

Grove Progressing reports a controller-observed unfinished operation. Its root
observedGeneration advances after an update converges, so Progressing does not
require that watermark to match the desired generation. The native update
condition does not identify its generation and can describe a superseded
operation. This is not evidence that the latest desired generation is making
progress. Pending and Running retain their generation checks. OnDelete,
deletion, an explicit updateEndedAt, or a topology failure suppress Progressing.

## Source basis

The distinction follows the Kubernetes
[Pod lifecycle](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/)
and [Deployment status](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#deployment-status).
Native conditions were checked against upstream controllers, including
[LWS](https://github.com/kubernetes-sigs/lws/tree/53466a1dddaae2528dd7357546fa5adf081fef70),
[KubeRay](https://github.com/ray-project/kuberay/tree/fa76e8c19501f13a9c810b85891e81c290353812),
[KServe](https://github.com/kserve/kserve/tree/2797996f1b50bd0687ee25995ae52d9fa1343b79),
[Grove](https://github.com/ai-dynamo/grove/tree/90d6ff4fba7669682da0807d82763b220053e6f5),
and [NIM Operator](https://github.com/NVIDIA/k8s-nim-operator/tree/0aa8bb72040607bf0439793e1df98e141856cace).
The live release behavior was also checked against
[NIM Operator v3.0.2](https://github.com/NVIDIA/k8s-nim-operator/tree/1cc1750182177c4186713f0ac7c4194f836676ee),
[KubeRay v1.6.2](https://github.com/ray-project/kuberay/tree/598eb66aa077c55ae04fa87b192238a3ec184e88),
and [Grove v0.1.0-alpha.14](https://github.com/ai-dynamo/grove/tree/78231d747d1dfe877dadf0bd9c6c261293b245a1).
Newer upstream signals only match when the CR actually contains them.

## Fresh live validation (2026-10-18)

The 2026-10-18 run captured 83 successful flows and 454 STATE frames across all
23 catalog definitions. Every fresh recording passed exact-set SDK replay and
recording validation. Flow predicates read native resource fields independently
of the SDK; replay compares the entire recorded phase set, including simultaneous
labels and Undefined observations.

| Catalog kind and API version | Tested operator release(s) | Successful flows | STATE frames | Observed complete phase sets |
| --- | --- | ---: | ---: | --- |
| CronJob batch/v1 | Kubernetes v1.34.0 | 4 | 5 | `Pending`; `Running`; `Suspended` |
| Deployment apps/v1 | Kubernetes v1.34.0 | 4 | 32 | `Undefined`; `Pending`; `Running`; `Pending + Progressing`; `Running + Progressing`; `Pending + Progressing + Failed` |
| DynamoGraphDeployment nvidia.com/v1alpha1 | Dynamo 1.2.1 | 3 | 27 | `Undefined`; `Pending`; `Running`; `Pending + Progressing` |
| DynamoGraphDeployment nvidia.com/v1beta1 | Dynamo 1.2.1 | 3 | 27 | `Undefined`; `Pending`; `Running`; `Pending + Progressing` |
| InferenceService serving.kserve.io/v1beta1 | KServe v0.20.0 | 2 | 10 | `Undefined`; `Pending`; `Running`; `Failed`; `Pending + Progressing` |
| Job batch/v1 | Kubernetes v1.34.0 | 7 | 25 | `Pending`; `Running`; `Completed`; `Failed`; `Suspended`; `Pending + Running`; `Running + Degraded` |
| JobSet jobset.x-k8s.io/v1alpha2 | JobSet v0.10.1 | 5 | 23 | `Pending`; `Running`; `Completed`; `Failed`; `Suspended` |
| LLMInferenceService serving.kserve.io/v1alpha1 | KServe v0.20.0 | 4 | 13 | `Undefined`; `Pending`; `Running`; `Failed`; `Suspended`; `Pending + Progressing` |
| LLMInferenceService serving.kserve.io/v1alpha2 | KServe v0.20.0 | 4 | 12 | `Undefined`; `Pending`; `Running`; `Failed`; `Suspended`; `Pending + Progressing` |
| LeaderWorkerSet leaderworkerset.x-k8s.io/v1 | LWS v0.7.0 | 2 | 15 | `Running`; `Progressing`; `Pending + Progressing` |
| MPIJob kubeflow.org/v2beta1 | Kubeflow/MPI bundle v1.9.0+mpiv0.8.2 | 5 | 18 | `Pending`; `Running`; `Completed`; `Failed`; `Suspended`; `Pending + Progressing` |
| Milvus milvus.io/v1beta1 | Milvus Operator 1.3.7 | 2 | 12 | `Undefined`; `Pending`; `Running`; `Pending + Progressing` |
| NIMCache apps.nvidia.com/v1alpha1 | NIM Operator v3.0.2 | 2 | 16 | `Undefined`; `Running`; `Completed`; `Failed`; `Pending + Progressing` |
| NIMService apps.nvidia.com/v1alpha1 | NIM Operator v3.0.2 | 2 | 7 | `Undefined`; `Pending`; `Running`; `Pending + Progressing` |
| Pod v1 | Kubernetes v1.34.0 | 4 | 20 | `Pending`; `Running`; `Completed`; `Failed`; `Pending + Progressing` |
| PodCliqueSet grove.io/v1alpha1 | Grove v0.1.0-alpha.10-rc1 | 3 | 23 | `Undefined`; `Pending`; `Running` |
| PodCliqueSet grove.io/v1alpha1 | Grove v0.1.0-alpha.14 | 4 | 36 | `Undefined`; `Pending`; `Running`; `Progressing` |
| PyTorchJob kubeflow.org/v1 | Kubeflow/MPI bundle v1.9.0+mpiv0.8.2 | 5 | 13 | `Pending`; `Running`; `Completed`; `Failed`; `Suspended` |
| RayCluster ray.io/v1 | KubeRay 1.6.2 | 3 | 13 | `Undefined`; `Pending`; `Running`; `Suspended`; `Pending + Progressing` |
| RayJob ray.io/v1 | KubeRay 1.6.2 | 5 | 41 | `Undefined`; `Pending`; `Running`; `Completed`; `Failed`; `Suspended`; `Pending + Progressing` |
| RayService ray.io/v1 | KubeRay 1.6.2 | 2 | 14 | `Undefined`; `Pending`; `Running`; `Pending + Progressing`; `Running + Progressing` |
| Sandbox agents.x-k8s.io/v1beta1 | Agent Sandbox v1.0.5 | 4 | 22 | `Pending`; `Running`; `Suspending`; `Suspended`; `Resuming`; `Pending + Progressing` |
| Service serving.knative.dev/v1 | Knative knative-v1.15.2 | 2 | 10 | `Pending`; `Running`; `Failed`; `Pending + Progressing` |
| StatefulSet apps/v1 | Kubernetes v1.34.0 | 2 | 20 | `Undefined`; `Pending`; `Running`; `Pending + Progressing`; `Pending + Degraded`; `Running + Progressing` |

Phase sets are observations, not guaranteed lifecycle sequences. Display order
does not imply precedence. Historical inputs and action events are excluded.

Representative scenarios include:

- [Paused Deployment scaling](../test/e2e/recorded_data/deployment/v1.34.0/apps-deployment-v1/paused-scaled.yaml)
  captures Pending + Progressing while a settled, paused Deployment scales from
  one to three replicas, then Running. It does not pause an active template rollout.
- [RayService replacement](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-rayservice-v1/upgraded.yaml)
  captures Running + Progressing while the active cluster remains ready.
- [Grove alpha.14 replacement](../test/e2e/recorded_data/grove/v0.1.0-alpha.14/grove-io-podcliqueset-v1alpha1/updated.yaml)
  captures UpdateInProgress=True and Progressing at generation 2 with
  observedGeneration 1. The flow then requires new ready Pod UIDs with the updated
  annotation and converged root status before completing.
- NIMCache [completion](../test/e2e/recorded_data/nim/v3.0.2/apps-nvidia-com-nimcache-v1alpha1/completed.yaml)
  and [failure](../test/e2e/recorded_data/nim/v3.0.2/apps-nvidia-com-nimcache-v1alpha1/failed.yaml)
  use controller-created Jobs and PVCs with the CPU cache command.
- Dynamo updates cover [v1alpha1](../test/e2e/recorded_data/dynamo/1.2.1/nvidia-com-dynamographdeployment-v1alpha1/updated.yaml)
  and [v1beta1](../test/e2e/recorded_data/dynamo/1.2.1/nvidia-com-dynamographdeployment-v1beta1/updated.yaml).
  LLMInferenceService startup, failure, missing-preset, and stopped flows cover
  [v1alpha1](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha1)
  and [v1alpha2](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2).

These recordings exercise real controller and Kubernetes object lifecycles
using small CPU applications and stand-ins. Dynamo uses its mocker.
LLMInferenceService uses a small HTTP server. NIM uses local readiness endpoints
and a cache command that writes a marker into a real PVC. The NIM fixture does
not download models. This run does not validate real model inference or GPU
execution. Native active-Job counts still do not establish container execution.

The NIM installer overrides the chart's mutable development-image default with
`nvcr.io/nvidia/cloud-native/k8s-nim-operator:v3.0.2@sha256:28a83f7f3f0322ac721b81385b0a7e1639286226564035809e07802f424fb7b8`.
The running image was verified against that pin before all four NIMService and
NIMCache flows were recorded. In this release, the universal NIMCache source
skips manifest inspection and runs create-model-store. No CR status is patched
to manufacture the cache lifecycle.

Grove alpha.14 proves the native unfinished-update branch while its generation
watermark lags. The condition still does not identify which generation it
reports and can describe a superseded operation. The default e2e pin,
v0.1.0-alpha.10-rc1, does not emit UpdateInProgress; its separate startup and
scaling recordings do not prove that branch. Its older updateProgress timestamps
are not substituted for the condition.

Coverage of every definition does not mean every branch was observed live.
For example, these JobSet recordings contain no Progressing observation.
Constructed tests and source review cover additional cases. The table describes
this dated run; later fixture edits do not automatically preserve its coverage.

## Ray startup and resume corrections (2026-10-19)

Two mapping gaps can be resolved from native fields already present on the root
resource:

- RayJob deployment Running can precede its first application jobStatus. A
  missing or empty application status now maps to Pending, meaning waiting for
  that status report. It does not establish whether execution has started.
  Deployment Running alone does not add Running or Progressing.
- RayCluster retains state=suspended while resumed Pods start. Explicit resume
  intent, equal known desired and observed generations, both suspension
  conditions False, RayClusterProvisioned=False, and HeadPodReady=False now
  allow Pending. Existing container startup or backoff reasons also allow
  Progressing. Stale observations and uncleared suspension conditions do not.

These decisions follow the KubeRay v1.6.2
[RayJob controller](https://github.com/ray-project/kuberay/blob/598eb66aa077c55ae04fa87b192238a3ec184e88/ray-operator/controllers/ray/rayjob_controller.go#L214-L287)
and [RayCluster controller](https://github.com/ray-project/kuberay/blob/598eb66aa077c55ae04fa87b192238a3ec184e88/ray-operator/controllers/ray/raycluster_controller.go#L1452-L1588).

### Comparison on identical observations

Replaying all 454 native STATE objects from
[the previous recording commit](https://github.com/AviadHayumi/workload-map/tree/86b916edae80e299cb0554910839613a2f813718/test/e2e/recorded_data)
with both definitions changes eight frames in five recordings. The other 446
frames are unchanged. Undefined frames decrease from 84 to 76. Recordings with
reappearing Undefined gaps decrease from 12 to 9. The strict whole-recording
categories remain 37 without gaps, 40 with Undefined but no return-to-waiting
transition, and six with return-to-waiting transitions. The improved flows
retain initial or transition gaps. This isolates
the mapping effect from changes in watch timing between live runs.

P means Pending, G means Progressing, U means Undefined, and S means Suspended.
Consecutive identical phase sets are collapsed.

| Flow | Previous definition | Corrected definition on the same objects |
| --- | --- | --- |
| RayCluster resumed | `S -> U -> Running` | `S -> U -> P -> P + G -> Running` |
| RayJob running | `U -> P + G -> U -> P -> Running` | `U -> P + G -> P -> Running` |
| RayJob completed | `U -> P + G -> U -> P -> Completed` | `U -> P + G -> P -> Completed` |
| RayJob failed | `U -> P + G -> U -> P -> Failed` | `U -> P + G -> P -> Failed` |
| RayJob resumed | `U -> S -> U -> P + G -> U -> P -> Running` | `U -> S -> U -> P + G -> P -> Running` |

### Fresh follow-up recordings

All eight RayCluster and RayJob flows passed on 2026-10-19 using a new two-node
kind cluster with Kubernetes v1.34.0, KubeRay 1.6.2, and Ray 2.40.0 on arm64.
The generated Karta definitions were installed and reconciled Ready before
recording. The existing Karta operator image was reused; its expression
evaluator receives these changes through the definitions. Recordings come from
real controller watch events with independent native predicates; saved phases
and summaries were not rewritten.

The eight new recordings contain 55 STATE frames. The remaining 75 recordings
are unchanged from the previous commit. The active corpus now contains
83 successful recordings and 455 STATE frames across all 23 definitions.

| Fresh flow | Recorded trajectory | STATE frames |
| --- | --- | ---: |
| [RayCluster resumed](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-raycluster-v1/resumed.yaml) | `Suspended -> Undefined -> Pending -> Pending + Progressing -> Running` | 8 |
| [RayCluster running](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-raycluster-v1/running.yaml) | `Pending -> Pending + Progressing -> Running` | 4 |
| [RayCluster suspended](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-raycluster-v1/suspended.yaml) | `Suspended` | 1 |
| [RayJob completed](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-rayjob-v1/completed.yaml) | `Undefined -> Pending + Progressing -> Pending -> Completed` | 8 |
| [RayJob failed](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-rayjob-v1/failed.yaml) | `Undefined -> Pending + Progressing -> Pending -> Failed` | 8 |
| [RayJob resumed](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-rayjob-v1/resumed.yaml) | `Undefined -> Suspended -> Undefined -> Pending + Progressing -> Pending -> Running` | 13 |
| [RayJob running](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-rayjob-v1/running.yaml) | `Undefined -> Pending + Progressing -> Pending -> Running` | 9 |
| [RayJob suspended](../test/e2e/recorded_data/kuberay/1.6.2/ray-io-rayjob-v1/suspended.yaml) | `Undefined -> Suspended` | 4 |

Fast completion and failure still skip an observed Running application status
in these RayJob recordings. The definitions do not invent that missing watch
observation. The full corpus passes exact-set SDK replay and recording
validation.

### Remaining limits

Missing initial status, RayJob's empty status during resume, and RayCluster's
stale or still-suspended observations remain Undefined. An arbitrary fallback
to Pending would conceal the lack of evidence. These changes also do not add
parent-level scheduling proof or guarantee that a watch captures every phase.

Six previously recorded return-to-waiting trajectories remain unchanged:

- Job: completed and resumed.
- JobSet: completed and failed.
- NIMCache: completed and failed.

Job and JobSet can expose the same spec and native status before startup and
after Running while waiting for terminal status. A stateless root mapping cannot
assign different labels to identical input. NIMCache can return to native
Pending when its child Job has no active Pods but is not yet marked terminal.
A faithful distinction requires child Job/Pod evidence, richer upstream status,
or an explicitly historical presentation. Keeping Running or predicting a
terminal state would overstate the available evidence.

## Recorded before and after

The historical table below evaluates the same 327 saved STATE objects in 68
flows using the previous catalog and the initial implementation of this change. Both columns are SDK output;
they are not the recorder's independently assigned state labels. Consecutive
identical sets are collapsed. Undefined frames remain visible, including stale
observations captured between a spec change and the controller's next update.

The table links to immutable historical inputs. The active replay suite uses
83 fresh recordings and compares each complete recorded phase set directly
with the SDK, including simultaneous statuses. Superseded historical recordings
are available through these links and Git history. The historical comparison
below is not fresh live evidence.

Current waiting-flow files use `pending.yaml`. Historical links and captured
recording metadata retain their original names.

| Saved flow | Before | After |
| --- | --- | --- |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/resumed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/resumed.yaml) | `Initializing -> Running -> Suspending -> Suspended -> Resuming -> Initializing -> Running` | `Pending -> Pending + Progressing -> Running -> Suspending -> Suspended -> Resuming -> Pending -> Pending + Progressing -> Running` |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/running.yaml) | `Initializing -> Running` | `Pending -> Pending + Progressing -> Running` |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended-from-running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended-from-running.yaml) | `Initializing -> Running -> Suspending -> Suspended` | `Pending -> Pending + Progressing -> Running -> Suspending -> Suspended` |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended.yaml) | `Suspended` | `Suspended` |
| [batch-job/v1.34.0/batch-job-v1/completed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/completed.yaml) | `Initializing -> Running -> Initializing -> Completed` | `Pending -> Running -> Pending -> Completed` |
| [batch-job/v1.34.0/batch-job-v1/degraded.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/degraded.yaml) | `Initializing -> Running -> Running + Degraded` | `Pending -> Running + Pending -> Running + Degraded` |
| [batch-job/v1.34.0/batch-job-v1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/failed.yaml) | `Initializing -> Failed` | `Pending -> Failed` |
| [batch-job/v1.34.0/batch-job-v1/resumed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/resumed.yaml) | `Suspended -> Initializing -> Running -> Initializing -> Completed` | `Suspended -> Pending -> Running -> Pending -> Completed` |
| [batch-job/v1.34.0/batch-job-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/running.yaml) | `Initializing -> Running` | `Pending -> Running` |
| [batch-job/v1.34.0/batch-job-v1/scaled.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/scaled.yaml) | `Initializing -> Running` | `Pending -> Running -> Running + Pending -> Running` |
| [batch-job/v1.34.0/batch-job-v1/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/suspended.yaml) | `Suspended` | `Suspended` |
| [cronjob/v1.34.0/batch-cronjob-v1/born-suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/born-suspended.yaml) | `Suspended` | `Suspended` |
| [cronjob/v1.34.0/batch-cronjob-v1/initializing.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/initializing.yaml) | `Initializing` | `Pending` |
| [cronjob/v1.34.0/batch-cronjob-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/running.yaml) | `Running` | `Running` |
| [cronjob/v1.34.0/batch-cronjob-v1/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/suspended.yaml) | `Running -> Suspended` | `Running -> Suspended` |
| [deployment/v1.34.0/apps-deployment-v1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/deployment/v1.34.0/apps-deployment-v1/failed.yaml) | `Initializing -> Failed` | `Undefined -> Pending + Progressing -> Failed + Pending + Progressing` |
| [deployment/v1.34.0/apps-deployment-v1/initializing.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/deployment/v1.34.0/apps-deployment-v1/initializing.yaml) | `Initializing` | `Undefined -> Pending + Progressing` |
| [deployment/v1.34.0/apps-deployment-v1/scaled.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/deployment/v1.34.0/apps-deployment-v1/scaled.yaml) | `Initializing -> Running -> Running + Initializing -> Running` | `Undefined -> Pending + Progressing -> Running -> Undefined -> Pending + Progressing -> Pending -> Running -> Undefined -> Running + Progressing -> Running` |
| [dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/initializing.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/initializing.yaml) | `Undefined -> Initializing` | `Undefined -> Pending` |
| [dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending -> Running` |
| [grove/v1.34.0/grove-io-podcliqueset-v1alpha1/initializing.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/grove/v1.34.0/grove-io-podcliqueset-v1alpha1/initializing.yaml) | `Initializing` | `Undefined` |
| [grove/v1.34.0/grove-io-podcliqueset-v1alpha1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/grove/v1.34.0/grove-io-podcliqueset-v1alpha1/running.yaml) | `Initializing -> Running` | `Undefined -> Pending -> Running` |
| [grove/v1.34.0/grove-io-podcliqueset-v1alpha1/scaled.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/grove/v1.34.0/grove-io-podcliqueset-v1alpha1/scaled.yaml) | `Initializing -> Running -> Initializing -> Running` | `Undefined -> Pending -> Running -> Undefined -> Pending -> Running -> Undefined -> Running` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/completed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/completed.yaml) | `Undefined -> Running + Initializing -> Running -> Running + Initializing -> Running -> Completed` | `Pending -> Running -> Pending -> Running -> Completed` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/failed.yaml) | `Undefined -> Running + Initializing -> Running -> Running + Initializing -> Undefined -> Failed` | `Pending -> Running -> Pending -> Failed` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/resumed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/resumed.yaml) | `Suspended -> Undefined -> Running + Initializing` | `Suspended -> Pending` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/running.yaml) | `Undefined -> Running + Initializing` | `Pending` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/suspended.yaml) | `Suspended` | `Suspended` |
| [knative/v1.34.0/serving-knative-dev-service-v1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/knative/v1.34.0/serving-knative-dev-service-v1/failed.yaml) | `Initializing -> Failed` | `Pending + Progressing -> Failed` |
| [knative/v1.34.0/serving-knative-dev-service-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/knative/v1.34.0/serving-knative-dev-service-v1/running.yaml) | `Initializing -> Running` | `Pending + Progressing -> Pending -> Running` |
| [kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml) | `Undefined -> Failed` | `Undefined -> Pending -> Failed` |
| [kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml) | `Undefined -> Running` | `Undefined -> Pending -> Pending + Progressing -> Pending -> Running` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/failed.yaml) | `Undefined -> Failed` | `Undefined -> Failed` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/missing-preset.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/missing-preset.yaml) | `Undefined -> Failed` | `Undefined -> Failed` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending + Progressing -> Pending -> Running` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/stopped.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/stopped.yaml) | `Undefined -> Initializing -> Running -> Suspended` | `Undefined -> Pending + Progressing -> Pending -> Running -> Suspended` |
| [kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml) | `Undefined -> Failed` | `Undefined -> Pending -> Failed` |
| [kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml) | `Undefined -> Running` | `Undefined -> Pending -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/completed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/completed.yaml) | `Initializing -> Completed + Initializing` | `Pending -> Pending + Progressing -> Completed` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/failed.yaml) | `Initializing -> Running + Initializing -> Failed + Initializing` | `Pending -> Pending + Progressing -> Running -> Failed` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/resumed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/resumed.yaml) | `Suspended + Initializing -> Initializing -> Running + Initializing` | `Suspended -> Pending -> Pending + Progressing -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/running.yaml) | `Initializing -> Running + Initializing` | `Pending -> Pending + Progressing -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/suspended.yaml) | `Suspended + Initializing` | `Suspended` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/completed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/completed.yaml) | `Initializing -> Running + Initializing -> Completed + Initializing` | `Pending -> Running -> Completed` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/failed.yaml) | `Initializing -> Running + Initializing -> Failed + Initializing` | `Pending -> Running -> Failed` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/resumed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/resumed.yaml) | `Suspended + Initializing -> Initializing -> Running + Initializing` | `Suspended -> Pending -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/running.yaml) | `Initializing -> Running + Initializing` | `Pending -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/suspended.yaml) | `Suspended + Initializing` | `Suspended` |
| [kuberay/v1.34.0/ray-io-raycluster-v1/resumed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-raycluster-v1/resumed.yaml) | `Suspended -> Undefined -> Running` | `Suspended -> Undefined -> Running` |
| [kuberay/v1.34.0/ray-io-raycluster-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-raycluster-v1/running.yaml) | `Undefined -> Running` | `Pending -> Pending + Progressing -> Running` |
| [kuberay/v1.34.0/ray-io-raycluster-v1/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-raycluster-v1/suspended.yaml) | `Suspended` | `Suspended` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/completed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/completed.yaml) | `Undefined -> Initializing -> Undefined -> Completed` | `Undefined -> Pending + Progressing -> Undefined -> Completed` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/failed.yaml) | `Undefined -> Initializing -> Undefined -> Initializing -> Failed` | `Undefined -> Pending + Progressing -> Undefined -> Pending -> Failed` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/resumed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/resumed.yaml) | `Undefined -> Initializing -> Undefined -> Suspended -> Undefined -> Initializing -> Undefined -> Running` | `Undefined -> Suspended -> Undefined -> Pending + Progressing -> Undefined -> Running` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/running.yaml) | `Undefined -> Initializing -> Undefined -> Running` | `Undefined -> Pending + Progressing -> Undefined -> Running` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/suspended.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/suspended.yaml) | `Undefined -> Initializing -> Undefined -> Suspended` | `Undefined -> Suspended` |
| [lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/running.yaml) | `Initializing -> Running` | `Pending + Progressing -> Running` |
| [lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/scaled.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/scaled.yaml) | `Initializing -> Running -> Running + Initializing -> Initializing -> Running` | `Pending + Progressing -> Running -> Progressing -> Running` |
| [milvus/v1.34.0/milvus-io-milvus-v1beta1/initializing.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/milvus/v1.34.0/milvus-io-milvus-v1beta1/initializing.yaml) | `Undefined -> Initializing` | `Undefined -> Pending` |
| [milvus/v1.34.0/milvus-io-milvus-v1beta1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/milvus/v1.34.0/milvus-io-milvus-v1beta1/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending -> Pending + Progressing -> Running` |
| [nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/initializing.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/initializing.yaml) | `Undefined -> Initializing` | `Undefined -> Pending + Progressing -> Pending` |
| [nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending + Progressing -> Pending -> Running` |
| [pod/v1.34.0/core-pod-v1/completed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/completed.yaml) | `Initializing -> Running -> Completed` | `Pending + Progressing -> Running -> Completed` |
| [pod/v1.34.0/core-pod-v1/failed.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/failed.yaml) | `Initializing -> Running -> Failed` | `Pending + Progressing -> Running -> Failed` |
| [pod/v1.34.0/core-pod-v1/initializing.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/initializing.yaml) | `Initializing` | `Pending` |
| [pod/v1.34.0/core-pod-v1/running.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/running.yaml) | `Initializing -> Running` | `Pending + Progressing -> Running` |
| [statefulset/v1.34.0/apps-statefulset-v1/degraded.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/statefulset/v1.34.0/apps-statefulset-v1/degraded.yaml) | `Initializing -> Degraded` | `Pending + Progressing -> Pending -> Pending + Degraded` |
| [statefulset/v1.34.0/apps-statefulset-v1/scaled.yaml](https://github.com/dsx-ai-factory/workload-map/blob/749feed8475901c23f71ee935a2fbf580cfd0eca/test/e2e/recorded_data/statefulset/v1.34.0/apps-statefulset-v1/scaled.yaml) | `Initializing -> Running -> Initializing -> Degraded -> Running -> Initializing -> Running` | `Pending + Progressing -> Pending -> Running -> Undefined -> Pending + Progressing -> Pending + Degraded -> Running -> Undefined -> Running + Progressing -> Running` |
