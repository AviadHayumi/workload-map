<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# Runtime rules

## 1. What the exporter gives us, on a real workload

Tested with a real dynamo Llama-3.1-8B mocker (frontend, decode,
prefill), real operator, all three pods Running, state `successful`.

The exporter published six metrics about it, all gauges:


| Metric | On the labels | The value | What you can do with it | From the dynamo run |
|---|---|---|---|---|
| `karta_workload_info` | workload, kind, namespace, and `karta` = which karta definition read it | always 1, the metric is its labels | list every workload in the cluster, any kind, in one query. count them per namespace or per karta definition | `{workload="dynamo-smoke", karta="nvidia-com-dynamographdeployment-v1beta1"} 1` |
| `karta_workload_status` | workload + `phase` | 1 = in that phase now, 0 = not. several phases can be 1 at once | alert on "Failed for 5 minutes" or act on "Running for 2 hours" with one rule for every kind | `{phase="Running"} 1`, the other eight at 0 |
| `karta_pod_workload_info` | `pod` + workload + `component` + `component_instance` | always 1, the metric is its labels | join it with dcgm or cadvisor and you get gpu and cpu per workload and per part | `{pod="dynamo-smoke-prefill-...", component_instance="prefill"} 1`, one per pod |
| `karta_workload_component_replicas` | workload + `component_instance` | how many pods the spec wants for that part | the "wanted" side: 2 decode workers wanted | `{component_instance="decode"} 1`, same for prefill and Frontend |
| `karta_workload_component_pods` | workload + `component_instance` + pod `phase` | how many pods that part has right now | the "actual" side: only 1 decode Running, alert. or "a part still has Pending pods", wait | `{component_instance="decode", phase="Running"} 1` |
| `karta_workload_generation` | workload | the `metadata.generation` number | draw a line on the graph every time the spec changed, so a gpu dip lines up with the deploy that caused it | `{workload="dynamo-smoke"} 1` |

Things you can build on these metrics. None of these ran in our lab:

- Suspend on idle gpu: dcgm publishes per pod, the join key turns it
  into "this trainer sat under 5% gpu for an hour, suspend it".
- Same per part: decode workers idle while prefill is busy? Scale down
  just the decode part.
- Same trick for cpu: "this notebook used less than a tenth of a core
  all day, tell its owner".
- Split a bill: join pod gpu allocation-hours (opencost) to workload and part,
  then apply prices. 
- Or let OpenCost do the pricing: it already prices every pod. Join its
  per-pod cost to the join key and you get cost per workload, per part,
  per team.
- Investigate uneven load: compare prefill and decode utilization
  before deciding what to resize.
- Catch stuck starts: `Initializing` still 1 after 90 minutes.
- A part is missing pods: the spec wants 2 decode workers, only 1 is
  Running. Alert on the difference.
- One `Failed` or `Degraded` alert across kinds.
- Skip fresh deploys: generation changed in the last 10 minutes, leave
  it alone.
- Do not kick something already hurt: `Degraded` is 1, skip.
- Pick targets by definition instead of listing kinds: the `karta`
  label.

## 2. Integrating with Kyverno

- Prometheus scrapes the exporter (every 5 seconds).
- A Kyverno mutate-existing policy acts on the history.
- every available `Running` workload with duration of at least 2 minutes get suspended by kyverno
- We resumed a suspended job by hand. Once it had 2 minutes of Running
  again, the rule suspended it again after 2 minutes.

```yaml
apiVersion: policies.kyverno.io/v1beta1
kind: MutatingPolicy
spec:
  evaluation:
    admission: {enabled: false}
    mutateExisting: {enabled: true}
  targetMatchConditions:
    - name: running-2m-and-existed-2m-ago
      expression: >-
        object.metadata.name in
        http.Get('http://prometheus...query=
          min_over_time(karta_workload_status{namespace="ai-team",
            phase="Running",workload_kind="Job"}[2m]) == 1
          and karta_workload_status{namespace="ai-team",
            phase="Running",workload_kind="Job"} offset 2m == 1')
        .data.result.map(r, r.metric.workload)
  mutations:
    - patchType: JSONPatch
      jsonPatch:
        expression: "[JSONPatch{op: 'add', path: '/spec/suspend', value: true}]"
```

