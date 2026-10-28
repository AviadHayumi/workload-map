<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# OTel collector

`Register` takes an OTel Meter and a Store `Capturer`. It returns a
`metric.Registration` with one callback for the four KEP metric families. Each
collection captures Store once and formats the saved results. It does not read
Kubernetes or run expressions.

The command owns the reader, HTTP endpoint and provider shutdown. It unregisters
the callback after Runtime stops, before shutting down the provider. Configure
`sdkmetric.WithCardinalityLimit(0)` on that provider: the SDK default of 2000
series per instrument would merge workload identities into an overflow series.
The command disables OTel scope labels to keep the KEP label sets unchanged.

The callback emits the nine SDK phases as dense zero-or-one status samples.
Saved custom labels appear on Pod info and status. Built-in labels take
precedence. Creation timestamps appear only when positive and their root sources
and discovery are healthy. A status-expression or Karta-source failure alone does
not remove creation output.

Invalid status and Pod mappings retain their saved values with
`observation_valid="false"`. Validity describes observed source health and the
last evaluation outcome. Healthy silence does not expire results. A scrape can
see a new workload status with an older Pod mapping because the workers publish
independently.

Pod info uses each Pod's namespace and saved root identity, even if no workload
result exists yet. Status, creation and workload health use the root namespace,
which is empty for a cluster-scoped root. Removing one result removes only its
series on the next collection.

## Failure scopes

`karta_exporter_observation_valid` uses the existing identity labels and these
scope values:

- `resource_type`: source collection health at its actual GVR and namespace scope,
  identified in the metric by the source GVK. Healthy sources have value 1 and
  reason `none`. Unhealthy sources have value 0.
- `workload`: failures across all saved Pod and workload results for one known
  root, including a first-attempt failure after discovering that root. One value
  0 is emitted per root. A successful Pod cannot clear another Pod's error.
- `discovery`: first-attempt errors without a confirmed root. The resource GVK and
  namespace identify the input collection. Workload name and UID are empty.
  Identical input GVK, namespace and reason are combined into one value 0. This
  scope applies to both Pods and selected non-Pod candidates.

Workload errors use deterministic precedence: `permission_denied`,
`api_unavailable`, `invalid_definition`, `unresolved_owner`, then
`evaluation_failed`. Unrecognized errors become `api_unavailable`, keeping reason
cardinality bounded. A first-attempt error emits no Pod mapping or workload phase
value. Source recovery does not clear saved object errors.

## Verify

From the exporter module directory:

```sh
go test -race ./pkg/adapters/otel
go test ./pkg/adapters/otel -run '^$' -bench BenchmarkCollectIndependentResults -benchmem
```

HTTP exposition tests check all four families, all nine phases, custom labels,
independent result updates, source recovery, discovery errors and series removal.
A 5001-Pod test checks that every identity survives without an OTel overflow
series. The collection benchmark covers many roots and a root with many Pods.
