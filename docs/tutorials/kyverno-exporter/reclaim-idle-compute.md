<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Reclaim idle compute with Kyverno and DSX

A Job requests 8 CPU cores and 16 GiB, then sits idle for an hour.
DSX links the Job to its Pods. With usage metrics in Prometheus, Kyverno can
suspend that Job and release its requests for other work.

| Benefit | Policy example | Evidence |
| --- | --- | --- |
| Release idle GPUs | If all allocated GPUs stay below 5% for an hour and no work is queued, suspend a restartable Job. | Historical test with simulated GPUs. Real-GPU recipe follows. |
| Free unused CPU and memory requests | Compare actual use with requests. Suspend only when the application is idle. | Proposed cAdvisor integration. |
| Limit time in status X | Suspend long-running Jobs. Flag workloads stuck Initializing or Degraded. | Running policy tested live. |
| Remove finished work | Delete Completed Jobs and their Pods after a retention window. | Tested live. |
| Give owners time to act | Warn before runtime or cost budgets are exhausted. | Proposed. Needs a notification service. |

Lower bills also require node scale-down or release of paid capacity.
Financial savings were not measured.

## Create and run the demo

This demo uses Job status to suspend a Running Job and clean up a Completed Job.
Both policies require two minutes of valid history. To detect idleness from
resource use, add the usage metrics listed here.

| Scenario | Prerequisites |
| --- | --- |
| Run this demo | Docker must be running. Install kind, kubectl, Helm, Bash, curl and Python 3. A GPU is not required. |
| Detect real GPU idleness | Functional NVIDIA GPUs on Linux, drivers, container/device access, DCGM Exporter, DSX and Prometheus. |
| Detect unused CPU/memory requests | Authorized kubelet cAdvisor scrapes, request metrics from kube-state-metrics or kube-scheduler, DSX and Prometheus. |

The commands install DSX, Prometheus and Kyverno. Add GPU/DCGM, cAdvisor
scraping and request metrics separately.

### 1. Create a kind cluster

kind runs Kubernetes inside Docker containers. Use these commands from the
repository root. Keep the same terminal for the rest of the demo.

A kubeconfig file tells kubectl how to connect to a cluster. The demo keeps its
own file and test results in `.artifacts/kyverno-exporter-tutorial`.

```sh
mkdir -p .artifacts/kyverno-exporter-tutorial
kind create cluster --name kyverno-exporter-tutorial \
  --image kindest/node:v1.34.3 \
  --kubeconfig .artifacts/kyverno-exporter-tutorial/kubeconfig \
  --wait 120s
export KUBECONFIG="$PWD/.artifacts/kyverno-exporter-tutorial/kubeconfig"
kubectl get nodes
```

Expect one control-plane node with status `Ready`.

### 2. Install DSX, Prometheus and Kyverno

Build the exporter from this checkout. Load its image into kind through an archive
that contains one platform. This avoids incomplete image indexes in Docker Desktop.

```sh
DEMO_PLATFORM="linux/$(docker version --format '{{.Server.Arch}}')"
docker build --platform "$DEMO_PLATFORM" \
  -f dsx-workload-exporter/Dockerfile \
  -t dsx-workload-exporter:kyverno-tutorial .
docker image save --platform "$DEMO_PLATFORM" \
  -o .artifacts/kyverno-exporter-tutorial/exporter.tar \
  dsx-workload-exporter:kyverno-tutorial
kind load image-archive .artifacts/kyverno-exporter-tutorial/exporter.tar \
  --name kyverno-exporter-tutorial
rm .artifacts/kyverno-exporter-tutorial/exporter.tar
```

Create the namespaces. Install the exporter with the bundled Job definition.
The [exporter values](manifests/exporter-values.yaml) disable the Karta operator,
webhook and CRD upgrader. They select the local image.

```sh
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/namespaces.yaml
helm upgrade --install dsx-workload ./charts/karta \
  --namespace karta-system --create-namespace --skip-crds \
  -f docs/tutorials/kyverno-exporter/manifests/exporter-values.yaml \
  --wait --timeout 5m
```

Install Prometheus and its recording rules. These rules store the Job UIDs that
meet each policy condition. A UID identifies one Kubernetes object.

