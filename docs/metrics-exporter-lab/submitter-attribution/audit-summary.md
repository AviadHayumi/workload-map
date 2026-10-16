<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta submitter attribution audit

16 October 2026. Eight execution cases across the original CEL run and its continuation. CPU fixtures in a fake-GPU lab; no GPU performance claim.

## What the tests proved

Karta identifies which workload owns a Pod. Optional CEL rules extract a submitter from existing metadata; optional operator admission records the original Kubernetes request principal. The three existing KEP metric names and labels remain unchanged.

| Submission path | Two tested cases | Observed identity |
| --- | --- | --- |
| Direct Kubernetes | Deployment ran; finite Job completed | Complete principal captured at admission, despite forged input annotations |
| Run:AI API | Finite training completed; Knative inference Ready and HTTP 200 | Complete principal on both workload metrics; root and Pod metadata agreed |
| OSMO API | Single task and leader/worker group completed | Partial identity on all three captured task Pods and their workload metrics |
| NVCF / NVCT | Function invoked with HTTP 200; finite task completed | Unknown submitter on inspected Kubernetes resources |

The cases exercise submission, execution and metadata propagation. The Run:AI fixtures are a timed shell command and an HTTP server, not model training or inference. The inference check crossed the Knative revision proxy through a local port-forward; external ingress was not tested.

## Why complete, partial and unknown all matter

The OSMO API retained `alice@example.test`, but Kubernetes received `alice`. Earlier tests also showed two email domains collapsing to that same label. CEL cannot reconstruct the missing domain. OSMO has no Kubernetes workflow root here, so the captured workload metric represents each task Pod, not the entire multi-task workflow.

Run:AI preserved `test@run.ai`. A shortened example of its actual workload sample is:

```prometheus
karta_workload_submitter_info{
  workload="cel-audit-training", submitter="test@run.ai",
  identity_source="runai:lab-tenant", status="complete"
} 1
```

This is an information gauge with value 1. Structural labels are omitted above. Complete means the configured rule returned an unshortened identifier; it does not prove a human or establish an authorization boundary. Principal type remains unknown.

NVCF account, function and task IDs identify resources, not the submitting person. Reporting unknown preserves that distinction instead of inventing an identity.

<!-- pagebreak -->

## What broke, and what changed

| Finding | Correction and proof |
| --- | --- |
| Valid namespaced-to-cluster owner links were rejected | Index and verifier now follow Kubernetes scope rules. Tests reject cross-namespace links, invalid reverse transitions and replacement UIDs. |
| Cluster roots used namespace/name keys even without a namespace | Standard cache keys now cover root publication, Pod attribution, descendant refresh, pending retries and deletion fences. Regression tests failed before the fixes and pass afterward. |
| Full-object and metadata-only DaemonSet responses differed at the same resource version | Verify each informer through the same API representation. Tests preserve stale detection for real unseen metadata edits. |

Lab setup also needed an explicit OSMO audit platform, two Knative integration settings and a namespace-scoped Run:AI owner read role. Both platform tests then passed. No cluster-wide WorkloadRunner permission was granted.

The owner-scope binary recovered 35 workload observations from unresolved-owner to healthy, matched by the same resource UID: 30 Deployments, four Pods and one StatefulSet. The metadata fix then recovered all 21 stale Pod observations. The final snapshot has 210 healthy workload observations and no workload-level failures. These are measured snapshots, not fleet-scale guarantees.

Exporter and root presubmits passed, including lint, vet, race tests and the new lifecycle and metadata regressions. Both Run:AI workload identities were captured again on the owner-scope binary. The timed Deployment fixture was stopped, Run:AI fixtures were deleted after capture, and the broad test exporter recovered readiness.

## Limits and next decisions

The original NVCF and OSMO fixtures were not rerun against the later scope fix. Run:AI was retested after that fix, before the final metadata correction. OSMO uses development identity headers; this does not establish production OIDC behavior. One replay group worker completed when its leader finished, without an independent exit code.

A platform must preserve full identity before CEL can expose it. Metadata trust, cross-tenant normalization, intermediate-owner inheritance and multiple served API versions still need deliberate policy. Production authentication across platforms, hosted NVCF, all workload modes and 100,000-workload identity performance remain unproven. The prior NVCA cancellation workaround is not an upstream fix.

[Detailed report and evidence](https://github.com/AviadHayumi/workload-map/blob/karta-metrics-exporter-poc-impl/docs/metrics-exporter-lab/submitter-attribution/IMPLEMENTATION.md) distinguish each build and test. The scope correction follows [Kubernetes owner-reference rules](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/).