<details>
<summary>watch one run happen, end to end</summary>

We created a new job called trainer-one,
created the policy as soon as its pod was Running,
waited for the rule to suspend it,
resumed it by hand,
waited again. One run, one log:

```text
--- part 1: new job, the rule waits for it to be 2 minutes old ---
23:08:02 pod Running, policy created
23:08:03 t+2s    value 2m ago=no-series  min[2m]=0  suspend=false
23:08:32 t+32s   value 2m ago=no-series  min[2m]=0  suspend=false
23:09:02 t+62s   value 2m ago=no-series  min[2m]=0  suspend=false
23:09:32 t+92s   value 2m ago=no-series  min[2m]=0  suspend=false
23:10:02 t+122s  value 2m ago=0          min[2m]=1  suspend=false
23:10:32 t+152s  value 2m ago=1          min[2m]=1  suspend=false
23:10:57 t+177s  trainer-one suspend=true   <- suspended by the rule

--- part 2: a human resumes it, the rule waits again ---
23:11:02 kubectl patch job trainer-one --type=merge -p '{"spec":{"suspend":false}}'
23:11:03 r+0s    value 2m ago=1  min[2m]=0  suspend=false
23:11:32 r+30s   value 2m ago=1  min[2m]=0  suspend=false
23:12:02 r+60s   value 2m ago=1  min[2m]=0  suspend=false
23:12:32 r+90s   value 2m ago=1  min[2m]=0  suspend=false
23:13:02 r+120s  value 2m ago=0  min[2m]=1  suspend=false
23:13:32 r+150s  value 2m ago=1  min[2m]=1  suspend=false
23:13:57 r+175s  trainer-one suspend=true   <- suspended again by the rule

--- the work item lifecycle ---
--- (kubectl get updaterequests -n kyverno -w --output-watch-events) ---
EVENT      NAME       POLICY               RULETYPE     STATUS
ADDED      ur-2z87p   metric-suspend-one   cel-mutate
MODIFIED   ur-2z87p   metric-suspend-one   cel-mutate   Pending
MODIFIED   ur-2z87p   metric-suspend-one   cel-mutate   Completed
DELETED    ur-2z87p   metric-suspend-one   cel-mutate   Completed
(six more identical rounds over the run, one per pass; the two suspensions were the fourth and the last)

--- the job now ---
suspend=true active=

--- events on the job ---
6m2s   Warning  PolicyViolation   policy metric-suspend-one/ fail: mutation is not applied
5m32s  Warning  PolicyViolation   policy metric-suspend-one/ fail: mutation is not applied
2m38s  Warning  PolicyViolation   policy metric-suspend-one/ fail: mutation is not applied
6m5s   Normal   SuccessfulCreate  Created pod: trainer-one-49wl8
3m8s   Normal   SuccessfulDelete  Deleted pod: trainer-one-49wl8
3m3s   Normal   SuccessfulCreate  Created pod: trainer-one-9h7mp
3m3s   Normal   Resumed           Job resumed
8s     Normal   Suspended         Job suspended
8s     Normal   SuccessfulDelete  Deleted pod: trainer-one-9h7mp
```

Kyverno does not patch your job straight from the policy.
Every time it wants to change an existing object it
writes itself a small to-do object, an UpdateRequest cr, and a background
worker picks it up.

- ADDED, status empty: Kyverno wrote the to-do. Nothing evaluated yet.
- Pending: a worker picked it up and is about to run the rule against
  the job.
- Completed: the worker is done with the to-do. Done, not "the patch
  happened". A run where the rule errored also ends here.
- DELETED: Kyverno throws finished to-dos away within a second. After a
  run there is nothing left to look at.

