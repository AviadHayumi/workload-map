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

## SDK usage examples

Eight runnable examples cover inventory inspection, template updates, Pod counts,
component scopes, discovery from a Pod, zero-Pod workloads, generic workload types,
and suspend/resume. The [additional recipes](recipes_test.go) assert terminal
JobSet ownership, desired versus observed counts, generic capability errors, and
local mutation isolation. They run without a cluster.

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

## Discovery allocation follow-up

The discovery path keeps the same membership, error, ordering and copy contracts.
The optimization sorts only the selected workload during a direct query, reuses
collection-local owner metadata, and allocates the complete Pod snapshot together.
A partial workload result copies its selected Pods so it cannot retain that
complete snapshot. Public `Collect` still returns the sorted complete dataset.

`ownership.TraceRoot` shares the full `Trace` traversal and validation algorithm.
It returns only the terminal reference, with the same typed failure and verified
error prefix. Short successful traversals use a local path buffer.

Regression coverage includes:

- [Ordering and duplicate identities](../../pkg/discovery/collection_ordering_test.go).
- [Exact-reference caching, cancellation and served aliases](../../pkg/discovery/collection_lookup_test.go).
- [Reader mutation during acquisition and result isolation](../../pkg/discovery/collection_copy_test.go).
- [Root-only traversal and error parity](../../pkg/ownership/trace_root_test.go).
- [GroupVersion and validation precedence](../../pkg/ownership/trace_validation_test.go).
- [Controller selection and retained reference copies](../../pkg/ownership/controller_selection_test.go).

The root-only traversal benchmark can be reproduced independently:

```bash
go test ./pkg/ownership -run '^$' -bench '^BenchmarkTraceResult$' -benchmem
```

This benchmark measures traversal only. It does not measure Kubernetes requests,
full namespace acquisition, semantic grouping or production cluster capacity.
