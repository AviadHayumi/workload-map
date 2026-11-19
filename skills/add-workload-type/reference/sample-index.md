<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Sample index

Pick the definition whose shape is closest to the target workload, copy it, and
adapt the GVK, CEL accessors, and status mapping. Every definition below lives in
`docs/catalog/`, which also holds minimal, suspend-aware definitions for the
built-in kinds. Adapting a working sample is faster and safer than starting
from an empty file.

This index describes the local v1alpha2 PoC. See `docs/design/cel-expressions/POC.md` for its build and support limits.

## How to choose

Answer these, then choose the most specific matching row in the table below (the
one naming the most of your workload's traits). When several rows match, a
multi-instance or nested pattern (for example Ray worker groups needing
`instanceIds.read.expression`) takes precedence over a generic role-based row.

1. Does the workload own other resources, or is it a single object with pods
   defined in its own spec?
2. Where does the pod template live: a full template, a bare pod spec, or
   scattered fields?
3. Does it report status through conditions, a phase string, or status fields?
4. Does any component hold several specs (worker groups, services, replicated
   jobs) that need per-instance identity?
5. Are there nested layers of ownership (a group that owns a leader and workers)?

## Decision table

| Workload shape | Closest sample | Why |
|---|---|---|
| Single Job, full pod template, status via conditions plus a computed expression | `docs/catalog/batch-job-v1.yaml` | `podTemplateSpec.read.expression: object.spec.template`, `byConditions` plus `byExpression`, suspend via `.spec.suspend`. |
| CronJob wrapping a Job template, pod template nested under `jobTemplate`, or any controller that creates batch Jobs from an embedded JobSpec (KEDA ScaledJob at `.spec.jobTargetRef.template`) | `docs/catalog/batch-cronjob-v1.yaml` | One ownership-only child `Job`, `podTemplateSpec.read.expression: object.spec.jobTemplate.spec.template` on the root, expression-only status, suspend. For another Job generator the root carries the template and `parallelism`, the `Job` child carries any bounds on the Job count, and status usually comes from the controller's own conditions, not `lastScheduleTime`. |
| Deployment or other controller that generates ReplicaSets, including progressive-delivery controllers (Argo Rollouts) | `docs/catalog/apps-deployment-v1.yaml`, `docs/catalog/serving-knative-dev-service-v1.yaml` | Root plus a generated child. In the Deployment the root carries `podTemplateSpec` and scale, and the `replicaset` child is ownership-only; in Knative the `revision` child carries the template. A controller that also reports an aggregate phase keeps the Deployment shape and maps `byPhase`, ANDing a condition where one phase covers two states. Do not copy its `Failed` rule on `ProgressDeadlineExceeded`: the controller clears it once pods become available (step 5 in `SKILL.md`). |
| Controller that reports only counters and `observedGeneration`, no conditions or phase, with the template on the root and no child (StatefulSet, DaemonSet). Also a Deployment-shaped CRD that owns its pods directly and adds a `Progressing` condition only under a progress deadline (OpenKruise CloneSet) | `docs/catalog/apps-statefulset-v1.yaml` | Root-only, `podTemplateSpec.read.expression: object.spec.template`, every rule a `byExpression` inside a `object.?status.?observedGeneration.orValue(0) > 0` guard (step 5 in `SKILL.md`; the sample lacks it). `Running` ANDs `observedGeneration == generation`; `Initializing` is its complement within the guard, so it also matches the stale `observedGeneration != generation` frame the replay asserts after a patch. No `scaleDefinition` when the count lives only in status. For the CloneSet shape, see Failed and Degraded and Spec-driven branches in `technical-guide.md`. |
| Role-based distributed job (master and worker, launcher and worker) with pods as children | `docs/catalog/kubeflow-org-pytorchjob-v1.yaml`, `docs/catalog/kubeflow-org-mpijob-v2beta1.yaml` | Children are `Pod` kind, one per role, each with a `componentTypeSelector` on a role label. The label key is operator-specific: PyTorchJob uses `training.kubeflow.org/replica-type`, MPIJob uses `training.kubeflow.org/job-role`. Verify the real label before copying. Do not copy two things: their `Initializing` rule on `Created` alone overlaps every later status (step 5 in `SKILL.md`), and a fixed child per optional map key breaks on a CR that omits the role (step 3). |
| A role whose running pod creates another role's pods, with pod fields scattered per role (Spark driver and executors) | `docs/catalog/kubeflow-org-pytorchjob-v1.yaml` for the role selectors, `docs/catalog/apps-nvidia-com-nimservice-v1alpha1.yaml` for the fragmented fields | Root with suspend and a phase; a creator `Pod` child owned by the root and a created `Pod` child owned by the creator (the `controller=true` owner on the created pods); a `fragmentedPodSpecDefinition` per role; scale only on the created role; no gang over both (Gang scheduling and creator pods in `technical-guide.md`). |
| Several worker groups under one role that need per-group identity | `docs/catalog/ray-io-raycluster-v1.yaml`, `docs/catalog/ray-io-rayjob-v1.yaml`, `docs/catalog/ray-io-rayservice-v1.yaml` | Worker component uses `instanceIds.read.expression` on `workerGroupSpecs[].groupName` paired with a `componentInstanceSelector`. |
| Replicated jobs, each a named template instance | `docs/catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml` | `replicatedjob` child with `instanceIds.read.expression: object.spec.replicatedJobs.map(job, job.name)` and a `componentInstanceSelector`. |
| Workload referencing a shared runtime (Trainer v2 TrainJob) | `docs/catalog/trainer-kubeflow-org-trainjob-v1alpha1.yaml` | Declared ClusterTrainingRuntime lookup; read-only image/resources/node-count fallbacks; trainer job selected by label and container by name. Supply a reader or manual references. Verify against the same defaulting snapshot as the workload. |
| Array of named tasks whose Pods are created directly (Volcano Job) | `docs/catalog/batch-volcano-sh-job-v1alpha1.yaml` | One multi-instance Pod child. Read IDs with `object.spec.tasks.map(task, task.name)` and keep every mapped list in that order. Omitted replica or minimum fields stay absent. |
| Nested ownership with identical replicated sub-structures (a group that owns a leader and workers) | `docs/catalog/leaderworkerset-x-k8s-io-leaderworkerset-v1.yaml` | `group` child owns `leader` and `worker`; `replicaSelector` on the group; `componentTypeSelector` distinguishes leader from worker. |
| Fields scattered across the spec, status via a phase string | `docs/catalog/nvidia-com-dynamographdeployment-v1alpha1.yaml`, `docs/catalog/nvidia-com-dynamographdeployment-v1beta1.yaml`, `docs/catalog/apps-nvidia-com-nimservice-v1alpha1.yaml`, `docs/catalog/apps-nvidia-com-nimcache-v1alpha1.yaml` | `fragmentedPodSpecDefinition` with per-field accessors; `phaseDefinition` plus `byPhase` mappings. |
| Status reported through both a phase and conditions | `docs/catalog/milvus-io-milvus-v1beta1.yaml` | Declares both `phaseDefinition` and `conditionsDefinition`; maps statuses `byPhase`. |
| Multi-service inference, each service its own component | `docs/catalog/serving-kserve-io-inferenceservice-v1beta1.yaml` | Predictor and transformer children mix `fragmentedPodSpecDefinition` and `podSpec` plus `metadata`; `componentTypeSelector` per service. |
| Nested pod cliques and scaling groups | `docs/catalog/grove-io-podcliqueset-v1alpha1.yaml` | Multiple multi-instance children (`clique`, `scalinggroup`) each with `instanceIds.read.expression` plus instance and replica selectors. This CRD has no aggregate phase, so status is mapped with `byExpression` over replica counts, not `byPhase`. |
| Templates that each run a pod directly, where no pod label carries the template name (Argo Workflows) | Start from `docs/catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml` for the multi-instance shape, then follow Instance ids carried outside labels in `technical-guide.md` | One `Pod` child with `fragmentedPodSpecDefinition` paths iterating `.spec.templates[]`, `instanceIds.read.expression` on the template name, and a `componentInstanceSelector` whose `read.expression` reads the pod spec itself (the `ARGO_TEMPLATE` env value). No `scaleDefinition`: a template runs any number of pods and the spec carries no count. |
| Pipeline tasks created through intermediate TaskRuns (Tekton PipelineRun) | Start from `docs/catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml` | Use a TaskRun child and combine `tasks` and `finally` in one ordered CEL list for both IDs and values. The Pod instance selector reads `object.metadata.labels["tekton.dev/pipelineTask"]`. Steps are not containers; keep projections read-only. |
| Single stateful pod from an embedded template, suspended through a mode field rather than a boolean | `docs/catalog/agents-x-k8s-io-sandbox-v1beta1.yaml` | One `Pod` child selected by key existence of a controller label; every status matcher except `Suspending` combines `byConditions` with a `byExpression` on the requested mode (AND), and `Suspending` is a `byExpression` alone, so lagging conditions resolve to `Suspending`/`Resuming` instead of a second status; string-valued suspend via `.spec.operatingMode`. |

## Do not copy

Parts of the existing tree that predate a rule. Copy the shape, not these.

| Source | Do not copy | Instead |
|---|---|---|
| `apps-deployment-v1.yaml` | `Failed` on `ProgressDeadlineExceeded` | `Degraded` for a Deployment; unmapped when the controller does not restart the clock (step 5) |
| `apps-statefulset-v1.yaml` | `Degraded` on a partial ready count, and the `Optional()` dip its flows declare for it | `Initializing` (step 5) |
| `apps-statefulset-v1.yaml` | Rules without the `object.?status.?observedGeneration.orValue(0) > 0` guard | Require a controller-written field in every settled rule (step 5) |
| `kubeflow-org-pytorchjob-v1.yaml`, `kubeflow-org-mpijob-v2beta1.yaml` | `Initializing` on `Created` alone | AND the absence of every later condition (step 5) |
| `kubeflow-org-pytorchjob-v1.yaml`, `kubeflow-org-mpijob-v2beta1.yaml` | One fixed child per optional map key | One multi-instance child keyed by the map (step 3) |
| The nearest sample's role label key | The `read.expression` value | The target controller's real pod labels (step 6) |
| Existing defaulted replica reads | Copying a fallback without checking the new workload schema | Preserve absence unless the controller or API defines that default. A computed read does not imply a writable count. |
| The PyTorchJob flow | The `Initializing` revisit before the terminal state | Declare a revisit only when the controller source can produce it (step 8) |
| `test/e2e/recorded_data/*/v1.34.0/` | Fixtures of an operator filed under the Kubernetes version | The operator's `version_of` string (step 8) |
| `karta-e2e-pytorch`, `karta-e2e-mpi`, `karta-e2e-sts`; the `pytorch` label and testdata directory | Object names without the flow suffix or with a shortened workload; a name that is not the lowercase kind | `karta-e2e-<workload>-<flow>` and the lowercase kind (step 8) |
| The existing builders' one or two line doc comments | A builder comment without the step 8 items | The 3 to 26 line builder comment (step 8) |
| A sibling catalog definition's gang format | Converting `podGroups` to `podGroup` while copying | Copy the format it uses (Optimization instructions in `technical-guide.md`) |

## Pattern quick reference

- Single full pod template: `podTemplateSpec`. See `batch-job-v1.yaml`.
- Bare pod spec with separate metadata: `podSpec` plus `metadata`. See
  the transformer child in `serving-kserve-io-inferenceservice-v1beta1.yaml`.
- Scattered fields: `fragmentedPodSpecDefinition`. See
  `nvidia-com-dynamographdeployment-v1beta1.yaml`.
- Conditions status: `conditionsDefinition` plus `byConditions`. See
  `ray-io-rayservice-v1.yaml`, which also uses `reason` to separate degraded from
  failed.
- Phase status: `phaseDefinition` plus `byPhase`. See `ray-io-raycluster-v1.yaml`.
- Expression status: `byExpression`. See `batch-job-v1.yaml`.
- Per-instance identity: `instanceIds.read.expression` plus `componentInstanceSelector`. See
  `ray-io-raycluster-v1.yaml`.
- Replica identity within identical sub-structures: `replicaSelector`. See
  `leaderworkerset-x-k8s-io-leaderworkerset-v1.yaml`.
