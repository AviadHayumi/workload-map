<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Metrics exporter implementation audit

## Contract and scope

[KEP-0001](../../keps/0001-metrics-exporter/README.md) governs the public metric
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
| Failure bookkeeping lifecycle | Completed verification prunes workload and collection failure epochs at or before that pass epoch, retaining failures that arrived concurrently. This bounds stale UID bookkeeping after deletion and late Pod upserts. | `pkg/store`: `TestVerificationRetiresFailureEpochsAfterDeletion`; implementation owner reported the focused state/store/collector/attribute race run passed |
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

The original full-object first Pod pass failed the strict 100,000-workload,
one-million-Pod health case: verification took 62.187569042 seconds and completed
only 1,092 of 2,101 required LISTs before its 60-second budget expired. Health
reported api_unavailable rather than accepting partial coverage. The metadata
first-pass optimization addresses that duplicate full-object decode cost; its
initial repeat reached 1,800 of 2,101 requests before timing out. Correcting the
fixture to serialize typed PodList responses directly completed all reads and
100,003 healthy rows in 60.150028042 seconds, with almost no deadline margin.
The verifier now indexes immutable snapshot Pod records by pointer instead of
copying each large record again. The next isolated run passed in 58.568248875
seconds (67.09 seconds including setup/assertions), with all 100,003 health rows
healthy, all 2,101 LISTs complete and no fallback GETs. This leaves only about
1.43 seconds of API-deadline headroom in the synthetic fixture. The original
failure remains part of the evidence. These capacity runs preceded the final
localized cache-churn fix; their source hashes are preserved with the results.

`TestScaleHealthVerification/workloads_100000/pods_1` passed in an isolated run:
100,000 Jobs and 100,000 Pods yielded 100,003 healthy rows. Verification took
13.225716542 seconds (20.43 seconds including fixture setup and assertions),
using 100 Job LISTs, 200 Pod LISTs, one Karta LIST and zero fallback GETs.
The first Pod LIST pass uses metadata and the second uses full objects.
The fixture uses real HTTP pagination and the production request budget, but a
loopback server and prepopulated informer stores. This is not a production API
server or informer-startup throughput claim.

The 1,000-workload health matrix also passed for zero, one and ten Pods per
workload, with respectively 4, 4 and 22 LISTs and all 1,003 health rows healthy.
The affected-descendant lookup benchmark took 118.9 ns/op among 1,000 owners and
127.2 ns/op among 100,000 owners, with one allocation and 96 bytes per lookup.
That benchmark measures lookup isolation, not complete event processing.

See the [scale decision](metrics-exporter-scale-decision.md) and
[raw scale results](../../exporter/test/scale/results/) for final scrape,
allocation, RSS and churn evidence. Dense nine-phase status alone requires
900,000 rows at 100,000 roots. A finite overflow cap would break completeness.
The capacity decision must account for total series and Pod ratio, not just roots.

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

## Validation status

Completed and reported by the responsible implementation workers:

- Normal, non-overlay `go test -race ./pkg/controller ./pkg/owner ./pkg/registry`
  after the final controller peer fixes.
- Controller/owner/registry golangci-lint with the repository configuration:
  zero issues. Scoped `git diff --check` passed.
- Focused `go test -race ./pkg/collector ./pkg/store ./pkg/server` after the
  latest store and scrape-concurrency changes, including the bounded-scrape
  health-responsiveness regression.
- Actual-tree `go test ./cmd -count=1`, Helm lint, all six promtool suites
  (25 scenarios), and 14 Python acceptance-inspector regressions.
- The health scale cases and owner lookup benchmark described above.

The focused controller race checks also passed after the final cache-churn
localization change. A repeat of large capacity measurements on that exact
revision is deferred while the live cluster stabilizes.

The final whole-repository `make check`, live operator matrix, fault/recovery
suite and delivery-mode acceptance results are not established by this audit
revision. The integration owner will update their final status. Source exists
for those harnesses, but source presence is not evidence of a completed run.

The October 14 lifecycle review reproduced a metadata-alias deletion that retained
a confirmed workload record. Deletion now removes that record and its pending
retry under the same publication lock. The normal-source focused race command
`GOMAXPROCS=2 GOFLAGS=-p=2 go test -race ./pkg/controller ./pkg/owner ./pkg/registry -count=1`
passed after the fix: controller 2.141s, owner 2.256s, registry 1.517s. This result
includes the deletion and relist regression; it does not establish live acceptance
or repeat the earlier large capacity measurements.

Follow-up regressions reproduced two teardown races: an event from a stopped
metadata watcher could restore its index entry, and the first publication of a
root could arrive after definition cleanup found no stored record. Both now
recheck publication under the controller lock shared with watcher teardown and
production registry mutation. The final focused race repeat passed on normal
source: controller 3.080s, owner 3.013s, registry 1.524s. Scoped golangci-lint
reported zero issues, and scoped `git diff --check` passed.

Final watcher-pruning review reproduced a served-version handoff bug: retiring
the last-publishing alias could remove an owner still present in another active
cache. Teardown now restores only the same namespace/name/UID from a surviving
desired watcher, using that watch's resource GVK and honoring deletion barriers.
The permanent regression covers a matching lifetime, replacement UID, absence,
a lagging deleted lifetime, and pending Pod recovery. The normal focused race
run passed: controller 2.713s, owner 1.816s, registry 1.408s. Scoped lint reported
zero issues after an equivalent boolean-expression simplification; scoped
whitespace validation passed.
