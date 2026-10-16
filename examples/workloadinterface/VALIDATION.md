<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# SDK validation

The tests check what a consumer receives: the whole workload, its exact Pods,
its desired templates, and errors when that information is incomplete.

For example, starting from one JobSet Pod must return the JobSet and all three
of its Pods. Returning only the Pod's immediate Job would fail the test.

## Recorded validation

The completed run tested Go source at
`630fbf78672161cfc3ac624aaef4e046518cd52a`.
The Linux query comparison uses that same source revision.

| Check | Recorded result |
| --- | --- |
| Root module: `go test -race ./...` | Passed |
| Exporter module: `go test ./...` | Passed |
| Live kind suite: `go test -tags=live_discovery -count=1 -v ./test/discovery` | Passed, no skipped tests |
| Linter | Passed |
| Clean-tree `make check` | Passed |

Native tests used Go 1.27.2.
The linter and presubmit used Go 1.26.3.

The sections below explain that coverage and how to reproduce it.
They are not a claim that every later revision or production environment was tested.

## Try the examples

Run from the repository root. No cluster is required:

```bash
go test ./examples/workloadinterface -run '^Example' -v
```

Nine runnable examples cover these uses:

| Use case | What the example checks |
| --- | --- |
| Inspect an inventory | Read desired templates, including the workload's own template. |
| Update a template | Change a local copy without changing the input object. |
| Count observed Pods | Include completed, terminating and unscheduled Pods. |
| Select part of a workload | Read Pods for a component, named instance or replica. |
| Start from one Pod | Return its terminal workload and sibling Pods. |
| Inspect a workload with no Pods | Return an empty Pod set and retain its desired template. |
| Inspect an unfamiliar workload type | Return generic Pod membership without inventing a Karta definition. |
| Suspend and resume | Change the local workload object when its definition supports it. |
| Reuse attribution | Publish a Pod update and retain an unchanged earlier observation. |

To run just the Pod-to-workload example:

```bash
go test ./examples/workloadinterface -run '^ExamplePodSource_fromPod$' -v
```

Its expected output is:

```text
root=training-set same UID=true
known=true pods=3
```

The [additional recipes](recipes_test.go) check terminal JobSet ownership,
desired versus observed counts and generic capability errors.

The [consumer tests](consumer_test.go) also check local mutation isolation.

An empty workload is a successful result with zero Pods.

A failed read, incomplete ownership or invalidated index is not a zero-Pod
workload. The caller must handle the returned error.

## Run the local checks

From the repository root:

```bash
go test -race ./pkg/karta/... ./pkg/tree ./pkg/jq/execution ./pkg/resource \
  ./pkg/ownership ./pkg/attribution ./pkg/discovery/... ./examples/workloadinterface
golangci-lint run -c .golangci.yml
```

From `exporter/`:

```bash
go test ./...
```

Expected result: the selected tests pass and the linter reports no findings.

The repository presubmit is `make check`.

Its generated-file validation needs a clean checkout. Commit the intended
changes before running it.

## Check against Kubernetes

Follow the [kind guide](../../test/discovery/README.md) to provision the fixtures
and their independent UID oracle. It includes the operator versions and commands.

The oracle reads Kubernetes objects and operator labels without importing the
Karta resolver. The live test compares the SDK's result with those expected UIDs.

| Fixture | Observed Pods | Desired template locations |
| --- | --- | --- |
| JobSet | 3 | 2 |
| LeaderWorkerSet | 4 | 2 |
| Deployment | 2 | 1 |
| Generic Bundle over two Deployments | 3 | No Karta template inferred |
| Standalone Pod | 1 | 1 |
| Deployment with zero replicas | 0 | 1 |
| Completed Job | 1 | 1 |

`TestLiveOperatorMembership` runs all seven fixtures through six modes:

- Default direct reads.
- Direct reads with owner metadata.
- Direct reads with Pod REST decoding.
- Pod REST decoding with owner metadata.
- An index without attribution reuse.
- An index with attribution reuse.

That is 42 fixture checks.

Each mode runs the same consumer functions shown in the README. It checks the
terminal workload, exact sibling Pod UIDs and component/instance/replica scopes.
The cache-enabled mode also repeats complete-object reads.

Separate live tests exercise:

- Owner reparenting, deletion and reuse of a name with a new UID.
- RBAC denial and index invalidation.
- Unscheduled and terminating Pods.
- A cluster-scoped owner with Pods in two namespaces.
- Repeated JobSet instance scopes and a metadata update after an API conflict.

