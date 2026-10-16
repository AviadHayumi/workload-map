<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Workload submitter attribution

Current label-only PoC and fresh execution evidence, 16 October 2026.

Start with the [shareable PDF](audit-summary.pdf) or its [editable Markdown](audit-summary.md).
Each platform shows the submitted request, resulting workload structure, CEL
mapping, actual metric output and remaining gaps. The final page collects
verification queries and open gaps. Admission scope, CEL matching and proposed
watch filtering are distinguished explicitly.

The PDF renderer uses [its layout settings](audit-summary-layout.json). The
[final verification record](evidence/platform-source-audit/report-verification.json)
separates fresh document checks from earlier live platform captures.

| Path | Verification | Result |
| --- | --- | --- |
| Direct Kubernetes | Authenticated client certificate; Deployment and finite Job | alice@example.test; spoof and update attempts rejected |
| Run:AI API | Finite training and Knative HTTP inference | test@run.ai inherited from the root; inference Pod has no submitter key |
| OSMO API | Single tasks, leader/worker groups and administrator mapping | Completed; mapping supplies the configured full principal; missing identity stays empty; task Pods remain individual workloads |
| NVCF / NVCT | Live container function/task; local Helm render and source review | HTTP 200 and COMPLETED; human metadata absent; live Helm creation blocked by registry prerequisites |

The exporter adds only `submitted_by` to existing `karta_pod_workload_info` and
`karta_workload_status` when enabled. Karta's CRD/SDK and the health metric remain
unchanged. The earlier two companion submitter metrics are removed.

| Read this | Purpose |
| --- | --- |
| [Implementation](IMPLEMENTATION.md) | Current contract, resolution, admission and validation |
| [Examples](../../../exporter/examples/submitter-attribution/README.md) | Single CEL expression, complex key lists, Helm and queries |
| [Fresh evidence](evidence/submitted-by/README.md) | Current requests, outputs, metrics, cleanup and checks |
| [Implementation history](IMPLEMENTATION-HISTORY.md) | Earlier companion-metric prototype, fixes and original build scopes |
| [Historical results](RESULTS.md) | Earlier installation and platform issues |
| [Operator metadata proposal](OPERATOR-CONTRACT.md) | Broader platform contract; not a claim that all fields are metric labels |
| [Platform source audit](evidence/platform-source-audit/README.md) | Existing configuration options, new live captures, prerequisites and remaining limits |
| [OSMO feature request draft](OSMO-FEATURE-REQUEST.md) | Lossless principal access in administrator templates; no issue opened |
| [NVCF feature request draft](NVCF-FEATURE-REQUEST.md) | Authenticated creator/deployer capture and metadata; no issue opened |

The initial [research PDF](submitter-attribution.pdf), [design](DESIGN.md) and
existing diagrams describe earlier proposals. Their identity-source/type/status
fields and companion metrics must not be confused with the current single-label
contract. Old raw evidence remains unchanged and is explicitly historical.

The tests use a fake-GPU lab and finite shell/HTTP fixtures. Production OIDC,
hosted NVCF, all submission modes, real GPU compute and 100,000-workload identity
performance remain unproven. Metadata extraction is not authentication or
cross-tenant username normalization. Existing real-GPU, MIG and DRA reports in
the parent directory are preserved.
