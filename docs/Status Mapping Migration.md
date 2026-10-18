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
| Grove PodCliqueSet v1alpha1 | Observed available-replica deficit | UpdateInProgress=True, or Unknown with ProgressDeadlineExceeded for a stalled update |
| PyTorchJob v1 | Created=True without running, terminal, or suspended state | That startup state plus active replica components |
| MPIJob v2beta1 | Created=True without running, terminal, or suspended state | That startup state plus active replica components |
| RayCluster v1 | Not provisioned and head not ready | Specific head container startup or backoff reasons during initial provisioning |
| RayJob v1 | Application PENDING, deployment Initializing, or interactive Waiting | Deployment Initializing while the application is not running or terminal |
| RayService v1 | Ready=False with Initializing reason | Allocated pending cluster during initialization, or UpgradeInProgress=True |
| DynamoGraphDeployment v1alpha1 and v1beta1 | Native pending state | Explicit rollingUpdate phase Pending or InProgress; schema default initializing alone is Undefined |
| Knative Service v1 | Ready=Unknown | Specific revision or traffic transition evidence during unresolved readiness |
| Milvus v1beta1 | Native Pending phase | MilvusUpdated=False with an explicit image/component update reason |
| NIMService v1alpha1 | Pending, NotReady, or PVC-Created with positive desired replicas | PVC-Created provisioning milestone; generic NotReady is not progress evidence |
| NIMCache v1alpha1 | Pending, PVC-Created, or Started, excluding completion | PVC/Job creation within that unfinished startup; retained creation history after completion is ignored |
| KServe InferenceService v1beta1 | Explicit unresolved readiness, or model Loading waiting for predictor availability | modelStatus.states.targetModelState=Loading |
| LLMInferenceService v1alpha1 and v1alpha2 | Unknown readiness or known readiness-wait reasons, excluding failed/stopped presets | Ready=False with reason Progressing; gateway or autoscaler wait names alone do not prove a rollout |
| Sandbox v1beta1 | Running intent, Ready=False/DependenciesNotReady, not suspended or finished | That startup state plus current PodScheduled=True |

The 21 rows cover all 23 catalog definitions. RayService, NIMCache, Dynamo
v1beta1, and LLMInferenceService v1alpha1 have constructed regression inputs but
no saved live flows. These examples establish matcher behavior, not an observed
sequence for those operators.

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
Running rule remains specific to its serverless readiness conditions.

A few existing matches change to make the new combinations truthful: training
Created history no longer adds startup to terminal or running jobs; JobSet
active alone no longer means Running; LWS fallback counts are compared with
desired groups; Deployment Running uses current availability. StatefulSet
serving capacity can coexist with a rollout. Fresh generation checks reject
known stale observations. Deployment, StatefulSet, and LWS at zero desired
replicas do not claim active execution; other native-ready mappings retain their
operator's readiness semantics.

## Source basis

