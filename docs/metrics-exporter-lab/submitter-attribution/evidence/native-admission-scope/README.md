<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Native admission matching and exporter namespace scope

Verified on 16 October 2026. These are isolated lab captures, not production
scale measurements. Generated namespaces, Pod names and UIDs are normalized
consistently. Credentials, webhook certificates and cluster addresses are omitted.

## Changes

The chart accepts `exporter.attribution.admission.matchConditions` and mirrors
native conditions on CREATE capture and UPDATE protection. Nonempty conditions
require Kubernetes 1.30+. Empty defaults preserve the previous rendered webhooks.
The chart validates shape, names and the 64-condition limit; Kubernetes compiles
and evaluates CEL. The legacy admission block accepts the same optional list.

This follows [Kubernetes webhook matching](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#matching-requests-matchconditions).
Conditions are evaluated separately on CREATE and UPDATE. A false condition skips
that webhook. Mutable metadata, current-caller or operation filters can therefore
remove update protection; the report uses stable resource-kind conditions.

## Live results

- Namespace label inclusion plus explicit namespace-name exclusion worked.
- An included finite Job received `scope-user@example.test`.
- Jobs in excluded and unselected namespaces received no submitter annotation.
- A suspended CronJob was excluded by its native CEL condition.
- Forged submitter values were rejected on CREATE and UPDATE.
- Both webhook configuration kinds rejected malformed CEL during server dry-run.
- The short report's exact rendered webhook configurations passed server dry-run.
- All three finite Jobs completed. Temporary namespaces, webhook configurations
  and the isolated exporter Deployment were confirmed absent after cleanup.

The caller for this scope test was an administrator impersonating a dedicated
Kubernetes test user with namespace RoleBindings. Earlier client-certificate
identity tests remain in [submitted-by evidence](../submitted-by/README.md).
Existing exporter and webhook configurations were not changed.

The temporary exporter used `--namespace=scope-included`. Its metrics contained
only that namespace. The unlabeled Job Pod inherited its root's identity in both
existing metric families; workload observation health was 1. The retained scrape
was collected while Running, before the completion checks.

See [results](results.json), [matching configuration](native-matching.json),
[selected resource fields](objects.json) and [metrics](metrics.prom).

## Watch verification and limits

`TestNamespaceScopeListWatchAndHealth` runs the real exporter controller and its
REST clients against an HTTP API fixture. The selected Deployment owns an
unlabeled ReplicaSet and Pod. Another namespace contains 10,000 workload fixtures.
All workload, Pod and owner LIST/WATCH requests, including health verification
LISTs, stayed within the selected namespace. Zero excluded objects were transferred.
Karta definition discovery remained cluster-scoped. Both metric families inherited
the configured submitter. Ten race-enabled runs passed.

```sh
cd exporter
go test -race ./pkg/controller \
  -run '^TestNamespaceScopeListWatchAndHealth$' -count=10
make check
cd ..
make helm-lint helm-validate
```

The existing `exporter.namespace` is one static namespace. It does not implement
a dynamic namespace label selector and does not reduce object traffic when all
10,000 workloads are in the selected namespace. Admission selectors do not alter
exporter watches. Root-label filtering cannot be copied onto Pod/owner watches:
those objects may have no identity label.

Namespace-aware stores are an established approach in
[kube-state-metrics](https://github.com/kubernetes/kube-state-metrics/blob/main/internal/store/builder.go)
and [controller-runtime cache options](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.22.1/pkg/cache/cache.go).
A future dynamic namespace selector must manage namespace changes, informer
startup/shutdown, stale series removal, and health verification scope together.
No new watch selector or metric/CRD field was added in this change.
