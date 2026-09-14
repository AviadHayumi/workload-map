<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# KEP-0003: Runtime rules

- Status: provisional
- Authors: @AviadHayumi
- Created: 2026-09-11
- Depends on: [KEP-0001](../0001-cel-expressions/README.md) (CEL expressions)
- Tracking issue: to be opened before this KEP merges

## Summary

Idle accelerators consume capacity and incur cost. A training job hangs
after epoch 40 and keeps its 64 GPUs. An inference endpoint nobody calls
holds 8 more. Today no tool acts on this with one rule: engines either
cannot address the workload, cannot express the condition without a
hand-written query per type, or act with a verb that does not fit.

This KEP proposes two components on top of Karta definitions. A metrics
exporter that republishes the structure a definition already declares as
Prometheus series, so "this JobSet is idle" becomes a fact any consumer can
read. And a runtime rules controller that acts on such facts: one
filter / condition / action rule, enforced across every described type,
acting once per allowance window through the type's own suspend handle,
with a receipt and a kill switch.

![one rule, any described workload](kep-rr-big-picture.png)

Diagram sources sit next to this file as `.excalidraw`; open them at
excalidraw.com to edit.

## Motivation

Take one concrete request an admin actually makes: "suspend a workload when
its GPU utilization stays below 5% for an hour."

Walk it through the tools that exist today:

- KEDA's ScaledObject route requires a scalable target: `scaleTargetRef`
  needs a `/scale` subresource and a JobSet has none. ScaledJob creates
  Jobs for queue work; neither path supplies native suspension of an
  existing JobSet. The trigger is a PromQL string the admin writes and
  maintains by hand. Their tracker carries an open, unassigned request for
  suspendable workloads
  ([keda#7548](https://github.com/kedacore/keda/issues/7548), opened
  March 2026).
- Kyverno owns the policy half convincingly: matching, cluster versus
  namespace authority, distribution. It supplies per-policy cron deletion
  and global periodic mutate-existing execution (default interval one
  hour, configurable). What it does not supply is the allowance protocol
  this KEP proposes. In a run against v1.19.1 (2026-09-10), an
  unconditional suspend policy re-suspended a user-resumed Job after 43
  seconds; the scan was configured to 60 seconds, and no further user
  write triggered the re-suspension. That tests that policy's convergence
  behavior, not the impossibility of a smarter one; the allowance,
  receipt and budget semantics below simply have no supplied
  implementation there today. A related tracker request for background
  time and status re-evaluation is open with no substantive response
  visible ([kyverno#16214](https://github.com/kyverno/kyverno/issues/16214),
  opened June 2026).
- Kubernetes itself has carried the missing-verb discussion since January
  2022: the generic suspend subresource issue remains open and frozen,
  and no associated KEP was identified in this review
  ([kubernetes#107294](https://github.com/kubernetes/kubernetes/issues/107294)).

Two things are missing everywhere, and they are the two things a Karta
definition already declares:

- Perception. Which pods form this workload, which of them hold GPUs, what
  its status means. Today that knowledge lives in a query string an admin
  maintains per type.
- Actuation. How to stop this type without destroying it. `.spec.suspend`
  here, `.spec.runPolicy.suspend` there, an annotation elsewhere.

A definition closes both gaps once, declaratively. This KEP makes that
knowledge operational.

### Goals

- Republish definition structure as metric series: a pod-to-workload join
  key and normalized status, versioned, facts only.
- One rule across all described types: filter and condition are type
  agnostic, only the action is type specific and it resolves through the
  definition, never through controller code.
- Actions execute once per allowance epoch: suspend through the type's
  native handle, resume belongs to the user, and a resumed workload gets a
  fresh window instead of an instant re-action.
- Safe by default: the controller installs observing, a service-level gate
  caps everything to observe and doubles as the kill switch, actions are
  bounded by blast-radius caps, unsupported actions are skipped and
  reported.
- Every action is answerable: a reason on the workload, and a receipt that
  survives the workload's deletion.
- A new type becomes governable by writing a definition, not by forking
  the controller.

### Non-goals

- No replica management and no autoscaling. Demand-driven scaling belongs
  to HPA and KEDA; this acts on waste, and composes with both.
- No admission control, no mutation compliance, no general policy engine.
  Kyverno keeps that ground; the relationship section below shows how the
  two compose.
- No new telemetry pipeline. The exporter republishes attribution facts;
  utilization comes from the cluster's existing pod metrics (DCGM,
  kubelet), joined in the metrics store.
- No promise of process-state restoration. Suspend preserves the workload
  object where the type supports it; the processes restart and in-flight
  progress may be lost. Delete is destructive and only ever happens as an
  explicitly requested rule action.
- Actions are bounded to workload lifecycle: observe, suspend, delete.
  Nothing else.

## Proposal

Three parts, in reading order: what a definition must supply, what the
exporter publishes, and what the rules controller does with both.

### The contract a definition supplies

The existing Karta schema supplies the raw material:

- normalized status: the `statusMappings` block that maps raw fields to
  initializing / running / suspended / completed / failed
- pod attribution: `podSelector` per component
- a suspend handle: the `suspendDefinition` block

The JobSet catalog definition carries this handle today
(`docs/catalog/jobset-x-k8s-io-jobset-v1alpha2.yaml`):

```yaml
suspendDefinition:
  suspendActions:
    - path: .spec.suspend
      value: "true"
  resumeActions:
    - path: .spec.suspend
      value: "false"
```

These fields are assignments, not a classification of their effects.
Whether a stop is clean, destructive (RayJob suspension tears down the
RayCluster and reruns from scratch), or configuration dependent (a
StatefulSet scaled down with a delete-on-scale claim policy loses
volumes) is information the schema does not carry today. Before
implementation, a capability contract must specify how a definition
declares action effects and configuration-dependent restrictions, whether
as an annotation contract, a separate descriptor, or a schema addition.
That choice is an unresolved dependency of this KEP, deliberately left to
the implementation review. Until a definition's action classification is
known and its type has passed conformance, the type is observe-only.

Describable is not the same as governable. A definition without a
`suspendDefinition` (Knative Service has no hold mechanism at all) still
gets observe and delete; suspend is skipped and reported.

### The metrics exporter

The exporter reads the Kubernetes API only and republishes what
definitions declare, as series. It emits facts and never aggregates;
consumers join and aggregate in their own metrics store.

| Series | Meaning |
|---|---|
| `karta_pod_workload` | the join key: one series per attributed pod, labels for workload, component, component instance |
| `karta_workload_status` | one 0/1 series per normalized phase per workload |
| `karta_workload_info` | identity anchor: GVK and governing definition |
| `karta_exporter_freshness` | time since last successful reconciliation, so consumers can refuse stale attribution |

Series names and the label set are provisional until the implementation
review. The attribution contract must carry enough identity to join
safely: namespace and pod name at minimum, with workload identity that
distinguishes same-named workloads across namespaces and name reuse after
deletion. When a pod has two described owners along an ownership chain,
metrics attribute to the top-level owner only, so every pod counts
exactly once.

A proof-of-concept (2026-08-19) drove one Grafana dashboard across
JobSet, PyTorchJob, LeaderWorkerSet and Deployment with zero per-type
code, and surfaced a real imbalance: one component instance at 92%
utilization while its sibling sat at 2.5%.

![attribution: from pods to a workload fact](kep-rr-attribution.png)

The join is the point. A single-cluster illustration, joining on
namespace and pod and grouping by namespace and workload:

```promql
avg by (namespace, workload) (
  DCGM_FI_DEV_GPU_UTIL
  * on(namespace, pod) group_left(workload) karta_pod_workload
)
```

This is shipped as a recording rule, not authored per type. The
expression computes a current average; the controller separately
evaluates threshold duration and sample coverage before any action, and
the rule that makes the join safe (unique attribution rows per pod
identity, normalized join keys for the metric source) is part of the
exporter contract, not left to each consumer.

### The runtime rules controller

One rule, three parts, evaluated left to right:

```yaml
apiVersion: rules.run.ai/v1alpha1   # provisional group, fixed in the implementation PR
kind: RuntimeRule
metadata:
  name: reclaim-idle-training
spec:
  filter:
    types:
      - apiVersion: jobset.x-k8s.io/v1alpha2
        kind: JobSet
      - apiVersion: kubeflow.org/v1
        kind: PyTorchJob
    namespaceSelector:
      matchLabels:
        team: research
  condition:
    gpuIdle:
      below: "5"
      for: 1h
  action: suspend
```

The exact group, kind and Go types belong in the implementation PR. The
semantics are the proposal:

- Filter selects a standing population: types, namespaces, labels.
  Exclusions are first class.
- Conditions are type agnostic because they read normalized status and
  attributed metrics: resource idle, absolute runtime, time in status, or
  any metric scoped to the workload. Conditions compose.
- The controller executes the explicitly requested action through the
  definition's handle. It never substitutes a different verb: a filter
  spanning types that cannot all perform the requested action applies it
  where supported and skips-and-reports the rest. Delete happens only when
  a rule explicitly requests delete.

The part that had no complete precedent in any project we surveyed is the
action protocol:

![the allowance lifecycle](kep-rr-allowance-flow.png)

1. A candidate must pass the global observe gate and reserve a
   blast-radius budget slot.
2. A receipt is persisted before a destructive act: rule, observed value,
   threshold, timestamp, allowance epoch. If the receipt cannot be
   written, the action does not happen.
3. The action is applied once, through the definition's handle, with a
   fresh re-check of the target at dispatch.
4. The workload owner sees the reason on the workload itself, and the
   receipt outlives the workload.
5. A resume by the user, under their own RBAC, starts a new epoch: the
   clock resets and a full new condition window must pass before the rule
   may act again. Resume restores the object's run state, not the
   processes' in-flight progress.

<details>
<summary>protocol fine print: gates, caps, failure behavior</summary>

- The controller installs in global observe mode. Enforcement needs both
  the gate opened and the rule set to enforce. Once gate closure is
  observed at the dispatch boundary, no new action is dispatched; writes
  already accepted by the API server complete and remain tracked in
  receipts.
- Caps follow the disruption-budget shape proven by node autoscalers:
  integer or percent, per reason, most restrictive wins, misconfiguration
  fails closed to zero.
- Metric conditions require fresh attribution for the target and
  sufficiently complete telemetry over the evaluation window: sample-age
  limits, expected coverage, and rules for when gaps reset the window are
  part of the contract. Missing or partial inputs produce Unknown and
  block action.
- Object conditions ride Kubernetes watches with deadline requeues.
  Metric conditions use a stated evaluation cadence, and a final query
  re-checks them at dispatch.
- Rule admission runs a best-effort permission preflight using the
  executor's identity; the controller also reports denied reads and
  writes at run time and re-evaluates readiness when grants or target
  coverage change.
- Outcomes include Unknown: a lost API response is reconciled before any
  retry, and a receipt is never silently rewritten.
- When several rules match one workload, the first condition to fire acts,
  actions apply serially, later rules see the post-action state. Only an
  action whose own condition fired is ever applied.
- If the controller is down nothing is actioned; in-flight actions and
  allowance windows survive a restart.

</details>

### Relationship to Kyverno

Kyverno is a policy engine: policies are Kubernetes resources, evaluated
at admission by webhooks, and converged in the background. It validates,
mutates, generates, verifies images, and deletes on schedules. For
admission, compliance and configuration governance it is the right tool
and this proposal does not touch that ground.

The two contracts differ where runtime reclamation lives. A convergence
engine re-applies desired state and reports current compliance; a
governor acts once per allowance, remembers, and answers for it. Both
behaviors are correct for their own job. Kyverno does not supply the
allowance protocol today; an annotation-based approximation remains
possible and unproven, and hosting a new runtime controller upstream
would need design agreement and a sponsor there.

<details>
<summary>capability comparison, source review plus selected experiments</summary>

The table combines source review with selected v1.19.1 experiments run on
2026-09-10. Executed observations used chart 3.9.1, a 60 second
background interval, and action-specific RBAC grants. Absence claims are
inferences from the inspected paths, not tests of every configuration.
The runtime rules column is a proposal, not an implementation.

| Capability | Kyverno v1.19.1 | Runtime rules (proposed) |
|---|---|---|
| Acts when | admission; per-policy cron for delete; a global background scan (default 1h, configurable) re-applies mutations | continuously, per-rule windows |
| Perceives | object fields; CEL with HTTP and context lookups; no bundled workload normalizer, authors supply queries or shared facts through context | normalized status plus attributed metrics, no per-type queries |
| Acts with | JSON patches re-applied on drift; scheduled delete | observe / suspend via the native handle / delete, explicit per rule |
| Can the user resume | the tested unconditional rule reverted a resume after 43 seconds; no supplied allowance protocol was found | one-shot per epoch, fresh allowance on resume |
| Explains itself | at tested defaults a deletion retained only lastExecutionTime on the inspected Event, report and status surfaces; optional policy events, logs and metrics exist; no retained execution-coupled receipt contract was found | reason on the workload plus a receipt that survives deletion |
| Safety | exceptions and global filters exist; no supplied integrated runtime action preview, fleet budget or global action gate was found | observe-first install, global gate, caps, skip-and-report |

</details>

They compose rather than compete:

- Kyverno validates RuntimeRule objects at admission: who may author rules
  at which scope, mandatory observe-first, cap ceilings.
- Karta facts can feed Kyverno: a GlobalContextEntry can cache normalized
  facts from a JSON provider, or query the Prometheus JSON API over the
  shared recording rules. Cached definitions are data; evaluating them
  still needs a Karta-aware provider or library.
- Optionally, a Kyverno GeneratingPolicy emits an action request that this
  controller validates and executes under its own protocol. A request is
  an intent, never an authorization.

## Examples

Illustrative commands for the proposed API. RuntimeRule and the
cluster-scoped RuntimeRulesConfig are new CRDs; the chart creates
`RuntimeRulesConfig/global` with its gate closed, and rules default to
observe. The API group and scope are fixed in the implementation PR.

The admin journey, end to end:

```console
$ helm install runtime-rules ...        # installs observing; the gate is closed
$ kubectl apply -f reclaim-idle-training.yaml
$ kubectl get runtimerule reclaim-idle-training
NAME                     MODE      MATCHED   WOULD-ACT   ACTED
reclaim-idle-training    observe   214       14          0
```

Two weeks of observe mode produce the number that justifies enforcement:
14 workloads and the GPU-hours they hold, without mutating a single
workload. Then:

```console
$ kubectl patch runtimerulesconfig global --type=merge -p '{"spec":{"gate":"enforce"}}'
$ kubectl patch runtimerule reclaim-idle-training --type=merge -p '{"spec":{"mode":"enforce"}}'
```

The researcher journey, after a suspension (in the workload's namespace):

```console
$ kubectl describe jobset train-llm | tail -3
Events:
  Suspended  runtime-rules  rule reclaim-idle-training: gpu utilization 2.4% for 61m (threshold 5% for 60m)
$ kubectl patch jobset train-llm --type=merge -p '{"spec":{"suspend":false}}'
```

The resume works, stays, and starts a fresh allowance window. No ticket,
no admin, no policy edit. Discovering why after the JobSet was deleted
works too, because the receipt is not owned by the workload.

## Prior art

The mechanisms exist individually across the ecosystem; the proposed
distinction is their integration with Karta definitions, attributed
metric conditions, and explicit allowance and receipt semantics. No
reviewed project was identified as supplying that complete contract.

- Kueue uses native suspend handles for queue management and for runtime
  readiness and recovery timeouts, through compiled adapters for
  supported kinds. It is the closest precedent for lifecycle
  interventions; this proposal resolves the same handles through
  definitions instead of adapters.
  ([kueue](https://github.com/kubernetes-sigs/kueue),
  [waitForPodsReady](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_wait_for_pods_ready/))
- Karpenter's disruption budgets are the cap shape this KEP adopts for
  its voluntary actions.
  ([karpenter disruption docs](https://karpenter.sh/docs/concepts/disruption/))
- The descheduler ships per-namespace and total eviction caps and a
  dry-run mode, both used here as precedents.
  ([descheduler](https://github.com/kubernetes-sigs/descheduler))
- Cloud Custodian's mark-for-op is deferred action with state on the
  resource. The tag itself is editable and dies with the resource;
  separate run artifacts exist but are not an execution-coupled
  per-action ledger.
  ([cloud custodian](https://cloudcustodian.io/docs/quickstart/policyStructure.html))
- kube-green sleeps workloads on schedules, suspends CronJobs natively,
  and stores restore state in a Secret it owns. py-kube-downscaler
  suspends Jobs natively alongside its replica scaling. gpu-pruner culls
  on DCGM idle windows across a fixed type list. k8s-cleaner persists a
  rollback snapshot before acting, with a report that can outlive the
  target but is overwritten on later runs. Each proves a piece; none
  combines described handles, attributed conditions, allowance epochs and
  retained receipts.
  ([kube-green](https://github.com/kube-green/kube-green),
  [py-kube-downscaler](https://github.com/caas-team/py-kube-downscaler),
  [gpu-pruner](https://github.com/wseaton/gpu-pruner),
  [k8s-cleaner](https://github.com/gianlucam76/k8s-cleaner))

<details>
<summary>the wider survey</summary>

Thirty projects were read for this proposal: actors on running workloads
(cullers, sleepers, evictors, janitors, remediation engines, schedulers)
and attribution consumers (cost allocation, dashboards, telemetry
processors). Mechanisms worth adopting were adopted: the budget schema,
condition-anchored timers with deadline requeues, reset-on-reactivate
state, restore memory, notify-then-act ladders. JupyterHub's idle culler
and the Kubeflow v1 notebook culling controller prove the
platform-attributed activity pattern this KEP generalizes.
([jupyterhub-idle-culler](https://github.com/jupyterhub/jupyterhub-idle-culler),
[kubeflow notebooks v1 culler](https://github.com/kubeflow/notebooks/blob/notebooks-v1/components/notebook-controller/controllers/culling_controller.go),
[sablier](https://github.com/sablierapp/sablier) for wake-on-demand,
[robusta](https://github.com/robusta-dev/robusta))

</details>

## Migration and versioning

- No additional change to the Karta schema is proposed beyond KEP-0001.
  The fields this KEP reads (status mappings, pod selectors, suspend
  handles) ship today; the action capability contract above is the one
  open dependency, and its home (annotation, descriptor, or schema
  addition) is decided at implementation review.
- Definitions without a suspend handle stay valid and are
  observe-or-delete only. Definitions without a known action
  classification are observe-only.
- The exporter's series names and labels are a public, versioned
  interface once they leave review; renames are breaking changes.
- RuntimeRule and RuntimeRulesConfig are new CRDs in their own group,
  versioned independently of the definition CRD, starting at v1alpha1.
- One active definition per workload type: an in-cluster definition takes
  precedence over the embedded catalog. Rules never reference a definition
  directly; the controller resolves type to definition.

## Validation

- Rule admission validates scope (a namespace author cannot affect other
  namespaces) and caps against admin ceilings, and runs the best-effort
  permission preflight described above.
- A definition is marked governable only when its suspend handle is
  present, its action classification is known, and its type passed the
  conformance checks below.
- Conditions that need telemetry validate that the exporter contract is
  reachable; when it is not, affected rules surface it in status instead
  of silently idling.

## Test plan

- Unit: the allowance state machine (epoch transitions, restart recovery,
  no double action), the action resolver (explicit verb, skip-and-report),
  cap accounting including the fail-closed paths, the coverage and
  staleness gates for metric conditions.
- Integration, on a kind cluster: observe to enforce promotion; suspend
  then user resume then fresh window; receipt survives workload deletion;
  gate closure stops new dispatches; missing telemetry degrades to skip;
  coexistence with a GitOps controller that re-applies specs; permission
  revocation after admission surfaces in rule status.
- Conformance, per catalog type: suspend releases the resources, resume
  restores the object, destructive and configuration-dependent effects
  are classified correctly. The action tier of every shipped definition
  is asserted by a test, not by a table in a document.
- Exporter: attribution parity against hand-written queries for the four
  proof-of-concept types; join-key uniqueness under same-named workloads
  in different namespaces and name reuse; freshness behavior under API
  pressure.

## Risks and mitigations

- Fighting other controllers. A GitOps engine that self-heals will revert
  a governed suspend exactly like the convergence behavior measured in
  the motivation. The controller must detect managed-by markers (Argo CD,
  Flux) and either integrate or skip-and-report; this is a named
  requirement, not a footnote. The same applies to autoscalers that own
  replica fields.
- Destructive and configuration-dependent stops. RayJob suspension reruns
  from scratch; a StatefulSet scaled to zero with a delete-on-scale claim
  policy loses volumes. The capability contract carries this
  classification, the conformance suite asserts it, and such actions take
  the delete-grade safeguards.
- Stale or partial facts. Acting on old attribution or incomplete
  telemetry is worse than not acting. Sample age, expected coverage and
  gap handling gate every metric condition; failing the gate means skip
  and report, never act.
- Fleet-scale mistakes. A bad rule at cluster scope is bounded by the
  observe-first default, the global gate, and the caps. The measured
  failure mode this protects against is real: a scoping mistake in a
  policy engine applied an action to every workload of a kind with no
  warning.
- Scale. Thousands of workloads, per-rule windows: object conditions
  anchor on watches and deadline requeues instead of polling; metric
  conditions run on a stated cadence with a final recheck at dispatch.

## Alternatives considered

- Extend Kyverno. Kyverno supplies valuable matching, expression,
  distribution and reporting infrastructure. The execution protocol
  (allowance state, receipts, gates, caps, the resolver) remains
  substantial new work in either home, and hosting it upstream needs
  design agreement and a sponsor there; the tracker issue closest to this
  capability has no substantive response visible. A dedicated controller
  removes the upstream merge dependency; the composition section keeps
  the integration benefits.
- A Kyverno policy pack with state in annotations. Cloud Custodian proves
  the deferred-action pattern at cloud scope. Annotation state is
  editable by any writer, carries no receipts and no caps, and rides the
  global scan cadence. It remains unproven for this use and may still
  ship as a reduced-contract option for clusters that want nothing new
  installed, clearly labeled as such.
- KEDA. The ScaledObject route requires a scalable target and the
  suspendable-workloads request is open and unassigned. KEDA stays the
  demand-side partner; a suspended service can even wake on traffic in
  front of it.
- Exporter only. Attribution alone makes dashboards and alerts better,
  and the value of acting then leaks to ad-hoc scripts with none of the
  protocol above. The survey shows where that path settles: fixed type
  lists and replica scaling, with native suspension appearing only as
  per-type special cases.
- Per-type operators. One controller per workload type is exactly the
  adapter spread Karta exists to remove.

## Future work

- Wake-on-demand: a suspended service that holds its URL behind a proxy
  and resumes on the first request.
- State-preserving suspend for GPUs: checkpoint-based pausing is maturing
  in the ecosystem; when it can hold GPU memory it becomes a new action
  tier above stop-and-restart.
- A conformance and readiness command that shows, per rule, what it can
  reach, act on, and why, before enforcement is enabled.
- Attribution consumers beyond rules: dashboard packs, cost attribution,
  trace enrichment.
- An action-request adapter so external engines can submit intents
  through the same protocol.

## Implementation history

- 2026-07-29: capability comparison against Kyverno v1.18.2 run on a live
  cluster (research, proposal-only).
- 2026-08-19: exporter proof of concept: four types, one dashboard, zero
  per-type code (proposal-only).
- 2026-08-31: exporter requirements revision (proposal-only).
- 2026-09-10: comparison re-verified against Kyverno v1.19.1 on a live
  cluster; thirty adjacent projects read for prior art (research,
  proposal-only).
- 2026-09-11: this KEP.
- 2026-09-14: external review applied: experiment scope corrections,
  prior-art precision, the capability contract named as an open
  dependency.

Nothing in this KEP is implemented. The definition fields it relies on
(status mappings, pod selectors, suspend handles) ship today; the action
capability contract, the exporter and the rules controller are the
proposal.