The distinction follows the Kubernetes
[Pod lifecycle](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/)
and [Deployment status](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#deployment-status).
Native conditions were checked against upstream controllers, including
[LWS](https://github.com/kubernetes-sigs/lws/tree/53466a1dddaae2528dd7357546fa5adf081fef70),
[KubeRay](https://github.com/ray-project/kuberay/tree/fa76e8c19501f13a9c810b85891e81c290353812),
[KServe](https://github.com/kserve/kserve/tree/2797996f1b50bd0687ee25995ae52d9fa1343b79),
and [NIM Operator](https://github.com/NVIDIA/k8s-nim-operator/tree/0aa8bb72040607bf0439793e1df98e141856cace).
Newer upstream signals only match when the CR actually contains them.

## Recorded before and after

The table below evaluates the same 327 saved STATE objects in 68 flows using
the previous catalog and this implementation. Both columns are SDK output;
they are not the recorder's independently assigned state labels. Consecutive
identical sets are collapsed. Undefined frames remain visible, including stale
observations captured between a spec change and the controller's next update.

The original recordings are unchanged. Exact new expectations are stored in
[test/e2e/replay_tests/testdata/status_migration.json](../test/e2e/replay_tests/testdata/status_migration.json),
with a SHA-256 for each original recording. The replay test rejects changed
inputs and compares the complete status set for every saved frame. A re-recorded
flow must remove its historical expectation entry and use its new native states.
These reviewed offline expectations are not fresh live recordings.

Live re-recording of all affected kinds remains required before merge. The local
attempt built the operator image, but was stopped because Docker memory and host
disk were nearly full. Its isolated cluster was removed without changing existing
clusters. Do not treat the offline replay as completion of the live validation
requirement in CONTRIBUTING.md.

| Saved flow | Before | After |
| --- | --- | --- |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/resumed.yaml](../test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/resumed.yaml) | `Initializing -> Running -> Suspending -> Suspended -> Resuming -> Initializing -> Running` | `Pending -> Pending + Progressing -> Running -> Suspending -> Suspended -> Resuming -> Pending -> Pending + Progressing -> Running` |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/running.yaml](../test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/running.yaml) | `Initializing -> Running` | `Pending -> Pending + Progressing -> Running` |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended-from-running.yaml](../test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended-from-running.yaml) | `Initializing -> Running -> Suspending -> Suspended` | `Pending -> Pending + Progressing -> Running -> Suspending -> Suspended` |
| [agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended.yaml](../test/e2e/recorded_data/agent-sandbox/v1.0.5/agents-x-k8s-io-sandbox-v1beta1/suspended.yaml) | `Suspended` | `Suspended` |
| [batch-job/v1.34.0/batch-job-v1/completed.yaml](../test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/completed.yaml) | `Initializing -> Running -> Initializing -> Completed` | `Pending -> Running -> Pending -> Completed` |
| [batch-job/v1.34.0/batch-job-v1/degraded.yaml](../test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/degraded.yaml) | `Initializing -> Running -> Running + Degraded` | `Pending -> Running + Pending -> Running + Degraded` |
| [batch-job/v1.34.0/batch-job-v1/failed.yaml](../test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/failed.yaml) | `Initializing -> Failed` | `Pending -> Failed` |
| [batch-job/v1.34.0/batch-job-v1/resumed.yaml](../test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/resumed.yaml) | `Suspended -> Initializing -> Running -> Initializing -> Completed` | `Suspended -> Pending -> Running -> Pending -> Completed` |
| [batch-job/v1.34.0/batch-job-v1/running.yaml](../test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/running.yaml) | `Initializing -> Running` | `Pending -> Running` |
| [batch-job/v1.34.0/batch-job-v1/scaled.yaml](../test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/scaled.yaml) | `Initializing -> Running` | `Pending -> Running -> Running + Pending -> Running` |
| [batch-job/v1.34.0/batch-job-v1/suspended.yaml](../test/e2e/recorded_data/batch-job/v1.34.0/batch-job-v1/suspended.yaml) | `Suspended` | `Suspended` |
| [cronjob/v1.34.0/batch-cronjob-v1/born-suspended.yaml](../test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/born-suspended.yaml) | `Suspended` | `Suspended` |
| [cronjob/v1.34.0/batch-cronjob-v1/initializing.yaml](../test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/initializing.yaml) | `Initializing` | `Pending` |
| [cronjob/v1.34.0/batch-cronjob-v1/running.yaml](../test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/running.yaml) | `Running` | `Running` |
| [cronjob/v1.34.0/batch-cronjob-v1/suspended.yaml](../test/e2e/recorded_data/cronjob/v1.34.0/batch-cronjob-v1/suspended.yaml) | `Running -> Suspended` | `Running -> Suspended` |
| [deployment/v1.34.0/apps-deployment-v1/failed.yaml](../test/e2e/recorded_data/deployment/v1.34.0/apps-deployment-v1/failed.yaml) | `Initializing -> Failed` | `Undefined -> Pending + Progressing -> Failed + Pending + Progressing` |
| [deployment/v1.34.0/apps-deployment-v1/initializing.yaml](../test/e2e/recorded_data/deployment/v1.34.0/apps-deployment-v1/initializing.yaml) | `Initializing` | `Undefined -> Pending + Progressing` |
| [deployment/v1.34.0/apps-deployment-v1/scaled.yaml](../test/e2e/recorded_data/deployment/v1.34.0/apps-deployment-v1/scaled.yaml) | `Initializing -> Running -> Running + Initializing -> Running` | `Undefined -> Pending + Progressing -> Running -> Undefined -> Pending + Progressing -> Pending -> Running -> Undefined -> Running + Progressing -> Running` |
| [dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/initializing.yaml](../test/e2e/recorded_data/dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/initializing.yaml) | `Undefined -> Initializing` | `Undefined -> Pending` |
| [dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/running.yaml](../test/e2e/recorded_data/dynamo/v1.34.0/nvidia-com-dynamographdeployment-v1alpha1/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending -> Running` |
| [grove/v1.34.0/grove-io-podcliqueset-v1alpha1/initializing.yaml](../test/e2e/recorded_data/grove/v1.34.0/grove-io-podcliqueset-v1alpha1/initializing.yaml) | `Initializing` | `Undefined` |
| [grove/v1.34.0/grove-io-podcliqueset-v1alpha1/running.yaml](../test/e2e/recorded_data/grove/v1.34.0/grove-io-podcliqueset-v1alpha1/running.yaml) | `Initializing -> Running` | `Undefined -> Pending -> Running` |
| [grove/v1.34.0/grove-io-podcliqueset-v1alpha1/scaled.yaml](../test/e2e/recorded_data/grove/v1.34.0/grove-io-podcliqueset-v1alpha1/scaled.yaml) | `Initializing -> Running -> Initializing -> Running` | `Undefined -> Pending -> Running -> Undefined -> Pending -> Running -> Undefined -> Running` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/completed.yaml](../test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/completed.yaml) | `Undefined -> Running + Initializing -> Running -> Running + Initializing -> Running -> Completed` | `Pending -> Running -> Pending -> Running -> Completed` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/failed.yaml](../test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/failed.yaml) | `Undefined -> Running + Initializing -> Running -> Running + Initializing -> Undefined -> Failed` | `Pending -> Running -> Pending -> Failed` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/resumed.yaml](../test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/resumed.yaml) | `Suspended -> Undefined -> Running + Initializing` | `Suspended -> Pending` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/running.yaml](../test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/running.yaml) | `Undefined -> Running + Initializing` | `Pending` |
| [jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/suspended.yaml](../test/e2e/recorded_data/jobset/v1.34.0/jobset-x-k8s-io-jobset-v1alpha2/suspended.yaml) | `Suspended` | `Suspended` |
| [knative/v1.34.0/serving-knative-dev-service-v1/failed.yaml](../test/e2e/recorded_data/knative/v1.34.0/serving-knative-dev-service-v1/failed.yaml) | `Initializing -> Failed` | `Pending + Progressing -> Failed` |
| [knative/v1.34.0/serving-knative-dev-service-v1/running.yaml](../test/e2e/recorded_data/knative/v1.34.0/serving-knative-dev-service-v1/running.yaml) | `Initializing -> Running` | `Pending + Progressing -> Pending -> Running` |
| [kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml) | `Undefined -> Failed` | `Undefined -> Pending -> Failed` |
| [kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml) | `Undefined -> Running` | `Undefined -> Pending -> Pending + Progressing -> Pending -> Running` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/failed.yaml](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/failed.yaml) | `Undefined -> Failed` | `Undefined -> Failed` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/missing-preset.yaml](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/missing-preset.yaml) | `Undefined -> Failed` | `Undefined -> Failed` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/running.yaml](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending + Progressing -> Pending -> Running` |
| [kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/stopped.yaml](../test/e2e/recorded_data/kserve/v0.20.0/serving-kserve-io-llminferenceservice-v1alpha2/stopped.yaml) | `Undefined -> Initializing -> Running -> Suspended` | `Undefined -> Pending + Progressing -> Pending -> Running -> Suspended` |
| [kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml](../test/e2e/recorded_data/kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/failed.yaml) | `Undefined -> Failed` | `Undefined -> Pending -> Failed` |
| [kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml](../test/e2e/recorded_data/kserve/v1.34.0/serving-kserve-io-inferenceservice-v1beta1/running.yaml) | `Undefined -> Running` | `Undefined -> Pending -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/completed.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/completed.yaml) | `Initializing -> Completed + Initializing` | `Pending -> Pending + Progressing -> Completed` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/failed.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/failed.yaml) | `Initializing -> Running + Initializing -> Failed + Initializing` | `Pending -> Pending + Progressing -> Running -> Failed` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/resumed.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/resumed.yaml) | `Suspended + Initializing -> Initializing -> Running + Initializing` | `Suspended -> Pending -> Pending + Progressing -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/running.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/running.yaml) | `Initializing -> Running + Initializing` | `Pending -> Pending + Progressing -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/suspended.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-mpijob-v2beta1/suspended.yaml) | `Suspended + Initializing` | `Suspended` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/completed.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/completed.yaml) | `Initializing -> Running + Initializing -> Completed + Initializing` | `Pending -> Running -> Completed` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/failed.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/failed.yaml) | `Initializing -> Running + Initializing -> Failed + Initializing` | `Pending -> Running -> Failed` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/resumed.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/resumed.yaml) | `Suspended + Initializing -> Initializing -> Running + Initializing` | `Suspended -> Pending -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/running.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/running.yaml) | `Initializing -> Running + Initializing` | `Pending -> Running` |
| [kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/suspended.yaml](../test/e2e/recorded_data/kubeflow/v1.34.0/kubeflow-org-pytorchjob-v1/suspended.yaml) | `Suspended + Initializing` | `Suspended` |
| [kuberay/v1.34.0/ray-io-raycluster-v1/resumed.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-raycluster-v1/resumed.yaml) | `Suspended -> Undefined -> Running` | `Suspended -> Undefined -> Running` |
| [kuberay/v1.34.0/ray-io-raycluster-v1/running.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-raycluster-v1/running.yaml) | `Undefined -> Running` | `Pending -> Pending + Progressing -> Running` |
| [kuberay/v1.34.0/ray-io-raycluster-v1/suspended.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-raycluster-v1/suspended.yaml) | `Suspended` | `Suspended` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/completed.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/completed.yaml) | `Undefined -> Initializing -> Undefined -> Completed` | `Undefined -> Pending + Progressing -> Undefined -> Completed` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/failed.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/failed.yaml) | `Undefined -> Initializing -> Undefined -> Initializing -> Failed` | `Undefined -> Pending + Progressing -> Undefined -> Pending -> Failed` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/resumed.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/resumed.yaml) | `Undefined -> Initializing -> Undefined -> Suspended -> Undefined -> Initializing -> Undefined -> Running` | `Undefined -> Suspended -> Undefined -> Pending + Progressing -> Undefined -> Running` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/running.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/running.yaml) | `Undefined -> Initializing -> Undefined -> Running` | `Undefined -> Pending + Progressing -> Undefined -> Running` |
| [kuberay/v1.34.0/ray-io-rayjob-v1/suspended.yaml](../test/e2e/recorded_data/kuberay/v1.34.0/ray-io-rayjob-v1/suspended.yaml) | `Undefined -> Initializing -> Undefined -> Suspended` | `Undefined -> Suspended` |
| [lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/running.yaml](../test/e2e/recorded_data/lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/running.yaml) | `Initializing -> Running` | `Pending + Progressing -> Running` |
| [lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/scaled.yaml](../test/e2e/recorded_data/lws/v1.34.0/leaderworkerset-x-k8s-io-leaderworkerset-v1/scaled.yaml) | `Initializing -> Running -> Running + Initializing -> Initializing -> Running` | `Pending + Progressing -> Running -> Progressing -> Running` |
| [milvus/v1.34.0/milvus-io-milvus-v1beta1/initializing.yaml](../test/e2e/recorded_data/milvus/v1.34.0/milvus-io-milvus-v1beta1/initializing.yaml) | `Undefined -> Initializing` | `Undefined -> Pending` |
| [milvus/v1.34.0/milvus-io-milvus-v1beta1/running.yaml](../test/e2e/recorded_data/milvus/v1.34.0/milvus-io-milvus-v1beta1/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending -> Pending + Progressing -> Running` |
| [nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/initializing.yaml](../test/e2e/recorded_data/nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/initializing.yaml) | `Undefined -> Initializing` | `Undefined -> Pending + Progressing -> Pending` |
| [nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/running.yaml](../test/e2e/recorded_data/nim/v1.34.0/apps-nvidia-com-nimservice-v1alpha1/running.yaml) | `Undefined -> Initializing -> Running` | `Undefined -> Pending + Progressing -> Pending -> Running` |
| [pod/v1.34.0/core-pod-v1/completed.yaml](../test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/completed.yaml) | `Initializing -> Running -> Completed` | `Pending + Progressing -> Running -> Completed` |
| [pod/v1.34.0/core-pod-v1/failed.yaml](../test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/failed.yaml) | `Initializing -> Running -> Failed` | `Pending + Progressing -> Running -> Failed` |
| [pod/v1.34.0/core-pod-v1/initializing.yaml](../test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/initializing.yaml) | `Initializing` | `Pending` |
| [pod/v1.34.0/core-pod-v1/running.yaml](../test/e2e/recorded_data/pod/v1.34.0/core-pod-v1/running.yaml) | `Initializing -> Running` | `Pending + Progressing -> Running` |
| [statefulset/v1.34.0/apps-statefulset-v1/degraded.yaml](../test/e2e/recorded_data/statefulset/v1.34.0/apps-statefulset-v1/degraded.yaml) | `Initializing -> Degraded` | `Pending + Progressing -> Pending -> Pending + Degraded` |
| [statefulset/v1.34.0/apps-statefulset-v1/scaled.yaml](../test/e2e/recorded_data/statefulset/v1.34.0/apps-statefulset-v1/scaled.yaml) | `Initializing -> Running -> Initializing -> Degraded -> Running -> Initializing -> Running` | `Pending + Progressing -> Pending -> Running -> Undefined -> Pending + Progressing -> Pending + Degraded -> Running -> Undefined -> Running + Progressing -> Running` |
