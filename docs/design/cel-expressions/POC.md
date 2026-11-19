<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Consolidated CEL PoC

This branch implements the [CEL KEP](../../keps/0002-cel-expressions/README.md),
the [SDK design](README.md), and explicit reference-based Pod membership. It is
a development branch. The API described here is not a published release.

A KServe model read returns the selected container fields. A write merges the
new image or resources at the selected model's JSON Pointer. The SDK retains
`storageUri` and other unmapped fields. A TrainJob can read its trainer image
from a supplied runtime or a caller-provided Kubernetes reader. Referencing a
shared runtime does not make its users one workload.

## What runs

| Requirement | Implementation and executable evidence |
| --- | --- |
| Separate read/write guards, pointers, boolean statuses | `v1alpha2` API, common validator and generated CRD; API contract tests |
| Optional values, lazy variables, bounded CEL | `pkg/cel`; compiled programs shared by definition revision, with scope, precision, cancellation and cost tests |
| Atomic JSON Merge Patch | `tree.Open` and `Mutate`; real editor tests for identity, overlap, unknown fields and failed re-extraction |
| Live or supplied references | `pkg/references` and factory options; missing/empty/error, cache, eligibility and fresh-observation tests |
| Kubernetes client interface | CLI dynamic adapter and runnable controller-runtime `ctrlreader` example; scope and pagination tests |
| Existing catalog | All 27 prior definitions converted, plus Trainer; status replay and write/read parity tests |
| Explicit Pod membership | `pkg/membership`; live/snapshot equivalence, UID chains, conflicts, namespace failures and operator examples |
| CLI grouping | Complete claimant collection, component Pod maps and exact target folding; fake-client command tests |
| Optional catalog installer | Post-install/upgrade hook; admission readiness, custom conflicts, partial retries and idempotency tests |

Run from the repository root:

```bash
make check
make helm-lint helm-validate test-e2e-updates
```

The offline examples are runnable independently:

```bash
cd docs/examples/quickstart
go run .
```

Membership inputs and results are documented in [Pod membership](pod-membership.md).
The six operator recipes and monitoring extension live in
[`membership/testdata/examples`](../../../karta/sdk/pkg/membership/testdata/examples).
They are tested against the real SDK. They are small membership examples, not
complete catalog replacements.

## Consumers

| Consumer | What it does |
| --- | --- |
| `kli get` and `describe` | Live reads use the caller's Kubernetes permissions. `describe -f trainjob.yaml --references runtime-results.yaml` uses a supplied snapshot. `--no-references` disables reads and extra attribution. |
| Headlamp and WebAssembly | Status rows evaluate status alone. Opening a workload shows its mapped root values, including the Trainer image and replicas. The live view uses the selected cluster and supports disabling references. |
| Workload exporter | The exporter reads references from its synchronized watches. A changed target requeues its dependent workloads and Pods. Metric workload identity follows the exporter's ownership and association settings. |
| Controller-runtime example | A reader maps kind and scope. The example rechecks workloads with references once a minute. |
| Offline verifier and replay | The verifier accepts named lookup or list results and can check root images and scale. Replay reads the references saved with each workload event. |

## Source history

The branch starts at upstream `145d2a7725b7d4c93d2bae95277e653e50ad0ba4`.
It cherry-picks the agreed design history and adapts the useful native CEL work
from `3e5a302084d606422293cc8e2c022c088b790ba6`. That endpoint includes the
path-based editor/reference work at `9b0b0a2ea949e7ad57d88712a84283dfcee86ff1`.
The port retains current upstream catalog and recorder fixes. It excludes the
policy proposal, draft APIs, custom fields and alternate patch policies.

The Trainer lifecycle recordings are copied from that native endpoint. Their
`v1.34.0` directory identifies Kubernetes, not the Trainer release. The source
installation pin is Trainer `v2.3.0`. The recordings contain TrainJob and runtime
snapshots; this branch does not present them as a newly recorded cluster run.

## Evidence boundaries

