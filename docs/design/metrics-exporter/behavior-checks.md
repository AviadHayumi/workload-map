<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter implementation: behavior checks

The local PoC connects the command, informers, discovery, definition inventory, CEL extractor and OTel adapter. These checks exercise that implementation. Earlier data-structure experiments are not used as proof of the connected exporter.

## Component and controller checks

The exporter `make check` passed at `61c78691` on 2026-10-20. It includes formatting, vet, lint and race tests. The publication commit `c66ea51d` contains the same component source without local agent state. Root `make check` also passed on that clean publication commit.

| Area | What the tests check |
| --- | --- |
| Ownership | Shared ancestors, late parents, UID replacement, reparenting, cycles and conflicting controller owners. The walk has no fixed depth cap. |
| Karta mapping | Actual catalog JobSet and LeaderWorkerSet selectors. Knative, KServe and RayJob owner chains preserve the true root even when only an intermediate owner has a Karta. |
| Inventory | Cluster/catalog precedence, include-or-label selection, exclusion, provenance, concurrent lookup and source failure. |
| Submitter | CEL compilation, metadata inputs, cancellation, cost and output limits, cache reuse and agreement across hidden Pods. |
| Reconciliation | Definition add/edit/delete, independent-cache skew, stale calculations, scope exit and independent validity of status, mapping and creation time. |
| Publication | Pending rows, retained invalid history, expiry, immutable pages and removal of superseded associations. |
| Delivery | Exact metric families and labels through Prometheus. Local OTLP HTTP/protobuf and gRPC receivers check delivery, headers, deletion and bounded shutdown. |
| Chart | Rendered configuration is loaded by the real command. Obsolete attribution fields fail validation. Native admission is separate from the exporter. |

## Connected Kind acceptance

Twelve scenarios passed with a physical build of `61c78691`. The binary ran the actual command without source overlays. No fixture wrote directly to its stores.

The test cluster ran Kubernetes 1.34.0, JobSet 0.10.1 and LeaderWorkerSet 0.7.0. It was created for this task. No remote cluster was used.

- JobSet and LWS produced the expected root UID, component, instance, group, status and creation time.
- LWS stayed the root after its Karta was removed, even with a StatefulSet Karta present.
- Adding, editing and deleting a cluster Karta changed interpretation correctly.
- A root identity propagated to Pods. A conflicting hidden Pod cleared it. Removing the conflict restored it.
- Pod-label changes and namespace selection changes updated or removed the expected rows.
- Recreating a JobSet with the same name removed the old root and Pod UIDs.

Each scenario required three consecutive correct scrapes. Assertions checked exact label sets and current object UIDs. Cleanup succeeded.

This run lasted about 44 seconds. It is not a long watch soak. Explicit ignored-owner boundaries and multiversion representations have deterministic controller tests; they were not separate scenarios in this Kind run.

## Native submitter admission

| Check | Result and boundary |
| --- | --- |
| Kubernetes 1.35.0 envtest | Passed with beta MAP and its API enabled. VAP used v1. |
| Kubernetes 1.36.2 envtest | Passed with stable MAP and VAP. |
| Helm lifecycle on Kubernetes 1.34.0 | Install, disable capture, upgrade and uninstall passed. The retained validation policy still rejected identity changes and removal. |

Native envtest, chart checks, root checks and operator checks passed at `73912f68`. The policy templates are unchanged from `339f44af`, which supplied the earlier Helm lifecycle check. The final local Dockerfile build and offline startup check passed at `c66ea51d` on linux/arm64.

Tests cover authenticated CREATE, forged identity, namespace and request conditions, UPDATE protection and metadata on subresources. They test API-server policies. They do not use an exporter webhook or an external identity mapper.

## Scale measurements

The scale harness runs the real exporter against a modeled Kubernetes API on loopback. It includes production informers, reconciliation, Karta evaluation and metric collection. It does not measure etcd, admission or a real API server.

The host has 48 GiB RAM. The exporter uses four CPU threads, a 6 GiB Go memory target and an 8 GiB sampled RSS limit. Each stage has a 180-second deadline. The host is shared with other development work.

| Workloads | Pods per workload | Source | Result |
| ---: | ---: | --- | --- |
| 100,000 | 0 | `3aa0289d` | Passed cold start, steady scrapes, status churn, deletion and settled cleanup. Peak sampled RSS: 6.08 GiB. |
| 100,000 | 1 | `3aa0289d` | Failed the Pod-update deadline. Convergence took about 203 seconds. |
| 100,000 | 1 | `63d74fc9` | All stages passed in 559.84 seconds. Pod update: 76.27 seconds. Peak sampled RSS: 7.07 GiB. |
| 100,000 | 10 | `63d74fc9` | Stopped during startup at 8.19 GiB sampled RSS. No complete metric validation was reached. |
| 1 | 100,000 | `63d74fc9` | All stages passed in 94.95 seconds. Peak sampled RSS: 4.85 GiB. |

The oracle checks every expected UID, phase, label, identity and deletion. An eventually correct scrape after the deadline remains a failure.

At 100,000 workloads and one Pod each, one uncompressed response is about 333 MB. Measured scrape time includes response generation, transfer and validation. These results are not a recommended production scrape interval or a guarantee for other workload shapes.

## Bugs found by review and tests

| Problem | Correction |
| --- | --- |
| Healthy WATCH requests ended after a global 30-second client timeout. | Streaming requests use the controller lifetime. LIST pages keep their own deadline. |
| A normal WATCH close was treated as an outage. | Normal closure reconnects without renewing evidence. Explicit failures still invalidate it. |
| Updating a Pod suppressed unchanged root status and sibling mappings. | Discovery separates changed objects from roots that only need reconciliation. |
| A stale membership calculation cleared newer submitter agreement. | Stale calculations are discarded without changing accepted agreement. |
| Deleted intermediate owners lost retained Pod history after reconciliation. | Still-associated Pods remain candidates until resolution confirms a new association or deletion. |
| Replacement informer synchronization was used as absence proof. | Only a complete fresh LIST can confirm absence. |
| A fresh LIST returned a replacement UID or newer resourceVersion before its watch event. | Retire the old UID or suppress its old evidence, without adopting unaccepted API bodies. |
| A hidden Pod row could return from history after a later failure. | Scope exit removes its output history while preserving membership tracking. |
| An owner-read failure left retained rows marked resolved. | Retained rows use unknown ownership until the path is verified again. |
| Generic Pod rows used a shared root API version. | Each row uses its own verified owner representation. |
| Unchanged definition refreshes queued every object again. | Only selection changes enqueue affected objects. Normal verification still renews evidence. |

The test harness also had a continuation-token defect. Its pages now retain a stable collection snapshot across unrelated writes. Results from the earlier fixture are not used as final capacity evidence.

## Remaining boundaries

The local PoC is not a released image. The million-Pod case exceeded the memory bound. Sustained high churn and other large-root shapes remain unmeasured. Cluster-scoped publication still needs a KEP decision. Multiple replicas observe duplicate scope; disjoint namespace deployments remain the partitioning option.

One intermediate full-suite run timed out while waiting for a Karta update. Five focused repeats and the final complete race suite passed. The isolated timing failure was not reproduced.

The current module pins client-go 0.36.3, cel-go 0.26.1 and OTel 1.47.0. Builds use Go 1.26.3. The Kind and envtest versions above describe the checks that ran, not a complete supported-version matrix.