`offset 2m` in the metrics are very important
if we dont set it job will be suspended after resume even if the job is alive for 10 seceond
user should be aware of this

</details>

<details>
<summary>fine print: what the query checks</summary>

</details>

## 3. The gaps the runs hit

Three gaps. For each one: what we did, what Kyverno showed, and whether
upstream can fix it.

### 1. Failures look like success

We broke a rule on purpose: `int(object.metadata.name) > 0`, on a job
named `cel-error-job`. It blows up on every run.

- The work items? The same four steps as the happy path. Completed,
  deleted.
- The job? Untouched. The policy? `ready: true`.
- The logs? Not one line names the policy.
- What did exist: the same "mutation is not applied" warning a healthy
  run fires mid-flight, and an error counter with no error text.

A rule that fails every time looks exactly like a rule mid-flight.

Upstream knows. [#17062](https://github.com/kyverno/kyverno/issues/17062)
is this exact bug and [#17063](https://github.com/kyverno/kyverno/pull/17063)
fixes it: open since 2026-08-11, CI green, no reviewer, while the same
fix for GeneratingPolicy merged the same day. We built 1.19.1 with that
patch and reran the broken rule: the work items go `Failed` with the
real error in their status, `type conversion error from 'string' to
'int'`, and the log says it too. Healthy rules still land. Not in any
release yet.

<details>
<summary>the raw broken-rule run, stock and patched</summary>
comment : if we describe this ? we cant get data explain what happend ? 

No. On stock 1.19.1 nothing you can describe explains it:

- Describe the job: one PolicyViolation event, "mutation is not
  applied", no reason attached.
- Describe the policy: ready true, message empty.
- Describe the work item: gone within a second, and its message field
  was empty while it lived.

```text
--- stock 1.19.1 ---
ADDED      ur-97xsp   rr-cel-runtime-error   cel-mutate
MODIFIED   ur-97xsp   rr-cel-runtime-error   cel-mutate   Pending
MODIFIED   ur-97xsp   rr-cel-runtime-error   cel-mutate   Completed
DELETED    ur-97xsp   rr-cel-runtime-error   cel-mutate   Completed
(4 rounds like this; job suspend=false; policy ready: true)
kyverno_mutating_policy_execution_duration_seconds_count{policy_name="rr-cel-runtime-error",result="error"} 4

--- 1.19.1 + PR 17063 ---
NAME       STATE     RETRY  MESSAGE
ur-vgwsj   Completed                (the policy-created event, still silent)
ur-rsz6b   Failed    1      mpol rr-cel-runtime-error rule evaluation error: failed to evaluate policy: type conversion error from 'string' to 'int'
ur-rsz6b   Pending   1      (same message)
ur-rsz6b   Failed    2      (same message)
...
log: ERR failed to evaluate mpol rule error="... type conversion error from 'string' to 'int'" mpol=rr-cel-runtime-error
```

Left open even with the patch: the first work item, the policy status,
and the events all still look fine.

</details>

### 2. A user resume gets reverted

A human resumed a suspended job. The rule suspended it again (section
2, part 2).

Not a bug. Convergence re-applies desired state, that is its job. The
rule has no memory that a human decided otherwise, and nothing upstream
offers one: no issue asks for it.
[#16214](https://github.com/kyverno/kyverno/issues/16214) is only the
periodic re-check, already merged, and
[#17284](https://github.com/kyverno/kyverno/issues/17284) is the write
race that bites any workaround built on the object itself.

<details>
<summary>the workaround we tried</summary>

Two policies and a state annotation on the job: one suspends and stamps
"we-suspended", the other sees a human resume and stamps
"user-resumed", and the first refuses stamped jobs. It held a resume for
7 minutes under the 60s scan. So act-once is writable. But the state is
an annotation anyone with write access can edit, nothing records what
was done, and the write race in #17284 is real: the background writer
re-fetches and overwrites, so two passes can lose a stamp.

</details>

### 3. Nothing remembers

We deleted the acted-on job and went looking for what had been done to
it. Nothing. No report, no work item, only events that expire on their
own.

Then we turned on everything Kyverno offers, success events and
mutate-existing reporting, and did it again. Before deletion: one report
line, `pass`, `success`, owned by the job, and no Kyverno event on the
job at all. After deletion: zero. Upstream closed this as done
([#3837](https://github.com/kyverno/kyverno/issues/3837),
[#2160](https://github.com/kyverno/kyverno/issues/2160)); done means a
line that dies with the workload.

<details>
<summary>what existed before and after, every opt-in on</summary>

```text
generateSuccessEvents=true, reporting.mutateExisting=true (chart default)
23:38:57 trainer-one suspend=true   <- suspended by the rule

--- before deleting the job ---
events on the job:    Resumed, SuccessfulCreate, Suspended, SuccessfulDelete   (all from job-controller)
events on the policy: PolicyViolation  Job ai-team/trainer-one: fail; mutation is not applied
                      PolicyViolation  Job ai-team/trainer-h:   fail; mutation is not applied
report:               PolicyReport owner=trainer-one policy=metric-suspend-one result=pass msg=success
work items:           0

--- after deleting the job ---
events on the job:    the same job-controller lines, until they expire
reports referencing the job: 0
work items:           0
```

Note trainer-h in the policy events: the rule was scoped to trainer-one
only, and the warning fired for trainer-h anyway. The report scanner
does not apply the target conditions.

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
  one, selected fields (Executed means the patch was accepted, not that
  the controller converged):

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
<summary>the proposed contract, in full</summary>

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
types).

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

<details>
<summary>raw outputs, all of them</summary>

Everything ran in
[the integration lab](https://github.com/AviadHayumi/workload-map/tree/exporter-integration-lab/labs/exporter-kyverno-rr)
(kind, Kubernetes v1.34.0 and v1.34.3, Kyverno chart 3.9.1 with a 60s
background scan, the exporter from the metrics-exporter branch with
`--use-catalog`). The files behind each section:

- Section 1:
  [the three-service dynamo scrape](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/61-dynamo-prefill-metrics.txt)
  and
  [the earlier two-service run](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/60-dynamo-real-metrics.txt).
  The dynamo is the e2e smoke DynamoGraphDeployment plus a prefill
  service running the mocker's `--is-prefill-worker` mode.
- Section 2:
  [the one test: new job, suspended, resumed by hand, suspended again](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/67-one-test.txt),
  [its manifest](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/manifests/05e-kyverno-metric-suspend-offset.yaml),
  [the original query's manifest](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/manifests/05b-kyverno-metric-suspend-http.yaml),
  [the old-query new-job test, 8 seconds](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/63-resume-window.txt),
  [the sample-count variant we dropped](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/65-happy-path-coverage.txt).
- Section 3:
  [the broken rule on stock 1.19.1](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/31-cel-error-repro.txt),
  [the broken rule on 1.19.1 plus PR 17063](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/68-gap1-pr17063.txt),
  [a healthy rule on the patched controller](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/70-healthy-on-pr17063.txt),
  [the after-deletion search](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/17-kyverno-forensics.txt),
  [every opt-in on, then delete](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/69-gap3-optins.txt),
  [the annotation workaround](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/50-annotation-epoch-spike.txt).
- Section 4:
  [observe mode](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/20-rr-observe.txt),
  [enforce and intent ordering](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/21-rr-enforce.txt),
  [the resume and escalation](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/22-rr-anti-trap.txt),
  [permission revoke and restore](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/23-rr-rbac.txt),
  [receipts after deletion](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/captures/24-rr-forensics.txt).
- The whole story with every step:
  [the lab log](https://github.com/AviadHayumi/workload-map/blob/exporter-integration-lab/labs/exporter-kyverno-rr/INTEGRATION-LOG.md).

</details>

The exporter and a rules prototype ran in the lab. The production
runtime rules API, the controller, and the action-effect contract remain
proposed.
