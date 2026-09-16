<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# KEP-0003: Runtime rules

- Status: provisional
- Authors: @AviadHayumi
- Created: 2026-09-11
- Depends on: [KEP-0001](../0001-cel-expressions/README.md) (CEL expressions),
  [KEP-0004](https://github.com/AviadHayumi/workload-map/blob/kep-metrics-exporter/keps/0004-metrics-exporter/README.md)
  (metrics exporter, proposed separately)
- Tracking issue: to be opened before this KEP merges

## Summary

![one rule, any described workload](kep-rr-big-picture.png)

The exporter mapped three dynamo pods to their workload and component
instances with no dynamo-specific code (capture 61). This KEP proposes
rules that act on facts like these: one rule across every described
type, one action per allowance window, with a receipt that survives the
workload.

Sections 1 and 2 ran for real; sections 3 and 4 are the gaps those runs
hit and the proposal. Measured results cite captures from
[the integration lab](https://github.com/AviadHayumi/workload-map/tree/exporter-integration-lab/labs/exporter-kyverno-rr);
proposed examples are labeled.

## 1. What the exporter gives us, on a real workload

We ran a real distributed inference workload: a DynamoGraphDeployment
with a frontend, a decode worker and a prefill worker (disaggregated
serving, the Llama-3.1-8B mocker), driven by the real dynamo operator
1.2.1 on a kind cluster. Edited excerpt, unrelated pods and the RESTARTS
column trimmed:

```console
$ kubectl get dgd,pods -n default
NAME           READY   BACKEND   AGE
dynamo-smoke   True              89s

NAME                                             READY   STATUS    AGE
dynamo-smoke-decode-e03467c0-8568474b87-tdfr6    1/1     Running   89s
dynamo-smoke-frontend-59d8d4c776-v7nx8           1/1     Running   88s
dynamo-smoke-prefill-e03467c0-7bf477dcd9-n6l6b   1/1     Running   88s
```

The exporter read the catalog definition for this kind and published
(capture 61, labels trimmed for width):

```text
karta_workload_status{workload="dynamo-smoke",phase="Running",...} 1
karta_pod_workload_info{pod="dynamo-smoke-frontend-59d8d4c776-v7nx8",
    component_instance="Frontend",workload="dynamo-smoke",...} 1
karta_pod_workload_info{pod="dynamo-smoke-decode-e03467c0-8568474b87-tdfr6",
    component_instance="decode",workload="dynamo-smoke",...} 1
karta_pod_workload_info{pod="dynamo-smoke-prefill-e03467c0-7bf477dcd9-n6l6b",
    component_instance="prefill",workload="dynamo-smoke",...} 1
karta_workload_component_replicas{component_instance="prefill",...} 1
karta_workload_component_pods{component_instance="decode",phase="Running",...} 1
```

- Status came out normalized: the operator wrote
  `.status.state: successful`, the definition maps that to `Running`, so
  `phase="Running"` is 1 and the other eight phases are 0 (capture 61).
  Kinds with a status definition all get the same phase set, so one
  query works for a Job and for dynamo.
- All three pods came out attributed: value 1, component instances
  `Frontend`, `decode` and `prefill`, straight from the definition's
  instance and pod selectors (capture 61).
- Desired vs observed per instance: `component_replicas` 1 and
  `component_pods{phase="Running"}` 1 for each of the three instances
  (capture 61).
- Those three pod-to-workload mappings are a join key: GPU and CPU
  series already exist per pod, and these labels tie them to workloads
  and components. An illustrative join sits in the details below; it was
  not run in this dynamo test.

Six workload metric families, all gauges, plus exporter self-metrics:

| Series | What it says |
|---|---|
| `karta_workload_info` | the workload exists, which definition governs it (value always 1) |
| `karta_workload_status` | nine 0/1 phase series, for workloads whose definition maps status |
| `karta_pod_workload_info` | this pod belongs to that workload, component, and instance (value always 1, the join key) |
| `karta_workload_component_replicas` | desired count per component instance, when the definition provides it |
| `karta_workload_component_pods` | observed pods per instance, split by pod phase |
| `karta_workload_generation` | the workload's `metadata.generation`; 1 in capture 61 |

How they work: watch handlers update cached workload state and pod
attribution (a phase-only pod update skips the attribution rules), and a
scrape renders the cache and counts its pod records. The value-1 info
series are the kube-state-metrics convention (`kube_pod_info` joins the
same way), and the 0/1 phase set is the `kube_pod_status_phase` shape:
inactive phases stay 0 while the workload is tracked. Duration queries
still need freshness and sample-coverage checks, including missing data.
The exporter also publishes its own health (`karta_exporter_*`:
tracked workloads, unattributed pods, last event timestamp).

What the series let you write. Illustrative queries, not run in this
lab:

- Suspend on idle GPU: per-pod DCGM utilization joined to workload and
  instance (the rule in section 4).
- Find stuck starts: `Initializing` still 1 after 15 minutes, for kinds
  whose definitions map that state.
- Replica shortfall: desired replicas against observed Running pods,
  per component instance.
- One alert for `Failed` or `Degraded` across kinds whose definitions
  map those states.
- Generation changes: plot `metadata.generation` alongside the other
  series.

<details>
<summary>provenance and the rest of the scrape</summary>

Cluster kind-karta-e2e (Kubernetes v1.34.0), dynamo-platform 1.2.1
installed by `hack/e2e/operators/dynamo/install.sh`, the smoke
DynamoGraphDeployment from `hack/e2e/operators/dynamo/smoke.yaml`
extended with a third service, prefill, running the mocker's
`--is-prefill-worker` mode, the exporter from the metrics-exporter
branch run with `--use-catalog`.
[Capture 61](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/61-dynamo-prefill-metrics.txt)
holds the scrape filtered to this workload (capture 60 is the earlier
two-service run). It also shows: the dense 0/1 phase set,
`karta_workload_generation` at 1, `karta_workload_info` naming the
governing definition
(`karta="nvidia-com-dynamographdeployment-v1beta1"`), and the operator's
intermediate Deployments tracked as workloads of their own kind.

The illustrative join, not run here:

```promql
avg by (namespace, workload) (
  DCGM_FI_DEV_GPU_UTIL
  * on(namespace, pod) group_left(workload) karta_pod_workload_info
)
```

</details>

## 2. Wiring the facts into Kyverno

Prometheus scraped the exporter every 5s, and a Kyverno mutate-existing
policy read the history. The lab has no GPUs, so the tested condition
uses Running history as the stand-in for the GPU-idle join; the
mechanism is the same, a Prometheus fact deciding an action. The tested
rule: suspend a job whose `Running`
series held 1 across the samples in the trailing 2 minutes (the query
does not check that 2 full minutes of samples exist). Abbreviated,
non-runnable excerpt; the
[tested manifest](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/manifests/05b-kyverno-metric-suspend-http.yaml)
also restricts targets to jobs in one namespace and skips already
suspended ones:

```yaml
apiVersion: policies.kyverno.io/v1beta1
kind: MutatingPolicy
spec:
  evaluation:
    admission: {enabled: false}
    mutateExisting: {enabled: true}
  targetMatchConditions:
    - name: exporter-says-running-2m
      expression: >-
        object.metadata.name in
        http.Get('http://prometheus...min_over_time(karta_workload_status{
          namespace="ai-team",phase="Running",workload_kind="Job"}[2m])==1')
        .data.result.map(r, r.metric.workload)
  mutations:
    - patchType: JSONPatch
      jsonPatch:
        expression: "[JSONPatch{op: 'add', path: '/spec/suspend', value: true}]"
```

Results:

- Direct query, above: policy applied at 17:45:19.049, target first
  observed suspended at 17:45:19.693, 644ms later (capture 10). On the
  second cluster: applied 18:20:47.211, both trainers observed suspended
  at 18:20:50.608, 3.4s (capture 30).
- Through Kyverno's cache: a GlobalContextEntry polling the same API,
  the condition reading the declared projection; trainer-gce still
  unsuspended at 18:09:26, first observed suspended at 18:09:59
  (capture 19).

Reading the facts is not the gap.

## 3. The gaps the runs hit

- Failures look like success. We shipped a policy whose expression
  fails at runtime. Its work items completed and were deleted, the job
  stayed untouched, and the policy stayed ready (capture 31):

```text
ADDED      ur-97xsp   rr-cel-runtime-error   cel-mutate
MODIFIED   ur-97xsp   rr-cel-runtime-error   cel-mutate   Pending
MODIFIED   ur-97xsp   rr-cel-runtime-error   cel-mutate   Completed
DELETED    ur-97xsp   rr-cel-runtime-error   cel-mutate   Completed
(4 rounds like this; job suspend=false; policy ready: true)
```

  The captured background-log query found no lines mentioning the
  policy. What does exist: a `PolicyViolation` event saying only
  "mutation is not applied", and the policy's error counter at 4.
  Neither carries the actual error or points at the failing expression
  (capture 31).
- A user resume is reverted. A human resumed a suspended job; the
  tested rule re-suspended it 123.947s later (capture 30). Convergence
  is doing its job; the rule has no allowance state to say "a human
  decided, stand down".
- Nothing durable ties an action to its evidence. After the acted-on
  job was deleted, zero reports and no work items remained; seven
  TTL-bound events survived, and none retained a record linking the
  action to its evaluation and evidence (capture 17). Turning on more
  reporting does not change this: mutate-existing report entries are
  owned by the workload and collected with it, and a report is cluster
  scoped only when the resource itself is.

We tried closing the resume gap with authoring alone before proposing
anything new: a two-policy state machine on an annotation kept a user
resume standing for 7 minutes under the 60s scan (capture 50). So
act-once is encodable. That run did not resolve the known write races,
the state is an annotation any writer can edit, and it included no
receipt writer.

## 4. What runtime rules adds

![the allowance lifecycle](kep-rr-allowance-flow.png)

One rule, three parts. Filter and condition are type agnostic; the
action resolves through the definition's suspend handle, never through
controller code. Proposed API; the lab prototype tested the
Running-history condition on Jobs:

```yaml
apiVersion: rules.run.ai/v1alpha1   # provisional group
kind: RuntimeRule
metadata:
  name: reclaim-idle-training
spec:
  filter:
    types:
      - apiVersion: jobset.x-k8s.io/v1alpha2
        kind: JobSet
    namespaceSelector:
      matchLabels: {team: research}
  condition:
    gpuIdle: {below: "5", for: 1h}
  action: suspend
```

What the prototype demonstrated, on the same cluster and the same
series Kyverno used:

- A user resume is respected. With re-arming on resume disabled, the
  prototype detected the resume after 0.692s and wrote an escalation
  receipt after 124.526s, right where Kyverno had re-suspended its own
  target (123.947s, a separate job). The prototype's job was still
  running 300s after the resume (capture 22).

```text
[18:28:44.598] receipt ResumeDetected      (0.692s after the resume)
[18:30:48.432] receipt EscalatedNeedsHuman (instead of re-suspending)
[18:33:44.053] trainer-f suspend=false active=1
```

- Receipts outlive the workload, and intent is persisted before the
  patch: Intended at 18:26:34.226, Executed at 18:26:34.539
  (capture 21). Four receipts remained after their job was deleted:
  Intended, Executed, and two WouldAct (capture 24). Executed records
  patch acceptance; selected fields (the full receipt also carries uid,
  time, and operation identity):

```json
{
  "rule": "suspend-running-2m",
  "phase": "Executed",
  "workload": {"namespace": "ai-team", "name": "trainer-e", "kind": "Job"},
  "evidence": {
    "promql": "min_over_time(karta_workload_status{namespace=\"ai-team\",phase=\"Running\"}[2m]) == 1",
    "value": "1"
  },
  "patchPointer": "/spec/suspend",
  "patchValue": true
}
```

- Not being able to act is a visible state, not a silent pass. With
  patch permission revoked the receipt was SkippedNotReady; with it
  restored, Intended then Executed (capture 23). In observe mode the
  rule wrote WouldAct without touching the jobs, and on a kind whose
  definition has no suspend handle it wrote SkippedNoHandle
  (capture 20).

On top of the demonstrated core, the proposal requires (not yet built):
allowance epochs whose default re-arms on a user resume (the tested run
disabled re-arming, which is what produced the escalation),
observe-by-default install with a global gate as the kill switch, and
blast-radius caps. Kyverno keeps admission
and compliance, can validate RuntimeRule objects, and reads the same
series; this controller adds the acting contract. For dynamo:
attribution is demonstrated (capture 60), rules over it are proposed,
and its definition declares no suspend handle yet, so it would be
observe-only until one lands.

<details>
<summary>proposed contract and untested requirements</summary>

- Enforcement needs the global gate opened and the rule set to enforce.
  Gate closure stops new dispatches; accepted writes complete and stay
  in receipts.
- A receipt is persisted before a destructive act. No receipt, no
  action. Receipts are never silently rewritten; lost API responses
  reconcile to Unknown before any retry.
- Caps follow the disruption-budget shape: integer or percent, per
  reason, most restrictive wins, misconfiguration fails closed to zero.
- Metric conditions require fresh attribution and sample coverage over
  the window; missing input produces Unknown and blocks action. A final
  query re-checks the condition at dispatch.
- Object conditions ride watches with deadline requeues; metric
  conditions evaluate on a stated cadence.
- Rule admission validates scope and caps and runs a best-effort
  permission preflight with the executor's identity.
- Several rules on one workload: first condition to fire acts, actions
  apply serially, later rules see the post-action state.
- Controller down means nothing is actioned; windows and in-flight
  actions survive restart.
- Suspend effects differ per type (RayJob suspension tears down its
  cluster and reruns). A capability contract must classify action
  effects per definition before enforcement; unclassified types stay
  observe-only. Its home (annotation, descriptor, or schema field) is
  decided at implementation review.

</details>

<details>
<summary>non-goals, alternatives, prior art</summary>

Non-goals: no replica management or autoscaling (HPA and KEDA own
demand; this acts on waste), no admission control, no new telemetry
pipeline, no process-state restoration promise. Actions are observe,
suspend, delete.

Alternatives: extending Kyverno reuses matching and distribution, but
the acting contract is new work in either home and needs an upstream
sponsor that has not appeared; the annotation state machine we built
(capture 50) may ship as a reduced-contract policy pack, clearly
labeled; KEDA cannot address non-scalable workloads and its
suspendable-workloads request sits unassigned
([keda#7548](https://github.com/kedacore/keda/issues/7548));
exporter-only leaves the acting layer to scripts.

Closest prior art: kueue (native suspend via compiled adapters; here
the handle comes from definitions), karpenter (the disruption-budget
cap shape), cloud custodian (mark-for-op state in editable tags, the
argument for a real ledger), kube-green (scheduled suspension, fixed
types). The
[lab log](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/INTEGRATION-LOG.md)
compares Kyverno with the rules prototype and records the policy
workarounds tested.

</details>

<details>
<summary>test plan and versioning</summary>

- Unit: the allowance state machine (epoch transitions, restart
  recovery, no double action), the action resolver (explicit verb,
  skip-and-report), cap accounting including fail-closed, staleness and
  coverage gates.
- Integration on kind: observe to enforce promotion; suspend, user
  resume, fresh window; receipt survives workload deletion; gate
  closure stops dispatch; missing telemetry degrades to skip;
  coexistence with a GitOps controller that re-applies specs.
- Conformance per catalog type: suspend releases resources, resume
  behaves as the definition documents, destructive effects classified
  correctly, asserted by test.
- The exporter's series names and labels are a public versioned
  interface once they leave review (KEP-0004); renames are breaking.
- RuntimeRule and RuntimeRulesConfig are new CRDs in their own group,
  starting at v1alpha1. Definitions without a suspend handle stay
  valid and observe-only.

</details>

## Implementation history

- 2026-07-29: capability comparison against Kyverno v1.18.2 (research).
- 2026-08-19: exporter proof of concept, four types, one dashboard.
- 2026-09-10: comparison re-verified on Kyverno v1.19.1; thirty
  adjacent projects read for prior art.
- 2026-09-11: this KEP.
- 2026-09-14: external review applied; leanness pass.
- 2026-09-14 to 2026-09-16: live integration lab. The exporter, Kyverno
  v1.19.1 and a rules prototype ran on kind; sections 1 to 4 rebuilt
  from its captures. Real dynamo run added 2026-09-16.

The exporter and a rules prototype ran in the lab. The production
runtime rules API, the controller, and the action-effect contract
remain proposed.