Status replay uses recorded workload objects. Mutation tests compare complete
objects against a jq test oracle. Frozen original jq catalog definitions also
check translation behavior. The suite compares 4,433 original jq reads and 3,416 assignable destinations.
It separately asserts 88 absent-Grove-group corrections. CronJob replicas and
PyTorch worker replicas are computed jq reads with no assignable destination;
their CEL writes use the raw-patch oracle. Raw replay checks 711 successful
writes and 246 guard rejections across 546 recorded states and eight synthetic
inputs. Synthetic inputs cover catalog modes absent from the recordings and
are labeled separately. jq is a test dependency only; the
SDK has one runtime expression engine.

The Job grouping replay uses two Jobs and three controller-created Pods recorded
on Kubernetes v1.34.0. It checks ownership, group keys and the required group
sizes of two and one. To refresh it, run:

```bash
python3 hack/e2e/record-job-groups.py --context "$CONTEXT" \
  --output test/e2e/replay_tests/testdata/grouping/jobs.json
```

A live CLI check used the Trainer v2.3.0 CRDs. A TrainJob with only a runtime
name returned one replica, 10m CPU and 16 MiB through both live and supplied
inputs. The verifier read the image `busybox:1.36`. A user without permission
to get the runtime received a forbidden error. After granting that permission
and changing only the runtime, the next live read returned three replicas.

The [optional Pod health example](../../examples/optional-pod-health.yaml) was
also exercised on kind. Two Ready Pods gave `Running`. An extra matching Pod
made the count differ from the desired two, so the rule returned `Undefined`.
With `--no-references`, the same Deployment returned `Running` from its own
status. The example assumes the full Deployment selector is the single
label `app`.

The Milvus, Strimzi, Flink and monitoring membership examples use synthetic
ownership inventories. They exercise the SDK rules. Live operator lifecycle
coverage is limited to the recordings present in the repository.

A complete supplied snapshot is the caller's promise. The SDK checks local
identity and selectors but cannot prove freshness or discover undeclared shared
consumers. Milvus Helm release metadata cannot prove the original creator UID
of a retained release with the same name. Deployment label-based health reads
can include unrelated labeled Pods; ownership-based membership remains separate.

Catalog conflict checks use the observed definitions and resource versions.
The existing admission uniqueness check is best-effort for concurrent creates
with different names. Installation has no cross-object transaction. A failure
can leave an installed prefix; rerunning validates the whole bundle again.

## Coordinated migration

Back up the existing Karta CRD, every Karta definition and the installed chart
values. Keep the old release artifact for rollback. Stop consumers that use the
old definitions while replacing the CRD and SDK together. Workload objects do
not need a format change.

`run.ai/v1alpha1` jq definitions are not automatically converted. Review custom
CEL replacements locally before the upgrade. Kubernetes also tracks stored CRD
versions: simply removing a stored `v1alpha1` from the CRD is not a migration.
For a disposable PoC cluster, back up and remove the old Karta CRD before
installing the generated `v1alpha2` CRD. This deletes its Karta objects, so restore
only the reviewed CEL definitions afterward. A production rollout needs a rehearsal with its custom definitions and consumers.

Set `catalogInstaller.enabled: true` to install the bundled CEL catalog after
the new CRD and admission webhook are ready. The default is false. Custom
Kartas claiming bundled workload types are rejected. Keep the installer off
when intentionally supplying custom replacements for those types.

Rollback is the reverse coordinated replacement: stop CEL consumers, preserve
any new custom definitions, restore the old CRD and backed-up jq definitions,
and start the old consumers. Helm rollback alone cannot restore definition data.

The kind rehearsal installs the legacy chart, saves its Job Karta, installs
all 28 CEL definitions, retries with one definition missing, then restores the
legacy CRD and saved Karta. It checks the native Job UID across both changes.
Run it with locally built operator images from the base revision above and the
current PoC:

```bash
LEGACY_IMAGE=karta:jq CEL_IMAGE=karta:cel bash hack/e2e/test-cel-migration.sh
```
