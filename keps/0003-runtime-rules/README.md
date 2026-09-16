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

A Karta definition already knows how to read a workload: which pods are
its, what its status means, which field suspends it. The exporter
(KEP-0004) publishes those facts as Prometheus series. This KEP adds the
part that acts on them: one rule for every described type, act once per
allowance, leave a receipt that survives the workload.

Sections 1 and 2 happened; sections 3 and 4 are what broke and the
proposal. Every measured number comes from
[a lab we ran](https://github.com/AviadHayumi/workload-map/tree/exporter-integration-lab/labs/exporter-kyverno-rr);
each section links its raw output in the fine print, and proposed things
are labeled.

## 1. What the exporter gives us, on a real workload

We took a real distributed inference workload: dynamo, one frontend, one
decode worker, one prefill worker (the Llama-3.1-8B mocker), run by the
real dynamo operator 1.2.1 on a kind cluster. All three pods were
Running and the operator reported `successful`. The exporter read the
catalog definition for this kind and published six metric families.
Real values from that run, labels shortened:

| Series | Type | What it says | For the dynamo above |
|---|---|---|---|
| `karta_workload_info` | gauge | the workload exists, and which definition read it (always 1) | `{workload="dynamo-smoke", karta="nvidia-com-dynamographdeployment-v1beta1"} 1` |
| `karta_workload_status` | gauge | one 0/1 line per phase | `{workload="dynamo-smoke", phase="Running"} 1` and eight phases at 0 |
| `karta_pod_workload_info` | gauge | this pod belongs to that workload and part (always 1, the join key) | `{pod="dynamo-smoke-prefill-...", component_instance="prefill"} 1`, one per pod |
| `karta_workload_component_replicas` | gauge | wanted count per part | `{component_instance="decode"} 1`, same for prefill and Frontend |
| `karta_workload_component_pods` | gauge | actual pods per part, by pod phase | `{component_instance="decode", phase="Running"} 1` |
| `karta_workload_generation` | gauge | the workload's `metadata.generation` | `{workload="dynamo-smoke"} 1` |

- The operator wrote `state: successful`; the definition turns that into
  `Running`, the same phase a Job gets, so one query fits both.
- Each pod came out labeled with its part: `Frontend`, `decode`,
  `prefill`. Nobody wrote dynamo code for this; the definition declares
  where pods hang.
- GPU and CPU metrics already exist per pod, and
  `karta_pod_workload_info` ties them to the workload and its parts.
  That join is the point.

How it works: watch events update a cache; a scrape reads it and counts
the pods. The always-1 series and the 0/1 phases are the same tricks
kube-state-metrics uses, so queries look the way people already write
them.

Things you could write with this, illustrative, not run in this lab:

- Suspend on idle GPU (the rule in section 4).
- Catch stuck starts: `Initializing` still 1 after 15 minutes.
- Replica shortfall: wanted vs actually `Running`, per part.
- One `Failed` or `Degraded` alert across kinds.
- Plot generation changes alongside the other series.

<details>
<summary>fine print: provenance, mechanics, the join, raw files</summary>

Provenance: cluster kind-karta-e2e (Kubernetes v1.34.0), dynamo-platform
1.2.1 installed by `hack/e2e/operators/dynamo/install.sh`, the smoke
DynamoGraphDeployment from `hack/e2e/operators/dynamo/smoke.yaml`
extended with a third service, prefill, running the mocker's
`--is-prefill-worker` mode. The exporter is the metrics-exporter branch
run with `--use-catalog`. The cluster view, edited (unrelated pods and
the RESTARTS column trimmed):

```console
$ kubectl get dgd,pods -n default
NAME           READY   BACKEND   AGE
dynamo-smoke   True              89s

NAME                                             READY   STATUS    AGE
dynamo-smoke-decode-e03467c0-8568474b87-tdfr6    1/1     Running   89s
dynamo-smoke-frontend-59d8d4c776-v7nx8           1/1     Running   88s
dynamo-smoke-prefill-e03467c0-7bf477dcd9-n6l6b   1/1     Running   88s
```

Raw files:
[the three-service scrape](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/61-dynamo-prefill-metrics.txt)
and
[the earlier two-service run](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/60-dynamo-real-metrics.txt).
They also show the operator's intermediate Deployments tracked as
workloads of their own kind.

Mechanics fine print: phase series exist for kinds whose definition maps
status, and replica counts when the definition provides them (that also
bounds the illustrative phase queries above). A phase-only pod update
skips the attribution rules; a scrape renders the cache and counts its
pod records. Inactive phases stay 0 while the workload is tracked;
duration queries still need freshness and sample-coverage checks,
including missing data. The six workload families are gauges; the
exporter's own health series add counters
(`karta_exporter_attribution_errors_total`) next to gauges for tracked
workloads, unattributed pods, and the last event timestamp.

The illustrative GPU join, not run in this dynamo test:

```promql
avg by (namespace, workload) (
  DCGM_FI_DEV_GPU_UTIL
  * on(namespace, pod) group_left(workload) karta_pod_workload_info
)
```

</details>

## 2. Wiring the facts into Kyverno

Prometheus scrapes the exporter every 5s, and a Kyverno mutate-existing
policy reads the history. Our lab has no GPUs, so the tested rule used
Running time as the stand-in for the GPU-idle join, same mechanism:
suspend when every `Running` sample in the last two minutes is 1. This
shortened yaml is not runnable; the one we ran also limits itself to
jobs in one namespace and skips jobs that are already suspended (full
yaml in the fine print):

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

It worked, both ways we tried:

- Direct query, above: policy applied at 17:45:19.049, job seen
  suspended at 17:45:19.693, 644ms. Second cluster: applied
  18:20:47.211, both trainers seen suspended at 18:20:50.608, 3.4s.
- Through Kyverno's cache (a GlobalContextEntry polling the same API):
  trainer-gce still unsuspended at 18:09:26, seen suspended at 18:09:59.

And this is what success looks like inside the machinery. Kyverno files
a work item per pass (an UpdateRequest), works it, marks it Completed,
deletes it; the job flips to `suspend=true`, its pod goes away, the job
controller writes a Suspended event. We captured one clean run:
trainer-hp, policy applied 19:56:09, suspended 19:56:26.

<details>
<summary>the raw happy-path run, and how to read it</summary>

```text
--- the work item lifecycle ---
--- (kubectl get updaterequests -n kyverno -w --output-watch-events) ---
EVENT      NAME       POLICY              RULETYPE     STATUS
ADDED      ur-g6lxv   metric-suspend-hp   cel-mutate
MODIFIED   ur-g6lxv   metric-suspend-hp   cel-mutate   Pending
MODIFIED   ur-g6lxv   metric-suspend-hp   cel-mutate   Completed
DELETED    ur-g6lxv   metric-suspend-hp   cel-mutate   Completed
(a second identical round follows on the next pass)

--- the job now ---
suspend=true active=

--- events on the job ---
24s    Warning  PolicyViolation   policy metric-suspend-hp/ fail: mutation is not applied
2m43s  Normal   SuccessfulCreate  Created pod: trainer-hp-bf2j5
9s     Normal   SuccessfulDelete  Deleted pod: trainer-hp-bf2j5
9s     Normal   Suspended         Job suspended
```

How to read the work item: ADDED with an empty status is the item being
filed, Pending means waiting to be processed, Completed is processed,
DELETED is cleanup. Keep this four-step shape in mind; the broken rule
in section 3 shows the exact same one.

One surprise worth knowing: the "mutation is not applied" warning fired
here too, in a healthy run. It precedes the Suspended event by about 15
seconds, which fits a reporting scan seeing the mutation before it was
applied. The same message the broken rule produces. Raw file:
[the happy path](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/62-happy-path.txt).

</details>

Other conditions the same series hand to a policy author, none of them
run in this lab:

- A deploy grace: skip any workload whose generation changed in the
  last 10 minutes.
- Only workloads with multiple parts: count their component instances.
- Do not kick something already hurt: skip when `Degraded` is 1.
- A rollout guard: block acting while any part has `Pending` pods.
- An activity guard: pause when the exporter has processed no events
  recently.
- Filter by definition: select the `karta` label on
  `karta_workload_info` and join it into the condition.

Reading the facts is not the gap.

<details>
<summary>fine print: what the query checks, raw files</summary>

The condition asks whether every `Running` sample in the trailing 2
minutes equals 1. It does not check that 2 full minutes of samples
exist; a young series can pass early. The production contract in
section 4 requires coverage checks. Timestamps are when our checks first
saw each change, not exact mutation times.

On the condition list above: the exporter's last-event timestamp is
global, so other workloads can keep it recent and a quiet healthy
cluster can leave it old; it does not prove the target's data is fresh.
And the `karta` label lives on `karta_workload_info`, not on every
series or on the Kubernetes object; the policy still needs its own
target scope and patch.

Raw files:
[the tested manifest](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/manifests/05b-kyverno-metric-suspend-http.yaml),
[the 644ms run](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/10-v2-http-timeline.txt),
[the second-cluster run](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/30-lab2-kyverno-phase.txt),
[the cache run](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/19-gce-corrected.txt).

</details>

## 3. The gaps the runs hit

Three gaps. Each one measured, each one a thing the happy path above
hides.

### Failures look like success

We broke a rule on purpose: its expression fails at runtime, every time.
Then we watched.

- The work items ran the exact same four steps as the happy path:
  filed, Pending, Completed, deleted. Completed means processed, not
  succeeded.
- The job: untouched. The policy: still ready.
- The captured log search found no lines naming the policy.
- The captured diagnostics: the same "mutation is not applied" warning
  the healthy run produced mid-flight, and an error counter at 4.
  Neither carries the actual error or points at the failing expression.

So the work items and the warning cannot tell a failed rule from one
still in flight. The one thing that flags the failure is a counter, and
it carries no error text.

<details>
<summary>the raw broken-rule run</summary>

```text
ADDED      ur-97xsp   rr-cel-runtime-error   cel-mutate
MODIFIED   ur-97xsp   rr-cel-runtime-error   cel-mutate   Pending
MODIFIED   ur-97xsp   rr-cel-runtime-error   cel-mutate   Completed
DELETED    ur-97xsp   rr-cel-runtime-error   cel-mutate   Completed
(4 rounds like this; job suspend=false; policy ready: true)
```

Raw file:
[the broken rule](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/31-cel-error-repro.txt).
The counter that moved:
`kyverno_mutating_policy_execution_duration_seconds_count{result="error"}`
reached 4, with the policy name as a label but no error text.

</details>

### A user resume gets reverted

- A human resumed a suspended job. 123.947s later the rule suspended it
  again.
- Not a bug. Convergence engines re-apply desired state; that is their
  contract. This rule had no saved allowance, nothing that says "a
  human decided otherwise".

### Nothing remembers

- We deleted the acted-on job, then went looking for what had been done
  to it.
- No reports left, no work items, just seven events that expire on
  their own. None tied the action to its evaluation and evidence.
- Turning on more reporting does not change it: those report entries
  belong to the workload and die with it, and cluster reports only hold
  cluster-scoped resources.

We did try to close the resume gap with authoring alone: two policies
and a state annotation held a user resume for 7 minutes under the 60s
scan. So act-once is writable. But the state lived in an editable job
annotation, that policy pack wrote no receipt, and the run did not
resolve the known write races.

<details>
<summary>raw files for this section</summary>

[The resume revert](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/30-lab2-kyverno-phase.txt),
[the after-deletion search](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/17-kyverno-forensics.txt),
[the annotation state machine](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/50-annotation-epoch-spike.txt).

</details>

## 4. What runtime rules adds

One rule, three parts: pick workloads, set a condition, choose an
action. The definition tells the controller how to suspend each kind.
Proposed API; the lab prototype tested the Running-history condition on
Jobs:

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

The proposed lifecycle; the lab tested the re-arm-off branch:

![one allowance: act once, then the human owns it](kep-rr-allowance-flow.png)

Lab timings in the diagram combine trainer-e's action and trainer-f's
resume.

In words: when the condition holds, write an Intended receipt, patch,
write Executed. That allowance is now spent; the rule does not fire
again on its own. When a human resumes, write ResumeDetected and reset:
by default a full new condition window must pass before the rule may act
again, or the rule escalates to a human instead.

What the prototype actually did, on the same cluster and the same series
Kyverno used:

- With re-arming off, it left the resumed job running. It saw the resume
  in 0.692s, and where Kyverno had re-suspended (123.947s, a different
  job on the same cluster), it wrote an escalation receipt instead
  (124.526s). The job was still running 300s after the resume:

```text
[18:28:44.598] receipt ResumeDetected      (0.692s after the resume)
[18:30:48.432] receipt EscalatedNeedsHuman (instead of re-suspending)
[18:33:44.053] trainer-f suspend=false active=1
```

- Receipts survive. Intent lands before the patch (Intended at
  18:26:34.226, Executed at 18:26:34.539). We deleted the job: four
  receipts stayed, Intended, Executed and two WouldAct. The Executed
  one, selected fields; Executed means the patch was accepted, not that
  the controller converged:

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

- Not being able to act is a state you can see. Permission revoked:
  SkippedNotReady. Restored: Intended, then Executed. Observe mode:
  WouldAct, jobs untouched. A kind with no suspend handle:
  SkippedNoHandle.

Still to build: allowance epochs that re-arm on a user resume by default
(the tested run had re-arming off), observe-by-default install with a
global gate as the kill switch, and blast-radius caps. Kyverno keeps
admission and compliance and can read the same series; this controller
adds the acting contract. Dynamo: reading it works, rules over it are
proposed, and without a suspend handle in its definition they would only
observe.

<details>
<summary>the proposed contract, in full, plus raw files</summary>

- Enforcement needs the global gate opened and the rule set to enforce.
  Gate closure stops new dispatches; accepted writes complete and stay
  in receipts.
- A receipt is persisted before a destructive act. No receipt, no
  action. Receipts are never silently rewritten; lost API responses
  reconcile to Unknown before any retry. The full receipt also carries
  uid, time, and operation identity.
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

Raw files:
[observe mode](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/20-rr-observe.txt),
[enforce and intent ordering](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/21-rr-enforce.txt),
[the resume and escalation](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/22-rr-anti-trap.txt),
[permission revoke and restore](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/23-rr-rbac.txt),
[receipts after deletion](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/24-rr-forensics.txt).

</details>

<details>
<summary>non-goals, alternatives, prior art</summary>

Non-goals: no replica management or autoscaling (HPA and KEDA own
demand; this acts on waste), no admission control, no new telemetry
pipeline, no process-state restoration promise. Actions are observe,
suspend, delete.

Alternatives: extending Kyverno reuses matching and distribution, but
the acting contract is new work in either home and needs an upstream
sponsor that has not appeared; the annotation state machine we built may
ship as a reduced-contract policy pack, clearly labeled; KEDA cannot
address non-scalable workloads and its suspendable-workloads request
sits unassigned
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
<summary>test plan, versioning, history</summary>

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
  starting at v1alpha1. Definitions without a suspend handle stay valid
  and observe-only.

History: 2026-07-29 capability comparison against Kyverno v1.18.2;
2026-08-19 exporter proof of concept (four types, one dashboard);
2026-09-10 re-verified on v1.19.1, thirty adjacent projects read;
2026-09-11 this KEP; 2026-09-14 external review, leanness pass;
2026-09-14 to 2026-09-16 the live integration lab, sections rebuilt from
its captures, real dynamo and happy-path runs added 2026-09-16.

</details>

The exporter and a rules prototype ran in the lab. The production
runtime rules API, the controller, and the action-effect contract remain
proposed.