```sh
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/prometheus-rules.yaml
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/prometheus.yaml
kubectl -n monitoring rollout status deployment/prometheus --timeout=180s
```

The kind nodes need registry access. Kubernetes pulls Prometheus `v3.14.0`,
BusyBox `1.37.0` and the Kyverno images when their Pods start.

Install Kyverno chart `3.9.1`, which runs Kyverno `1.19.1`. Then grant the
permissions that the two demo policies need.

```sh
helm upgrade --install kyverno kyverno \
  --repo https://kyverno.github.io/kyverno/ --version 3.9.1 \
  --namespace kyverno --create-namespace \
  -f docs/tutorials/kyverno-exporter/manifests/kyverno-values.yaml \
  --wait --timeout 5m
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/kyverno-rbac.yaml
kubectl -n karta-system rollout status deployment/dsx-workload-exporter --timeout=180s
kubectl -n kyverno get deployments
```

Expect the exporter and Prometheus rollouts to finish and all Kyverno deployments
to be ready.

### 3. Create the example Jobs

```sh
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/workloads.yaml
kubectl -n ai-lab get jobs
```

Five Jobs appear. The policies target `running-worker` and `completed-report`.
The unselected and failed Jobs must remain. They show whether the policies act
on the intended Jobs.

### 4. Activate the policies

| Kyverno policy | Required state of the current Job | Action and result |
| --- | --- | --- |
| `MutatingPolicy` | Label `tutorial.karta.run.ai/action: suspend`. Active and ready Pods. Two minutes of valid Running history for the Job UID. | Set `spec.suspend=true`. The Job controller terminates active Pods and releases their resource requests. |
| `DeletingPolicy` | Label `tutorial.karta.run.ai/action: delete`. `Complete=True`. Two minutes of valid Completed history for the Job UID. | Delete the Job with `Background` propagation. Kubernetes garbage collection removes its owned Pods. |

Both policies target `ai-lab`. Kyverno matches Prometheus results to the current Job UID and namespace.
A replacement Job cannot inherit eligibility through its name.

The policies require fresh, valid metrics. Missing results or query errors cannot
supply a matching UID. Suspension terminates Pods without creating a checkpoint.

![DSX observes, Prometheus evaluates history, Kyverno acts, Kubernetes removes Pods](images/policy-flow.png)

DSX is read-only. The Kyverno background controller modifies existing Jobs.
Admission mutation is disabled. Setup supplies the [Prometheus rules](manifests/prometheus-rules.yaml)
and [RBAC](manifests/kyverno-rbac.yaml), which defines access permissions.
Cleanup can list Jobs across the cluster. Action permissions apply in `ai-lab`.

<details>
<summary>Full MutatingPolicy YAML</summary>

```yaml
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
apiVersion: policies.kyverno.io/v1
kind: MutatingPolicy
metadata:
  name: tutorial-suspend-running
spec:
  matchConstraints:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: ai-lab
    objectSelector:
      matchLabels:
        tutorial.karta.run.ai/action: suspend
    resourceRules:
      - apiGroups: [batch]
        apiVersions: [v1]
        operations: [CREATE, UPDATE]
        resources: [jobs]
  evaluation:
    admission:
      enabled: false
    mutateExisting:
      enabled: true
  targetMatchConditions:
    - name: current-target-is-opted-in
      expression: >-
        object.metadata.namespace == 'ai-lab' &&
        has(object.metadata.labels) &&
        'tutorial.karta.run.ai/action' in object.metadata.labels &&
        object.metadata.labels['tutorial.karta.run.ai/action'] == 'suspend'
    - name: still-running-and-not-suspended
      expression: >-
        !object.spec.?suspend.orValue(false) &&
        object.status.?active.orValue(0) > 0 && object.status.?ready.orValue(0) > 0
    - name: verified-running-history-for-this-uid
      expression: >-
        http.Get('http://prometheus.monitoring.svc:9090/api/v1/query?query=tutorial%3Ajob_running%3Aeligible%20and%20%28time%28%29%20-%20timestamp%28tutorial%3Ajob_running%3Aeligible%29%20%3C%2015%29').data.result.exists(r, r.metric.workload_uid == object.metadata.uid && r.metric.namespace == object.metadata.namespace)
  mutations:
    - patchType: JSONPatch
      jsonPatch:
        expression: "[JSONPatch{op: 'add', path: '/spec/suspend', value: true}]"
```

