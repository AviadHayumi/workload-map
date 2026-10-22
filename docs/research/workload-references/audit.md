<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Research audit

## Live boundary

Target context: `kmetric-playground-dev`. Target namespace: `test-ref`.
The namespace was created and later read successfully. Connectivity returned
briefly, then cluster API and control-plane timeouts recurred.

- OSMO: isolated backend and pool registration passed. `karta-ref-single-4`
  completed and its real Pod UID was checked against exporter/KSM metrics.
  `karta-ref-group-2` completed with two captured Pods, a real PodGroup and a
  group ConfigMap. A successful later read found all four attempts' group
  resources absent. Default routing stayed
  unchanged. See [execution status](osmo/execution-status.json).
- SkyPilot: the isolated client and pinned server chart are installed. Real
  workload Pods have been captured and paired with exporter/KSM samples.
  The shared runtime request failed during Kubernetes connection refusal and
  a Ray agent-port startup timeout. Runtime, concurrent-job and managed-job
  execution remain unvalidated. See
  [validation status](skypilot/validation-status.json) for case completion.
- Exporter: all six resources are applied. The Deployment is Running and Ready.
  Effective RBAC matches the selected read kinds. Two real SkyPilot Pod rows
  were paired with the live KSM capture. See [setup](exporter/setup.md),
  [validation](exporter/validation.json), and [live evidence](spike/live/README.md).

The first OSMO single submission failed because the new pool omitted resource
variables required by its reused template. The fix copied the existing defaults
and validation references only into the new pool. The first group submission
failed with Kubernetes HTTP 500, `etcdserver: request timed out`. Both are
preserved in [single failure](osmo/evidence/first-submission-failure.json) and
[group failure](osmo/evidence/first-group-failure.json). Their eventual resource
cleanup is checked independently of terminal workflow status.

The historical OSMO capture is from 2026-10-16 in `osmo-submitter-lab`. It keeps
actual old UIDs and is marked `historical_capture`. It is not a fresh `test-ref`
execution. Synthetic spike fixtures test rules; they do not prove live behavior.

## Source and artifact pins

| Input | Revision |
| --- | --- |
| Exporter baseline | `01352db5c4be54b0b3eb6d53a0a04fb5faa34520` |
| Implemented CEL references | `cd91d670a9b6613e4d1c21e066b37a8446eb9fb1` |
| Reference KEP | `4d9534b9fdfa46d337c5d5c250dbad5db282be3e` |
| OSMO source inspection | `19b5170268bc2e221f2db9367f5893687b060864` |
| OSMO chart source | `ed59b69715e4ee6fff70bcd9d715106358a7205f` |
| Live OSMO backend version | `6.3.1.3326e42b` |
| SkyPilot initial inspection | `358c245e42eebe4ef8bc8ca7412ae2822c63ce02` |
| SkyPilot client/server source | `2b12f65313bfc045c4b673dfd383be8c317885f9` |

The SkyPilot client is `1.0.0.dev20261021` in a local temporary virtual
environment. Chart `1.0.0-dev.20261021` and the server image have exact checksums
in its validation file. OSMO's backend chart is version 1.3.1, app version 6.3.1.
The live service predates the inspected main branch. No claim depends on treating
those versions as identical.

## Executed local checks

| Check | Result | Record |
| --- | --- | --- |
| Focused exporter discovery tests | Seven top-level tests passed | [commands and output](karta/evidence/README.md#ownership) |
| CEL resolver, bindings and factory specs | 21 selected specs passed | [commands and output](karta/evidence/README.md#cel-references) |
| Generic association spike | Eight tests passed with `-race` | [commands and output](karta/evidence/README.md#association-spike) |
| Historical OSMO input | Three Pods, two workflows, twelve metadata observations; PodGroups absent | [output](karta/evidence/osmo-historical-associations.json) |
| PromQL with promtool 3.14.0 | Five scenarios, ten expression checks passed | [rules and commands](spike/promql/README.md) |
| OSMO chart and input files | Scope, exact request payloads and capture sanitization passed | [record](osmo/local-validation.json) |
| Exporter Helm | Strict lint and six-resource scope/config checks passed | [record](exporter/validation.json) |
| SkyPilot client and chart | Version, rendering, four task parses and probe syntax passed | [record](skypilot/validation-status.json) |

CEL tests used local commit `0fff67e9`; a direct diff found the inspected API,
reader, runner and factory files identical to the pinned remote implementation.
No production code changed. Full repository presubmit and publication are the
remaining repository checks.

## Review and remaining work

The requested Claude review could not run because the local CLI was not logged
in. No model ID or Claude review is claimed. Independent code and architecture reviews are complete. The code review
found a false-success path in two SkyPilot probes. They now require both runtime
completion and the expected metadata. Nine fake-SDK tests passed. The
architecture review requested clearer pending-test wording and an explicit
label-writer trust boundary. Both changes are applied. Final verdicts are
APPROVE for code and CLEAR for the bounded research architecture. They do not
certify the unfinished SkyPilot runtime tests.

Automatic review rejected reading a missing Secret's mounted token from an
existing Pod. That action did not run and was not retried. The supported OSMO
access-token API subsequently issued a dedicated token directly into a new
Kubernetes Secret. Tokens, private endpoints, node names and real user identities
are excluded from tracked captures.

| Goal | State |
| --- | --- |
| G1: source contracts | Complete |
| G2: isolated platform runs | OSMO single/group and cleanup completed; SkyPilot case status recorded by its lane |
| G3: generic mappings | Local tests, live joins, real PodGroup mapping and five-stage lifecycle checks passed; review pending |
| G4: review and publication | Independent reviews passed; full presubmit and publication pending |

Completion still requires the remaining SkyPilot cases and repository checks.
Status files retain their own capture times. Separate API reads are not an
atomic cluster snapshot.
