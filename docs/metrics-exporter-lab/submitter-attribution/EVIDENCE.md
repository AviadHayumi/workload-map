<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Evidence and reproducibility

Evidence checkpoint: 15 October 2026 UTC. Document assembled 16 October 2026.
The final health check is historical; it does not assert current cluster health.

## Versions and modes

| Item | Inspected or deployed revision | Scope |
| --- | --- | --- |
| Report repository base | `5d0661ab19a6830625f26f77faf51cbe3ea5964f` | Existing SDK, owner index and metric contract |
| OSMO source inspection | `19b5170268bc2e221f2db9367f5893687b060864` | Label-generation source |
| OSMO live release | 6.3.1; `ed59b69715e4ee6fff70bcd9d715106358a7205f` | Matching released runtime/charts; development identity headers |
| NVCF source inspection | `81d815fedda70477655a4ecc1b1a4ee6b4187a32` | NVCA metadata and audit principal derivation |
| NVCF control-plane stack | self-managed/v1.0.1; `67c010f24304b5c06d0493fa1b976a69b6ee8dea` | Local self-managed installation |
| NVCF compute stack | v1.0.0; `63ac4456d3422508f2ff4b219c7dff8fba1c21c0` | NVCA operator/agent 3.10.0 |
| Task helper images | init OSS 1.2.1; task OSS 1.1.4 | Registry-verified images; result uploads disabled |
| Run:AI source | `7add034fd350afbebdfafc4213afd4e29071e6a6` | Composer inspection only; no authenticated live submission |

The KIND cluster advertised fake GPU resources. Workloads exercised scheduling,
service execution and metadata propagation. No real GPU computation was tested
in this experiment. NGC access was read/pull only; no artifact was pushed there.

## Source-to-claim map

Paths below are relative to the named repository. No private source repository
URLs or credentials are included. A source inspection is not a live test.

| Claim | Repository path / symbol |
| --- | --- |
| OSMO email fallback and single-character omission | OSMO `src/utils/job/task.py`, `system_labels` |
| Run:AI root and Pod annotations | Run:AI `control-plane/workloads-and-policies/composer/composer_v2/iw_tw_workloads/iw_tw_to_job.go`; constants in `composer/runai_constants.go` |
| NVCF account/function/task labels | NVCF `src/compute-plane-services/nvca/pkg/types/common_labels.go`, `GetLabelsForRequest` |
| NVCF audit actor and subject derivation | NVCF `src/libraries/java/nv-boot-parent/nv-boot-starter-audit/src/main/java/com/nvidia/boot/audit/AuditService.java`, `getActorId`, `getSubjectId` |
| NVCT helper initializes/chmods result directory | NVCF `src/compute-plane-services/worker-task/internal/worker/worker.go`, `Setup` |
| NVCT timestamp validation and completion at 100 | NVCF `src/compute-plane-services/worker-task/internal/progress/progress.go`, `sendProgressUpdate` |
| SDK built-in definition resolution | [pkg/catalog/catalog.go](../../../pkg/catalog/catalog.go) via repository-root path noted below; `Get`, `ErrNotFound` |
| SDK factory and tree | `pkg/resource/component_factory.go`, `NewComponentFactoryFromObject`; `pkg/tree/builder.go`, `Build` |
| Owner changes are compared by UID/edge | `exporter/pkg/owner/index.go`, `UpsertTrackedObject`; controller lifecycle tests |
| Existing mapping labels include Pod/workload UID | `exporter/pkg/collector/collector.go` and `names.go` |

Local repository links: [catalog](../../../pkg/catalog/catalog.go),
[factory](../../../pkg/resource/component_factory.go),
[tree](../../../pkg/tree/builder.go),
[owner index](../../../exporter/pkg/owner/index.go),
[owner lifecycle tests](../../../exporter/pkg/controller/ownership_test.go),
[metric names](../../../exporter/pkg/collector/names.go).

## Selected observations

| Observation | Saved evidence |
| --- | --- |
| Four OSMO workflows COMPLETED; full API identity | [Workflow outcomes](evidence/osmo-identity-results.json) |
| Existing Pod/PodGroup metadata captures | [Metadata index](evidence/osmo-label-evidence-index.json), [alice](evidence/osmo-alice-resources.json), [single-character case](evidence/osmo-edge-case-resources.json) |
| NVCF function ACTIVE | [Function state](evidence/nvcf-function-active.json) |
| Function invocation HTTP 200 | [Invocation result](evidence/nvcf-function-invocation.json) |
| Function creation audit correlation | [Function audit](evidence/nvcf-api-audit-identity.json) |
| Finite NVCT task COMPLETED | [Task result](evidence/nvcf-task-completed.json) |
| Running task and ICMSRequest identity | [Metadata](evidence/nvcf-task-metadata-1792090018.json) |
| Creation and completion audit events | [Task audit](evidence/nvct-api-audit-identity.json) |
| Malformed request cleanup preconditions | [Cleanup](evidence/nvcf-malformed-request-cleanup.json) |
| All 32 lab controllers ready; temporary CPU restored | [Final health](evidence/lab-health.json) |

The two email-to-alice observations were recorded during execution in
[FINDINGS.md](FINDINGS.md). Their final API outcome files remain, but the final
resource snapshot is empty after cleanup and independent raw Pod snapshots for
both email runs were not retained in this package. The formatting behavior is
also supported by the inspected source. This distinction is intentional.

Published JSON is a selected-field projection of local observations. Container
arguments, environment values, endpoints, tokens, kubeconfigs and rendered Secret
manifests are omitted. The local issuer in one audit is replaced with a marker;
client/subject type and correlation IDs are retained. These are not full cluster
backups or directly replayable installation manifests.

## Reproduction outline

1. Create an isolated lab and use an explicit context plus a private kubeconfig.
2. Install matching released OSMO charts/runtime and register its backend.
3. Submit finite workflows under the four documented development identities.
   Capture both API identity and Pod/PodGroup metadata before cleanup.
4. Install the pinned self-managed NVCF control/compute stack. Register the lab
   cluster and configure an instance shape compatible with its advertised GPUs.
5. Create/deploy a finite function fixture and invoke its supported HTTP method.
   Record resource IDs and matching creation audit. Undeploy after verification.
6. Submit a finite task using resolved helper images and a valid progress file:
   monotonically increasing percent, matching task ID and RFC3339 timestamp.
   Use result handling NONE. Capture metadata while running and terminal status.
7. Restore temporary resource adjustments; verify readiness and test cleanup.
8. Repeat separately with production authentication before asserting human identity.

Detailed fixes and remaining installation limitations are in FINDINGS.md and
RESULTS.md. This is a reproducibility outline, not a promise of a one-command
production installation. No CEL, admission-stamping or canonical-metadata end-to-end
test has been run. No production performance conclusion follows from this lab.

## Follow-up ownership

Platform owners define and preserve authenticated principal semantics. The fleet
operator agrees the authority/tenant namespaces, verified cross-source mappings,
canonical display usernames and organization identifiers. Those agreements were
not established by this lab. The extractor
maintainer implements bounded evaluation, provenance and cache invalidation.
Monitoring owners choose cardinality, retention, queries and display behavior.
The consumer owns any action taken from those observations.
