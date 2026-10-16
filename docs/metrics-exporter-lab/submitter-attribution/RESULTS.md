<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Submitter attribution verified results

Historical record: this document predates the label-only rerun. Its companion
metrics, overwriting admission behavior and build coverage are superseded by
[the current implementation](IMPLEMENTATION.md) and [fresh report](audit-summary.md).
Original observations below are preserved as evidence of the earlier build.

Target: isolated KIND test cluster. NGC was used for reads and pulls only.
Original source repositories and the shared kubectl context were not changed.

The [implementation report](IMPLEMENTATION.md) and [audit summary](audit-summary.md)
include the resumed authenticated Run:AI and CEL-enabled OSMO tests. The initial
OSMO identity-collision experiments below are retained as historical evidence.

## Verified findings

| Product/path | What was tested | Submitter evidence |
| --- | --- | --- |
| OSMO 6.3.1 | Four finite workflows completed | Workflow API preserves the full submitted_by; Pod and PodGroup labels may lose information |
| NVCF self-managed | Function ACTIVE; GET invocation returned HTTP 200 | Pod has account/function/version IDs; correlated creation audit identifies client ncp |
| NVCT self-managed | Finite task reached COMPLETED; Pod and ICMSRequest metadata captured | Pod/root object contain task/account IDs; audit identifies API-key owner svc@nvct-api.local |
| Run:AI | Authenticated training completed; inference Ready and HTTP 200 | Complete principal observed on Job/Service roots and Pod metadata; namespace-scoped workload metrics verified |

OSMO results:

| Submitted identity | API submitted_by | Pod / PodGroup osmo.submitted_by |
| --- | --- | --- |
| alice | alice | alice |
| alice@team-a.example | alice@team-a.example | alice |
| alice@team-b.example | alice@team-b.example | alice |
| a | a | absent |

These OSMO tests use development identity headers, not production OIDC login.
They prove propagation and formatting on the normal task path, not universal
coverage or trustworthy human authentication. Arbitrary user-created Kubernetes
objects and nested Pod templates are outside this tested path.

## Meaning for a generic solution

CEL can normalize metadata that exists on the workload or its Pods. It cannot
recover an email domain removed from a label, or identify the person behind a
shared service identity. In the tested NVCF paths, metadata alone is insufficient:
use function/version or task IDs to correlate with audit/API identity, then retain
whether the identity represents a human, service, or unknown principal.

The NVCF task root ICMSRequest uses nvca.nvcf.nvidia.io/task-id; its Pod uses
task-id. Both carry account identity, but neither carries the audited submitter.
Applying extraction to both objects does not create the missing identity.

Do not assume account ID, API-key owner, Kubernetes service account, and workload
submitter mean the same thing. Even the local API Keys response uses owner_type
USER for the service-style owner svc@nvcf-api.local.

## Fixes and limits

- OSMO main chart and published runtime did not match. Used matching 6.3.1 charts.
- Fixed lab bootstrap shell/service routing and node selectors.
- Corrected Cassandra heap settings, OpenBao replica count, NATS auth startup
  order, and CPU reservations for this crowded KIND environment.
- NVCF function invocation needs its function Host header with localhost
  forwarding. The static Nginx fixture supports GET; default POST returns 405.
- NVCT chart task helper names did not resolve. Used registry-verified
  nvcf-worker-init-oss:1.2.1 and nvcf-worker-task-oss:1.1.4.
- NVCT requires a progress file; a sleep-only task does not report completion.
  The fixture also needed lastUpdatedAt and had to let the helper create its
  shared results directory. The corrected finite task reached COMPLETED, and
  its Pod was removed automatically.
- Shared-cache / Helm workload mode, production SSO, hosted NVCF, and real GPU
  computation were not tested. This cluster uses fake GPUs.
- Deprecated NVCF cluster-list endpoint returned 403 with this CLI auth setup;
  the documented ICMS cluster-management endpoint worked.
- Run:AI training/inference are now verified through authenticated submission. Broader composers and cluster-wide owner permissions remain outside this test scope.
- One malformed task remained as an ICMSRequest after API cancellation. Its
  finalizer was removed only after verifying no owned workload/storage resources.
  This lab cleanup does not fix the underlying NVCA cancellation behavior.
