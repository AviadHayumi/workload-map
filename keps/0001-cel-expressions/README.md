<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# KEP-0001: CEL expressions in Karta definitions

- Status: implementable
- Authors: @AviadHayumi
- Created: 2026-09-10
- Tracking issue: to be opened before this KEP merges

## Summary

Karta definitions stop using jq paths and start using CEL, the expression
language Kubernetes already uses for CRD validation rules and admission
policies. Reads become CEL expressions. Writes become patches the definition
declares. This breaks the CRD, so it ships as a new version, `run.ai/v1alpha2`.
Since the version breaks anyway, this KEP also uses the moment to bring the
rest of the CRD closer to how Kubernetes upstream shapes its own APIs.

This document is the proposal. A prototype of the CEL engine exists on
development branches, but none of the `v1alpha2` shapes below are merged.

![the value accessor model](accessor-model.png)

The diagram source is `accessor-model.excalidraw`; open it at excalidraw.com to edit.

## Motivation

Today every value in a definition is a jq path:

```yaml
podTemplateSpecPath: .spec.template
replicasPath: .status.ready // 0
```

Three problems keep coming back.

First, jq is one more language to learn. Someone who writes admission
policies or CRD validation rules already knows CEL. Nobody reviews jq at
work.

Second, jq answers wrong questions instead of refusing them. A CronJob's
`status.active` is a list of job references, not a count. Ask jq
`.status.active > 0` and it says `true`, because in jq a list is always
"bigger" than a number. The workload shows Running when it is not. CEL
refuses the comparison with a typed error, and the mistake dies in review
instead of in production.

Third, in jq the read path is also the write location: the engine writes
through the same string it reads. Reads and writes cannot be separated,
which blocks the references design, where a value is read from one object
and written to another.

### Goals

- CEL becomes the only expression language in definitions.
- Reads and writes are separate: reads are expressions, writes are
  declared patches.
- The change ships as `run.ai/v1alpha2`, and the break is used to align
  the rest of the CRD with upstream naming and shapes.
- The whole catalog is converted and proven against the recorded
  fixtures.

### Non-goals

- Reading other objects (`references`). That is its own KEP.
- Renaming the `Definition` suffix family. Its own, smaller KEP.
- Graduating past alpha. The criteria are in the migration section.
- Changing the consumer-facing Go interfaces.

## Proposal part 1: CEL

### How a read looks

Every field that was a path becomes an expression. The workload is bound
as `object`:

```yaml
# before
podTemplateSpecPath: .spec.template

# after
podTemplateSpec:
  expression: object[?"spec"][?"template"].orValue(null)
```

The `[?"key"]` form means "this field may be missing". A missing field
becomes a value you handle with `.orValue(...)`, instead of an error or a
silent null. jq made that choice for you; CEL makes you write it down.
One trap to know when converting: jq's `// 0` default also fires when the
value is `false`. CEL's `orValue` fires only on absence. Where a
definition relied on the jq behavior, spell it out:

```yaml
expression: ([dyn(object.?status.?ready.orValue(null))].filter(v, v != null && v != false) + [0])[0]
```

### How a write looks

A write is not a path. It is a patch the definition declares, in the same
shape MutatingAdmissionPolicy declares its mutations: a list of entries,
each carrying its `patchType` and an expression that builds the patch.

```yaml
podTemplateSpec:
  expression: object[?"spec"][?"template"].orValue(null)
  patches:
    - patchType: MergePatch
      expression: '{"spec": {"template": value}}'
  patchStrategy: Replace
```

- `patchType: MergePatch` means the expression builds a map, applied as a
  JSON merge patch (RFC 7386, what `kubectl patch --type merge` does;
  null deletes a field).
- `patchType: JSONPatch` means the expression builds a list of RFC 6902
  operations (`kubectl patch --type json`). Karta is a bit friendlier
  than the RFC: an `add` creates the missing map parents on the way.
- The expression sees `value` (what Karta is writing), `instance` and
  `index` (which instance of a multi-instance component), and
  `variables.<name>`.
- The result must match the declared type. A map for `MergePatch`, an
  operation list for `JSONPatch`, anything else is an error. `{}` and
  `[]` mean no change.
- `patchStrategy: Replace` clears the field first (the patch runs once
  with `value` bound to null, then with the real value). Use it when the
  new value must not merge into the old one, like a pod template.
  Default is `Merge`. Only meaningful for `MergePatch`, because RFC 6902
  authors write their removals explicitly.

