<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Replay existing workloads through the SDK

The SDK preserves the recorded workload states in the tested corpus.

The tests also compare complete desired trees with the existing extraction APIs.
They compare errors when an existing definition cannot build a tree.
An unchanged error is a compatibility result, not a successful extraction.

## What runs without a cluster

```bash
go test -race ./test/discovery -run TestRecorded -count=1 -v
```

The default fixtures are five real JobSet recordings:

- Running.
- Completed.
- Failed.
- Suspended.
- Resumed.

They contain 21 state frames.
Each frame runs with the attribution cache disabled and enabled.

The recorder judged each state from the workload's own fields.
The SDK does not generate those expected states.

For each frame, the test:

- Publishes the root into the index.
- Checks its recorded state using the historical definition.
- Compares its complete desired tree with `tree.Build` and `karta.New(...).Tree`.
- Repeats acquisition to check stable output.
- Publishes the next frame and verifies the previous observation stays unchanged.
- Repeats the tree comparison using this branch's current catalog.

The historical definition is intentional.
A recording's expected state belongs to the definition used for that recording.
Current catalog changes are tested separately against the existing API.

## Mutate a workload safely

Six frames from the completed JobSet recording exercise local edits.
Both cache modes run every check.

| Operation | Assertion |
| --- | --- |
| Change the workers' scheduler | Only the selected template's scheduler field changes. |
| Request unsupported fields | Return `ErrNotSupported`; keep the entire object unchanged. |
| Select an unknown instance | Reject the edit without partial changes. |
| Supply an invalid field | Reject the edit without partial changes. |
| Cancel the edit | Return an error without partial changes. |
| Suspend, then resume | Only `.spec.suspend` changes for each action. |
| Publish the edited root | A new observation sees the edit; the old observation stays unchanged. |
| Inspect status afterward | A local spec edit does not invent a controller status transition. |

These are offline edits.
They do not prove that a Kubernetes API server will accept an arbitrary patch.
The separate [live tests](README.md) exercise real API conflicts, replacements,
owner changes and admission constraints.

## Group Pods and follow updates

The recordings contain root CRs, not complete descendant Pod snapshots.
The grouping test therefore adds explicit fixtures to a recorded JobSet root.
It does not describe those Pods as recorded cluster evidence.

The fixture has:

- Two named instances: `workers` and `chief`.
- Replica keys `0` and `1` in each instance.
- Two full Pods per replica, for eight Pods total.
- Realistic ownership edges: Pod -> Job -> JobSet.

Starting from one Pod, the test resolves the complete JobSet.
It checks the exact full Pods for each component, instance and replica scope.

Next, it changes one Pod's instance label, replica label and phase.
Its UID and resourceVersion remain unchanged.

The next observation must:

- Include the complete updated Pod in its new scope.
- Remove it from its previous scope.
- Keep all eight workload Pod UIDs exactly once.
- Preserve every unrelated Pod body.
- Leave the earlier observation unchanged.

`Replica(key)` selects the definition's replica axis.
Scheduling groups declared in `optimizationInstructions.gangScheduling` are a
separate concept.

## Replay the larger public corpus

The larger run uses the public recordings and definitions at
[revision 57b3815](https://github.com/run-ai/karta/tree/57b3815619871b8759e28546cf03f2e28d000499/test/e2e/recorded_data).

Run from the repository root:

```bash
revision=57b3815619871b8759e28546cf03f2e28d000499
corpus="$PWD/.context/recording-corpus"
mkdir -p "$corpus"
git fetch --no-tags https://github.com/run-ai/karta.git "$revision"
git archive "$revision" test/e2e/recorded_data docs/catalog |
  tar -x -C "$corpus"

KARTA_RECORDING_CORPUS="$corpus" \
  go test -race ./test/discovery -run TestRecorded -count=1 -v
```

Recorded on 2026-10-16 with Go 1.26.3:

| Evidence | Result |
| --- | --- |
| Supported recordings | 60 recordings across 17 workload types. |
| Recorded states | 291 frames, checked in both cache modes: 582 checks passed. |
| Current-catalog compatibility | 582 complete-tree or matching-error comparisons passed. |
| Historical tree limitation | 22 Grove frames could not build a full desired tree; both APIs returned the same error in both modes. |
| Outside this branch's catalog | Eight recordings: four Agent Sandbox and four LLMInferenceService. They are reported as excluded, not covered. |
| Local edits and grouping | Passed with the race detector in both cache modes. |
| Live kind validation | Seven roots, fourteen Pods and thirty-two API objects matched an independent oracle in all six reader modes. Mutation checks passed; no tests skipped. |

The Grove limitation is an absent optional `podCliqueScalingGroups` array in
historical objects.
Its definition iterates that array without an empty-array fallback.
Recorded root states still match; full desired-tree extraction does not succeed
for those frames.

The replay follows the upstream recorder's contract: the strongest recorded state
must appear in Karta's normalized statuses.
It does not require exact equality with every overlapping phase saved by the
recorder.
Complete returned status and tree values are compared with the existing API.

This is regression evidence for these inputs and operations.
It does not prove all workload types, controller versions or production sizes.

## Fixture provenance

The five checked-in recordings retain their original payloads.
Only SPDX and copyright comment lines were added.
The historical definition is unchanged.

[provenance.json](testdata/recordings/provenance.json) records the immutable source
paths and original SHA-256 values.
No live cluster capture or kubeconfig is included.
