<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Historical metrics exporter peer-review disposition

This records an earlier PoC. Results and decisions below apply to the cited
revisions. See the [current design](metrics-exporter/README.md) and
[verification guide](../../karta-metrics-exporter/README.md#verify).

The final [KEP-0001](../keps/0001-metrics-exporter/README.md) governs these decisions. Opus 4.8 supplied the broad static review; it did not execute tests. GPT-6 Astra performed implementation and evidence-based triage. The first two broad Sonnet 5.5 responses were empty. A narrowed four-file Sonnet 5.5 review completed normally and reported no concrete correctness or concurrency findings in collector, server, store and store types. That limited result predates the scrape-concurrency limit, does not review the controller, and does not establish executed-test evidence.

| Finding | Disposition | Evidence and action |
| --- | --- | --- |
| C1: returned owner pointer aliases informer UID fields | Rejected as described | `controllerRef` copies the OwnerReference value before returning its address. UID, name, kind, and API version are copied strings, so mutating the input slice cannot change committed edges. Resolve does not read the copied Controller pointer. Added `TestOwnerReferenceInputIsCopied` to prove input UID/name mutation affects the index only after the next committed update. |
| C2: cache shared-owner read failures | Rejected | Every descendant of the failed owner depends on that same read; they are not unrelated observations. A failed direct read cannot establish freshness for those descendants. Caching one failure within a pass bounds API demand and keeps that pass consistent. The cache is recreated for each verification pass, so successful fresh reads recover health on the next pass. |
| H1: remove collection count equality | Accepted narrowly; inventory completeness retained | A reproduced three-root regression showed one Pod or Job cache mismatch invalidated unrelated roots. The verifier now compares one immutable informer inventory with all fresh UIDs/resourceVersions and enumerates cache-only objects. Resource-type coverage stays unhealthy, but a completed collection localizes mismatches through the fresh owner graph. Reparenting invalidates both old and new roots; cache-only unmapped Pods are attributed conservatively. Unknown owners still invalidate their namespace; failed LISTs and missing/unsynced informers still invalidate all dependent roots. Karta mismatches mark both old and fresh target group/kinds; invalid targets retain any known previous selected kind. `TestHealthCacheMismatchIsolatedToAffectedRoots` covers eight churn/read cases, and `TestHealthDefinitionCacheMismatchIsolatedByKind` covers unrelated, retargeted, deleted and invalid definitions. This replaces the initial rejection of H1; dropping completeness checks entirely remains incorrect. |
| H2: omit failed inner candidates entirely | Rejected | A successful walk to a higher selected root already suppresses both child status and child health (`verifyHealth` skips the record, and collector filters confirmed nonroots). An incomplete walk cannot establish that a candidate is a confirmed nonroot. Its unhealthy candidate row exposes the required unresolved-owner failure, with no status or Pod mapping. Deleting these records also loses candidate component data and recovery state. Existing zero-Pod and ignored-boundary tests cover both confirmed nonroots and unresolved candidates. |
| H3: old served-version delete can leak a lifetime | Accepted and fixed | The same UID identifies the same lifetime across served versions. Removed the version-only deletion guard; any delete for that UID removes its workload and Pod mapping. A recreated object has a different UID. Renamed and updated the version-transition regression to check selected-version label replacement followed by authoritative lifetime deletion from the old served version. This supersedes the earlier bug-log claim that the guard was required. |
| H4: allocation/cardinality | Allocation concern accepted; finite metric cap rejected | The collector allocates and constructs sorted OTel attributes per sample. An immutable attribute-set prototype reduced allocation but did not establish enough throughput improvement. A retained phase-label cache was rejected after measurement: at 100,000 roots and zero Pods it retained 754,067,552 heap bytes and reached 4,336,730,112 bytes peak RSS; its three scrapes took 7.772, 8.294 and 8.184 seconds. The nonretained attribute-set prototype retained 64,988,880 heap bytes with 3,665,068,032 bytes peak RSS and 7.242, 7.466 and 7.441 second scrapes. The final fixed-array implementation and capacity decision are documented in the [scale decision](metrics-exporter-scale-decision.md); these rejected prototypes are not final capacity claims. The exact dense nine-phase contract already requires about 900,000 status rows at 100,000 roots. A finite overflow cap would silently violate completeness; it is not an acceptable safety fix. Collector optimization and final measurements are tracked in the scale decision. |
| M-A: split Track and Upsert publication | Accepted and fixed | The original separate calls could expose a newly tracked node before its owner edge was published, temporarily making an incomplete chain look complete. Added `UpsertTrackedObject`, publishing identity, forward edge, reverse edge and pending-drain changes under one lock. All production controller paths now use it. `TestTrackedOwnerPublicationIsAtomic` exercises concurrent creation/deletion and resolution; no partial node may become a confirmed root. |
| M-B: verification timeout at scale | Measured risk; proposed partial success rejected | A failed namespace-wide Pod LIST cannot establish complete Pod coverage for any root: unvisited Pods may belong to already visited roots. Marking all dependent roots unhealthy is required unless collection is explicitly partitioned by trustworthy ownership. The production verifier uses QPS 50 / burst 100, 1,000-item pages and a 60-second pass budget. Complete coverage and measured deadline headroom are recorded in the [scale decision](metrics-exporter-scale-decision.md). The loopback HTTP fixture is not a live-cluster throughput claim; larger ratios and real API latency remain capacity concerns. |
| M-C: pending entries after watcher teardown | Accepted with a narrower fix | Retry now removes a pending entry when its definition is no longer selected or its selected version changed. It also removes a matching obsolete stored record. A temporarily absent watcher for a still-selected definition is retained for API recovery rather than blindly discarded. Normal definition reconciliation already removes matching pending entries; the retry check also covers late event publication. Added both removed-definition cleanup and temporary-watcher-absence regressions. |
| M-D: replicas omitted from identity comparison | Rejected | `Attribute` evaluates replica selectors against the Pod only. Workload-derived attribution inputs are component instance IDs, not desired replica counts. No workload scale range is passed to the Pod querier. `instanceSetChanged` therefore compares exactly the inputs relevant to attribution. Pod selector input changes still trigger evaluation. |
| M-E: collection and object 404 reasons | Accepted and fixed | Added a collection-specific classifier used by watch errors and both fresh LIST passes; collection 404s report api_unavailable. A named missing-object GET reports unresolved_owner. A generic API endpoint 404 without a named object reports api_unavailable. Added reason-classification regressions. |
| M-F: default trimmed Pod cache | Already fixed before review; requested validation approach rejected | The executable now defaults FullPodCache to true, and a regression evaluates a container CPU request selector plus a spec-only update. Explicit advanced trimming remains an opt-in tradeoff, not the default. Fresh verification evaluates selectors against full directly read Pods, so divergent trimmed-cache attribution cannot become freshly healthy. Marking a valid arbitrary selector invalid_definition would misclassify a cache configuration limitation. |

Additional findings from the follow-up review were fixed independently: fresh ownership now rejects a UID used with the wrong group/kind; metadata informer events use the watched resource GVK rather than the PartialObjectMetadata envelope; and catalog arbitration follows oldest creation time then ascending name exactly.

The final peer-fix batch includes atomic-publication, input-copying, pending-cleanup, version-lifetime deletion and 404 classification regressions.

Earlier peer-fix validation passed without an overlay: `go test -race ./pkg/controller ./pkg/owner ./pkg/registry` completed successfully (controller 2.944s, owner 2.223s, registry 1.484s). Scoped golangci-lint reported 0 issues. The HTTP verification smoke matrix also passed for 1,000 workloads with 0, 1, and 10 Pods per workload: all 1,003 health rows were healthy, with respectively 4, 4, and 22 bounded LIST requests and no fallback GETs.

The focused controller/owner/registry race run also passed after H1 localization (controller 2.295s). Earlier health measurements predate that localization change; their source hashes are retained with the raw results. A subsequent completed five-file Sonnet 5.5 metrics review included the final collector/store/server snapshot, scrape-concurrency cap and snapshot root gate, and reported no concrete bugs. It did not extend to this controller change. Subsequent failure-epoch cleanup is covered by `TestVerificationRetiresFailureEpochsAfterDeletion`; the implementation owner reported that the subsequent focused state/store/collector/attribute race run passed.

A later Opus controller review raised no additional proven defect after source
triage. Its conditional definition-fallback concern assumed a metadata-only Karta
cache. Production Karta informers and fresh Karta LISTs retain full unstructured
objects, so old and fresh target kinds remain available to the mismatch check.
Invalid fresh targets fall back to the known selected entry. If a definition has
already left the selected entries, the workload verification path explicitly
marks its record unhealthy. The review withdrew its slice-aliasing and verifier
reset claims; its Pod reparent analysis also confirmed that both roots are
invalidated. This disposition is a static source assessment, not test evidence.

Independent lifecycle review reproduced a remaining served-version deletion gap:
a metadata alias could delete its owner index entry while leaving a confirmed
workload record in the store. `onChildDelete` now also removes the workload and
pending retry under the publication lock. `TestMetadataAliasDeletionRetiresSelectedRoot`
covers direct deletion and a relist UID replacement, including attempts by the
lagging full observer to restore the old lifetime.

The October 14 normal-source focused race run passed with `GOMAXPROCS=2` and
`GOFLAGS=-p=2`: controller 2.141s, owner 2.256s, registry 1.517s. It includes bounded
publication stripes, current cache UID checks, served-version and duplicate-Pod
UID barriers, definition teardown, and relist cleanup. Those focused checks did
not replace repository checks, live acceptance or repeated capacity measurements.

Follow-up deterministic tests also reproduced late publication from a stopped
metadata watcher and first-root publication after definition removal. Registry
mutation and the final publication checks now share the controller lock with
watcher teardown; workload evaluation remains outside that lock. The checks
validate the selected definition, watcher identity, and current cache UID before
publishing. `TestStoppedMetadataWatcherCannotPublishLateEvent` and
`TestDefinitionRemovalRejectsFirstWorkloadPublication` failed before this fix and
passed afterward. The final normal-source controller/owner/registry race run
passed in 3.080s, 3.013s, and 1.524s respectively. Scoped golangci-lint reported
zero issues, and scoped whitespace validation passed.

The final pruning review found one further served-version handoff defect. A node
last published by an obsolete watcher disappeared even when a surviving desired
alias already had the same object in its synced cache. The fix reuses only a
matching namespace/name/UID from that active cache and rejects UIDs with deletion
barriers. It preserves pending retries and runs descendant callbacks after
unlocking. `TestPrunedAliasPreservesOnlySameLifetimeInSurvivingCache` covers
matching, absent, replacement, and deleted-lifetime cases. Focused race tests
passed (controller 2.713s, owner 1.816s, registry 1.408s); final scoped lint reported
zero issues and whitespace checks passed.

## Frozen implementation validation

Commit `02a613462995f11ac3825c6b3db7a7f51c485e0c` includes all fixes above.
Root `make check` and `make -C exporter check` passed, including the complete
exporter race suite and zero lint issues. The [live acceptance evidence](../../exporter/test/e2e/results/acceptance-2026-10-14.json)
records 21 workload cases, 21 faults and four transport checks on that production
image. Later fixture corrections are recorded separately. The [scale decision](metrics-exporter-scale-decision.md)
identifies the measured source and capacity limits. Static model reviews do not
replace those executable results.