</details>

<details>
<summary>Full DeletingPolicy YAML</summary>

```yaml
apiVersion: policies.kyverno.io/v1
kind: DeletingPolicy
metadata:
  name: tutorial-delete-completed
spec:
  matchConstraints:
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: ai-lab
    objectSelector:
      matchLabels:
        tutorial.karta.run.ai/action: delete
    resourceRules:
      - apiGroups: [batch]
        apiVersions: [v1]
        operations: [CREATE, UPDATE]
        resources: [jobs]
  schedule: "*/1 * * * *"
  deletionPropagationPolicy: Background
  conditions:
    - name: still-completed
      expression: >-
        object.status.?conditions.orValue([]).exists(c,
          c.type == 'Complete' && c.status == 'True')
    - name: verified-completed-history-for-this-uid
      expression: >-
        http.Get('http://prometheus.monitoring.svc:9090/api/v1/query?query=tutorial%3Ajob_completed%3Aeligible%20and%20%28time%28%29%20-%20timestamp%28tutorial%3Ajob_completed%3Aeligible%29%20%3C%2015%29').data.result.exists(r, r.metric.workload_uid == object.metadata.uid && r.metric.namespace == object.metadata.namespace)
```

</details>

Apply both policies:

```sh
kubectl apply -f docs/tutorials/kyverno-exporter/manifests/policies.yaml
```

### 5. Observe the result

```sh
kubectl -n ai-lab get jobs --watch
```

After the observation window, Kyverno suspends `running-worker` and deletes
`completed-report`. Kubernetes removes their Pods. The control Jobs remain.
Actions require at least two minutes of observations and can occur later.
Press Ctrl-C, then inspect the Jobs and Pods:

```sh
kubectl -n ai-lab get job running-worker -o jsonpath='{.spec.suspend}{"\n"}'
kubectl -n ai-lab get pods -l batch.kubernetes.io/job-name=running-worker
kubectl -n ai-lab wait --for=delete job/completed-report --timeout=5m
kubectl -n ai-lab get pods -l batch.kubernetes.io/job-name=completed-report
```