These checks establish SDK behavior for the fixtures.

They do not rerun GPU, MIG, DRA or DCGM validation. They do not prove a production
consumer migration or capacity at 100,000 workloads.

## Check attribution reuse across updates

The [runnable cache example](recipes_test.go) demonstrates the observable result:

```bash
go test ./examples/workloadinterface -run '^ExamplePodSource_attributionCache$' -v
```

Expected output:

```text
new observation=true pods=1
previous=Running current=Succeeded
```

The example publishes the completed Pod through the caller-owned index.

A later query sees the new phase. The earlier observation keeps the old phase.
Repeated queries return separate observation handles and detached object copies.

The [cache tests](../../pkg/discovery/attribution_cache_test.go) also check:

- Workload-entry and Pod-membership limits, eviction and concurrent fills.
- Cancellation, reparenting and index invalidation.
- Changed identities and isolation of returned objects.
- Release of retired object bodies.

[Structural-path tests](../../pkg/discovery/attribution_root_paths_test.go) check
named instances, empty declarations and preparation failures.

They explicitly check which catalog expressions can reuse attribution.
RayJob, Grove PodCliqueSet and both DynamoGraphDeployment definitions bypass
this optimization; discovery remains available through the uncached path.

The [live JobSet test](../../test/discovery/root_instances_test.go) checks repeated
named scopes, a metadata update with a forced API conflict, and replacement by a
new UID.

JobSet declarations are immutable through its Kubernetes API. Same-UID declaration
changes and actual internal cache reuse are therefore checked by unit tests.

[Alias lifecycle tests](../../pkg/discovery/index_alias_lifecycle_test.go) check
objects served through one or several API versions:

- Adding a version and replacing the object with fewer versions.
- Returning the exact body requested and preserving earlier observations.
- Keeping duplicate-identity error ordering.
- Reporting a Pod depth error with the exact core API identity, even when that
  version was supplied after another alias.

These tests verify correctness. They do not establish latency or memory parity
with another implementation.

## Fixes protected by regression tests

| Previous behavior | Correction | Coverage |
| --- | --- | --- |
| The desired tree omitted the workload's own template and raw status | Include `Root` and `RootStatus`; retain the existing child view. | [Tree inspection](../../pkg/tree/inspection_test.go) |
| Typed nil inputs could panic | Reject nil workload objects; return `ErrEmptyPatch` for nil updates. | [Constructors](../../pkg/karta/regression_test.go), [patches](../../pkg/karta/patch_contract_test.go) |
| A read-only metadata path blocked a spec-only update | Check and route only the requested part. | [Interface regression](../../pkg/karta/regression_test.go) |
| Editing one instance could add null fields to another | Write only the selected concrete locations. | [Patch contract](../../pkg/karta/patch_contract_test.go) |
| Replacing affinity retained old constraints | Replace the selected affinity object, including with an empty object. | [Patch contract](../../pkg/karta/patch_contract_test.go) |
| Dotted or slashed map keys were treated as jq paths | Encode patch field names as literal keys. | [Patch contract](../../pkg/karta/patch_contract_test.go) |
| JSON conversion could round large integers | Reject integer tokens outside the safe range at the interface boundary. | [Interface regression](../../pkg/karta/regression_test.go) |
| Float metadata broke integer getters such as `GetGeneration` | Restore Kubernetes unstructured number types at `Object()`. | [Patch contract](../../pkg/karta/patch_contract_test.go) |
| The recording fake retained caller-owned slices | Copy recorded inputs and returned capabilities. | [Interface regression](../../pkg/karta/regression_test.go) |

Mutations write to a detached scratch object.

The SDK publishes that object only after every write succeeds. Tests include a
failure after an earlier write, malformed patches, unsupported capabilities and
input/output isolation.

The fake records mutation intent. It does not simulate a cluster or return a
populated inspection tree.

## Run and interpret the benchmarks

Each benchmark answers a specific question. Neither command below measures a
production cluster or proves performance parity with another library.

### What does returning only the terminal owner cost?

```bash
go test ./pkg/ownership -run '^$' -bench '^BenchmarkTraceResult$' -benchmem
```

Compare the `full-path` and `root-reference` rows:

- `ns/op`: time for one owner traversal.
- `B/op`: bytes allocated during one traversal.
- `allocs/op`: allocation count during one traversal.

Both use the same traversal and validation algorithm.

