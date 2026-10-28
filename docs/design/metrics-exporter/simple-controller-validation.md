<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Simple controller implementation and validation

Baseline: `ee7d2ac6fdb0c558b3e907e8c6e331a2afd9540e`.
Branch: `poc-export-controller-is-simple`.

## What changed

For `Pod -> ReplicaSet -> Deployment`, the Pod queue discovers the Deployment and
publishes one Pod mapping. The workload queue evaluates that Deployment and
publishes one workload result. Both use shared watched objects. A scrape only
reads saved results.

| Controller file | Main responsibility |
| --- | --- |
| `runtime.go` | Start workers, select definitions and track final root membership |
| `sources.go` | Share collection watches, record health and route events |
| `reader.go` | Read accepted objects with UID and source checks |
| `leaf.go` | Discover and publish Pods; run coalesced scope scans |
| `root.go` | Admit candidates, evaluate canonical roots and publish workloads |

The controller has five production files and 1,812 production lines, compared
with six files and 3,818 lines at the baseline. This is about 53 percent fewer
production lines; tests and Markdown are excluded from that count. `owners.go` was removed. Root-owned Pod
pages, freshness proofs, expiry, renewal scheduling and reverse owner-path state
were removed. Store now holds independent immutable Pod and workload results.
The collector reads those results and their source health.

Discovery traversal, inventory election, SDK evaluators, catalog definitions and
the public Kubernetes API types remain in their existing components. Compiled
expressions and definition snapshots can be shared. Each reconciliation creates
its own mutable SDK factory and label closure.

The [source comparison](../simple-exporter-controller-decision.md) explains the
Kubernetes, controller-runtime and kube-state-metrics patterns used here. The
[collection contract](consistency.md) describes the resulting behavior.

## Compatibility changes

- Pod and workload output can represent different Kubernetes revisions.
- Healthy quiet objects do not expire. The exporter records observed source and
  evaluation failures, without a per-object freshness deadline.
- Supplying `refreshInterval` or `maxAge` returns a migration error, including
  explicitly empty values. Their previous behavior is not silently ignored.
- Traversal ancestors require collection LIST/WATCH access. GET-only access is
  insufficient. Metadata representation fallback is limited to HTTP 406 and 415.
- An error before discovering a root uses observation-valid `scope="discovery"`
  with input kind and namespace. It does not invent a workload identity.

Existing successful values retain their identity and provenance on failure and
become invalid. A status error alone does not hide verified root creation time.
Generic Pod mappings preserve the API version verified in their own owner chain.

## Small performance changes

Profiles showed repeated definition-only structure indexing, empty-selector label
map extraction, duplicate root-path allocation and repeated mapping lookup. The
controller now shares the immutable structure summary with its accepted definition,
skips empty-selector extraction, and records the actual root source once. A healthy
full-cache read avoids watch setup only when no earlier mapping failure needs
cleanup. Factories and mutable label closures remain private to a reconciliation.

These changes added four net production lines. Regression tests cover definition
structure replacement, source promotion, selected-version dependencies and recovery
from a prior mapper failure. Matched reruns measure their effect separately from
the earlier candidate; profiler overhead is excluded from timing comparisons.

## Regression coverage

| Behavior | Replacement coverage |
| --- | --- |
| Independent Pod and workload writers | `TestIndependentPodAndWorkloadPublication` |
| UID replacement and stale tombstones | `TestTombstoneAndSameNameReplacement` |
| Selected children admitting zero-Pod generic roots | `TestZeroPodCandidateFindsGenericParentAndMoves` |
| Last-good output and definition provenance | `TestInvalidReplacementRetainsOldProvenanceUntilSuccess` |
| Initial Karta failure with independent creation output | `TestInitialDefinitionFailureStillPublishesConfirmedRootCreation` and real API integration |
| Version-specific generic Pod attribution | `TestGenericPodVersionsAndSelectedVersionScope` retains the original three-case table |
| Retired elected version returning to a generic alias | `TestGenericCanonicalVersionRecoversAfterElectionRemoval` |
| Selected-version replacement synchronization | `TestSelectedVersionHandoverWaitsForReplacementAndUsesItsBoundary` |
| Root updates during Pod evaluation | `TestRootChangeDuringPodEvaluationRequeuesRegisteredMembership` |
| Deletion during Pod evaluation | `TestPodDeletionDuringEvaluationCannotPublish` |
| Source promotion and retired callbacks | `TestSourcePromotionWaitsForFullSnapshotAndRetiresMetadata` |
| Complete, partial and malformed lists | `TestCompleteRelistDeletionAndMalformedList`, `TestFailedListRetainsCacheAndLastGoodRows` |
| Watch failure and reconnect | `TestWatchFailureAndEOFRemainVisibleUntilReconnect` and real API integration |
| Namespace withdrawal during failed source mapping | `TestLateMappingFailureCannotReviveRemovedNamespace` |
| Ordinary Pod churn versus leader-Pod ownership | `TestOrdinaryPodChurnDoesNotSweepAndAncestorPodAdoptionDoes` |
| Pending owner becoming known before first mapping success | `TestResolvedOwnerReplacesUnknownIdentityBeforeFirstMappingSucceeds` |
| Full Pod expressions and root-only label changes | Controller unit tests and real API integration using Pod spec/status and workload spec |

