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

![one rule, any described workload](kep-rr-big-picture.png)

Idle accelerators consume capacity and incur cost, and today no tool acts
on them with one rule. This KEP proposes two components on top of Karta
definitions. A metrics exporter republishes the structure a definition
already declares as Prometheus series, so "this JobSet is idle" becomes a
fact any consumer can read. A runtime rules controller acts on such facts:
one filter / condition / action rule across every described type, acting
once per allowance window through the type's own suspend handle, with a
receipt and a kill switch.

Diagram sources sit next to this file as `.excalidraw`; open them at
excalidraw.com to edit.

## Motivation

A training JobSet hangs after epoch 40 and keeps its 64 GPUs at 2%
utilization. The admin's request is one sentence: suspend a workload when
its GPU utilization stays below 5% for an hour. Every existing route
fails it differently. KEDA cannot address the JobSet (no `/scale`
subresource; ScaledJob creates Jobs, it does not suspend them) and its
suspendable-workloads request sits open and unassigned
([keda#7548](https://github.com/kedacore/keda/issues/7548)). Kyverno has
the policy machinery but not the semantics: in a v1.19.1 run (2026-09-10)
an unconditional suspend policy re-suspended a user-resumed Job after 43
seconds, because a convergence engine re-applies desired state; that
tests that policy's behavior, not the impossibility of smarter ones, but
the allowance protocol this KEP needs has no supplied implementation
there, and the open request for background time and status re-evaluation
has no substantive response
([kyverno#16214](https://github.com/kyverno/kyverno/issues/16214)).
Kubernetes has carried the missing-verb discussion since January 2022
with no associated KEP identified
([kubernetes#107294](https://github.com/kubernetes/kubernetes/issues/107294)).

Two things are missing everywhere, and a definition already declares
both. Perception: which pods form this workload and what its status
means, knowledge that otherwise lives in a hand-maintained query per
type. Actuation: how to stop this type without destroying it,
`.spec.suspend` here, `.spec.runPolicy.suspend` there, an annotation
elsewhere. This KEP makes that declared knowledge operational.

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

- No replica management and no autoscaling; demand-driven scaling belongs
  to HPA and KEDA. This acts on waste and composes with both.
- No admission control, no mutation compliance, no general policy engine.
- No new telemetry pipeline: utilization comes from the cluster's
  existing pod metrics (DCGM, kubelet), joined in the metrics store.
- No promise of process-state restoration: suspend preserves the workload
  object where the type supports it, the processes restart. Delete is
  destructive and only ever happens as an explicitly requested action.
- Actions are bounded to workload lifecycle: observe, suspend, delete.

## Proposal

### The contract a definition supplies

The existing schema supplies the raw material: normalized status
(`statusMappings`), pod attribution (`podSelector` per component), and a
suspend handle. The JobSet catalog definition carries the handle today
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
volumes) is information the schema does not carry. Before implementation,
a capability contract must specify how a definition declares action
effects, whether as an annotation contract, a separate descriptor, or a
schema addition. That choice is an unresolved dependency of this KEP.
Until a type's classification is known and it passed conformance, it is
observe-only. Describable is not governable: a definition without a
suspend handle (Knative Service has no hold at all) still gets observe
and delete, and suspend is skipped and reported.

### The metrics exporter

![attribution: from pods to a workload fact](kep-rr-attribution.png)

The exporter reads the Kubernetes API only and republishes what
definitions declare, as series. It emits facts and never aggregates;
consumers join and aggregate in their own metrics store.

| Series | Meaning |
|---|---|
| `karta_pod_workload` | the join key: one series per attributed pod, labels for workload, component, component instance |
| `karta_workload_status` | one 0/1 series per normalized phase per workload |
| `karta_workload_info` | identity anchor: GVK and governing definition |
| `karta_exporter_freshness` | time since last successful reconciliation, so consumers can refuse stale attribution |

Series names and labels are provisional until the implementation review.
The contract must carry enough identity to join safely: namespace and pod
at minimum, with workload identity that distinguishes same-named
workloads across namespaces and name reuse after deletion. When a pod has
two described owners along an ownership chain, metrics attribute to the
top-level owner only, so every pod counts exactly once.

The join is the point. A single-cluster illustration, shipped as a
recording rule rather than authored per type:

```promql
avg by (namespace, workload) (
  DCGM_FI_DEV_GPU_UTIL
  * on(namespace, pod) group_left(workload) karta_pod_workload
)
```

The expression computes a current average; the controller separately
evaluates threshold duration and sample coverage before any action. A
proof of concept (2026-08-19) drove one dashboard across JobSet,
PyTorchJob, LeaderWorkerSet and Deployment with zero per-type code, and
surfaced one component instance at 92% utilization while its sibling sat
at 2.5%.

### The runtime rules controller

![the allowance lifecycle](kep-rr-allowance-flow.png)

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

Exact group, kind and Go types belong in the implementation PR. The
semantics are the proposal. Filter selects a standing population, with
exclusions first class. Conditions read normalized status and attributed
metrics, so they are type agnostic, and they compose. The controller
executes the explicitly requested action through the definition's handle
and never substitutes a verb: a filter spanning types that cannot all
perform the action applies it where supported and skips-and-reports the
rest.

The allowance protocol is the part with no complete precedent in any
project we surveyed:

1. A candidate must pass the global observe gate and reserve a
   blast-radius budget slot.
2. A receipt is persisted before a destructive act: rule, observed value,
   threshold, timestamp, allowance epoch. No receipt, no action.
3. The action is applied once, through the definition's handle, with a
   fresh re-check of the target at dispatch.
4. The owner sees the reason on the workload; the receipt outlives the
   workload.
5. A resume by the user, under their own RBAC, starts a new epoch: the
   clock resets and a full new condition window must pass before the rule
   may act again.

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
- Metric conditions require fresh attribution and sufficiently complete
  telemetry over the window: sample-age limits, expected coverage, and
  rules for when gaps reset the window are part of the contract. Missing
  or partial inputs produce Unknown and block action.
- Object conditions ride Kubernetes watches with deadline requeues.
  Metric conditions use a stated evaluation cadence, and a final query
  re-checks them at dispatch.
- Rule admission runs a best-effort permission preflight using the
  executor's identity; the controller also reports denied reads and
  writes at run time and re-evaluates readiness when grants change.
- Outcomes include Unknown: a lost API response is reconciled before any
  retry, and a receipt is never silently rewritten.
- When several rules match one workload, the first condition to fire
  acts, actions apply serially, later rules see the post-action state.
- If the controller is down nothing is actioned; in-flight actions and
  allowance windows survive a restart.

</details>

### Relationship to Kyverno

Kyverno evaluates policies at admission and converges them in the
background; for admission, compliance and configuration governance it is
the right tool and this proposal does not touch that ground. The two
contracts differ where runtime reclamation lives: a convergence engine
re-applies desired state and reports current compliance, a governor acts
once per allowance and answers for it. Kyverno does not supply the
allowance protocol today; an annotation-based approximation remains
possible and unproven, and hosting a new runtime controller upstream
would need design agreement and a sponsor there.

They compose rather than compete. Kyverno validates RuntimeRule objects
at admission: who may author rules at which scope, mandatory
observe-first, cap ceilings. Karta facts can feed Kyverno: a
GlobalContextEntry can cache normalized facts from a JSON provider or
query the Prometheus JSON API over the shared recording rules (cached
definitions are data; evaluating them still needs a Karta-aware
provider). Optionally, a Kyverno GeneratingPolicy emits an action request
that this controller validates and executes under its own protocol; a
request is an intent, never an authorization.

<details>
<summary>capability comparison, source review plus selected experiments</summary>

The table combines source review with selected v1.19.1 experiments run on
2026-09-10 (chart 3.9.1, a 60 second background interval, action-specific
RBAC grants). Absence claims are inferences from the inspected paths, not
tests of every configuration. The runtime rules column is a proposal.

| Capability | Kyverno v1.19.1 | Runtime rules (proposed) |
|---|---|---|
| Acts when | admission; per-policy cron for delete; a global background scan (default 1h, configurable) re-applies mutations | continuously, per-rule windows |
| Perceives | object fields; CEL with HTTP and context lookups; no bundled workload normalizer, authors supply queries or shared facts through context | normalized status plus attributed metrics, no per-type queries |
| Acts with | JSON patches re-applied on drift; scheduled delete | observe / suspend via the native handle / delete, explicit per rule |
| Can the user resume | the tested unconditional rule reverted a resume after 43 seconds; no supplied allowance protocol was found | one-shot per epoch, fresh allowance on resume |
| Explains itself | at tested defaults a deletion retained only lastExecutionTime on the inspected Event, report and status surfaces; optional policy events, logs and metrics exist; no retained execution-coupled receipt contract was found | reason on the workload plus a receipt that survives deletion |
| Safety | exceptions and global filters exist; no supplied integrated runtime action preview, fleet budget or global action gate was found | observe-first install, global gate, caps, skip-and-report |

</details>

## Examples

Illustrative commands for the proposed API. RuntimeRule and the
cluster-scoped RuntimeRulesConfig are new CRDs; the chart creates
`RuntimeRulesConfig/global` with its gate closed, and rules default to
observe.

```console
$ helm install runtime-rules ...        # installs observing; the gate is closed
$ kubectl apply -f reclaim-idle-training.yaml
$ kubectl get runtimerule reclaim-idle-training
NAME                     MODE      MATCHED   WOULD-ACT   ACTED
reclaim-idle-training    observe   214       14          0
```

Two weeks of observe mode produce the number that justifies enforcement:
14 workloads and the GPU-hours they hold, without mutating a single
workload. Then open the gate and set the rule to enforce:

```console
$ kubectl patch runtimerulesconfig global --type=merge -p '{"spec":{"gate":"enforce"}}'
$ kubectl patch runtimerule reclaim-idle-training --type=merge -p '{"spec":{"mode":"enforce"}}'
```

The researcher, after a suspension, in the workload's namespace:

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

The mechanisms exist individually; no reviewed project combines described
handles, attributed conditions, allowance epochs and retained receipts.

- [kueue](https://github.com/kubernetes-sigs/kueue): native suspend
  handles for queueing and runtime readiness timeouts, via compiled
  adapters ([waitForPodsReady](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_wait_for_pods_ready/));
  the closest lifecycle precedent, resolved here through definitions
  instead of adapters.
- [karpenter](https://karpenter.sh/docs/concepts/disruption/): the
  disruption-budget shape this KEP adopts for caps.
- [descheduler](https://github.com/kubernetes-sigs/descheduler):
  per-namespace and total eviction caps, and a dry-run mode.
- [cloud custodian](https://cloudcustodian.io/docs/quickstart/policyStructure.html):
  mark-for-op deferred actions with state in tags; the tag is editable
  and dies with the resource, which is the argument for a real ledger.
- [kube-green](https://github.com/kube-green/kube-green): scheduled
  sleep, native CronJob suspension, restore state in an owned Secret.
- [py-kube-downscaler](https://github.com/caas-team/py-kube-downscaler):
  schedule-based downscaling with native Job suspension.
- [gpu-pruner](https://github.com/wseaton/gpu-pruner): DCGM idle windows,
  scale to zero, a fixed type list.
- [k8s-cleaner](https://github.com/gianlucam76/k8s-cleaner): rollback
  snapshot persisted before acting; its report can outlive the target but
  is overwritten on later runs.
- [jupyterhub-idle-culler](https://github.com/jupyterhub/jupyterhub-idle-culler)
  and the [kubeflow v1 notebook culler](https://github.com/kubeflow/notebooks/blob/notebooks-v1/components/notebook-controller/controllers/culling_controller.go):
  the platform-attributed activity pattern this KEP generalizes.
- [sablier](https://github.com/sablierapp/sablier): wake-on-demand behind
  a proxy, the future-work complement to suspend.
- [robusta](https://github.com/robusta-dev/robusta): alert-driven
  remediation playbooks; perception stays hand-authored alerts.

## Migration and versioning

- No additional change to the Karta schema is proposed beyond KEP-0001.
  The action capability contract above is the one open dependency; its
  home (annotation, descriptor, or schema addition) is decided at
  implementation review.
- Definitions without a suspend handle stay valid, observe-or-delete
  only; definitions without a known classification are observe-only.
- The exporter's series names and labels are a public, versioned
  interface once they leave review; renames are breaking changes.
- RuntimeRule and RuntimeRulesConfig are new CRDs in their own group,
  versioned independently of the definition CRD, starting at v1alpha1.
- One active definition per workload type: an in-cluster definition takes
  precedence over the embedded catalog; rules never reference a
  definition directly.

## Validation

- Rule admission validates scope (a namespace author cannot affect other
  namespaces) and caps against admin ceilings, and runs the best-effort
  permission preflight.
- A definition is governable only when its suspend handle is present, its
  action classification is known, and its type passed conformance.
- Conditions that need telemetry validate that the exporter contract is
  reachable; when it is not, affected rules surface it in status instead
  of silently idling.

## Test plan

- Unit: the allowance state machine (epoch transitions, restart recovery,
  no double action), the action resolver (explicit verb,
  skip-and-report), cap accounting including fail-closed paths, the
  coverage and staleness gates.
- Integration, on a kind cluster: observe to enforce promotion; suspend,
  user resume, fresh window; receipt survives workload deletion; gate
  closure stops new dispatches; missing telemetry degrades to skip;
  coexistence with a GitOps controller that re-applies specs; permission
  revocation after admission surfaces in rule status.
- Conformance, per catalog type: suspend releases resources, resume
  restores the object, destructive and configuration-dependent effects
  are classified correctly. Every shipped definition's action tier is
  asserted by a test, not a table in a document.
- Exporter: attribution parity against hand-written queries for the four
  proof-of-concept types; join-key uniqueness under same-named workloads
  and name reuse; freshness under API pressure.

## Risks and mitigations

- A GitOps engine that self-heals will revert a governed suspend exactly
  like the convergence behavior in the motivation. Detect managed-by
  markers (Argo CD, Flux) and integrate or skip-and-report; same for
  autoscalers that own replica fields.
- Destructive and configuration-dependent stops (RayJob rerun,
  StatefulSet volume claims). The capability contract classifies them,
  conformance asserts it, and they take delete-grade safeguards.
- Stale or partial facts are worse than no action. Sample age, coverage
  and gap handling gate every metric condition; failing the gate means
  skip and report.
- A bad rule at cluster scope is bounded by observe-first, the global
  gate and the caps; the measured failure mode this guards against is a
  scoping mistake acting on every workload of a kind with no warning.
- Scale: object conditions ride watches and deadline requeues, not
  polling; metric conditions run on a stated cadence with a final
  re-check at dispatch.

## Alternatives considered

- Extend Kyverno: real reuse (matching, CEL, distribution, reports), but
  the execution protocol is substantial new work in either home and
  upstream hosting needs a sponsor that has not appeared; a dedicated
  controller removes the merge dependency and keeps composition.
- A Kyverno policy pack with annotation state: proven pattern at cloud
  scope, but the state is editable by any writer, has no receipts or
  caps, and rides the global scan cadence; may still ship as a clearly
  labeled reduced-contract option.
- KEDA: requires a scalable target and its suspendable-workloads request
  is open and unassigned; it stays the demand-side partner.
- Exporter only: attribution improves dashboards, then the acting layer
  gets filled by scripts with none of the protocol; the survey shows
  where that settles.
- Per-type operators: one controller per type is the adapter spread Karta
  exists to remove.

## Future work

- Wake-on-demand: a suspended service that holds its URL behind a proxy
  and resumes on the first request.
- State-preserving suspend for GPUs: checkpoint-based pausing, when it
  can hold GPU memory, becomes a new action tier above stop-and-restart.
- A readiness command showing, per rule, what it can reach, act on, and
  why, before enforcement is enabled.
- Attribution consumers beyond rules: dashboard packs, cost attribution,
  trace enrichment.
- An action-request adapter so external engines can submit intents
  through the same protocol.

## Implementation history

- 2026-07-29: capability comparison against Kyverno v1.18.2 on a live
  cluster (research, proposal-only).
- 2026-08-19: exporter proof of concept: four types, one dashboard, zero
  per-type code (proposal-only).
- 2026-08-31: exporter requirements revision (proposal-only).
- 2026-09-10: comparison re-verified against Kyverno v1.19.1 on a live
  cluster; thirty adjacent projects read for prior art (research,
  proposal-only).
- 2026-09-11: this KEP.
- 2026-09-14: external review applied; leanness pass.

Nothing in this KEP is implemented. The definition fields it relies on
(status mappings, pod selectors, suspend handles) ship today; the action
capability contract, the exporter and the rules controller are the
proposal.
