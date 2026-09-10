<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# Karta Enhancement Proposals (KEPs)

A KEP is a design document for a change that is too big for a pull request
description: anything user visible, anything that changes the CRD or the
public Go API, anything that needs agreement before code is written.

The name and the process follow the Kubernetes Enhancement Proposal model,
scaled down to this project. One document per enhancement, reviewed as a
pull request, kept in the repo forever as the record of what was decided
and why.

## When a KEP is required

- A new field, a renamed field, or a removed field on the Karta CRD.
- A new CRD version or a change to conversion or storage.
- A change to the expression engine or its semantics.
- A new public package or a breaking change to an exported Go interface.
- Any change a consumer has to react to.

Bug fixes, docs, tests, and internal refactors do not need a KEP.

## Process

1. Copy `TEMPLATE.md` into `keps/NNNN-short-name/README.md`, where `NNNN`
   is the next free number and `short-name` is a two-or-three word slug.
2. Fill in every section. Delete a section only if it truly does not apply,
   and say why in its place.
3. Open a pull request containing only the KEP. Review happens on the PR;
   the KEP merges when the approvers in `OWNERS` accept the design.
4. Implementation PRs reference the KEP by number. When the last one
   merges, update the KEP status to `implemented`.

## Status values

| Status | Meaning |
|---|---|
| `provisional` | The idea is captured; the design is still moving. |
| `implementable` | The design is accepted; implementation may start or is in progress. |
| `implemented` | The change is merged and released. |
| `rejected` | Considered and decided against; kept for the record. |
| `withdrawn` | The author pulled it back. |
| `replaced` | Superseded by a later KEP, named in the doc. |

## Structure of a KEP

Every KEP has the same skeleton, defined in `TEMPLATE.md`:

- Summary: three sentences a newcomer can follow.
- Motivation, with explicit goals and non-goals.
- Proposal: the design itself, including the API at a high level.
- Examples: real definitions, before and after where relevant.
- Migration and versioning: what breaks, and what a user does about it.
- Test plan: what each layer of testing proves.
- Risks and mitigations: what can go wrong in operation.
- Alternatives considered: what was rejected and why.
- Future work: what is deliberately left out.
- Implementation history: dated milestones.

## Index

| KEP | Title | Status |
|---|---|---|
| [0001](0001-cel-expressions/README.md) | CEL expressions in Karta definitions | implementable |
