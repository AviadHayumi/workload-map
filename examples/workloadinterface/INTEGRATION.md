<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Replace consumer logic without changing its behavior

The SDK provides reusable ownership traversal, workload interpretation and Pod
classification. The application still owns its event loop and published records.
The [first three examples](README.md#replace-logic-in-an-existing-consumer) show
those replacement points using APIs available in this checkout.

These examples describe integration paths. They do not establish a completed
migration of another controller or parity with every version of the exporter.

## An ownership inventory service

| Before | Replace with | Keep in the consumer |
| --- | --- | --- |
| Walk each Pod's controller owners | `ownership.Trace` and an injected lookup | API/cache access and metadata retention |
| Build one root-to-Pod path | Reverse the returned Pod-to-root path | Convert nodes to the consumer's representation |
| Merge paths into a physical resource tree | No equivalent high-level result | Tree merging and stored labels/annotations |
| Return only workload identity and all members | `WorkloadForPod`, `Root`, `Object`, `Pods` | Consumer filters and presentation |
| Reconcile the stored inventory | No SDK controller | Watches, retries, writes, deletion and readiness |

`ownership.Object` contains identity, owner references and scope.
It does not retain the labels and annotations a physical-tree UI may display.
Keep those separately in the lookup adapter if the output needs them.
The optional discovery owner-metadata reader also does not promise those fields.

`Trace` returns a verified prefix plus an error when a walk fails.
A consumer that currently accepts a partial path must make that policy explicit;
using the prefix does not prove a terminal workload was found.
UID mismatch, cycles and multiple controller owners must not become successful
root-and-member discovery by accident.

The high-level discoverer requires complete Pod coverage and owner closure.
Filtering Pods before owner traversal and skipping failed walks is a different
contract. Apply display or eligibility filters after successful discovery, or
retain the consumer's selected inventory and use local semantic enrichment.

## A workload integration controller

| Before | Replace with | Keep in the consumer |
| --- | --- | --- |
| Select framework-specific replica/template fields | A definition plus `karta.New(...).Tree(ctx)` | Accounting and product field conversion |
| Hand-walk a fixed number of Karta component levels | Recursive traversal, as in `SummarizeDesired` | Output representation and stable IDs |
| Classify already confirmed Pod members | `attribution.Compile`, `Prepare`, `Evaluate` | Eligibility and handling incomplete coordinates |
| Recover membership through labels written by the controller | `WorkloadForPod(...).Pods()` after membership, boundary and coverage agreement | Label compatibility for downstream consumers |
| Convert native conditions into application status | Karta phases and raw root/Pod status as inputs | Priority rules and application-specific status |

An integration may already use Karta definitions. The SDK can replace its repeated
factory traversal and grouping without introducing a new discovery controller.
Native handlers and custom definitions need an explicit precedence rule.

Keep these distinctions when moving code:

- Replacing label-derived membership requires controller owner references to
  identify the same Pod set. If labels express a different relationship, retain
  that membership source and use local attribution on the caller-confirmed Pods.
- Strict attribution requires an unambiguous Pod-producing component. A consumer's
  owner-kind fallback or first-match grouping is not automatically equivalent.
- Semantic coordinates are component, instance and replica values. They do not
  replace stable database IDs or identity that survives recreation of a child.
- A definition can produce several lifecycle phase matches. The consumer still
  chooses any single product status and accounts for external state.
- `Tree` also evaluates templates and scale. For status-only consumers, retain a
  separate root-status read so unrelated extraction failures do not suppress status.
- A scheduler-filtered Pod cache is not a complete discovery reader or dataset.
  Supply complete coverage, or keep the existing membership path and classify
  its verified Pods with the local attribution API.

## The metrics exporter

The exporter adapters in this branch already share the owner index, legacy Pod
attribution and instance traversal. They still use the exporter's original
controller and metric store.

```text
object event -> resolve root -> classify Pod -> update metric record
scrape       -> read metric-store snapshot -> emit existing metrics
```

Moving the controller to `NewDiscoverer` would be additional work.
Before doing that, preserve its definition-selection rules, root boundaries,
unknown fields, health state, zero-Pod reporting and `owner_resolution` label.
`Workload` does not currently expose a per-Pod membership getter; calling
`attribution.Evaluate` separately is possible but repeats work already performed
inside discovery.

The [PoC comparison](COLLECTOR.md) explains the implemented compatibility adapters
and newer PoC fixes still absent from this SDK branch. In particular, the current
shared legacy owner adapter does not yet include the newer PoC cluster-scope fix.
A passing SDK query is not evidence that the latest exporter can be replaced unchanged.

## Keep a workload below an orchestration parent

A controller can own several independently managed workloads:

```text
WorkloadEnvelope
  +-- JobSet train-a
  |     +-- Job -> Pod a0
  |     +-- Job -> Pod a1
  +-- JobSet train-b
        +-- Job -> Pod b0
```

All arrows here are Kubernetes controller-owner edges.
`WorkloadEnvelope` is a fictional example kind.

| Policy | Starting from Pod a0 | Returned Pods |
| --- | --- | --- |
| Current `NewDiscoverer` | Terminal WorkloadEnvelope | a0, a1, b0 |
| Proposed stop before WorkloadEnvelope | JobSet train-a | a0, a1 |
| Same proposed policy, starting from b0 | JobSet train-b | b0 |

Without a definition for the envelope, the current result is generic.
Adding a JobSet definition alone does not change the selected root.
`Workload(ctx, trainARef)` currently returns `ErrNotWorkloadRoot` because train-a
still has a controller owner. `MaxDepth` is an error limit, not a root selector.

### What already exists

The exporter uses `ownership.Index.Resolve` with an exact-GVK ignored-owner predicate.
It stops before reading that owner and keeps the highest accepted workload
candidate found below it. A successful boundary result reports `ignored_owner`.
If no candidate exists, this path does not invent a generic workload.
This is a lower-level compatibility API, separate from `discovery.Index`.

The candidate rule matters:

```text
Pod -> defined Job -> undefined Bridge -> ignored Envelope

Exporter candidate selection: Job
Proposed generic cutoff:      Bridge
```

Ignoring an owner is not the same as skipping through it to a grandparent.
It stops traversal at that edge. Nor does it globally exclude that kind from
all independent observations.

### Proposed SDK behavior

This is a design discussion. No ignored-owner constructor option is implemented.
The proposed first policy is a copied, immutable set of exact parent GVKs:

- Preserve terminal-owner discovery by default.
- Stop before mapping or reading a configured controller owner. Keep the last
  verified child as the workload, even if that child has no Karta definition.
- Validate the reference's syntax before stopping. Its referenced UID cannot be
  checked against an object intentionally not read.
- Preserve original object bodies and owner references. Do not hide the parent by
  editing objects inside a reader wrapper.
- Keep sibling workloads separate. Use the same boundary for seed resolution,
  explicit-root queries and every Pod's membership check.
- An explicit query for the parent still reads that parent. Cut child edges must
  not cause the parent's observation to reabsorb those separate workloads.
- Expose why traversal stopped if a consumer needs boundary provenance.
  Preserving the exporter's `ignored_owner` metric label requires this.

Exporter-style selection of the highest accepted candidate is a separate policy.
It needs an explicit rule for accepted kinds and the no-candidate case; it must
not silently change generic discovery when a definition is added.

This cannot be implemented only in `NewDiscoverer`:

- `Collect` currently follows the full owner chain.
- `Index.Replace` requires full owner closure and partitions Pods by terminal root.
- Capture, explicit-root validation and attribution reuse depend on that partition.

Acquisition and indexing must share the policy. Otherwise an unreadable ignored
parent can fail initial collection, or sibling Pods can be merged before the query.
Changing the policy must rebuild the affected inventory and invalidate derived state.
Client reuse or `CacheOptions` alone does not solve this.

### Acceptance cases before claiming support

Run each case through direct discovery, indexed capture and attribution reuse:

- Two sibling workloads under one parent stay separate; all Pod phases remain visible.
- A missing or unreadable ignored parent causes no lookup of that parent.
- Exact API versions match correctly; malformed or multiple controller references fail.
- Unknown children remain generic; zero-Pod children can be queried explicitly.
- Missing owners, UID replacement, invalid scope, cycles and depth errors below
  the boundary still fail instead of silently selecting a different workload.
- Reparenting and policy changes update membership while prior observations stay unchanged.
- Exporter candidate selection, field degradation and boundary provenance retain
  their deliberate behavior, including cluster-scoped owner cases.

These are required future tests, not claims about the current live-test suite.