- Optional task telemetry targets an unconfigured dummy endpoint and logs export
  failures. Task execution and identity inspection do not depend on that endpoint.
- Some resource/placement fixes were applied directly to lab controllers. The
  corresponding local Helmfile overrides are saved, but not every installed
  release has been upgraded to store those overrides. Do not perform an upgrade
  with upstream defaults; use the saved lab overrides and review the diff.

## Evidence

- [Full experiment log](FINDINGS.md)
- [OSMO workflow outcomes](evidence/osmo-identity-results.json)
- [NVCF ACTIVE function](evidence/nvcf-function-active.json)
- [Successful NVCF invocation](evidence/nvcf-function-invocation.json)
- [Function audit correlation](evidence/nvcf-api-audit-identity.json)
- [Completed NVCT task](evidence/nvcf-task-completed.json)
- [Task audit correlation](evidence/nvct-api-audit-identity.json)
- [Running task and parent metadata](evidence/nvcf-task-metadata-1792090018.json)
- [OSMO metadata evidence index](evidence/osmo-label-evidence-index.json)
- [Final service health](evidence/lab-health.json)
- Timestamped Pod/root metadata captures are under evidence/.

Private installation configuration remains outside this published directory.
Only the selected, sanitized observations listed in EVIDENCE.md are included.

## Installed test scope

OSMO uses released 6.3.1 code and charts. The source main checkout was inspected
separately and is not claimed to be the exact deployed build.

NVCF uses the self-managed v1.0.1 stack source, compute-plane v1.0.0 stack,
NVCA 3.10.0, and the registry-verified OSS helper images recorded in evidence/.
Only local container-function and task submission paths were tested.

All lab kubectl calls used an explicit context and a private kubeconfig copy.
The original context name is omitted here. Helm, CLI state, and port forwards are task-local.
Existing Run:AI, Karta, monitoring, scheduler, and operator installations were
preserved. The new OSMO and NVCF services remain installed for further testing.

## Source evidence

- OSMO: src/utils/job/task.py, system_labels. The inspected regex requires at
  least two characters. Email fallback keeps only the text before @.
- Run:AI: control-plane/workloads-and-policies/composer/composer_v2/
  iw_tw_workloads/iw_tw_to_job.go, job and Pod metadata composition.
- NVCF: src/compute-plane-services/nvca/pkg/types/common_labels.go.
- NVCT: src/compute-plane-services/worker-task/internal/progress/progress.go
  validates lastUpdatedAt and declares completion at percentComplete 100.
  Result upload is conditional on the UPLOAD strategy; the fixture uses NONE.

Source locations are relative to the matching repositories. The selected API
and Kubernetes captures under evidence/ support the live-test claims; see
EVIDENCE.md for the observations whose complete resource snapshots were not retained.

## Final checkpoint

- 32 lab Deployments/StatefulSets have their requested replicas ready, including
  all 13 OSMO Deployments. No lab service Pod is unready.
- All four OSMO identity workflows completed.
- NVCF function invocation returned HTTP 200; the function was undeployed after
  verification. Its definition remains in the local API.
- NVCT task 5bc18dde-08c1-445c-beee-9ab218d121c7 completed at
  2026-10-15T18:48:11Z. Both containers had been ready with zero restarts.
- No Pods remain in nvcf-backend.
- Temporary CPU reservations were restored: the affected OpenBao Pod now
  requests 50m CPU for each of its two containers and is Ready.
- No NGC push or upload was performed. NVCT resultHandlingStrategy was NONE.
- No original repository files or shared kubectl context were changed.

The first five NVCT attempts were canceled or errored while correcting image
packaging and fixture setup. Only the final task is claimed as a successful
execution. Setup workarounds are not claimed as upstream product fixes.

## CEL implementation follow-up

The subsequent [implementation report](IMPLEMENTATION.md) records two direct
Kubernetes submissions and two additional NVCF/NVCT executions with the new
exporter. Direct admission captured the request principal. NVCF metadata remained
unknown. New OSMO execution and authenticated Run:AI tests are blocked. Earlier
OSMO results above must not be presented as tests of the new CEL binary.
