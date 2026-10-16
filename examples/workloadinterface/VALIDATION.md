<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# SDK validation

The tests check what a consumer receives: the whole workload, its exact Pods,
its desired templates, and errors when that information is incomplete.

For example, starting from one JobSet Pod must return the JobSet and all three
of its Pods. Returning only the Pod's immediate Job would fail the test.

## Ownership paths and reusable memberships

The API update was validated on 2026-10-16 with Go 1.26.3.
Its Go source is committed at `9cbc3baecf75760faefef68001d100ac368fb507`.
The checks ran before commit against the same relevant source files.

| Check | Result |
| --- | --- |
| Root module with the race detector | Passed, including the new path and membership regression tests. |
| Exporter module | Passed; metric and Helm contracts were unchanged. |
| Full recording corpus | 582 state checks passed; the existing Grove limitation and eight exclusions still apply. |
| Live suite with the race detector | All seven top-level live tests passed, including 42 membership cases; zero skips. |
| Normal and live-tagged lint | Passed. |
| Consumer examples | All 16 runnable examples passed. |
| README Go snippets | All ten blocks compile with typed function parameters and caller-owned outputs. |
| Complete inspect command | Both modes matched the independent JobSet oracle: exact three Pods, component coordinates and Pod -> Job -> JobSet path. |

The new [path tests](../../pkg/discovery/ownership_path_test.go) cover depth limits,
missing owners, cycles, scope, cancellation and UID reuse.
They require no Pod list, allow unrelated incomplete ownership, and verify that a
built-in index never mixes generations during a concurrent owner change.

The [membership tests](../../pkg/discovery/pod_memberships_test.go) cover direct,
indexed and cached observations, generic workloads and incomplete classification.
Changing returned maps, coordinate pointers or issue slices cannot change the observation.
The live suite compares these coordinates with the independent operator-label oracle.

[Scheduling groups](groups_test.go) now reuse captured memberships.
A regression with a time-dependent selector proves that grouping an older
observation cannot silently reclassify its Pods.

The first live run caught a test startup race: a newly created Pod changed between
GET and LIST, so discovery correctly returned `ErrStaleInput`.
The test now retries only that error for up to ten seconds while acquiring its
initial observation. It still rejects a partial result, and all other errors fail.
Production consistency checks and subsequent owner-change assertions remain intact.
The full live suite passed after this test-only adjustment.

## Earlier end-to-end consumer flows

