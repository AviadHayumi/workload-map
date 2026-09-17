<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# Metrics exporter integration to Kyverno + runtime rules proposal

## 1. How the metrics exporter works

<details>
<summary>the six metrics, real values from a dynamo run, and what you can build on them</summary>

Tested with a real dynamo Llama-3.1-8B mocker (frontend, decode,
prefill), real operator, all three pods Running, state `successful`.

The exporter published six metrics about it, all gauges:


| Metric | On the labels | The value | What you can do with it | From the dynamo run |
|---|---|---|---|---|
| `karta_workload_info` | workload, kind, namespace, and `karta` = which karta definition read it | always 1, the metric is its labels | list every workload the exporter tracks, any kind, in one query. count them per namespace or per karta definition | `{workload="dynamo-smoke", karta="nvidia-com-dynamographdeployment-v1beta1"} 1` |
| `karta_workload_status` | workload + `phase` | 1 = in that phase now, 0 = not. several phases can be 1 at once | alert on "Failed for 5 minutes" or act on "Running for 2 hours" with one rule for every kind | `{phase="Running"} 1`, the other eight at 0 |
| `karta_pod_workload_info` | `pod` + workload + `component` + `component_instance` | always 1, the metric is its labels | join it with dcgm or cadvisor and you get gpu and cpu per workload and per part | `{pod="dynamo-smoke-prefill-...", component_instance="prefill"} 1`, one per pod |
| `karta_workload_component_replicas` | workload + `component_instance` | how many pods the spec wants for that part | the "wanted" side: 2 decode workers wanted | `{component_instance="decode"} 1`, same for prefill and Frontend |
| `karta_workload_component_pods` | workload + `component_instance` + pod `phase` | how many pods that part has right now | the "actual" side: only 1 decode Running, alert. or "a part still has Pending pods", wait | `{component_instance="decode", phase="Running"} 1` |
| `karta_workload_generation` | workload | the `metadata.generation` number | draw a line on the graph every time the spec changed, so you can see if a gpu dip lines up with a deploy | `{workload="dynamo-smoke"} 1` |

Things you can build on these. None ran in our lab:

- Suspend on idle gpu: dcgm publishes per pod, the join key turns it
  into "this trainer sat under 5% gpu for an hour, suspend it".
- Same per part: decode workers idle while prefill is busy? Scale down
  just the decode part.
- Same trick for cpu: "this notebook used less than a tenth of a core
  all day, tell its owner".
- Split a bill: OpenCost already prices every pod. Join its per-pod
  cost to the join key and you get cost per workload, per part, per
  team.
- Compare how busy prefill and decode are before deciding what to
  resize.
- Catch stuck starts: `Initializing` still 1 after 90 minutes.
- Skip fresh deploys: generation changed in the last 10 minutes, leave
  it alone.
- Do not kick something already hurt: `Degraded` is 1, skip.
- Pick targets by definition instead of listing kinds: the `karta`
  label.

if you want to see how metrics exporter works from inside
https://aviadhayumi.github.io/workload-map/courses/karta-metrics/sequence/

</details>

## 2. How we connected it to Kyverno

- Prometheus scrapes the exporter (every 5 seconds).
- A Kyverno mutate-existing policy uses that history to change jobs
  already in the cluster.
- every available `Running` workload with duration of at least 2 minutes get suspended by kyverno
- We resumed the job by hand. Once it had 2 minutes of Running again,
  the rule suspended it again.

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

Without `offset 2m` a brand-new job gets suspended 8 seconds after
creation: every sample it has is `1`, so the rule is happy on the first
sample. A resumed job never had that problem, its window still holds
the 0s from the suspended time.

## 3. The gaps

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
reports this bug. [#17063](https://github.com/kyverno/kyverno/pull/17063)
fixes it. The PR has been open since 2026-08-11, CI green, no reviewer.
The same fix for GeneratingPolicy merged that same day. We built 1.19.1
with the patch and reran the broken rule: the work items go `Failed`
with the conversion error in their status. The log shows it too. Healthy
rules still land. Not in any release yet.

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
7 minutes under the 60s scan. So you can build it with policies. But
anyone with write access can edit the annotation. Nothing records what
was done. And the background writer reads the job again and overwrites
it, so two passes can lose a stamp (#17284).

</details>

### 3. Nothing remembers

We deleted the job Kyverno had suspended and went looking for what it
had done. Nothing. No report, no work item, only events that expire on
their own.

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

The rule targeted trainer-one, but the report scanner also warned about
trainer-h. It does not apply the target conditions.

</details>

## 4. What runtime rules gives you

- You write one rule and it covers every kind whose karta definition
  has a suspend handle. No per-kind code, no per-kind query.
- It acts once. When someone resumes a job by hand, it stays resumed.
  Nobody fights the human.
- You always know what it did, when, and why. Delete the workload, the
  record stays.
- When it cannot act, it says so. No more "completed" that did nothing.
- It starts by watching. You see what it would have done before you let
  it touch anything.
- One switch turns everything off, and a cap says how much it may touch
  at once.
- Kyverno keeps checking incoming requests and reporting rule
  violations. Runtime rules adds the acting part.
