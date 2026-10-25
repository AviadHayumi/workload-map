<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Optional submitter attribution

Use [values.yaml](values.yaml) or [values-complex.yaml](values-complex.yaml) with
an exporter image built from this branch. The feature is off by
default. A nonempty `exporter.attribution.submittedByExpression` adds only
`submitted_by` to `karta_pod_workload_info` and `karta_workload_status`. Their
names, values and other labels remain unchanged. Observation health has no new
label. The earlier companion submitter metric families are removed.

```sh
helm upgrade --install karta ./charts/karta \
  --namespace karta-system --create-namespace \
  --values docs/metrics-exporter-lab/examples/submitter-attribution/values.yaml
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

The simple example checks Run:AI's workload annotation, the admission annotation,
then OSMO's Pod label. The complex example filters two ordered key lists, combines
the values and selects the first nonempty result. Its precedence is deliberate:
conflicting keys inside the same object are not detected by this expression.

Pods and the selected root are evaluated independently. Their nonempty candidates
must agree. The resolved workload value is placed on both metrics, so a root-only
value can appear on a Pod sample without copying metadata to the Kubernetes Pod.
Conflicts, evaluator errors and unhealthy observations produce an empty label.
An empty label does not distinguish those causes from missing metadata. A
nonempty label can be a shortened or unverified identifier, including a service.

The selected root and all its descendant Pods participate, including Pods hidden
by `exporter.podSelector`. Intermediate owners do not contribute identity.
Configure one `exporter.attribution.submittedByExpression` using trusted metadata
keys. Legacy `enabled`, `rules`, and `resourceKinds` settings are unsupported.

## Capture direct Kubernetes submission

`submitterAdmission.enabled: true` installs native mutation and validation
policies in the Kubernetes API server. Capture needs MAP v1 on Kubernetes 1.36+
or MAP v1beta1 with feature and API enablement on 1.34-1.35. VAP v1 is also
required. The chart checks that the policies and their bindings are served.
The operator and exporter can both be disabled.

Default capture rules cover Deployments, Jobs and Pods in all namespaces.
These examples limit capture to namespaces labeled
`karta.run.ai/capture-submitter=true`. Configure native include and exclude rules
under `submitterAdmission.matchConstraints`. Every rule uses
`operations: [CREATE]` and `scope: Namespaced`. Exclusions win. An empty
namespace selector matches all namespaces.
`submitterAdmission.matchConditions` adds native CEL conditions. These settings
do not filter exporter watches.

On CREATE, mutation fills a missing `karta.run.ai/submitted-by` annotation with
`request.userInfo.username`. Validation rejects a supplied identity that differs
from the caller, including an empty value. Other annotations are preserved.
Service accounts remain service-account identities. Existing objects are not
backfilled.

A separate validation policy rejects changes or removal of an existing identity
on UPDATE. It covers all namespaced resources and their subresources, regardless
of capture selectors or conditions. This protection remains when capture is
disabled and survives Helm uninstall. Removing it requires an administrator to
delete the protection policy and binding. All policies fail closed; validation
bindings use `Deny`. Admission runs independently of exporter CEL and operator
availability.

The earlier `exporter.attribution.admission` option and operator webhook capture
are replaced by this top-level configuration. Move matching settings beneath
`submitterAdmission.matchConstraints`, remove `identitySource`, and stop using
`operator.webhookOnly`. Existing annotations from the earlier webhook are not
converted to `karta.run.ai/submitted-by`.

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

The expression compiles at startup. Helm config changes roll the exporter through
a checksum; editing a mounted ConfigMap alone does not hot-reload it. Limits are
8 KiB for the expression, 256 KiB for configuration, 10,000 CEL cost units and
512 bytes per output. Invalid configuration rejects startup. Runtime errors
clear the label; values are never silently truncated.

The [execution report](../../../docs/metrics-exporter-lab/submitter-attribution/audit-summary.md)
shows fresh submissions, workload structures, raw samples, fixes and platform
gaps. NVCF cannot supply a missing human through CEL. OSMO's shortened label
cannot reconstruct its original email domain.
