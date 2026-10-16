<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Selected CEL execution evidence

Captured 16 October 2026 from the isolated implementation tests.

| Files | Meaning |
| --- | --- |
| `direct-*-request.json` | Actual Deployment and Job requests, including the deliberately forged identity |
| `direct-created.json` | Whitelisted creation metadata and annotations after admission |
| `direct-job-completion.json` | Job status proving completion |
| `final-direct-metrics.prom` | Both workload identities and observation health after final rollout |
| `prometheus-query-result.json` | Actual query and both complete workload identities, with target addresses omitted by aggregation |
| `deployment-readiness.json` | Both isolated Deployments ready after the final binary rollout |
| `chart-validation.json` | Positive and negative chart rendering checks |
| `direct-conflict.prom`, `direct-recovery.prom` | Conflicting Pod metadata clears identity; removal restores it |
| `adversarial-results.json` | Root identity mutation denial and conflict test observations |
| `nvcf-function-create-response.json` | Function creation result, before deployment |
| `function-invocation.json` | Successful invocation HTTP status and response length |
| `nvcf-*-metadata.json` | Resource identity, label/annotation key lists and owner references; unrelated metadata values omitted |
| `nvcf-function-metrics.prom`, `nvct-task-metrics.prom` | Captured unknown identities; no human was inferred from an account ID |
| `nvct-task-final.json` | Task API result proving COMPLETED |
| `nvct-task-request-template.json`, `nvct-task.sh` | Submitted request shape and decoded task script; lab cluster name and base64 body replaced with placeholders |
| `osmo-*-prepared.json` | Prepared one-CPU fixtures, not successful execution evidence |

The latest executable source and test outcomes are in
[IMPLEMENTATION.md](../../IMPLEMENTATION.md). The two NVCF metric captures precede
the final cache-verification hardening rollout. Both used the same CEL rules and
metric schema. They do not prove a human identity or a function-level root join.

Unknown Pod identity alongside complete direct workload identity is expected:
admission captures the root request, and the workload metric reuses that root.
The Deployment's ReplicaSet inherits its annotations; the Pod does not.

No authenticated Run:AI workload was submitted in this run. No new OSMO workflow
passed capacity validation. Source-backed unit fixtures are not substitutes for
those live tests. The initial research evidence is retained separately.
