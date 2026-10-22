<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Live metadata comparison

The unchanged exporter and an isolated kube-state-metrics (KSM) instance run in
`test-ref`. A local Prometheus 3.14.0 process periodically scrapes both through
localhost port-forwards. The result retains the real Kubernetes root and adds
OSMO workflow or SkyPilot cluster metadata as a separate dimension.

KSM uses the public digest observed on the existing cluster installation. Its
service account can only get, list and watch Pods in `test-ref`. It requests
10m CPU and 32Mi memory. Shared monitoring is unchanged. Saved scrapes include
only the relevant Pod identity and label metric families.

## Verified results

| Check | Evidence | Result |
| --- | --- | --- |
| First Sky provisioning Pod | [KSM scrape](./ksm-labels-first.prom), [mapping](./sky-provisioning-associations.json) | Real UID and cluster label agree. No job execution claim. |
| Sky multinode head and worker | [paired scrape](./paired-scrapes-evidence.json), [replay report](./platform-replay.test.report.json) | Two distinct Pod roots retain their exact UID/name/GVK and gain the same cluster label. |
| OSMO task Pod | [paired scrape](./paired-osmo-single-evidence.json), [replay report](./osmo-single-replay.test.report.json) | One OSMO workflow UUID joins to its real Pod root. That snapshot also contains two Sky rows. |
| Running two-Pod OSMO group | [periodic query](./prometheus-osmo-group-query.json), [replay](./osmo-group-replay.test.report.json), [target mapping](./osmo-group-associations.report.json) | Both actual Pod UIDs group under one workflow UUID and one real PodGroup UID; their Kubernetes roots stay separate. |
| Actual periodic Prometheus rules | [query results](./prometheus-live-query.json) | Both targets are up. One OSMO row and the current shared Sky Pod row preserve every original root label. |
| Actual OSMO PodGroup target | [frozen input](./osmo-podgroup-capture.json), [mapping](./osmo-live-associations.json), [report](./osmo-live-associations.report.json) | Four metadata relations and one uniquely resolved real PodGroup UID. The PodGroup does not replace the Pod root. |
| Disposable lifecycle | [replay report](./lifecycle-replay.report.json), [live Prometheus queries](./prometheus-lifecycle-live.json) | Label removal/replacement, deletion and new-UID recreation converge. The deleted fixture disappears from the live rule. |

The PodGroup input records separate successful Pod and complete namespace
PodGroup reads. It does not claim an atomic snapshot. The Sky shared Pod later
reused its name and cluster label with a new UID; its
[new mapping](./sky-shared-recreated-associations.json) preserves that distinction.
A cluster label alone is not a cluster incarnation identity or a job ID.
After OSMO reported successful completion and API cleanup, its actual periodic
[group query became empty](./prometheus-osmo-group-cleanup.json).

## Reproduce the paired scrape check

`replay_scrapes.py` compares captured Pod namespace, UID and name with actual
Karta and KSM samples. An explicitly ownerless Pod must retain its exact UID,
name, kind, API group and version as the root. Captured platform labels must
agree with KSM. The generated promtool test adds only the association label.

```sh
python3 replay_scrapes.py \
  --karta karta-platform.prom --ksm ksm-platform.prom \
  --capture sky-multinode-capture.json \
  --output platform-replay.test.json
promtool test rules platform-replay.test.json
promtool test rules osmo-single-replay.test.json
```

These commands replay saved live samples. The separate periodic scrape check
uses the [local Prometheus configuration](./prometheus.yaml), the actual binary
hash and query timestamp recorded in its [result](./prometheus-live-query.json).
It runs on the workstation, with HTTP on `127.0.0.1:19092`. It creates no
Prometheus resources in the cluster. The final
[runtime check](./prometheus-runtime-status.json) confirms all six current
rules loaded and both actual scrape targets up.

## Disposable lifecycle

The separate `ref-lifecycle` Pod uses pause 3.10 by digest, requests 1m CPU and
4Mi memory, disables the service-account token and has a 600-second active
deadline. It is a generic fixture, not a SkyPilot workload. The configured input
reuses `skypilot-cluster-name`, which was already in KSM's allowlist. Its values
`ref-life-a` and `ref-life-b` are test data. The generic Go code contains no
platform keys.

The executed stages were create, remove label, replace label, delete and
recreate the same Pod name. Its UID changed from
`fb68ba26-455f-45ba-819e-3a5dbb371861` to
`246fde39-2a4a-4e71-bb80-6f3bc3f7bf49`. Each stage has a sanitized API capture,
KSM scrape, Karta scrape and timestamped report in this directory. All six
captures, including cleanup, also passed the
[generic snapshot evaluator](./lifecycle-associations.json).

```sh
python3 replay_lifecycle.py created label-removed label-replaced deleted recreated
promtool test rules lifecycle-replay.test.json
```

Six expression checks passed, including deliberate use of the old root with
the new Pod's metadata. The live Prometheus rule then returned only the new UID.
The fixture was deleted and its live rule result became empty. Managed platform
Pods were not changed by these checks.

The KSM manifest also contains `karta.test/association`. That configuration
update was delayed at the deployment controller, so lifecycle tests did not
depend on it. [Final deployment status](./ksm-final-deployment-status.json)
records generation 2 observed with one ready replica. Its normal rollout
closed the old port-forward; the new Pod was reconnected without forcing a restart.

## Limits

This comparison uses one exporter, one KSM and one cluster. The rules collapse
KSM scrape copies but retain exporter scrape labels. They do not deduplicate
multiple exporter replicas or establish a federation identity contract.

Lifecycle stages were recorded after API, KSM and valid Karta observations
converged. Their minute spacing in promtool is replay time. It is not a measured
latency bound or an atomic update guarantee. KSM can continue serving cached
labels during API watch failure. UID joins prevent cross-incarnation matches,
but do not certify metadata freshness. See the
[query scope and freshness notes](../promql/README.md).

The target lookup tests use captured Kubernetes objects. They do not add CEL
references to the exporter or implement reference watches. Platform job
completion and concurrent-job execution belong to the separate platform reports.
