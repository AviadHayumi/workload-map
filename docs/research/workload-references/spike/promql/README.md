<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# kube-state-metrics join baseline

These recording rules retain Karta's Kubernetes root and attach a separate
OSMO workflow UUID or SkyPilot cluster label. They use the actual current metric
name, `karta_pod_workload_info`, from
[collector/names.go](../../../../../exporter/pkg/collector/names.go).

Configure kube-state-metrics with this Pod label allowlist:

```text
--metric-labels-allowlist=pods=[osmo.workflow_uuid,skypilot-cluster-name]
```

The exported label names are `label_osmo_workflow_uuid` and
`label_skypilot_cluster_name`. The rules normalize KSM's `uid` to `pod_uid`,
collapse duplicate scrape copies and require one metadata value per namespace
and Pod UID. `group_left` adds only the platform label. It preserves Karta's
`workload_uid`, kind, name and owner resolution. Invalid retained Karta rows are
excluded. These examples use one exporter and one KSM in one cluster. They collapse
KSM scrape copies but retain exporter scrape labels on the left side. They do
not deduplicate multiple exporter replicas. A shared multi-cluster query must
include the same cluster label in grouping and matching on both inputs.

Source: [KSM Pod metrics](https://github.com/kubernetes/kube-state-metrics/blob/main/docs/metrics/workload/pod-metrics.md),
[KSM allowlists](https://github.com/kubernetes/kube-state-metrics/blob/main/docs/developer/cli-arguments.md),
[PromQL vector matching](https://prometheus.io/docs/prometheus/latest/querying/operators/#many-to-one-and-one-to-many-vector-matches).

Run the [official rule-test tool](https://prometheus.io/docs/prometheus/latest/configuration/unit_testing_rules/):

```sh
promtool test rules associations.test.yaml
```

Tested with promtool 3.14.0. Five synthetic scenarios contain ten expression
checks. They verify root preservation and duplicate-scrape collapse, Pod name
reuse with a new UID, namespace isolation, invalid Karta observations and
conflicting metadata. [Output](../../karta/evidence/promql-tests.log).

These synthetic cases isolate query behavior. Separate
[live captures and replay](../live/README.md) exercise the same rules against
the real exporter and isolated KSM. The metadata-only join needs no extra
association sidecar. The join does
not certify external workflow existence or divide a shared SkyPilot Pod's usage
between concurrent jobs. Scrapes from separate exporters remain eventually
consistent. If the Pod Karta is disabled and no Karta row exists, the joined
series is absent; `kube_pod_labels` can still be used directly for Pod grouping.

UID matching prevents a recreated Pod from inheriting the old Pod's root. It
does not prove that either informer's metadata is current. KSM can still serve
cached labels during API connectivity failure. Prometheus staleness handles
series no longer returned by scrapes, but cannot detect a still-exported old
label. Strict freshness needs a separate policy based on scrape and watch
health. See [Prometheus staleness](https://prometheus.io/docs/prometheus/latest/querying/basics/#staleness).
