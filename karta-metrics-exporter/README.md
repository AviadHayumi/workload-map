<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta metrics exporter

This branch provides the contracts for the exporter components. It does not yet
watch Kubernetes or expose metrics. The CEL label evaluator is implemented. Source selection and metric delivery
remain scaffold work.

| Package | Input and result |
| --- | --- |
| [kartadefinitioninventory](pkg/kartadefinitioninventory/) | Root GVK -> selected Karta and its source |
| [discovery](pkg/discovery/) | Object reference -> workload root and missing root coverage |
| [labelevaluator](pkg/labelevaluator/) | One object -> one configured label value |
| [labelresolver](pkg/labelresolver/) | Root, optional Pod and metric names -> labels for each metric |
| [controller](pkg/controller/) | Watches and queued work -> publications |
| [store](pkg/store/) | Publications -> consistent captured view |
| [adapters/otel](pkg/adapters/otel/) | Captured view -> OTel observations |

The command reads `labelRules` and `labelLimits`, validates the rules and compiles
their expressions at startup. The remaining components expose interfaces and
data types for their implementations.

## Run

```sh
make build-exporter
./bin/karta-metrics-exporter --version
./bin/karta-metrics-exporter
```

Startup currently uses generated Mockery mocks and an OTel no-op meter. The runtime
waits for SIGINT or SIGTERM. It does not process example workloads, open an HTTP
endpoint or produce metrics. Discovery and label calls return
`ErrNotImplemented`. Unexpected Store writes fail rather than silently losing data.

[internal/scaffold/components.go](internal/scaffold/components.go) is the wiring
point. Each factory returns a component interface. Replace a factory with the
real constructor when that component is ready. Its dependencies are already
passed there. The mock factories do not use those dependencies yet.

Store owns its locking. Runtime orders input verification and publication.
Startup registers collection before starting workers. Shutdown waits for Runtime to stop,
then unregisters collection. An eventual delivery provider and HTTP server belong
in the command; neither is implemented here.

## Develop a component

Implement the interface in its package. Keep the component tests next to the
implementation. Use Ginkgo/Gomega, as in the SDK and operator. Mocks are generated into `internal/scaffold/mocks` for the
scaffold and consumer tests:

```sh
make exporter-generate-mocks
make check-exporter
```

The generator is pinned to Mockery v3.8.1. `make validate` also regenerates mocks
to detect drift. Public component contracts are still under development.

Build the image with `make exporter-image IMAGE_TAG=dev`. The chart keeps the
exporter disabled by default. Use `exporter.labelRules` and `exporter.labelLimits`; the old attribution settings
are removed. See the [configuration and migration example](pkg/labelevaluator/README.md).