The former tests for proof expiry, renewal deadlines and whole-publication pages
were retired with those mechanisms. Their lifecycle guarantees were mapped to
the tests above. This is a contract change, not a claim of complete behavioral
equivalence with the previous publication model.

## Verification record

The exporter presubmit includes formatting, vet, pinned lint, module checks, unit
tests, chart tests, real API integration, e2e compilation and a version smoke test.
The full repository `make check` passed in a clean checkout of implementation
and evidence commit `28ad1db7`. Regeneration left that checkout clean. The command
reused the same pinned tools through `LOCALBIN`. Prometheus query-rule tests also
passed. Subsequent changes only record validation and clarify documentation.
The completed exporter presubmit passed. Scoped race detection covers the
controller, store, collector, app and configuration. The real API suite contains
21 controller cases. Deployed kind and matched performance results are recorded
separately below.

Independent code review approved the implementation with no unresolved findings.
Independent architecture review found no blocker and retained one performance
watch: global cache scans during scope sweeps. Review fixes have regression tests
for initial Karta denial, first-error identity, rejected snapshots, late mapping
failures and API-version handover/removal.

## Deployed kind result

The final image passed 27 executed cases on the existing `exporter-e2e` kind
cluster. The default profile includes 23 workload cases and the regression where
an unchanged Pod moves after a replacement Deployment adopts its ReplicaSet.

| Run | Result |
| --- | --- |
| Default profile with bare-Pod catalog opt-in | 24 passed, 0 failed |
| Alternate Dynamo v1beta1 and LLMInferenceService v1alpha1 | 2 passed, 0 failed |
| Default catalog bare-Pod exclusion | 1 passed, 0 failed; 23 intentionally unselected cases skipped |

The [case record](../../../karta-metrics-exporter/test/e2e/results/simple-controller.json)
contains per-case durations, report hashes and the exact image manifest digest.
Its production-source hashes were checked against commit `28ad1db7`. All runs
used `make test-exporter-e2e`; the final run selected only the Pod case and omitted
the catalog opt-in. The cluster and existing operators were reused. The suite
cleaned its exporter release and test namespaces.

These cases check the recorded CPU fixtures and supported API profiles. They do
not establish every operator feature, production topology or performance limit.

## Matched performance result

The final [optimized comparison](../../../karta-metrics-exporter/test/scale/results/simple-controller/optimized/README.md)
passed all 14 runs. It includes paired root and Pod-label updates, Pod recreation,
ancestor UID mismatch/recovery, and WATCH EOF/410/paginated relist recovery.

For ten roots with 100 Pods each, whole-process allocation was about 34 percent
above baseline. CPU was 21 to 38 percent higher in the two short repeats. RSS and
ACK latency stayed close. This improves the initial 83 percent allocation increase
but does not eliminate the regression. One thousand roots with one Pod each stayed
near baseline allocation. Pod-label updates had lower ACK latency in this fixture.

Profiles separated test-observer work from controller work. The intermediate
candidate's extra diagnostic grouping accounted for only part of its allocation
increase. These intermediate samples cannot be subtracted exactly from the final
runs. The report preserves the cold-start outlier, exact commands and binary hashes.

The remaining cost is a review limitation. This branch does not claim performance
parity or large-cluster capacity. More per-root caches or batching would add state
to the controller; they were not added without a demonstrated operational need.

## Limits

Scope sweeps still scan the object cache while holding controller and reader
locks. Coalescing limits queued sweeps, not the duration of one scan. Ordinary Pod
churn does not trigger these scans unless the Pod has served as an ancestor.

The [performance report](../../../karta-metrics-exporter/test/scale/results/simple-controller/README.md)
records exact exercised shapes, binary identities and measurement limitations.
Small synthetic runs do not establish production capacity or absence of bugs.
The kind tests exercise real operators and API behavior, separately from that
synthetic performance fixture.