The additional [live tests](../../test/discovery/README.md#exercise-real-consumer-flows)
run against Kubernetes v1.34.0, JobSet v0.10.1 and LWS v0.7.0 in the dedicated kind cluster.

- A Job is suspended, edited through the SDK, persisted and resumed.
  Its Pod must inherit the edit. Karta must expose the expected lifecycle phases.
- A suspended JobSet receives a workers-only template label.
  Actual worker Pods inherit it; the chief does not.
- JobSet component, instance, replica and scheduling-group queries return exact full Pods.
- Each LWS scheduling gang contains its real leader and worker across component boundaries.
- Direct, indexed and cached discovery preserve earlier observations and detached return values.

The Job test distinguishes three status views:

- `DesiredTree(ctx).Status.Phases`: Karta phases `Suspended`, `Running` and `Completed`.
- `DesiredTree(ctx).RootStatus.Conditions`: the controller's raw condition type,
  status, reason and message, preserved from the observed root.
- `Pod.Status.Phase`: Kubernetes `Running`, then `Succeeded`.

The API waits require Job `Suspended=True`, then `status.ready=1`, then `Complete=True`.
Karta evaluates its phases from that controller-produced status.

Full Pod expectations use an independent dynamic-client read and conversion.
The finite Job waits for an explicit completion signal, so a slow test cannot
miss its running state. API status is supplied by controllers throughout.

Independent source review found a mutation contract bug: a container patch
containing only a name could return success without changing anything.
The shared validator now returns `ErrEmptyPatch` when no operation is present.
[Regression tests](../../pkg/karta/empty_container_patch_test.go) cover typed and raw
patches, static validation, meaningful mixed updates and atomic rejection of unknown selectors.

The Go source and live tests for this run are committed at
`e65001570192709b37045d7d3667411470107c3e`.
The commands ran against those exact source files with Go 1.26.3 before commit;
only documentation changed afterward.

After preparing the dedicated cluster and corpus with the linked guides:

```bash
GOTOOLCHAIN=go1.26.3 GOWORK=off KARTA_DISCOVERY_MUTATION_TESTS=true \
  go test -race -tags=live_discovery ./test/discovery -count=1 -v
GOTOOLCHAIN=go1.26.3 GOWORK=off \
  go test -race ./test/discovery -run TestRecorded -count=1 -v
```

The recording command requires `KARTA_RECORDING_CORPUS` to point to the full
pinned corpus described in the [replay guide](../../test/discovery/RECORDINGS.md).
Without it, the test uses the smaller checked-in fixture set.

The final live run passed all seven live tests with the race detector and no skips.
This includes the existing 42 operator-membership cases and the three new flows.
Root race tests, exporter tests, normal lint and live-tagged lint passed too.
The full recording replay still passed 582 state checks after the mutation fix.
Its previously documented Grove extraction limitation and eight exclusions remain.

The E2E run also corrected an earlier blanket statement about JobSet immutability.
Suspended JobSets allow selected template scheduling edits; instance-name changes
remain outside that allowance. The test proves the label-edit case directly.

## Earlier recording replay and live validation

The earlier recording run added regression tests and consumer examples.
Its production library, exporter, catalog, metric and Helm files were unchanged.
The subsequent empty-patch fix and consumer E2E coverage are described above.

| Check | Result |
| --- | --- |
| Public recording replay | 60 supported recordings, 17 workload types, 291 frames in both cache modes: 582 state checks passed. |
| Historical and current tree compatibility | Complete trees or matching errors agree with the existing APIs. |
| Mutation isolation | Exact named-template changes, atomic failures and suspend/resume passed with the race detector. |
| Grouping updates | Exact full Pods by component, instance and replica; relabeling removes old membership without duplication. |
| Live kind suite | All 42 membership cases and the ownership/update checks passed; zero skips. |
| Consumer examples | Fourteen runnable examples passed. Scheduling-gang tests include one PyTorch gang spanning master and worker components. |
| Full root module | `go test -race ./...` passed with Go 1.26.3. |
| Exporter compatibility | `go test ./...` passed in `exporter/` with Go 1.26.3. |
| Root linter | Passed with Go 1.26.3. |

The [replay guide](../../test/discovery/RECORDINGS.md) includes reproduction commands,
fixture provenance, the eight excluded recordings and 22 historical Grove frames
whose full desired trees fail in both APIs.
Those existing failures are not successful extractions.

The grouping fixtures add explicit Pods because the historical recordings contain
root objects only. Live operator fixtures independently check ownership and Pod UIDs.

## Earlier full validation

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

Sixteen runnable examples cover these uses:

| Use case | What the example checks |
| --- | --- |
| Inspect an inventory | Read desired templates, including the workload's own template. |
| Update a template | Change a local copy without changing the input object. |
| Count observed Pods | Include completed, terminating and unscheduled Pods. |
| Select part of a workload | Read Pods for a component, named instance or replica. |
| Start from one Pod | Return its terminal workload and sibling Pods. |
| Read one ownership path | Return Pod, intermediate Job and terminal JobSet without listing siblings. |
| Reuse Pod classification | Read captured component, instance and replica coordinates by UID. |
| Inspect a workload with no Pods | Return an empty Pod set and retain its desired template. |
| Inspect an unfamiliar workload type | Return generic Pod membership without inventing a Karta definition. |
| Suspend and resume | Change the local workload object when its definition supports it. |
| Reuse attribution | Publish a Pod update and retain an unchanged earlier observation. |
| Traverse a workload | Start from one Pod, inspect root phases and raw conditions, then list every instance's full Pods. |
| Select an inference group | Return exact leader and worker Pods for known group IDs. |
| Edit one named instance | Change only its scheduler and preserve the captured observation. |
| Build scheduling groups | Combine full Pods by PodGroup name and explicit grouping keys. |
| Enrich an existing inventory | Interpret one known root while retaining the caller's existing Pod members. |

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
The [traversal examples](traversal_test.go) show status, scopes and named edits.
The [scheduling-group tests](groups_test.go) check complete Pod bodies, cross-component
gangs, key boundaries, returned-copy isolation and unsupported instruction formats.

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

JobSet instance-name changes are not permitted through its Kubernetes API.
Same-UID instance declaration changes and actual internal cache reuse are checked
by unit tests. The consumer E2E test separately persists a supported template-label edit.

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
| A selector-only container patch silently succeeded | Return `ErrEmptyPatch`; preserve validation and atomicity for meaningful mixed patches. | [Empty container patches](../../pkg/karta/empty_container_patch_test.go) |
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
