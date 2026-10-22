<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Association snapshot spike

This program reads captured Pods and configurable label or annotation rules.
It emits separate association results. It never emits `workload_uid`, changes a
Kubernetes owner reference or replaces Karta's root selection.

For example, a captured OSMO Pod's `osmo.workflow_uuid` label produces an
`osmo-workflow` association. Its Pod UID stays the UID read from Kubernetes.
The logical workflow value is not turned into a Kubernetes UID or GVK.

## Run

```sh
GOCACHE=/tmp/karta-reference-go-build go test ./... -race -count=1 -v
GOCACHE=/tmp/karta-reference-go-build go run ./cmd/associate \
  -input fixtures/osmo-historical.json -rules fixtures/osmo-rules.json
```

The Go module is isolated from production packages and uses the standard library.
Metadata keys are configuration. The existing CEL reference library is tested
separately in [the source contract](../karta/README.md). This spike does not add a
second expression engine or implement the proposed reference KEP.

## Input and result

The capture contains raw Kubernetes Pod metadata under `pods`. It can also carry
named `targetSets`. A target has a logical ID, namespace and matching attributes.
If it represents a real Kubernetes object, its `kubernetes` field must carry the
captured API version, kind, namespace, name and UID. The caller supplies provenance
alongside these fields, distinguishing live, historical and generated input.

This is a single-cluster snapshot demo. `Capture` and `Result` do not encode a
cluster ID, platform instance or provenance. The CLI ignores the capture's
provenance field; the surrounding evidence preserves it. Logical IDs are scoped
to the supplied platform snapshot. Combining captures from different clusters
or platform instances requires an explicit identity contract. This schema is
not a federation identity contract.

Each rule names a relation and a source label or annotation. An optional
`targetSet` and `matchAttribute` requests a unique target with a matching value in
the same namespace. Source keys and target kinds are not hardcoded in Go.

| Result | Meaning |
| --- | --- |
| `observed_metadata` | A Pod carries a value. No external existence or ownership claim. |
| `resolved_target` | Exactly one supplied target matches in the Pod namespace. |
| `absent` | No source value, or a complete target set contains no match. |
| `ambiguous` | More than one target matches; no target is selected. |
| `error` | The target read failed, is incomplete, was not supplied, or has incomplete identity. |

The optional target mode models a query over a complete snapshot. It trusts
the capture producer to certify completeness and identity. It does not verify
the Kubernetes API, external database or authorization. Those are integration
requirements, not properties proved by this program.

Every invocation replaces the full result. Pod deletion removes its rows. A
recreated Pod uses its new UID. Target deletion removes the association; a
replacement target uses its new real UID. A streaming implementation would need
watch synchronization, dependency invalidation and freshness checks.

## Platform scope

The fixture in `fixtures/osmo-historical.json` is a sanitized historical capture
from 2026-10-16. It is not a
new submission in `test-ref`. It has no complete PodGroup inventory. Missing
PodGroup annotations therefore produce `absent`, without inventing groups.

Separate [live evidence](./live/README.md) covers real OSMO task and group Pods,
complete PodGroup inventories, Sky Pod captures and a disposable lifecycle.
The two-Pod OSMO group resolves to one real PodGroup while retaining two Pod
roots in the exporter. Historical and live inputs remain separate.

The shared-cluster unit test supplies two logical job targets for one Pod label.
The result is `ambiguous`. This validates rejection of exclusive job attribution;
it does not measure either job's share of CPU or GPU usage.

The output is a proposed downstream association representation, not a new public
Karta metric contract. kube-state-metrics label/annotation allowlists and a
downstream join can supply the metadata-only case without this program running
as a service. The [PromQL baseline](./promql/README.md) is validated with promtool.
See [the options comparison](../karta/README.md#options-for-a-two-week-increment).
