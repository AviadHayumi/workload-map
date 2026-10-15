<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Interface integration validation

The integration starts from discovery SDK commit `ce91b4a0` and ports the offline
interface from `feat/karta-workload-interface` at `b063a855`. It preserves the
current module path, dependency versions, compiled-selector cache, catalog,
replica-selector opt-out behavior, exporter metric contract and Helm values.

## Issues addressed

| Issue | Correction | Regression coverage |
| --- | --- | --- |
| The desired tree omitted the root's template and raw status | Add `Root` and `RootStatus`; preserve existing child view | [Tree inspection tests](../../pkg/tree/inspection_test.go) |
| Typed nil input could panic | Reject nil workload objects and update values | [Constructor tests](../../pkg/karta/regression_test.go), [patch tests](../../pkg/karta/patch_contract_test.go) |
| A read-only split metadata path blocked a spec-only update | Check and route only the requested arm | [Interface regression tests](../../pkg/karta/regression_test.go) |
| Targeting one instance could add null fields to another | Write only selected concrete locations | [Patch contract tests](../../pkg/karta/patch_contract_test.go) |
| Affinity replacement retained old scheduling constraints | Replace the selected affinity object, including an empty object | [Patch contract tests](../../pkg/karta/patch_contract_test.go) |
| Dotted and slashed map keys were interpreted as jq paths | Encode patch-derived field names as literal keys | [Patch contract tests](../../pkg/karta/patch_contract_test.go) |
| JSON conversion could round large integer input | Reject integer tokens outside the safe range at the new interface boundary | [Interface regression tests](../../pkg/karta/regression_test.go) |
| Returned float metadata broke integer getters such as `GetGeneration` | Restore Kubernetes unstructured number types at `Object()` | [Patch contract tests](../../pkg/karta/patch_contract_test.go) |
| A recording fake retained caller-owned patch/capability slices | Copy recorded input and returned capabilities | [Interface regression tests](../../pkg/karta/regression_test.go) |

Mutations use a detached scratch object and publish it only after every write
succeeds. Tests include failure after an earlier write, constructor/input/output
isolation, instance targeting, malformed patches, and unsupported capabilities.
The fake records mutation intent; it does not simulate a cluster or provide a
populated inspection tree.

## Reproduce the checks

From the repository root:

```bash
go test ./examples/workloadinterface -run '^Example' -v
go test -race ./pkg/karta/... ./pkg/tree ./pkg/jq/execution ./pkg/resource \
  ./pkg/ownership ./pkg/attribution ./pkg/discovery/... ./examples/workloadinterface
golangci-lint run -c .golangci.yml
```

From `exporter/`:

```bash
go test ./...
```

The repository presubmit is `make check`. Its generated-file validation requires
a clean checkout; run it after committing the intended changes.

The [kind guide](../../test/discovery/README.md) includes fixture provisioning,
operator versions, the independent UID oracle and the live command. The interface
checks run as part of `TestLiveOperatorMembership` in both direct and indexed
modes. They execute the same consumer functions linked from the README.

| Real fixture | Observed Pods | Desired template locations |
| --- | --- | --- |
| JobSet | 3 | 2 |
| LeaderWorkerSet | 4 | 2 |
| Deployment | 2 | 1 |
| Generic Bundle over two Deployments | 3 | No Karta template inferred |
| Standalone Pod | 1 | 1 |
| Deployment with zero replicas | 0 | 1 |
| Completed Job | 1 | 1 |

Separate live tests cover owner reparenting, deletion, UID reuse, RBAC denial,
unscheduled and terminating Pods, index invalidation, and a cluster-scoped owner
with Pods in two namespaces. These fixtures verify ownership and SDK semantics.
They do not rerun GPU, MIG, DRA or DCGM validation, migrate production EWI, or
establish production capacity at 100,000 workloads.
