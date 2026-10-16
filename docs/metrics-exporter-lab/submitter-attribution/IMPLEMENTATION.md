<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Submitter attribution implementation

Current contract and fresh eight-case audit, 16 October 2026.

The [shareable report](audit-summary.md) and [PDF](audit-summary.pdf) show each
submission, resulting workload structure, CEL mapping, metric output and gap.
The [evidence directory](evidence/submitted-by/README.md) contains the sanitized
requests and captures from the current label-only implementation.

## Contract

Karta's CRD and SDK are unchanged. Attribution is opt-in. A nonempty
`exporter.attribution.submittedByExpression` enables extraction. It adds exactly
one label, `submitted_by`, to `karta_pod_workload_info` and
`karta_workload_status`. Their other labels, metric names and value meanings are
unchanged. `karta_exporter_observation_healthy` remains unchanged.

The earlier `karta_resource_submitter_info` and
`karta_workload_submitter_info` families are removed. With attribution disabled,
the original exact three-family contract remains unchanged.

Both existing advanced rules and the new single expression are supported.
Configuring both is rejected. The nested admission option and legacy top-level
`submitterAdmission` are likewise alternatives. This preserves the earlier
configuration path without maintaining the removed metric families.

## Resolution

1. Evaluate CEL against cached labels, annotations and resource identity.
2. Combine the selected workload root and its currently attributed Pods.
3. Inherit a root-only value onto the Pod metric without writing Pod metadata.
4. Emit the shared resolved value on both existing metrics.
5. Clear the value for conflicts, errors or unhealthy observations.

The Pod mapping must still match the selected root, its UID and owner resolution.
Metadata changes reevaluate identity; status-only updates can reuse extraction.
Deletion removes UID-keyed records. A stale/mismatched association cannot inject
another workload's identity. The current scrape aggregates stored results; it
makes no platform API calls or CEL evaluations.

Only the selected root and its Pods contribute. Extra metadata watches can read
other kinds, but their identity does not automatically flow through intermediate
owners. No separate resource-identity metric is emitted. Unsupported structural
relationships still need an appropriate Karta definition/owner mapping.

## Admission and preservation

`exporter.attribution.admission.enabled` uses the operator's existing TLS webhook.
The tested cluster exposes validating admission policies, but no native mutating
admission policy API. Namespace and resource selectors define the capture scope.
An explicit nonempty namespace selector is required.

CREATE records the request principal as the canonical submitter annotation plus
source/type annotations. Existing matching values and unrelated metadata are
preserved. Conflicting supplied values are rejected instead of overwritten.
Captured fields are immutable on UPDATE within scope. System-owned child creates
are skipped. No existing resources are backfilled.

The test used an actual one-hour client certificate for `alice@example.test`.
It did not use impersonation. Its scoped Role/RoleBinding and CSR were removed
after the completed test. This proves capture of that authenticated principal,
not production identity-provider integration or that all principals are human.

## Configuration and limits

The [example guide](../../../exporter/examples/submitter-attribution/README.md)
explains both CEL examples, rollout behavior and queries. Generic extraction
keys belong to configuration, not platform-specific code in Karta core.

- CEL variables are string maps: `labels`, `annotations`, `resource`.
- The resource map has namespace, name, group, version and kind.
- Missing maps are empty; a missing unguarded key can fail evaluation.
- The expression returns a string. Empty means no candidate.
- First-key selection can hide same-object conflicts; cross-object differences clear the resolved label.
- Rules compile at startup. A Helm config checksum triggers rollout; direct ConfigMap editing is not hot reload.
- Limits: 32 legacy rules, 8,192 bytes per expression, 256 KiB config, 10,000 cost units per expression, 512 bytes per output.
- Runtime errors, invalid/control characters and oversized values clear the label. Identity is not truncated.
- A single label cannot expose missing versus conflict versus evaluation error, authority, completeness or principal type.
- A nonempty raw value is not a directory-verified identity. The simple expression does not distinguish equal strings from different authorities.
- Multiple served API versions of one kind and 100,000-workload identity performance remain unvalidated.

## Test and deployment scope

All eight cases were freshly submitted on implementation commit `49ff26cc`:
direct Deployment/Job, Run:AI training/inference, OSMO single/group, NVCF function
and NVCT finite task. The report records authentication scope and limitations
beside each result. These were CPU/shell/HTTP fixtures with fake GPU allocation,
not real GPU computation.

Exporter and operator presubmits passed. Root `make check` passed in a clean
checkout of the implementation commit, preserving unrelated untracked documents.
Helm lint/validation and existing Prometheus rule tests passed. Fourteen chart
regression cases are retained in `exporter/test/chart`; a fifteenth explicit
check verifies that changing the expression changes the rollout checksum.

The live conflict test proved Alice -> empty -> Alice recovery on both metrics,
with healthy observation throughout. Spoofed creation and captured-identity
updates were denied. No old companion families were found in fresh scrapes.

The audit uses two exporters: one general observer and a Run:AI namespace-scoped
observer with exact WorkloadRunner reads. No cluster-wide WorkloadRunner grant
was added. An independently named ConfigMap and binary filenames preserve the
old audit configuration for recovery. The public demo was not redeployed.

The lab runs local Linux binaries through a temporary volume. It does not claim
published-image provenance or production release acceptance. All fixture
resources and temporary local API keys were cleaned up after evidence capture.
The operator and both exporters remained Ready.

## Earlier findings

[Implementation history](IMPLEMENTATION-HISTORY.md) preserves the earlier
companion-metric prototype and its original tests. Those metric samples are
historical, not the current contract. Its owner-scope, cache-key and API
representation fixes are included in the current rerun. The earlier NVCA
malformed-request cancellation gap was not fixed upstream; it did not recur in
the fresh successful fixtures.