Why one list and not a single `patch` field plus a list? Because upstream
never does that. MutatingAdmissionPolicy has `mutations`, a list;
ValidatingAdmissionPolicy has `validations`, a list; ClusterClass has
`patches`, a list. One patch is a list with one entry. One field, one
shape, nothing to choose between.

An accessor with no `patches` is read-only. Writing through it is a loud
error, never a guessed location.

Patches are built against the document as it looked before the write
started, and a multi-instance write lands all-or-nothing: if instance 3
fails, instances 1 and 2 roll back. The rollback covers the in-memory
document Karta hands back to the consumer; pushing it to the cluster is
still the consumer's single update call.

### Conditional writes

Sometimes the right patch depends on the document. A CronJob keeps its
pod template under `spec.jobTemplate`, a Deployment under `spec` - one
definition serving a family of shapes needs "if this exists, patch here,
else patch there". Entries take match conditions, first match wins:

```yaml
podTemplateSpec:
  expression: ...
  patches:
    - matchConditions:
        - name: has-job-template
          expression: object.?spec.?jobTemplate.hasValue()
      patchType: MergePatch
      expression: '{"spec": {"jobTemplate": {"spec": {"template": value}}}}'
    - patchType: MergePatch
      expression: '{"spec": {"template": value}}'
```

The rules are short:

- Entries are checked in order. All of an entry's conditions must hold.
  The first entry that matches supplies the patch; the rest are not
  evaluated.
- An entry without conditions always matches, so a last entry without
  them reads like `else`. It may only appear last.
- A condition must return a CEL boolean. An error, a null, or a
  non-boolean is a failed write, not a `false`. Errors never mean false.
- If nothing matches, the write fails loudly. The definition said
  nothing about this document shape, and guessing is what this KEP
  removes.
- With `patchStrategy: Replace`, the entry is picked once and reused for
  both passes.

