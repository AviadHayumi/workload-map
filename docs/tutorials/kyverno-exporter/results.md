<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Live verification: 2026-11-08

The complete live verifier passed in 670 seconds. It tested suspension,
completed-Job deletion, dependent Pod removal, UID isolation, outage handling
and recovery. Suspension recovered automatically. Cleanup recovered after an
explicit policy requeue. That operator-assisted step is part of the pass result.

## Revisions and environment

The `kyverno-exporter-tutorial` worktree starts from fork branch
`kep-runtime-rules` at `1a90457b`. Its 23 commits were rebased on upstream
`8370c0b5a720771dcb2e8c944d2e4bba39e7df3a`, producing base HEAD
`cba10f960b8d0dc8c938e23d8795579810e2c4c1`. The tutorial follows that base.
Exporter code comes from that upstream revision.

| Component | Tested version |
| --- | --- |
| kind | 0.30.0 |
| Kubernetes | 1.34.3, Linux arm64 |
| Docker | 29.0.1 |
| Helm | 4.0.1 |
| Kyverno | 1.19.1, chart 3.9.1 |
| Prometheus | 3.14.0 |
| Example image | busybox:1.37.0 |

After recovery from a shared Docker storage failure, the setup script completed
a full rerun with its recording rules and corrected cleanup permissions.
The exporter was built from the checkout and loaded locally. Its image ID and
deployed image names are in the ignored `inventory.json` artifact.

Associated branch references consulted:

| Reference | Purpose |
| --- | --- |
| `fork/kep-runtime-rules` | Runtime policy goals and historical examples. |
| `fork/exporter-integration-lab` | Earlier policy manifests and observations. |
| `fork/exporter-kind-e2e` | Current kind setup and exporter test conventions. |
| `fork/exporter-dsx-naming` | Exporter naming and installation defaults. |
| `fork/exporter-controller-runtime` | Exporter implementation context. |
| `fork/grafana-dashboard` | GPU integration context. |
| `fork/exporter-demo-release-9c14b074` | Historical image provenance. |

## Observed outcomes

The final run started at 13:50:44 UTC. Elapsed values are check completion times,
not exact controller action timestamps. The verifier polls every five seconds.

| Check | Elapsed | Observation |
| --- | --- | --- |
| Effective permissions | 0s | Background update and cleanup delete allowed in ai-lab and denied in default |
| Verified workload observation | 6s | Current exporter reports a verified Running Job |
| Fresh Jobs wait | 52s | Fresh Jobs survived 45 seconds after the first Running observation |
| Suspension and cascading cleanup | 189s | Kyverno suspended running-worker and deleted completed-report; both Jobs' Pods disappeared |
| Unselected and failed Jobs retained | 190s | Unselected Running/Completed Jobs and selected Failed Job remain untouched |
| Same-name replacement with a new UID | 266s | Replacement was observed under its new UID and survived 65 seconds of healthy telemetry without inheriting eligibility |
| Exporter outage | 433s | 145 seconds without exporter scrapes produced no eligible Jobs or policy actions |
| Previously eligible Job during exporter outage | 433s | An already observed Running Job was opted in during the outage and remained active |
| Prometheus outage | 506s | 65 seconds with Prometheus stopped left selected Jobs unchanged |
| Recovery waits for fresh history | 578s | A fresh minute of recovered telemetry was insufficient for action |
| Automatic suspension recovery | 639s | Suspension resumed after a new qualifying observation window |
| Explicit cleanup recovery | 670s | Equivalent schedule changes resumed cleanup; eligible completed Jobs and their Pods disappeared |
| Opt-out before manual resume | 670s | running-worker remained active beyond the policy window |
| Complete verifier | 670s | All checks passed; exporter and Prometheus restored to one replica |

## Cleanup recovery limitation

An earlier outage run found that cleanup did not resume automatically.
In Kyverno 1.19.1, a DeletingPolicy evaluation error returns before the controller
schedules its next run. Ten failed retries can then remove the policy from the
queue. In the live run, `lastExecutionTime` stopped advancing even though
Prometheus again returned the completed Jobs as eligible. This follows the
[pinned scheduler](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/controllers/deleting/controller.go#L264)
and [retry handling](https://github.com/kyverno/kyverno/blob/v1.19.1/pkg/utils/controller/run.go#L66).

The final verifier made two equivalent schedule changes to requeue the policy.
It confirmed that `lastExecutionTime` advanced and that both eligible completed
Jobs and their Pods disappeared. The tutorial includes the recovery commands.

## Dependent Pod cleanup

A preceding 635-second run passed its Job-level assertions, but a separate Pod
check found orphaned completed Pods. Kubernetes defaults this Job deletion path
to orphaning dependents. See the
[pinned Job strategy](https://github.com/kubernetes/kubernetes/blob/v1.34.3/pkg/registry/batch/job/strategy.go#L60).

The policy now sets `deletionPropagationPolicy: Background`. The final run
checked Pod removal by the Job UID after both initial deletion and recovery.
This also detects orphaned Pods whose owner references have been removed.

## Other validation

- At 14:02:21 UTC, the API and all six deployments were healthy. The exporter
  scrape was successful and 0.872 seconds old. Both recording rules were healthy.
  A separate check confirmed the final Job and Pod outcomes.
- Exporter image build and Helm rendering passed.
- Bash syntax, ShellCheck, Python syntax, YAML parsing and Prometheus rule checks passed.
- Both PNG diagrams were rendered and visually inspected. Their editable sources
  are retained in the ignored local artifact directory.
- Effective controller permissions were checked on the live cluster: update and
  delete are allowed in `ai-lab` and denied in `default`. Cleanup additionally
  has read-only cluster-wide Job listing.

These checks do not establish GPU idleness, multi-cluster behavior, production
scale, or atomicity between a Prometheus query and a Kubernetes write. Invalid
exporter observations were not fault-injected. Samples do not prove what
happened between scrapes.

## Local evidence

Artifacts are under `.artifacts/kyverno-exporter-tutorial/`:

- `up-recovered.log`: successful setup rerun after Docker recovery.
- `verification.json` and `verify-cascading-cleanup.log`: final successful run.
- `final-health.json`: nine final health and workload checks.
- `verification-before-cascading-cleanup.json` and `verify-recovered.log`:
  earlier Job-level pass before dependent Pod assertions were added.
- `verification-query-recovery-failure.json` and
  `verify-query-recovery-failure.log`: automatic cleanup recovery failure.
- `inventory.json`: revisions and deployed images.
- `pod-cleanup-finding.json`: orphaned Pod finding and correction.
- `infra-failure.json` and `recovery.json`: shared Docker storage failure and recovery.
- `diagrams/`: editable diagram sources.
