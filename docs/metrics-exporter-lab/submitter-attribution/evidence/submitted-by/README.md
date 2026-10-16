<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Label-only attribution evidence

Freshly captured on 16 October 2026 against implementation commit `49ff26cc`.
The current [report](../../audit-summary.md) explains these files. Previous
`evidence/cel` captures belong to the earlier companion-metric contract.

## Requests and authentication

- `direct-*-request.json`: submitted Deployment and finite Job manifests; unrelated metadata retained.
- `direct-authentication.json`: actual client-certificate authentication, no impersonation. No certificate/private key is included.
- `direct-spoof-rejection.json` and `direct-update-rejection.json`: rejected identity forgery and subsequent modification.
- `runai-*-request-template.json`: actual training/inference specifications; only project and cluster IDs are placeholders.
- `osmo-*-request-template.json`: actual workflow specifications; audit platform name is a placeholder. Development identity headers supplied alice@example.test.
- `nvcf-function-request.json` and `nvcf-deploy-request.json`: function creation and one-instance deployment requests.
- `nvct-task-request.json`: finite task request with result uploads disabled. Its base64 environment value is the non-secret public fixture script in `../cel/nvct-task.sh`.
- `rules.json`: the actual external single CEL expression loaded by both audit exporters.

## Outputs and metrics

- `converged-resources.json`: selected root/Pod metadata from direct, Run:AI and OSMO observations. The name and UID identify each captured object.
- `converged-global.prom`: current Pod mapping/status labels for the direct and repeated OSMO fixtures, plus observation health.
- `converged-runai.prom`: Run:AI samples from the namespace-scoped exporter with exact owner read permissions.
- `nvcf-active-*`: resources and scrapes while the function was ACTIVE. Its Pod is a healthy Karta root with an empty submitted_by.
- `nvct-running-*`: resources and scrapes while the finite task ran. Its Pod also has an empty submitted_by.
- `osmo-outcomes.json`: API completion and per-task exit codes for the first and repeated single/group workflows. The repeated pair has three exit-zero tasks.
- `nvcf-outcome.json` and `nvcf-invocation.json`: ACTIVE state and invocation HTTP 200 / 896 bytes.
- `nvct-outcome.json`: COMPLETED, percentComplete 100, and API timestamps. The lastUpdatedAt timestamp records the completion state.
- `runai-inference-http.json`: successful response through the Knative revision proxy, not external ingress.
- `live-conflict-recovery.json`: captured Alice -> empty -> Alice sequence on existing metrics while observation health stayed 1.

Scrapes retain all metric labels and phase values. These are point-in-time
captures, not a promise that cleaned-up workloads remain visible now. Both
removed submitter metric families were asserted absent from every fresh scrape.
OSMO/Run:AI workload-level samples must be read from their stated capture/exporter,
not inferred from a different observer's partial scope.

## Reproduction and final state

- `lab-adjustments.json`: transport/authentication/capacity failures and their scoped corrections; no persistent OSMO code fix is claimed.
- `chart-validation.json`: fifteen explicit render cases, including changed-expression checksum behavior. Fourteen are retained as Go chart tests.
- `validation.json`: source commit, deployed binary hashes and presubmit results.
- `cleanup.json`: API deletions, local API-key revocation, direct test access removal and uploader cleanup.
- `final-lab-state.json`: all three audit deployments Ready, no remaining fixture objects in the three inspected namespaces, and native mutating-policy API availability.

Credentials, certificates, kubeconfigs, private endpoints, node names and
unrelated values are omitted. Workload UIDs remain for correlation. This lab
uses fake GPU allocation and CPU/shell/HTTP fixtures. No GPU benchmark, production
SSO, hosted NVCF, universal platform coverage or 100,000-workload proof is implied.
