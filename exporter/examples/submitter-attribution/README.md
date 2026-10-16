<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Optional submitter attribution

Use [values.yaml](values.yaml) or [values-complex.yaml](values-complex.yaml) with
operator and exporter images built from this branch. The feature is off by
default. A nonempty `exporter.attribution.submittedByExpression` adds only
`submitted_by` to `karta_pod_workload_info` and `karta_workload_status`. Their
names, values and other labels remain unchanged. Observation health has no new
label. The earlier companion submitter metric families are removed.

```sh
helm upgrade --install karta ./charts/karta \
  --namespace karta-system --create-namespace \
  --values exporter/examples/submitter-attribution/values.yaml
```

For an existing installation, review the rendered change and merge these options
with its current values and image references. The example is not a complete
replacement for an installation's configuration.

## Extract metadata

CEL receives maps named `labels`, `annotations`, and `resource`. The resource
map contains namespace, name, group, version and kind. Missing metadata maps are
empty. The expression must return a string; empty means no candidate. A literal
dot in a map key does not need a backslash. Neither expression performs platform
API calls or directory lookups.

The simple example checks the admission annotation, Run:AI's workload annotation,
then OSMO's Pod label. The complex example filters two ordered key lists, combines
the values and selects the first nonempty result. Its precedence is deliberate:
conflicting keys inside the same object are not detected by this expression.

Pods and the selected root are evaluated independently. Their nonempty candidates
must agree. The resolved workload value is placed on both metrics, so a root-only
value can appear on a Pod sample without copying metadata to the Kubernetes Pod.
Conflicts, evaluator errors and unhealthy observations produce an empty label.
An empty label does not distinguish those causes from missing metadata. A
nonempty label can be a shortened or unverified identifier, including a service.

Only the selected root and its currently attributed Pods participate. Additional
`exporter.attribution.resourceKinds` and explicit read RBAC can observe metadata
on other kinds, but do not make an intermediate parent a root or a contributor
to the resolved label. No separate resource-identity metric is emitted.

Legacy `exporter.attribution.enabled` plus `rules` remains supported. Those rules
have match, submitter, source, type and partial fields for internal resolution.
Their extra fields are not metric labels. Configure rules or the single
expression, never both. Use scoped expressions and trusted metadata keys.

## Capture direct Kubernetes submission

`exporter.attribution.admission.enabled` installs scoped TLS mutation and
validation webhooks in the operator. Label intended namespaces with
`karta.run.ai/capture-submitter=true`. Keep the operator namespace out of scope.
Default rules cover Deployments, Jobs and Pods; extend them for the original
controller or CRD when needed. This is an operator webhook, not a native
MutatingAdmissionPolicy.

On CREATE, the handler records `request.userInfo.username` in
`workload.dgxc.nvidia.com/submitter-id`, plus identity-source and submitter-type
annotations. It preserves unrelated metadata, accepts matching captured values,
and rejects conflicting supplied values instead of overwriting them. Captured
fields are immutable on UPDATE within the selected scope. Existing objects are
not backfilled. System callers creating owned children are skipped; direct
service-account submissions remain service identities. The handler cannot infer
a human behind a platform's service account.

Admission is independent of CEL. It uses `failurePolicy: Fail`, so operator
availability affects matching submissions and updates. The legacy top-level
`submitterAdmission` settings remain supported, but cannot be enabled alongside
the nested option. `operator.webhookOnly=true` serves admission without Karta
reconciliation. Changing the identity source needs a deliberate migration.

## Query the output

Show current Pod-to-workload mappings with the extracted label:

```promql
karta_pod_workload_info{namespace="example-team"}
```

Show active phase samples without discarding missing identity:

```promql
karta_workload_status{namespace="example-team"} == 1
```

Count workloads by the raw identifier, deduplicating phase samples:

```promql
count by (submitted_by) (
  max by (namespace, workload_uid, submitted_by) (karta_workload_status)
)
```

These are current observations, not a durable submitter ledger. Use workload and
Pod UIDs plus a consistent cluster label for joins. Never join old GPU samples
to today's mapping. Do not assume that equal names from different authorities
identify the same person, or use these metrics as an authorization check.

Rules compile at startup. Helm config changes roll the exporter through a
checksum; editing a mounted ConfigMap alone does not hot-reload it. Bounds are
32 legacy rules, 8 KiB per expression, 256 KiB configuration, 10,000 CEL cost
units per expression and 512 bytes per output. Invalid configuration rejects
startup. Runtime errors clear the label; values are never silently truncated.

The [execution report](../../../docs/metrics-exporter-lab/submitter-attribution/audit-summary.md)
shows fresh submissions, workload structures, raw samples, fixes and platform
gaps. NVCF cannot supply a missing human through CEL. OSMO's shortened label
cannot reconstruct its original email domain.
