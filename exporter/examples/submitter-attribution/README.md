<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Optional submitter attribution

Use [values.yaml](values.yaml) with operator and exporter images built from this
branch. Edit the namespace scopes and identity authorities before installing.
The feature is off by default. Existing Karta metric names and labels remain
unchanged; two companion families appear when attribution is enabled.

```sh
helm upgrade --install karta ./charts/karta \
  --namespace karta-system --create-namespace \
  --values exporter/examples/submitter-attribution/values.yaml
```

## Extract metadata

Rules use CEL maps named `labels`, `annotations`, and `resource`. The last map
contains namespace, name, group, version and kind. Missing metadata maps are
empty. Compile and type errors reject startup. Evaluate expressions only on
metadata updates; scrapes read stored results.

| Field | Required result |
| --- | --- |
| `matchExpression` | Boolean selecting the intended resource scope |
| `submittedByExpression` | String; empty means no candidate |
| `identitySourceExpression` | String identifying the authority and tenant |
| `submitterTypeExpression` | `human`, `service`, or `unknown` |
| `partial` | Set true for lossy values such as a shortened email |

Every matching candidate is compared. Different nonempty identities produce
`conflict`; no winner is guessed. Ternary chains inside a rule implement their
own precedence. They do not detect conflicts in keys that the expression skips.

Pods and observed workload resources are evaluated independently. Add exact
`exporter.attribution.resourceKinds` entries and get/list/watch RBAC for other
resource types. A resource metric does not establish a Karta workload hierarchy.

## Capture direct Kubernetes submission

`submitterAdmission.enabled` installs TLS mutation and validation handlers in the
operator. Label only intended namespaces with
`karta.run.ai/capture-submitter=true`. Keep the operator namespace out of scope.
The default resource rules cover Deployments, Jobs and Pods. Extend them for the
original CRD or controller object when needed.

The handler records `request.userInfo.username` on CREATE. It overwrites supplied
values and protects captured annotations on UPDATE. System callers creating
owned children are skipped. Direct service-account submissions remain service
identities. This cannot recover the human behind a platform's service account.
Existing objects are not backfilled. Admission uses `failurePolicy: Fail`, so
operator availability affects submissions and updates in the selected scope.

Admission is independent of CEL. An extraction-only deployment can disable it.
`operator.webhookOnly=true` serves admission without reconciling Karta definitions.
Changing the configured identity authority requires an explicit migration plan;
do not silently reinterpret existing annotations.

## Query the output

```promql
karta_workload_submitter_info{status="complete", submitter!=""}
```

```promql
count by (status) (karta_resource_submitter_info)
```

Statuses are `unknown`, `partial`, `complete`, `conflict`, `evaluation_error`, and
`stale`. Complete describes a configured identifier, not authentication or
coverage of every Pod. Keep all statuses visible when reporting coverage.
Stale and conflicting observations clear the identity fields.

Workload output merges the selected root and currently attributed Pods, not every
intermediate owner. Resource output uses resource UID; workload output uses
workload UID. Preserve those keys and cluster identity in joins. Do not join
historical GPU measurements to today's identity mapping.

Rules compile once. Helm changes trigger a rollout through a configuration
checksum; editing a mounted ConfigMap does not hot-reload the running process.
Budgets: 32 rules, 8 KiB per expression, 256 KiB configuration, 10,000 CEL cost
units per expression and 512 bytes per output field.

The [execution report](../../../docs/metrics-exporter-lab/submitter-attribution/IMPLEMENTATION.md)
contains actual submissions, raw metric examples, fixes and unverified cases.
NVCF account IDs cannot be converted into a missing human identity by CEL. OSMO
short labels remain partial. Metadata is not an identity attestation mechanism;
use trusted platform scopes and never treat these metrics as authorization.
