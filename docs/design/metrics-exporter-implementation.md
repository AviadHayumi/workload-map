<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Metrics exporter implementation audit

Historical audit of the earlier prototype. The metric contract, component layout
and verification protocol below have since changed. See the current
[implementation design](./metrics-exporter/README.md) and
[component verification](../../karta-metrics-exporter/README.md#verify).

## Contract and scope

[KEP-0001](../keps/0001-metrics-exporter/README.md) governs the public metric
names, labels and Helm values. This audit compares the implementation with the
complete exporter prototype at `ef7eec03`. The target is 100,000 workloads.
Synthetic capacity measurements and live operator acceptance are separate gates.

The exporter exposes three Karta gauge families. Kubernetes events update cached
interpretation and attribution; collection performs no Kubernetes reads. A
separate bounded fresh verification pass establishes observation health. Platform
operators own recording rules and downstream telemetry joins.

## Initial findings

All fixes below are implemented. The evidence column names executable regressions;
it does not imply that the final repository or live acceptance gates have passed.
Test files are under `exporter/` unless another location is stated.

| ID | Finding and resulting behavior | Regression evidence |
| --- | --- | --- |
| EXP-001 | Removed obsolete instruments and inventory filters. Exactly the final three gauge families and labels remain; selected phases are dense 0/1 rows. | `pkg/collector`: `TestExactKEPContract`, `TestSelectedPhasesRemainDense`, `TestMultiplePhasesAndDefinitionReplacement` |
| EXP-002 | Described children no longer publish independent status. Ownership is resolved for zero-Pod candidates; only confirmed roots publish status. | `pkg/controller`: `TestZeroPodRootsAndBoundary`; `pkg/collector`: `TestRootStatusAndUnknownOwner` |
| EXP-003 | Exact ignored owner GVKs stop traversal before reading the wrapper. The boundary applies to attribution and fresh health. | `pkg/owner`: `TestRootBoundaryIsExactAndStopsBeforeLookup`; `pkg/controller`: `TestFreshOwnerMissingAndUIDMismatch`, `TestZeroPodRootsAndBoundary` |
| EXP-004 | Changed and deleted intermediate owners reattribute existing affected descendants. Reverse indexes avoid scanning unrelated workloads or Pods. | `pkg/controller`: `TestOwnerChangeAndDeletionReattributeDescendant`; `pkg/owner`: `TestDescendantsTrackChangedAndDeletedOwner` |
| EXP-005 | Health uses resource_type/workload scopes and the bounded KEP reasons. New failures invalidate prior success; only fresh recovery replaces the reason. | `pkg/store`: `TestHealthFreshnessAndFailureIsolation`, `TestHealthReasonPreservesLatestFailure`; `pkg/collector`: `TestRecoveryReplacesReasonAndDeletionRemovesSeries` |
| EXP-006 | Missing owners and replacement UIDs cannot establish a root or healthy observation. Previously attributed mappings are removed on failure. | `pkg/controller`: `TestFreshOwnerMissingAndUIDMismatch`, `TestHealthDetectsMissingAttributionAndPodReplacement`, `TestCacheMatchesRejectsReplacementUID` |
| EXP-007 | Invalid definitions affect their target kinds instead of globally invalidating unrelated workloads. | `pkg/controller`: `TestInvalidDefinitionDoesNotPoisonUnrelatedWorkload`, `TestMalformedWorkloadDoesNotPoisonPeer` |
| EXP-008 | The chart wires the four final KEP configuration values; exact GVK parsing rejects incomplete segments and wildcards. Full Pod caching is the default. | `cmd`: `TestExporterChartConfiguration`, `TestParseKinds`, `TestExporterChartDisabledAndUnsupportedDelivery` |
| EXP-009 | Removed chart-owned prototype recording rules and their obsolete values. Platform examples and regression suites use final identities and query semantics. | `cmd`: `TestKEPQueryExamplesMatchDocument`; six promtool suites with 25 scenarios |
| EXP-010 | ServiceMonitor rendering requires the exact ServiceMonitor CRD capability instead of only its API group/version. | `cmd`: `TestExporterChartMonitoringKinds` covers five capability combinations |
| EXP-029 | Duplicate full-Pod verification reads and large copied snapshot indexes exhausted the 60-second budget at one million Pods. First-pass metadata and pointer indexes reduce the cost while retaining the full second pass and strict coverage oracle. | `TestHealthMetadataPassPreservesPodOwners`, `TestScaleHealthVerification`; final capacity results below |

## Additional correctness and concurrency fixes

| Area | Change | Regression evidence |
| --- | --- | --- |
| Zero-Pod recovery | An indexed pending-workload retry repairs the owner-arrival/publication race. Obsolete definitions remove pending records; temporary watcher absence retains recoverable work. | `TestPendingZeroPodWorkloadRecoversWithoutAnotherOwnerEvent`, `TestPendingWorkloadRemovedWhenDefinitionDisappears`, `TestPendingWorkloadSurvivesTemporaryWatcherAbsence` |
| Owner publication | Owner identity and its edge publish under one lock, preventing a partially published node from temporarily becoming a root. Input owner references are copied. | `TestTrackedOwnerPublicationIsAtomic`, `TestOwnerReferenceInputIsCopied` |
| API identity | Metadata envelopes use the watched resource GVK. Fresh walks check UID, name, namespace and group/kind while allowing served-version aliases. | `TestMetadataEnvelopeUsesWatchedOwnerKind`, `TestFreshOwnerRejectsCrossKindUID` |
| Version changes and deletion | A selected-version change replaces labels. A deletion for the same UID from any served version removes that lifetime; a replacement has a different UID. | `TestSelectedAPIVersionReplacesMappingAndHonorsLifetimeDelete` |
| Publication and deletion | Fixed arrays of 256 Pod and 256 workload locks serialize same-object publication and deletion without retaining a lock per UID. Current cache UID checks reject queued stale events; descendant retries run outside locks to avoid stripe-collision deadlocks. | `TestPodDeletionSerializesWithInFlightAttribution`, `TestWorkloadDeletionSerializesWithInFlightPublication`, `TestOldPodEventCannotReplaceNewLifetimeOrPendingWork`, `TestOldWorkloadRetryCannotReplaceNewLifetime`, both stripe-collision regressions |
| Alias and relist lifetime cleanup | Temporary UID barriers prevent lagging served-version caches and duplicate Pod observers from restoring a deleted lifetime. Synced absence or UID replacement retires the barrier. Metadata alias deletion also removes stored workload status and pending retries. | `TestRelistUIDReplacementRemovesOldLifetimes`, `TestPodRootDeletionAcrossDuplicateObservers`, `TestMetadataAliasDeletionRetiresSelectedRoot` |
| Definition and watcher teardown races | Cleanup shares the publication lock and rechecks the selected definition. Final publication validates the selected definition, active watcher, and cache UID under the same controller lock as registry mutation and watcher teardown. Workload evaluation runs outside that global lock. Pending cleanup uses compare-and-delete so stale retries cannot remove newer work. | `TestDefinitionRemovalSerializesWithInFlightWorkload`, `TestObsoleteCleanupRechecksRestoredDefinition`, `TestDefinitionRemovalRejectsFirstWorkloadPublication`, `TestStoppedMetadataWatcherCannotPublishLateEvent`, pending cleanup regressions |
| Definition arbitration | Cluster definitions override catalog fallback. Ties use oldest creation time and ascending name, including catalog entries. | `TestRegistry`: oldest-selection, name-tie, override and fallback cases; `TestCatalogOwnershipChains` |
| Definition and owner events | Rebuild only changed group/kind candidates and affected descendants. Former root types can remain metadata-watched intermediate owners. Unknown-hop discovery requests are coalesced by GVK. | Ownership change/recovery tests; `TestWorkloadKindIndexLifecycle` |
| Obsolete observation cleanup | Retire unreferenced discovered watchers and their outgoing index edges. Keep explicitly required APIs and referenced former roots as metadata observers; new references restore observation. Removing an explicit Pod root preserves the dedicated Pod observer's edges. A surviving desired alias retains only matching namespace/name/UID cache entries that have no deletion barrier; pending callbacks run after unlocking. Obsolete resource-type health disappears on verification. | `TestOwnerKindReferencesFollowEdgeLifecycle`, `TestForgetKindRetiresOutgoingEdgesAndPreservesVersionAliases`, `TestRemovedDefinitionRetainsOnlyNeededOwnerWatch`, `TestRemovingPodRootKeepsDedicatedPodOwnerEdges`, `TestPrunedAliasPreservesOnlySameLifetimeInSurvivingCache` |
| Unused scale selectors | Workload state evaluates status and component instance identities only. Desired replica counts are not Pod attribution inputs; a failing unused scale selector cannot make exporter health fail. | `pkg/state`: `Build` / `does not evaluate unused scale selectors` |
| Pod interpretation | Full Pod fields support arbitrary valid selectors. Spec-only changes re-evaluate attribution. Bare Pods require an explicit selected Pod definition. | `TestFullPodCacheEvaluatesCustomResourceSelector`, `TestBarePodRequiresExplicitDefinition` |
| Failure reasons | Collection 404s mean api_unavailable; named missing-owner object GETs mean unresolved_owner. Permission and evaluation failures retain their own bounded reasons. | `TestNotFoundReasonsDistinguishObjectAndCollection`, `TestHealthPermissionFailureAndQuietRecovery` |
| Store failure races | Workload/dependency indexes bound invalidation fanout. Namespace failures use an epoch rather than scanning all records for each failed Pod. A failure during verification wins over that older pass. | `TestNamespaceFailureSurvivesConcurrentVerification`, `TestHealthReasonPreservesLatestFailure` |
| Inventory and churn | One immutable informer inventory is compared with every fresh UID/resourceVersion, including cache-only objects. Completed-list mismatches invalidate affected roots while preserving unrelated observations; reparenting invalidates both old and new roots. Resource-type coverage stays unhealthy. LIST/sync failures and unresolved owners retain their required broader failure scope. | `TestHealthCacheMismatchIsolatedToAffectedRoots` (eight cases), `TestHealthCacheLagAndUnknownKind`, `TestCacheMatchesRejectsReplacementUID` |
| Definition cache churn | Mismatched Karta objects invalidate their old and fresh target group/kinds. Deleted or newly invalid definitions preserve any known previous selected target; unrelated kinds remain usable. | `TestHealthDefinitionCacheMismatchIsolatedByKind` (four cases) |
| High cardinality | OTel preserves all expected series instead of overflowing at its default cardinality limit. Fixed-size attribute construction reduces transient allocation without retaining a million label sets. | `TestOTelPreservesMoreThanDefaultCardinality`, `TestConcurrentOTelScrapesAndUpdates`, `TestScalePrometheusExposition` |
| Snapshot ownership races | A late Pod upsert after root deletion, ownership failure or identity change cannot restore an orphan mapping. Pod output requires a matching confirmed root in the same store snapshot, even when status is absent. | `pkg/collector`: `TestPodMappingRequiresConfirmedRootInSameSnapshot` |
| Concurrent scrape memory | Only one metrics request is served at a time, bounding overlapping OTel collection allocation while health endpoints remain responsive. | `pkg/server`: `TestConcurrentScrapeIsBoundedAndHealthStaysResponsive` |
| Failure bookkeeping lifecycle | Completed verification prunes workload and collection failure epochs at or before that pass epoch, retaining failures that arrived concurrently. This bounds stale UID bookkeeping after deletion and late Pod upserts. | `pkg/store`: `TestVerificationRetiresFailureEpochsAfterDeletion` |
| Pending owner index | Removing one pending Pod no longer scans every missing-owner bucket. | `TestLargePendingIndexForgetsOnlyRequestedPod`, `BenchmarkAffectedDescendants` |

Fresh verification uses 1,000-item LIST pages, a pass budget of half the maximum
health age (60 seconds by default), and an interval cooldown after completion.
It retains metadata and evaluated records rather than every full fresh manifest.
The first Pod pass requests metadata when Pods are not selected workload roots;
the second pass reads complete Pods and evaluates selectors. Explicit Pod roots
retain full first-pass objects. `TestHealthMetadataPassPreservesPodOwners` checks
a Pod-owned Pod chain through real HTTP metadata negotiation. The full selector
and explicit Pod-root regressions also verify fresh health.
Fallback owner GETs are bounded and failed shared reads are cached for that pass.
The client uses internal QPS 50 / burst 100; no public Helm knob was added.

Unresolved candidates may publish unhealthy workload observations before root
selection completes. They publish no status or Pod attribution. Confirmed
nonroots publish neither workload status nor workload health.

## Measured scale evidence

The frozen `02a613462995f11ac3825c6b3db7a7f51c485e0c` implementation passed all
nine scrape scenarios: 1,000, 10,000 and 100,000 roots with zero, one and ten Pods
per root. Each fresh process ran an independent complete-output oracle before
three timed scrapes. The 100,000-root deletion and UID-replacement oracle also
passed. Before/after manifests matched all 123 measured source files. The worktree
was dirty only in documentation, acceptance fixtures and installer corrections;
production and scale-harness source remained unchanged.

Fresh verification passed with the unchanged 60-second pass budget:

| Roots | Pods | Healthy rows | LIST requests | Fallback GETs | Verification seconds | Deadline headroom seconds |
| --- | --- | --- | --- | --- | --- | --- |
| 100,000 | 100,000 | 100,003 | 301 | 0 | 8.749 | 51.251 |
| 100,000 | 1,000,000 | 100,003 | 2,101 | 0 | 47.229 | 12.771 |

These cases use 1,000-item pages, QPS 50 / burst 100, loopback HTTP and prepopulated
informer stores. Initial sandbox attempts could not bind the local test server;
those startup failures supplied no capacity measurement and are retained
separately. The two health cases were repeated with loopback access.

The largest scrape emitted 2,000,000 samples and about 558 MB of text. Median
scrape time was 18.423 seconds and process peak RSS was 9.325 GB, including fixture
and oracle work. That RSS is higher than the earlier 7.812 GB measurement; the
optimization does not establish uniformly lower memory use. The default 1 GiB
chart limit is insufficient for these 100,000-root scenarios. The [scale decision](metrics-exporter-scale-decision.md)
and [raw results](../metrics-exporter-lab/historical-poc/scale) record every timing, exact
allocation/RSS value, runtime, source hash and deployment limit.

Earlier failures remain evidence. The original full-object first Pod pass failed
at one million Pods after 62.188 seconds, completing only 1,092 of 2,101 LISTs.
The metadata-first experiment reached 1,800 requests before timing out. Correcting
the fixture to encode typed Pod lists completed coverage in 60.150 seconds;
indexing immutable snapshot Pod records by pointer subsequently passed in
58.568 seconds. Those older source manifests are preserved rather than relabeled
as final measurements. Partial API coverage never counted as healthy.

The measurements are synthetic, isolated from other heavy test jobs, and use
compact fixtures on a local 14-core host. They do not establish production API
server or informer-startup throughput, combined health-plus-scrape memory,
network transfer, or Prometheus ingestion capacity. Dense nine-phase status alone
requires 900,000 rows at 100,000 roots; a finite overflow cap would break the
contract. Capacity depends on total series and Pod size as well as root count.

## Peer review

The [peer-review disposition](metrics-exporter-peer-review.md) records accepted
fixes, rejected recommendations and their code/test evidence. Opus 4.8 supplied
the broad review; GPT-6 Astra performed implementation and evidence-based
triage. Two broad Sonnet 5.5 responses were empty. A narrower completed Sonnet
5.5 review of four collector/store/server files reported no concrete findings;
its snapshot predates the scrape-concurrency limit and its scope does not cover
the controller or replace execution evidence. A final completed five-file Sonnet
5.5 metrics review included the scrape-concurrency cap and snapshot root gate
and reported no concrete bugs. Failure-epoch pruning was added after that review
and has its own lifecycle regression.

## Acceptance setup corrections

- EXP-030: The NIM 3.0.2 source chart used the mutable `release-3.0` image.
  Its newer controller wrote fields absent from the pinned CRDs and repeatedly
  recreated a NIMCache helper. The installer now selects the matching official
  release by digest and rejects mismatched existing releases. Both the controller
  and CRD hook render that image. Local installer checks cover fresh, matching
  existing and mismatched existing releases. The CPU fixture now implements the
  actual 3.0.2 Hugging Face command; the stable expected population is one
  completed download Pod. Both NIMService and NIMCache passed live on both
  exporter replicas after correction.
- EXP-031: The RayService CPU fixture allowed its only Serve actor on the head,
  leaving the worker without the local Serve proxy required by its readiness
  probe. Reserving zero Ray task CPUs on the head places the actor on the worker.
  The fresh RayService retry passed with both Pods ready and both exporter
  replicas agreeing. Exporter code and metric expectations were unchanged.
- The first Knative attempt encountered a container-runtime image alias problem
  despite having the pinned image content. Repairing that alias in the dedicated
  cluster restored the Pod. Its recorded elapsed time includes the repair; it is
  not a normal startup-latency measurement.

The [acceptance evidence](../Exporter%20Acceptance.md) retains failed attempts,
fixture changes and successful retries separately from production provenance.

## Validation status

The implementation is committed as `02a613462995f11ac3825c6b3db7a7f51c485e0c`.
The following checks passed on October 14:

- Root `make check` on the clean commit, including generation and license checks,
  library and CLI tests, CLI lint and version stamping. No generated-file drift.
- `make -C exporter check`: formatting, vet, lint with zero issues, and all
  exporter packages under the race detector. This includes every lifecycle and
  watcher-pruning regression in the tables above.
- Helm lint and rendering validation, all six promtool suites (25 scenarios),
  image-lock verification and fixture tests, and shell lint.
- All 16 Python acceptance-inspector regressions.

The Go checks used `GOMAXPROCS=2 GOFLAGS=-p=2` to limit compilation and test load
while preparing the dedicated kind cluster. These are correctness checks, not
performance measurements.

Direct Prometheus and external Collector transport acceptance passed on this
commit: exact two-replica parity, rollout, deletion freshness and same-name UID
replacement. The bridge uses an explicit timestamp freshness guard; native OTLP
remains deferred.

The final kind run passed all 21 workload cases and all 21 fault/recovery
checks against the same frozen exporter image. Every workload case required two
consecutive passing observations from both exporter replicas. Fault cleanup
removed obsolete API-group rows from both replicas without a restart. The
exporter replicas had zero restarts, and the control-plane restart counts did
not increase during the final matrix. These CPU/mock fixtures validate
attribution and observation, not real GPU execution or application performance.

The final nine-case synthetic scrape matrix, deletion/UID churn and both large
fresh-health checks passed on this production commit. Earlier measured results
retain their original source provenance and limits.