Expect `true` and two empty Pod lists after the controllers finish.
After a Prometheus outage, cleanup can require the [explicit requeue](#live-results).

### 6. Opt out and resume

```sh
kubectl -n ai-lab patch job running-worker --type=merge -p \
  '{"metadata":{"labels":{"tutorial.karta.run.ai/action":"observe"}},"spec":{"suspend":false}}'
```

The patch opts out and resumes in one update. The Job controller creates a new
Pod. Re-enabling the action label can reuse history for that UID.

The optional recovery verifier resets its labeled Jobs and two policies.
It temporarily stops the exporter and Prometheus, then restores one replica each.
Do not edit or rerun setup during this test. Run it before removing the cluster:

```sh
python3 docs/tutorials/kyverno-exporter/verify.py
```

Results go to `.artifacts/kyverno-exporter-tutorial/verification.json`.

<details>
<summary>Inspect Prometheus metrics and recorded lab versions</summary>

To inspect metrics, run this in another terminal with the same kubeconfig:

```sh
kubectl -n monitoring port-forward service/prometheus 19090:9090
```

Open [Prometheus](http://127.0.0.1:19090/query) and query:

```promql
dsx_workload_status{namespace="ai-lab",workload_name="running-worker",phase="Running",observation_valid="true"}
```

Before suspension, a ready Job reports `1`. Its `workload_uid` matches the
Job UID. A valid observation means the exporter inputs are valid.
It does not establish application health. For policy decisions, inspect `tutorial:job_running:eligible`,
`tutorial:job_completed:eligible` and `/api/v1/rules` for errors.

The lab uses kind 0.30.0, Kubernetes 1.34.3 and Kyverno 1.19.1 (chart 3.9.1).
It also uses Prometheus 3.14.0 and busybox 1.37.0.
The recorded host used Docker 29.0.1, Helm 4.0.1 and Linux arm64 nodes.
Setup builds the exporter from the checkout.
Its bundled `batch-job-v1` definition needs no Karta operator or CRD.

The run used exporter source `8370c0b5a720771dcb2e8c944d2e4bba39e7df3a`.
All six deployments and both recording rules were healthy afterward.
Local records are in `.artifacts/kyverno-exporter-tutorial/{verification,final-health,inventory}.json`.
Explicit `Background` propagation fixed orphaned completed Pods in an earlier run.
The verifier checks dependent Pods by Job UID after deletion and recovery.

</details>

### 7. Remove the lab

Stop the port-forward before removing the cluster:

```sh
kind delete cluster --name kyverno-exporter-tutorial
```

Prometheus retains two hours of history in temporary storage. Restarting its Pod clears that history.

## Extend the policies

### Idle GPUs

For the one-hour GPU rule, require utilization data for every allocated device.
Also require valid Running history, no queued work and a safe restart or checkpoint path.
Enable DCGM Kubernetes mapping and `DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID=true`.
Join DCGM to DSX by namespace and Pod UID.

Three reported devices out of four are insufficient. MIG, shared GPUs and DRA need separate attribution checks. [GPU query examples](../../dsx-workload-exporter/queries.md#3-gpu-measurements-by-pod-uid).

The fork's [original proposal](https://github.com/AviadHayumi/workload-map/blob/1a90457bbdc349bdc1fb5c7f0924198f291e6a04/keps/0003-runtime-rules/README.md#2-the-goals)
covered GPU idleness, phase duration, retention and warnings. Its
[simulated-GPU run](https://github.com/AviadHayumi/workload-map/blob/f28da01bdf140355964fff5d450f867265371608/labs/exporter-kyverno-rr/captures/75-gpu-idle-fake-gpu.txt)
changed utilization from 90% to 1%. It suspended the Job after the last busy
sample expired from a two-minute window. The run did not test physical GPUs.
This lab uses current `dsx_*` metrics.

### Unused CPU and memory requests

If the 8-core/16-GiB Job uses under 0.1 core and 300 MiB for an hour, investigate.
cAdvisor supplies CPU rate in cores and memory working set in bytes.
Join usage and requests to the same current Pods and workload UID.

Before suspension, require fresh, complete history, positive requests, application-idle confirmation and a safe restart path.
Low CPU can mean the application waits for useful I/O. Low memory use does not
establish inactivity. Releasing a 16-GiB request does not mean 16 GiB of physical
memory was in use.

### Time in status X

| Condition | Example response |
| --- | --- |
| Running for eight hours | Suspend a safely restartable workload. |
| Initializing for 90 minutes | Investigate the stalled start. |
| Degraded for ten minutes | Notify the owner. |
| Completed for 24 hours | Delete after retaining outputs. |

This demo tests only Running and Completed. Change the rule window, sample
count, age and offset checks together. Workload age is not phase duration.
`Job.spec.suspend` applies only to Jobs. If `activeDeadlineSeconds` or
`ttlSecondsAfterFinished` fits the requirement, use native Job deadlines or TTL.

Each additional policy needs its own inputs and tests:

- Stalled training: Require absent progress and checkpoint confirmation.
- Priority queues: Reclaim low-priority Jobs with confirmed idleness while important work waits.
- Idle development services: After sessions and traffic stop, scale to zero. Define restoration and autoscaler coordination.
- Retained results: Before deleting workloads, make sure that their artifacts are retained.
- Repeated schedule failures: Suspend the CronJob. Handle existing Jobs separately.
- Quiet components: Scale attributed idle workers while busy components continue.
- Team budgets: Join a cost source with defined price, currency and period. Warn before action.

Notifications need a delivery service. Checkpointing needs application support.
DSX supplies neither. Sampling is not an exact financial cap.

<details>
<summary>PromQL, identity joins and sampling details</summary>

For one regular container without init containers or Pod overhead, 0.1 core is
1.25% of an 8-core request. CPU usage:

```promql
rate(container_cpu_usage_seconds_total{
  namespace!="", pod!="", container!="", container!="POD"
}[5m])
```

Memory use is `container_memory_working_set_bytes`, a gauge in bytes.
Do not apply `rate()`. For requests, copy the kube-state-metrics Pod `uid`
to `pod_uid` and join the [deduplicated DSX attribution rule](../../dsx-workload-exporter/queries.md#1-deduplicate-pod-attribution):

```promql
label_replace(
  kube_pod_container_resource_requests{resource="cpu",unit="core"},
  "pod_uid", "$1", "uid", "(.+)"
)
* on (namespace, pod_uid)
  group_left (workload_name, workload_uid, workload_group, workload_kind)
  dsx:pod_attribution:unique{observation_valid="true"}
```

For memory requests, use `resource="memory",unit="byte"`. These fragments help
inspect the data. They are not installed action rules.
Remove duplicate scrapes. Compare the same current running Pods and containers.
Require a positive request denominator.

Record the usage/request ratio for each workload. Require its maximum to stay
low for the full hour. Also require fresh, complete history and application-idle confirmation.

The [CPU/memory query examples](../../dsx-workload-exporter/queries.md#2-cpu-and-memory-by-component-instance-and-group)
join usage by Pod name. Automatic actions need stronger identity checks when
Pods are replaced. Container-request sums omit init/overhead scheduling effects.
For effective Pod requests, use the scheduler metric `kube_pod_resource_request`
with a validated identity join. See the [request metric reference](https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/workload/pod-metrics.md).

Use the highest GPU utilization across all allocated devices and the whole
window. Preserve device IDs and remove duplicate measurements.
Use a real GPU workload to make sure that the labels are correct. [DCGM Exporter setup](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html).
GPU Operator can manage drivers and device access.

Prometheus scrapes and evaluates every five seconds. The Kyverno background
controller scans for suspension every 30 seconds. Its DeletingPolicy runs once
a minute. Two minutes is the observation window. Actions can occur later.
Each rule requires:

- All available valid phase samples to equal `1`, with at least 23 samples.
- The phase to equal `1` now and two minutes ago, with both samples under 15 seconds old.
- Creation age over 120 seconds, no invalid status or workload-error observations,
  and sufficient healthy exporter scrapes with a fresh successful current scrape.

Kyverno also requires the recording sample to be under 15 seconds old.
Empty results and query errors cannot supply a matching UID. A failed scrape
blocks eligibility until it leaves the window and fresh history qualifies.

![Evidence required before a policy acts](images/evidence-window.png)

Samples cannot prove what happened between scrapes. The Job can change after the query and before the policy write. Production use needs retained audit records, resource limits
and an explicit policy for concurrent user changes.

![Workload policy examples and their evidence](images/value-overview.png)

</details>

## Live results

The kind verifier passed in 670 seconds on 2026-11-08:

| Check | Result |
| --- | --- |
| Suspension and cleanup | Selected Jobs lost their Pods. Initial actions checked by 189s. |
| Controls and UID isolation | Unselected and failed Jobs remained. A new UID did not inherit eligibility. |
| Exporter and Prometheus outages | Selected Jobs unchanged during tested outages, including opt-in while exporter scrapes were unavailable. |
| Recovery | Fresh history required. Suspension recovered automatically. Cleanup needed explicit requeue. |
| Permissions and resume | Actions allowed in `ai-lab` and denied in `default`. Opt-out/resume stayed active. |

The tests did not cover physical-GPU idleness, cAdvisor-driven actions, production
scale, multiple clusters or fault-injected invalid observations.
The historical runtime-rules proposal is not an installed controller.
The Job can change between a sample and the policy write.

On Kyverno 1.19.1, repeated query failures can exhaust ten cleanup retries and
leave the DeletingPolicy unscheduled. After Prometheus recovers, requeue it:

```sh
kubectl patch deletingpolicy tutorial-delete-completed --type=merge \
  -p '{"spec":{"schedule":"* * * * *"}}'
kubectl patch deletingpolicy tutorial-delete-completed --type=merge \
  -p '{"spec":{"schedule":"*/1 * * * *"}}'
```

Make sure that `status.lastExecutionTime` advances and the eligible Job and Pods disappear. Reapplying the same spec or changing an annotation does not requeue
it. [Pinned scheduler](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/controllers/deleting/controller.go#L264),
[retry handling](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/utils/controller/run.go#L66).