This benchmark excludes Kubernetes requests, full namespace acquisition and
component grouping.

### What does transferring decoded Pods save?

```bash
go test ./pkg/discovery/clientgo -run '^$' -bench '^BenchmarkReaderWorkloadPodTransfer$' -benchmem
```

Compare `transfer` and `fallback` for the same Pod count:

- Both decode HTTP JSON through an in-process transport.
- Both acquire the selected workload through the client-go adapter.
- `fallback` hides the adapter's transfer capability behind the ordinary reader
  interface.
- The output reports time, allocations, API GET/LIST counts and listed Pods.

The timed region excludes the final public getter copies and payload assertions.
There is no API-server network latency in this benchmark.

For an end-to-end comparison, match the starting reference, output objects and
freshness contract. Report acquisition, cache population and repeated-query costs
separately. A warm indexed query and a direct Kubernetes query do different work.

<details>
<summary>What the acquisition optimizations preserve</summary>

Direct discovery keeps membership, error, ordering and copy behavior:

- Sort only the selected workload during a direct query.
- Reuse owner metadata within that collection.
- Allocate the complete Pod snapshot together.
- Copy selected Pods into a partial result so it cannot retain the full snapshot.
- Keep public `Collect` results complete and sorted.

`ownership.TraceRoot` uses the same traversal and validation as `Trace`.
It returns the terminal reference with the same typed failure and verified error
prefix. Short successful traversals use a local path buffer.

Coverage:

- [Ordering and duplicate identities](../../pkg/discovery/collection_ordering_test.go).
- [Exact-reference caching, cancellation and served aliases](../../pkg/discovery/collection_lookup_test.go).
- [Reader mutation during acquisition and result isolation](../../pkg/discovery/collection_copy_test.go).
- [Root-only traversal and error parity](../../pkg/ownership/trace_root_test.go).
- [GroupVersion and validation precedence](../../pkg/ownership/trace_validation_test.go).
- [Controller selection and retained reference copies](../../pkg/ownership/controller_selection_test.go).

The client-go adapter can transfer freshly decoded Pods to discovery.
The SDK keeps a compact value array and adopts its nested data.
Ordinary readers keep the defensive-copy path.

Later direct owner reads copy ownership metadata. The selected workload and its
Pods retain their complete payloads. Public `Collect` retains complete owner
objects too.

Coverage:

- [Transfer validation and backing-array retention](../../pkg/discovery/transfer_test.go).
- [Complete workloads and raw ownership metadata](../../pkg/discovery/direct_projection_test.go).
- [Paginated decoding and nested isolation](../../pkg/discovery/clientgo/reader_transfer_test.go).

</details>

<details>
<summary>What the optional read modes change</summary>

`NewReaderWithOwnerMetadata` requests metadata for later direct owner reads.

The selected workload, Pods and public `Collect` results stay complete.
See the [acquisition option](../../docs/Live%20Workload%20Discovery.md#optional-metadata-reads-for-owners).

Owner GVK comes from the exact request and mapper. Every typed owner reference
must have complete identity fields. Default full reads retain their existing
checks.

Coverage:

- [Dispatch, complete bodies, aliases and copy isolation](../../pkg/discovery/owner_metadata_test.go).
- [Malformed ownership and failures without fallback](../../pkg/discovery/owner_metadata_validation_test.go).
- [JSON, protobuf, full-JSON responses, HTTP errors and cancellation](../../pkg/discovery/clientgo/owner_metadata_transport_test.go).

`WithPodRESTClient` wraps a reader with an injected core REST client for Pod lists.

The live test builds that client from its existing config using the core scheme
and JSON serialization. It imports no generated typed core client.

The wrapper keeps owner metadata support only when the base reader provides it.
Both wrapped modes retain Pod transfer support.

The live suite uses the same seven fixtures and independent UID oracle for all
six modes. Counters require actual owner metadata reads through both the direct
reader and the Pod REST wrapper.

Live fixtures supplement the transport tests. Valid cluster objects alone cannot
prove malformed-response, pagination, cancellation or copy-isolation behavior.

</details>

<details>
<summary>Integration scope</summary>

The integration starts from discovery SDK commit `ce91b4a0` and ports the offline
interface from `feat/karta-workload-interface` at `b063a855`.

It preserves the module path, dependency versions and catalog.
It also preserves the compiled-selector cache, replica-selector opt-out behavior,
exporter metric contract and Helm values.

</details>
