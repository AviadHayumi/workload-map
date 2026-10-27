<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# OTel Collector

`Register` takes an OTel Meter and a Store `Capturer` and returns a
`metric.Registration`. It registers one callback for the four KEP metric families.
Each collection captures Store once, checks evidence validity and emits saved
values. It does not read Kubernetes or run expressions.

The command owns the reader, HTTP endpoint and provider shutdown. It unregisters
the callback after Runtime stops, before shutting down the provider. Configure
`sdkmetric.WithCardinalityLimit(0)` on that provider: the SDK default of 2000
series per instrument would merge workload identities into an overflow series.
The command disables OTel scope labels to keep the KEP label sets unchanged.

The callback emits the nine SDK phases as dense zero-or-one status samples.
Saved custom label maps are added to Pod info and status. Built-in labels take
precedence. Creation timestamps are emitted only when positive and verified.
Invalid status and Pod mappings retain their values with
`observation_valid="false"`. Workload health reports one bounded failure reason, including invalid creation
evidence even when no status or Pod mappings are present.

Tests use an OTel Prometheus reader and HTTP handler to verify the exported
series. Runtime integration tests can publish through `store.Writer` and scrape
the same reader. Neither pure component starts a Kubernetes client or envtest
server.

Pod-info uses each Pod's namespace and verified workload GVK. Status, creation
and workload health use the root namespace, which is empty for a cluster-scoped root. Removing a publication
or Pod row removes its series on the next collection.

## Verify

From the exporter module directory:

```sh
go test -race ./pkg/adapters/otel
```

The HTTP exposition tests check all four families, all nine phases, arbitrary
custom labels, invalid evidence, creation failures and series removal. A 5001-Pod
test checks that the configured provider preserves every identity without an
OTel overflow series.
