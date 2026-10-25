<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Workload submitter attribution

Current label-only PoC and fresh execution evidence, 16 October 2026.

Start with the eight-page [shareable PDF](who-submitted-the-workload.pdf) or its
[editable Markdown](who-submitted-the-workload.md). It covers configuration,
submissions, resulting metadata, metrics, required platform settings and current
gaps. Native admission conditions are implemented and tested; exporter watches
use the existing namespace setting.

The [scope verification](evidence/native-admission-scope/README.md) records the
fresh live tests, the 10,000-excluded-workload API fixture, and document checks.
The longer [audit PDF](audit-summary.pdf) and [audit Markdown](audit-summary.md)
retain earlier details. Their proposed matchConditions section is superseded by
the tested short report.

| Path | Verification | Result |
| --- | --- | --- |
| Direct Kubernetes | Authenticated client certificate; Deployment and finite Job | alice@example.test; spoof and update attempts rejected |
| Run:AI API | Finite training and Knative HTTP inference | test@run.ai inherited from the root; inference Pod has no submitter key |
| OSMO API | Single tasks, leader/worker groups and administrator mapping | Completed; mapping supplies the configured full principal; missing identity stays empty; task Pods remain individual workloads |
| NVCF / NVCT | Live container function/task and Helm function | HTTP 200 and COMPLETED; Helm supplied identity reached both Karta metrics; authenticated human metadata remains absent |

The exporter adds only `submitted_by` to existing `karta_pod_workload_info` and
`karta_workload_status` when enabled. Karta's CRD/SDK and the health metric remain
unchanged. The earlier two companion submitter metrics are removed.

| Read this | Purpose |
| --- | --- |
| [Implementation](IMPLEMENTATION.md) | Current contract, resolution, admission and validation |
| [Examples](../examples/submitter-attribution/README.md) | Single CEL expression, complex key lists, Helm and queries |
| [Fresh evidence](evidence/submitted-by/README.md) | Current requests, outputs, metrics, cleanup and checks |
| [Implementation history](IMPLEMENTATION-HISTORY.md) | Earlier companion-metric prototype, fixes and original build scopes |
| [Historical results](RESULTS.md) | Earlier installation and platform issues |
| [Operator metadata proposal](OPERATOR-CONTRACT.md) | Broader platform contract; not a claim that all fields are metric labels |
| [Native matching and watch scope](evidence/native-admission-scope/README.md) | Native conditions, finite Jobs, anti-spoof tests and namespace LIST/WATCH verification |
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
