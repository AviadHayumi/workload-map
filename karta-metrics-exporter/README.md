<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter

The exporter watches Kubernetes workloads and Pods, resolves their ownership,
and exposes Prometheus metrics through OpenTelemetry. For example, a Pod owned
by a ReplicaSet and Deployment produces a Pod-info row identifying the Deployment
root and the Pod UID. The Deployment also has status and creation-time rows,
even when it has no Pods.

## Components

| Package | Responsibility |
| --- | --- |
| [kartadefinitioninventory](pkg/kartadefinitioninventory/) | Select a catalog or cluster Karta for each root GVK |
| [discovery](pkg/discovery/) | Resolve ownership and report missing root coverage |
| [labelevaluator](pkg/labelevaluator/) | Compile and evaluate one CEL expression against a full object |
| [labelresolver](pkg/labelresolver/) | Resolve configured labels with ordered source fallback |
| [controller](pkg/controller/) | Watch inputs, reconcile roots and publish verified results |
| [store](pkg/store/) | Own immutable publications, evidence validity and captured views |
| [adapters/otel](pkg/adapters/otel/) | Emit saved observations from one captured view per scrape |

Runtime evaluates labels when inputs change. Collection reads saved values and
does not call Kubernetes or CEL. Store owns synchronization; Runtime orders
input verification and publication. The application registers collection before
workers start. Shutdown stops workers before unregistering collection and
closing the metric provider.

## Run

From the repository root:

```sh
make build-exporter
./bin/karta-metrics-exporter --version
./bin/karta-metrics-exporter --kubeconfig /path/to/kubeconfig \
  --config /path/to/exporter.yaml --metrics-bind-address 127.0.0.1:8080
```

The same listener serves `/metrics`, `/healthz` and `/readyz`. The default listen
address is `:8080`. In Kubernetes, the command uses its ServiceAccount credentials.
A config file uses the exporter settings directly, without the Helm `exporter:`
wrapper. See the [label configuration example](pkg/labelevaluator/README.md#configuration)
and [deployment configuration](../docs/karta-metrics-exporter/configuration.md).

Build an image with `make exporter-image IMAGE_TAG=dev`. The chart keeps the
exporter disabled by default. Set `exporter.enabled=true` to enable it and
`operator.enabled=false` for a standalone exporter installation. Its RBAC grants
read-only access. Configure additional workload permissions with
`exporter.rbac.workloadRules`.

## Verify

See the [verification results](../docs/karta-metrics-exporter/verification.md)
for component coverage, deployed tests and measured scale limits.

Run commands from the repository root unless shown otherwise:

```sh
make test-exporter-unit
make test-exporter-integration
make build-exporter-e2e
make exporter-rules-test
make check-exporter
```

Unit and chart tests need no Kubernetes API server. The integration target starts
an envtest API server for Discovery, Inventory and Runtime tests. Pure component
race tests can be run independently:

```sh
cd karta-metrics-exporter
go test -race ./pkg/store ./pkg/adapters/otel ./pkg/labelevaluator ./pkg/labelresolver
```

The [operator matrix](test/e2e/) installs the real exporter into an isolated
cluster with the repository's pinned operators. Run `make test-exporter-e2e`
from the repository root after building and loading a fresh exporter image.
Set `KUBECONFIG` and `EXPORTER_E2E_IMAGE` explicitly; the matrix guide documents
context, focus, API profile and report options. `make check-exporter` compiles
the matrix and monitoring suites without running their deployed tests.
The separate [monitoring suite](test/monitoring/) verifies deployed Prometheus,
RBAC, Helm and NetworkPolicy behavior. The [scale harness](test/scale/)
uses a modeled Kubernetes API; its results do not establish production capacity.
Prometheus query examples have separate promtool fixtures in [test/rules](test/rules/).

Component mocks are generated into `internal/testutil/mocks` with
`make exporter-generate-mocks`, using pinned Mockery v3.8.1. They support consumer
tests; the running command wires real components through `internal/app`.