The names come from upstream: `matchConditions` with a required `name`
is how admission policies gate on CEL, and the name makes failures
addressable ("condition has-job-template failed" instead of "condition 0
failed"). Writing the same branch as a ternary inside one expression is
legal CEL, but control flow belongs in the API, not smuggled into a
string. The catalog uses `matchConditions`.

Suspend and resume actions use the same entry shape, so a conditional
suspend needs no new grammar:

```yaml
suspendDefinition:
  suspendActions:
    - patchType: MergePatch
      expression: '{"spec": {"suspend": true}}'
  resumeActions:
    - patchType: MergePatch
      expression: '{"spec": {"suspend": false}}'
```

### Variables

Long expressions get a name once and are reused everywhere as
`variables.<name>`, the same way a ValidatingAdmissionPolicy composes:

```yaml
spec:
  variables:
    - name: specReplicas
      expression: ([dyn(object[?"spec"][?"replicas"].orValue(null))].filter(v, v != null && v != false) + [1])[0]
  ...
      scaleDefinition:
        replicas:
          expression: variables.specReplicas
```

Rules: names are unique CEL identifiers, may not shadow the reserved
words (`object`, `value`, `instance`, `index`, `variables`,
`references`), and a variable may use earlier variables. Resolution is
by mention: an expression only pays for the variables it names,
transitively. One honest difference from VAP: a mentioned variable runs
even if it sits on a branch the expression would not take, and a dynamic
access like `variables[x]` resolves all of them.

### Status matching

Status matchers keep their family: `byPhase`, `byConditions`,
`byExpression`. What changes is that `byExpression` becomes a plain
boolean, like every CEL gate upstream:

```yaml
# before
byExpression:
  expression: (.status.active // 0) > 0
  expectedResult: "true"

# after
byExpression:
  expression: object.?status.?active.orValue(0) > 0
```

`expectedResult` goes away. It existed because jq answers were strings to
compare; a CEL predicate just answers true or false. Within one matcher
the clauses AND; across a status's matchers they OR. When several
statuses match, the fixed order picks one: Resuming, Suspending,
Suspended, Running, Failed, Completed, Initializing, Degraded.

### What each expression sees

| Where the expression lives | `object` is | extra bindings |
|---|---|---|
| specDefinition, scaleDefinition, statusDefinition, suspendDefinition, variables | the workload | `variables`; patch expressions and match conditions also see `value`, `instance`, `index` |
| podSelector, `groupByExpressions` | one pod | none |

`instance` holds the selected instance id string (null for a
single-instance component), `index` its position. Every evaluation is
compiled once, cached, and charged a per-run cost budget, so a runaway
expression fails fast with a named error instead of stalling a
reconcile. The extension functions available are CEL optional types plus
the list and string helpers Kubernetes enables; adding more is an API
change and needs a KEP.

### A whole definition, before and after

```yaml
# v1alpha1 (jq)
apiVersion: run.ai/v1alpha1
kind: Karta
metadata:
  name: batch-job-v1
spec:
  structureDefinition:
    rootComponent:
      name: job
      kind: { group: batch, version: v1, kind: Job }
      specDefinition:
        podTemplateSpecPath: .spec.template
      scaleDefinition:
        replicasPath: .spec.parallelism // 1
      statusDefinition:
        statusMappings:
          running:
            - byExpression:
                expression: (.status.active // 0) > 0 and (.status.ready // 0) > 0
                expectedResult: "true"
      suspendDefinition:
        suspendActions:
          - path: .spec.suspend
            value: "true"
        resumeActions:
          - path: .spec.suspend
            value: "false"
```

```yaml
# v1alpha2 (CEL)
apiVersion: run.ai/v1alpha2
kind: Karta
metadata:
  name: batch-job-v1
spec:
  variables:
    - name: specParallelism
      expression: ([dyn(object[?"spec"][?"parallelism"].orValue(null))].filter(v, v != null && v != false) + [1])[0]
  structureDefinition:
    rootComponent:
      name: job
      apiVersion: batch/v1
      kind: Job
      specDefinition:
        podTemplateSpec:
          expression: object[?"spec"][?"template"].orValue(null)
          patches:
            - patchType: MergePatch
              expression: '{"spec": {"template": value}}'
          patchStrategy: Replace
      scaleDefinition:
        replicas:
          expression: variables.specParallelism
      statusDefinition:
        statusMappings:
          running:
            - byExpression:
                expression: object.?status.?active.orValue(0) > 0 && object.?status.?ready.orValue(0) > 0
      suspendDefinition:
        suspendActions:
          - patchType: MergePatch
            expression: '{"spec": {"suspend": true}}'
        resumeActions:
          - patchType: MergePatch
            expression: '{"spec": {"suspend": false}}'
```

A multi-instance component, where the patch addresses the instance by
`index`:

```yaml
instanceIDs:
  expression: object.spec.replicatedJobs.map(x, x[?"name"].orValue(null))
podTemplateSpec:
  expression: object.spec.replicatedJobs.map(x, x[?"template"][?"spec"][?"template"].orValue(null))
  patches:
    - patchType: JSONPatch
      expression: '[{"op": "add", "path": "/spec/replicatedJobs/" + string(index) + "/template/spec/template", "value": value}]'
```

An instanced read returns one entry per id, in the same order; ids are
unique non-empty strings.

### CEL vs jq at a glance

| | jq (v1alpha1) | CEL (v1alpha2) |
|---|---|---|
| read a field | `.spec.template` | `object[?"spec"][?"template"].orValue(null)` |
| default | `.status.ready // 0` (also fires on `false`) | `.?status.?ready.orValue(0)` (absence only) |
| iterate | `.spec.jobs[].name` | `object.spec.jobs.map(x, x[?"name"].orValue(null))` |
| filter and test | `.status.conditions[] \| select(.type == "Ready") \| .status == "True"` | `object.status.conditions.exists(c, c.type == "Ready" && c.status == "True")` |
| wrong-typed question | `[...] > 0` answers `true` | typed error |
| write | the read path is the write location | a declared patch |
| conditional write | not expressible | `patches` with `matchConditions` |
| shared sub-expression | `$variables.name` | `variables.<name>` |
| cost | unbounded | compiled once, cached, budgeted |

## Proposal part 2: a new version is the chance to go upstream

Adopting CEL breaks the CRD, so `run.ai/v1alpha2` is a new version
either way. A breaking version is the one cheap moment to fix names and
shapes that drifted from how Kubernetes upstream does things. Each
change below stands on an upstream precedent; expand for the details.

<details>
<summary><code>optimizationInstructions</code> becomes <code>scheduling</code>, and <code>podGroup</code> replaces the deprecated plural</summary>

The section was named for one consumer. What it actually carries - pod
grouping for gang scheduling - is consumed the same way by KAI
Scheduler, Kueue, and Volcano. `scheduling` is neutral, and it is
already a shipping core field name (`RuntimeClass.spec.scheduling`).

Inside it, the v1alpha1 API had two models and had already deprecated
one: the flat `podGroups` list carries a deprecation notice pointing at
the singular `podGroup` mapping with `subGroups`. The new version
finishes that move: `scheduling.podGroup` is the one model, the plural
and the `gangScheduling` wrapper are gone. The topology fields stay
(`topologyName`, `preferredTopologyLevel`, `requiredTopologyLevel`):
their values are plain strings the definition supplies, no scheduler's
CRD is referenced, so they pass the neutrality rule. Anything that
cannot be said without naming a scheduler does not belong in the CRD.

Grouping keys: each `groupByExpressions` entry runs once per pod and
contributes one element to an ordered tuple; groups are equal when their
tuples are equal, no string concatenation tricks. The section is a
pointer with omitempty, so definitions without it carry no empty stanza.

Translating a Karta pod group into a scheduler's own object stays the
consumer's job.
</details>

<details>
<summary><code>kind: {group, version, kind}</code> becomes flat <code>apiVersion</code> + <code>kind</code></summary>

No user-facing Kubernetes API has a `gvk` field or a nested
group/version/kind struct. The upstream way is `apiVersion` + `kind`,
the same two lines every manifest starts with (admission policies'
`paramKind`, cluster-api's patch selectors). It also removes the
`kind.kind` stutter:

```yaml
# before
kind: { group: batch, version: v1, kind: Job }

# after
apiVersion: batch/v1
kind: Job
```
</details>

<details>
<summary><code>replace: true</code> becomes <code>patchStrategy: Merge | Replace</code></summary>

Upstream prefers an enum over a boolean when the field names a strategy,
because an enum can grow a third value without a breaking change. And
`replace` collides with the RFC 6902 operation of the same name, which
now legitimately appears inside `JSONPatch` expressions one line above.
Default is `Merge`.
</details>

<details>
<summary><code>byExpression.expectedResult</code> is removed</summary>

Every CEL gate upstream is a bare boolean: admission `validations`,
`matchConditions`, CRD validation rules. Comparing a stringified result
against an expected string is a jq leftover, and it blurs types (`true`
the boolean and `"true"` the string compare equal). A `byExpression` is
a predicate now. Existing catalog matchers already are booleans, so
their migration is deleting one line.
</details>

<details>
<summary><code>instanceIds</code> becomes <code>instanceIDs</code></summary>

Upstream capitalizes initialisms inside camelCase: `machineID`,
`systemUUID`, `providerIDList`. There is no `Ids` spelling anywhere in
the Kubernetes API types.
</details>

<details>
<summary><code>additionalChildKinds</code> entries become <code>{apiVersion, kind}</code> and the list is keyed by both</summary>

Same flattening as component kinds. Keying the list by `kind` alone
collides when two groups define the same kind name, and a list key can
only change on a version bump - so it changes now.
</details>

<details>
<summary><code>conditionsDefinition</code> field names get metav1 defaults</summary>

`typeFieldName`, `statusFieldName`, `messageFieldName`, and
`reasonFieldName` stay - each names a literal key inside the workload's
condition objects, which is exactly what a `FieldName` suffix means -
but they now default to `type`, `status`, `message`, `reason`, the
`metav1.Condition` spellings. A definition for a workload that follows
the Kubernetes condition convention writes only the expression.
</details>

<details>
<summary>Names that stay, on purpose</summary>

- `expression` - the exact field name every admission policy uses for a
  CEL string.
- `variables[{name, expression}]` - byte-for-byte the VAP shape, with
  the same `variables.<name>` access.
- `patches` and `matchConditions` - the admission policy vocabulary.
- The CEL bindings `object` and `variables` - identical to VAP.
  `value`, `instance`, and `index` have no upstream analogue because
  upstream has no instanced writes; they are documented reserved words.
- `structureDefinition`, `rootComponent`, `childComponents`,
  `suspendDefinition` - kept as-is so this KEP's diff stays about the
  expression language. Whether the `Definition` suffix family should be
  renamed is a separate, smaller KEP before v1.
- `references` - reserved for the follow-on references KEP; it appears
  in this document only as a reserved binding name. The follow-on KEP
  owns its shape, and the flattening rules above (apiVersion + kind,
  selectors kept verbatim) apply to it too.
</details>

## Migration and versioning

- `run.ai/v1alpha2` is served and stored; no conversion webhook. Alpha
  to alpha carries no compatibility promise, which is why the change
  lands now and not at beta.
- A stored version cannot just disappear. The operator procedure:
  export the existing definitions; pause writers and consumers; apply
  the transitional CRD (v1alpha1 served, v1alpha2 storage); apply the
  rewritten definitions; confirm nothing is still stored as v1alpha1;
  drop v1alpha1 from served versions and from `status.storedVersions`;
  restart consumers. Rolling back is the same walk in reverse with the
  exported originals.
- The CRD, the catalog, admission validation, and the consuming
  binaries move together. Mixed old-language consumers against
  new-language definitions are not supported.
- Rewriting a definition is table work, not magic: every `*Path` field
  maps to an accessor as shown above, but jq streams, `//`-on-false
  defaults, and write addressing each need a human look.
  `hack/karta-verify` checks a rewritten definition against a real
  manifest before it ships. The recorded fixtures replay every captured
  operator state through the engine offline, so a catalog conversion
  mistake fails a test, not a cluster.
- Go consumers of the factory, accessor, component, and tree interfaces
  recompile unchanged. Code importing the versioned API package moves
  to `v1alpha2`.

Removed in v1alpha2: all 24 `*Path` fields, `filters`,
`groupByKeyPaths`, `expressionLanguage`, path-and-value suspend
actions, the `gvk`-style nested kind structs, the deprecated `podGroups`
plural, and `expectedResult`. The release notes carry the complete
field-by-field mapping.

## Validation

Three stages, kept distinct:

- Schema and `KartaValidator`: required fields, list keys, name rules,
  the entry rules above (an unconditional entry only last, result type
  matches `patchType`, no `patches` on `instanceIDs`).
- Compilation: every expression compiles against its environment at
  admission time, so a typo fails when the definition is applied, not on
  first use.
- Evaluation: types, cost, and constructed patches are checked at run
  time. Errors name the definition, component, field, and entry, and do
  not embed the workload's contents.

## Test plan

- Unit: absent vs null vs false vs empty; entry selection order and the
  errors-never-mean-false rule; `Replace` two-pass behavior; RFC 6902
  parent creation and pointer escaping; instance ordering; rollback
  boundaries; variable dependency selection and shadowing rejection.
- Integration: the generated v1alpha2 schema, defaults, list keys, and
  admission compilation on and off; definitions from the API server and
  from raw YAML behave identically.
- Migration: start from stored v1alpha1 definitions, run the documented
  procedure, verify the data and the `storedVersions` cleanup.
- Catalog: every definition converted and replayed against the recorded
  fixtures; CronJob (nested template, suspend) and JobSet (instances,
  grouping) verified live.

## Risks and mitigations

- Definitions get longer. The explicit absence handling and the
  separate write declaration cost lines jq did not. Variables keep the
  worst of it out of every field.
- Nothing forces a read and its patches to address the same field.
  Review and fixture replay catch a mismatch; the schema cannot.
- A hot loop can hit the evaluation budget. The failure is a named,
  typed error, not a stall.
- A patch can build something the workload's controller fights.
  Recording a flow makes the write's effect visible before the
  definition ships.

## Alternatives considered

- Keep jq. The typed-refusal, write-separation, and review-language
  problems stay.
- Both engines behind `spec.expressionLanguage`. Built and rejected:
  every definition doubles its verification surface, and the engines
  disagree exactly where it hurts (`//` on false, list typing).
- A single `patch` field with `patches` as an alternative. Rejected:
  upstream is list-only everywhere (`mutations`, `validations`,
  ClusterClass `patches`); two ways to say one thing is API noise.
- Inferring merge-vs-operations from whether the expression returned a
  map or a list, with no `patchType`. Rejected: MAP declares
  `patchType` as a union discriminator for a reason - readers and
  validators should not have to run the expression to know what kind of
  patch it is.
- `when` (Kyverno) or `enabledIf` (cluster-api) instead of
  `matchConditions`. The admission policy family is the model this KEP
  follows, and its named conditions give addressable errors.
- Renaming the `Definition` suffix family now. Deferred to its own
  small KEP so this one stays reviewable.

## Future work

- The references KEP: reading other objects as `references.<name>`,
  with a reader interface, permission checks, and recorder support.
- Declaring `variables.<name>` per definition in the CEL environment,
  the way VAP declares params, so an undeclared name fails at admission
  instead of at evaluation.
- Per-context CEL environments, so a pod-selector expression that names
  `value` fails at compile time.
- Aggregate evaluation budgets and size limits, chosen with beta.

## Implementation history

- 2026-09-08: CEL engine and converted catalog prototyped; recorded
  fixtures replay green.
- 2026-09-09: references prototyped on top of the read/write split.
- 2026-09-10: this KEP; status `implementable`. The `v1alpha2` shapes
  are proposal only.
