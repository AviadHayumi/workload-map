<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# A simpler exporter controller

## Problem and example

In baseline `ee7d2ac6`, a Pod label update schedules its root worker. That worker combines
workload status, Pod pages and freshness proofs before publishing. The Pod's
change therefore passes through state that belongs to the workload and source
lifecycle.

For `Pod p -> ReplicaSet r -> Deployment d`, the Pod worker should publish p's
mapping to d. The workload worker should publish d's status. If r changes owner,
one queued namespace sweep should rediscover affected Pods and selected objects.

## Purpose and method

Three locally prepared, pinned repositories informed this decision. Kubernetes
shows queue, owner identity and deletion patterns. controller-runtime shows
shared source and cache synchronization. kube-state-metrics shows per-object
publication. No additional repositories or network downloads were needed.

These reports describe source behavior, not test evidence for Karta. The
implementation and its validation are recorded separately.

## Comparison matrix

| Topic | Kubernetes | controller-runtime | kube-state-metrics | Karta decision |
| --- | --- | --- | --- | --- |
| Work scheduling | Domain queues and coalesced keys | Current-state reconciliation by key | Reflectors update stores | Pod and workload queues |
| Reads | Shared listers; owner UID checks | Shared informer by representation | Shared LIST/WATCH per scope | Full evaluated objects; metadata ancestors |
| Intermediate changes | Owner events and cached scans | Dependency scheduling is caller-owned | No owner traversal | Coalesced scope sweep |
| Publication | Controller-specific | Reconciler-owned | UID-keyed generated output | Independent immutable UID records |
| Recovery | Queue retries and reflector recovery | Initial cache/handler synchronization | LIST/WATCH call observations | Sync and observed stream health |
| Freshness | No universal cross-resource transaction | No cross-cache transaction | No per-object age eviction | No expiry guarantee |

## Options, ranked

1. Separate Pod and workload results with shared sources. This follows the
   upstream queue and per-object publication boundaries. It removes root-owned
   Pod pages, proof renewal and reverse owner-path indexes. Risk: the two results
   can represent different revisions. This is acceptable for these metrics and
   must be explicit in their contract.
2. Keep combined root publications but remove proof expiry. This is a smaller
   change, but a Pod update still schedules root work and retains page management.
   It does not meet the requested separation as clearly.
3. Maintain a complete owner dependency graph. Kubernetes garbage collection
   needs such a graph for deletion policy. This exporter only derives metrics.
   The graph adds bookkeeping without a demonstrated need at current scale.

## Decision for Karta

Use five controller files with one purpose each:

| File | Responsibility |
| --- | --- |
| `runtime.go` | Queue workers, selection and final root membership |
| `sources.go` | Shared LIST/WATCH sources, recovery and event routing |
| `reader.go` | UID-checked reads of accepted cache objects |
| `leaf.go` | Pod discovery, mapping and one Pod result |
| `root.go` | Candidate admission and one canonical workload result |

Keep existing discovery traversal, inventory selection and SDK evaluation.
Selected evaluated resources and Pods retain full objects. Custom Pod expressions
may read spec or status. Traversal-only ancestors use metadata coverage. A source
must synchronize before its data supports a valid result.

Keep UID checks, source retirement fences and complete-list deletion handling.
These protect concrete object lifecycle races. Remove per-object proof expiry,
periodic metadata verification, dependency failure reference counts and renewal
scheduling. Healthy quiet objects keep their last result.

Root events schedule their associated Pods because workload expressions can
change Pod labels. Intermediate ownership changes schedule one scope sweep before
scanning. The scan includes selected candidates so zero-Pod workloads are covered.
A Pod registers membership before reading the current root body. Queue dirty
semantics retain changes that arrive while work is in flight.

Source failures and object evaluation failures have separate owners. A failed
result retains its last successful value and original provenance, marked invalid.
First discovery errors without a root use `scope="discovery"` on the existing
observation-valid metric with empty workload identity. They do not invent a root
or claim that transport failed. Status evaluation errors do not suppress a
verified creation timestamp.

The upstream recommendations differ in details. controller-runtime uses name
keys and leaves UID cleanup to reconcilers. This exporter retains UID in derived
state. Its report suggests a configuration queue, but configuration here is
source orchestration; the two domain queues remain Pod and workload. A persistent
reverse path graph is unnecessary when a coalesced scope sweep covers dependents.
The kube-state-metrics report proposes dropping failed output. Karta retains its
existing last-good, explicitly invalid output contract instead.

## What NOT to do

- Do not replace the proof graph with another generic dependency framework.
- Do not create a watch or root GET for each Pod.
- Do not infer source health from a successful object evaluation.
- Do not treat a failed or partial list as deletion evidence.
- Do not share a mutable label-resolution closure across worker goroutines.
- Do not hide the consistency change behind the old refresh/max-age settings.
- Do not promise that tests prove absence of every bug or production-scale cost.

## Per-repo index

- [Kubernetes](./simple-exporter-controller-research/kubernetes.md): queues, sweeps and UID-safe ownership.
- [controller-runtime](./simple-exporter-controller-research/controller-runtime.md): source sharing and synchronization.
- [kube-state-metrics](./simple-exporter-controller-research/kube-state-metrics.md): per-object publication and source observations.
