<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Validation record

Executed on 2026-10-22. The tests below use local source and a temporary Go build
cache. They make no Kubernetes writes.

## Ownership

Working directory: exporter module at commit `01352db5`.

```sh
GOCACHE=/tmp/karta-reference-go-build go test ./pkg/discovery \
  -run 'Test(ResolveUsesTrueRoot|IgnoredOwnerIsExactGVK|LateParentAndMissingParent|ReplacementDeletionAndReparent|OwnershipNamespaceScope|OwnershipConcurrentPublicationNeverInventsRoot|ResolveMissingOwnerRemainsPendingUntilSync)$' \
  -count=1 -v
```

Result: exit 0. Seven top-level tests passed. Complete output:
[discovery-focused-tests.log](./discovery-focused-tests.log).

## CEL references

Working directory: local `cel-references` branch at `0fff67e9`. The inspected
reference implementation files match the remote branch at `cd91d670`.

```sh
GOCACHE=/tmp/karta-reference-go-build go test \
  ./pkg/references ./pkg/cel ./pkg/resource \
  -run 'Test(References|CEL|Karta)$' -count=1 \
  -ginkgo.focus='Resolve|References binding|Factory reference options|Declared list references' \
  -ginkgo.no-color -ginkgo.seed=1 -v
```

Result: exit 0. Resolver: 6/6. CEL: 8/33 selected. Factory: 7/237 selected.
Complete output: [cel-reference-focused-tests.log](./cel-reference-focused-tests.log).

## Association spike

Working directory: `docs/research/workload-references/spike`.

```sh
GOCACHE=/tmp/karta-reference-go-build go test ./... -race -count=1 -v
GOCACHE=/tmp/karta-reference-go-build go run ./cmd/associate \
  -input fixtures/osmo-historical.json -rules fixtures/osmo-rules.json
```

Result: test and capture evaluation exit 0. Tests:
[spike-tests.log](./spike-tests.log). Captured-input output:
[osmo-historical-associations.json](./osmo-historical-associations.json).
The OSMO input is explicitly historical, captured on 2026-10-16. It is not a
fresh cluster test. Target lifecycle and concurrent-job cases use synthetic
unit-test objects with placeholder identities.

## PromQL baseline

Working directory: `docs/research/workload-references/spike/promql`.

```sh
promtool test rules associations.test.yaml
```

Result: exit 0 with promtool 3.14.0, darwin/arm64. Five synthetic scenarios
contain ten expression checks. Complete output: [promql-tests.log](./promql-tests.log).
The cases cover both platform labels, root preservation, duplicate KSM scrapes,
Pod UID replacement, namespace isolation, invalid observations and conflicts.

## Remote heads

```sh
git ls-remote https://github.com/AviadHayumi/workload-map.git \
  refs/heads/cel-references refs/heads/kep-references \
  refs/heads/karta-metrics-exporter-poc-impl
```

The read-only remote command succeeded after network sandbox escalation:

```text
cd91d670a9b6613e4d1c21e066b37a8446eb9fb1 refs/heads/cel-references
01352db5c4be54b0b3eb6d53a0a04fb5faa34520 refs/heads/karta-metrics-exporter-poc-impl
4d9534b9fdfa46d337c5d5c250dbad5db282be3e refs/heads/kep-references
```
