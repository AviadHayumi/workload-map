<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# KEP-0001: CEL expressions and value accessors

- Status: implementable
- Authors: @AviadHayumi
- Created: 2026-09-10
- Last updated: 2026-09-10
- Tracking issue: to be opened before this KEP merges

## Summary

Karta definitions move from jq path strings to CEL, the expression language
Kubernetes itself uses for CRD validation rules and admission policies. Every
read becomes a CEL expression evaluated with the workload bound as `object`,
and every write becomes a patch the definition constructs. The change breaks
the CRD, so it ships as a new API version, `run.ai/v1alpha2`, and the version
bump is used to decouple the scheduling section from any single scheduler.
This document describes the target design; the Implementation status section
states exactly what the prototype branches have and lack.

![the value accessor model](accessor-model.png)

The diagram source is `accessor-model.excalidraw`; open it at excalidraw.com to edit.

## Implementation status

As inspected on 2026-09-10: the `cel-native` branch (052db5f2) implements CEL
accessors, named variables, single patch expressions, and a converted catalog;
the `cel-references` branch (cd91d670) adds reference declarations, resolution
through a reader interface, and the `references` binding. Both branches still
use the `v1alpha1` Go package and a CRD serving and storing `run.ai/v1alpha1`,
and both retain `optimizationInstructions.gangScheduling`. Neither implements
`run.ai/v1alpha2`, the `scheduling.podGroup` layout, the structured `patches`
list, the discriminated patch union, boolean-only status expressions, or the
renames in the naming table. Conditional writes on the prototype use CEL
ternaries inside the single patch expression. Statements below describe the
target design unless explicitly labeled as prototype behavior; prototype test
results do not demonstrate completion of the proposed API version or its
migration.

## Motivation

Every value a Karta extracts today is a jq path. That served the first
catalog well, but it has four structural costs.

- jq is not the Kubernetes ecosystem's language. An author who writes a
  ValidatingAdmissionPolicy or a CRD validation rule already knows CEL;
  jq is one more dialect to learn and to review.
- jq is untyped over a total order. `.status.active > 0` answers `true`
  when `active` is a list of job references, silently producing a wrong
  status. CEL refuses the comparison with a typed error.
- A jq path doubles as an l-value: the engine writes through the same
  string it reads. That makes reads and writes inseparable, which blocks
  the resource-references design, where a value is read from another
  object but written to the workload.
- Evaluation has no cost model. CEL programs are compiled once, cached,
  and charged a per-step budget, so a runaway expression fails fast
  instead of stalling a reconcile.

### Goals

- One expression language, CEL, for every read in a definition.
- Reads and writes separated: `expression` reads, a patch construct writes.
- A new CRD version, `run.ai/v1alpha2`, carrying the change; the old
  fields are removed, not deprecated in place.
- A scheduler-agnostic scheduling section, decoupled from any single
  scheduler's vocabulary.
- The consumer-facing Go API (factory, accessor, component, tree) is
  source-compatible for callers of the retained runtime interfaces.
  This is not a guarantee for code importing the `v1alpha1` package,
  constructing versioned structs, or registering schemes.
- The catalog, the recorder fixtures, and the docs ship converted.

### Non-goals

- A jq compatibility layer or a dual-engine mode. Two engines means two
  sets of semantics to verify per definition.
- A conversion webhook. The migration is a documented maintenance
  operation (see Migration and versioning).
- Resource references. They build on this KEP and are owned by a
  follow-on KEP; their appearance in the sketch below is a
  non-normative preview.

## Naming decisions

Every name below was reviewed against Kubernetes API conventions and the
admissionregistration, cluster-api ClusterClass, Gateway API, Kyverno, and
Kueue precedents. Conflicts between reviewers are recorded, not averaged.

| Name | Decision | Justification |
|---|---|---|
| `expression` | keep | The exact VAP field name for a CEL source string (`validations[].expression`, `variables[].expression`). |
| `patch` (single CEL string) | reshape into a discriminated union `patch{patchType, expression}` | Inferring merge-vs-operations from the result's runtime type is the undiscriminated-union pattern the conventions forbid; MutatingAdmissionPolicy declares `patchType` for the same reason. Arm names are `MergePatch` and `JSONPatch`, not MAP's `ApplyConfiguration`, because Karta's merge is RFC 7386, not schema-aware apply. |
| `patches` | keep (new list) | Plural list of its entry, mirroring MAP's `mutations` and ClusterClass `patches`; ordered, listType=atomic. |
| `patches[].matchConditions[{name, expression}]` | adopt (over `when`) | No core API has a `when` field; `matchConditions` with a required `name` is the upstream named CEL gate and gives addressable failures. Conflict recorded: Kyverno uses `when`, cluster-api uses `enabledIf`; the admissionregistration family this KEP models itself on wins. |
| `replace: true` | rename to `patchStrategy: Merge \| Replace` (default `Merge`) | Conventions prefer enums over booleans for strategies; `replace` collides with the RFC 6902 operation name. Applies to `MergePatch` only. |
| `variables[{name, expression}]` | keep; listType=map keyed by `name` | Byte-for-byte the VAP `Variable` type and the `variables.<name>` access idiom. |
| `byExpression` | keep the arm name; drop `expectedResult`; the expression must evaluate to a boolean | Unanimous across the review panel: every upstream CEL gate is a bare boolean (`MatchCondition`: "must evaluate to bool"). The arm name stays for family symmetry with `byPhase` and `byConditions`. Prototype note: the prototype still compares a stringified result to `expectedResult`. |
| `references` (list and CEL binding) | keep | A named-binding list keyed by `name`, the plural analog of VAP's `params`. Conflict recorded: Gateway API prefers `refs`; the full word is kept because entries carry lookup semantics, not bare pointers. |
| `references[].gvk{group,version,kind}` | flatten to `apiVersion` + `kind` on the entry | No user-facing Kubernetes API has a `gvk` field; `paramKind{apiVersion, kind}` and cluster-api's `PatchSelector` are the precedents. |
| component `kind{group,version,kind}` | flatten to `apiVersion` + `kind` | Same verdict; also removes the `kind.kind` stutter the references design flags as unresolved. |
| `references[].lookup{nameExpression}` | drop the wrapper; `nameExpression` sits on the entry, one-of with `selector` | `paramRef` models the same name-or-selector choice flat; the `nounExpression` suffix follows `messageExpression`. |
| `references[].list` | rename to `selector` | `paramRef.selector` and every core selector field name the selecting thing `selector`; `list` names the consumer's verb. |
| selector shape | `matchLabels` stays `map[string]string` verbatim; expression-sourced values move to a sibling `matchLabelExpressions[{key, expression}]` | Reuse upstream shapes verbatim or diverge loudly, never near-miss: redefining `matchLabels` values would break the most recognized selector shape in Kubernetes. `matchExpressions{key, operator, values}` stays byte-for-byte `metav1.LabelSelectorRequirement`. |
| `scheduling` (was `optimizationInstructions`) | adopt | Consumer-neutral; `RuntimeClass.spec.scheduling` is a shipping core field of this name. |
| `scheduling.podGroups` | rename to `podGroup` (singular, with `subGroups`) | The v1alpha1 code already deprecates the plural in favor of the singular mapping; Kueue and scheduling/v1alpha3 endorse the pod-group vocabulary. See the scheduling section for the full surviving field set. |
| `members[{componentName, groupByExpressions}]` | keep | `componentName` follows the `fooName` string-reference convention (Gateway `sectionName`); `groupByExpressions` follows the plural `*Expressions` suffix. |
| `instanceIds` | rename to `instanceIDs` | Conventions capitalize initialisms (`machineID`, `systemUUID`, `providerIDList`); no `Ids` spelling exists in staging API types. |
| `suspendDefinition{suspendActions, resumeActions}` | keep, deferred | Pre-existing v1alpha1 surface kept under this KEP's shape-preservation rule. The panel flagged the `Definition` suffix and the suspend stutter; recorded in Alternatives as a pre-v1 skeleton-rename KEP. |
| `conditionsDefinition{expression, typeFieldName, ...}` | keep; the four field names default to `type`, `status`, `message`, `reason` | The value each names is a single key, not a path, so `FieldName` is honest; defaults follow `metav1.Condition`. Conflict recorded: two seats preferred a `*Path` suffix. |
| `structureDefinition` / `rootComponent` / `childComponents` | keep, `Definition` suffix deferred with the skeleton-rename KEP | Parent/child vocabulary matches upstream usage. |
| `additionalChildKinds` | keep the name; entries become `{apiVersion, kind}` and the list map key becomes both fields | `additional*` matches `additionalPrinterColumns`; keying by `kind` alone collides across groups, and only a version bump can change a list key. |
| CEL `object` | keep | The exact admission-policy binding. |
| CEL `value` | keep | Reads naturally inside patch construction; no collision. A `self` alternative was rejected: `self` means the scoped object in CRD validation rules, not the incoming write. |
| CEL `instance`, `index` | keep | No upstream analogue (an absence finding). Documented as reserved words; `instance` holds the instance id string, not an object. Renaming them would also break every existing prototype definition for no convention gain. |
| CEL `variables`, `references` | keep | `variables` is the exact VAP binding; `references` distinguishes multiple named bindings from VAP's single `params`. |

## Proposal

### The value accessor

Every field the old API addressed with a `*Path` string becomes a value
accessor:

```yaml
podTemplateSpec:
  expression: object[?"spec"][?"template"].orValue(null)
  patch:
    patchType: MergePatch
    expression: '{"spec": {"template": value}}'
  patchStrategy: Replace
```

- `expression` is CEL, evaluated with the workload bound as `object`.
  Absence is explicit: `[?"key"]` and `.?field` make a missing field a
  value, and `.orValue(...)` names its default.
- `patch` declares its format and constructs the change. `patchType:
  MergePatch` requires the expression to produce a map, applied as a
  JSON merge patch (RFC 7386, null deletes). `patchType: JSONPatch`
  requires an operation list (RFC 6902). Any other result is a typed
  evaluation error; `{}` and `[]` mean no change. The expression sees
  `value` (what Karta is writing), `instance` and `index` (which
  instance of a multi-instance component), and `variables.<name>`.
- `patchStrategy: Replace` evaluates the selected merge patch twice:
  once with `value` bound to null to clear the field, then with the
  real value. It clears only what the null pass actually sets to null;
  it is not whole-object replacement, and it is restricted to
  `MergePatch` (RFC 6902 authors state removals explicitly). Default
  `Merge`. A pod template update wants `Replace`; an annotations merge
  does not.

Prototype note: the prototype has a single string `patch` whose format
is inferred from the result type, a boolean `replace`, and does not
reject scalar results. The union, the enum, and the per-arm result
typing are this KEP's target contract.

### Accessor validity

- A present accessor requires a non-empty `expression`.
- `patch` and `patches` are a one-of. Neither present means read-only;
  writing through a read-only accessor is a loud error, never a guessed
  location. An explicitly empty `patch.expression` or an empty
  `patches` list is invalid.
- In `patches`, an entry without `matchConditions` always matches and
  may appear only last. Every entry requires a non-empty patch
  expression.
- `patchStrategy` is invalid on a read-only accessor.
- `instanceIDs` is read-only by definition: a `patch` on it is
  rejected.

### Conditional patches

Control flow belongs in the API shape, not inside expression strings.
An expression that embeds branching becomes a small language of its
own, which is exactly what this KEP is removing. Conditional writes are
therefore structured, mirroring the match-list idiom `statusMappings`
already uses and the `mutations` list a MutatingAdmissionPolicy carries:

```yaml
podTemplateSpec:
  expression: ...
  patches:
    - matchConditions:
        - name: has-job-template
          expression: object.?spec.?jobTemplate.hasValue()
      patch:
        patchType: MergePatch
        expression: '{"spec": {"jobTemplate": {"spec": {"template": value}}}}'
    - patch:
        patchType: MergePatch
        expression: '{"spec": {"template": value}}'
  patchStrategy: Replace
```

Semantics:

- Entries are evaluated in declaration order against the pre-write
  document, with the same bindings the selected patch will see. All of
  an entry's `matchConditions` must hold (AND); the first matching
  entry supplies the patch. Later conditions and unselected patch
  expressions are not evaluated.
- A condition must return a CEL boolean. `false` advances to the next
  entry. Null, a non-boolean result, an evaluation error, cancellation,
  or a spent cost budget fails the write; errors never mean false.
- No entry matching fails the write loudly: the definition said nothing
  about this document shape.
- Selection happens once, before any `Replace` null pass; the selected
  entry is reused for both passes.
- This first-match list differs from admission `matchConditions`, which
  collectively gate one policy, and from MAP's mutations, which all
  apply in order. The difference is deliberate: exactly one write shape
  is correct per document.

A ternary inside a single `patch` expression remains legal CEL - the
engine cannot prevent it - but it is discouraged beyond one trivial
condition, and the catalog does not use it once `patches` exists.
Suspend and resume entries carry the same optional `matchConditions`
grammar, so a conditional suspend never needs a second grammar or a
breaking change.

Prototype note: `patches`, `matchConditions`, and the no-match error are
not implemented; the prototype's conditional writes are ternaries.

### What a patch is, precisely

CEL only constructs the patch value; applying it is Karta's job, and
the semantics are cited standards, not invented. A `MergePatch` map is
RFC 7386 (`kubectl patch --type merge`); a `JSONPatch` list is RFC 6902
(`kubectl patch --type json`). The relationship to
MutatingAdmissionPolicy is analogous, not identical: MAP's
`applyConfiguration` performs schema-aware structural merging where
keyed lists merge; without a schema, Karta's merge replaces lists
wholesale. Karta's RFC 6902 processing extends `add` by creating
missing map parents; it does not create missing arrays or array
elements.

Write boundaries, stated honestly: patches are constructed against the
pre-write document and applied all-or-nothing with a snapshot rollback.
That rollback covers one in-memory update of one workload document; it
is not a transaction across resources or API requests. Prototype note:
the prototype's suspend and resume action lists apply sequentially -
each entry observes the previous entry's changes, and a mid-list error
leaves earlier entries applied; aligning them with the all-or-nothing
contract is part of implementing this KEP.

### Variables

`spec.variables` names CEL expressions once and makes them available to
every expression and patch as `variables.<name>`:

```yaml
variables:
  - name: specReplicas
    expression: ([dyn(object[?"spec"][?"replicas"].orValue(null))].filter(v, v != null && v != false) + [1])[0]
```

The shape is VAP's `Variable` exactly; the evaluation strategy is not
VAP's, and the difference is stated rather than hidden: resolution is
eager by static mention. Only the variables an expression syntactically
references (transitively) are resolved, in declaration order, later
variables seeing earlier ones; a mentioned variable runs even on a CEL
branch evaluation would not take, and a dynamic access such as
`variables[x]` falls back to resolving all of them. Name rules: unique,
valid CEL identifiers, no shadowing of the reserved bindings (`object`,
`value`, `instance`, `index`, `variables`, `references`). A variable's
lifetime is one evaluation, or the frozen addressing context of one
multi-pass write.

### Expression environments

The CEL environment is API surface. Per field group:

| Field group | `object` is | Extra bindings |
|---|---|---|
| specDefinition, scaleDefinition, statusDefinition, suspendDefinition, variables | the workload manifest | `variables`; patch expressions and conditions also see `value`, `instance`, `index` |
| podSelector, scheduling `groupByExpressions` | one Pod manifest | none |
| references name and selector expressions (follow-on KEP) | the workload manifest | none |

`instance` binds the selected instance id string, or null for a
non-instanced component; `index` binds its zero-based position. During
ordinary reads the write bindings are null. The enabled extension
libraries are CEL optional types plus the list and string extensions
Kubernetes also enables; extending the environment is an API change and
needs a KEP. Every evaluation is charged a per-program cost budget
(1,000,000 cost units on the prototype) and fails fast when the budget is spent -
this bounds one program evaluation, not a whole reconcile, and the
compiled-program cache has no eviction bound. Aggregate budgets and
size limits (expression length, entry counts, constructed-result size)
are deliberately unchosen here and listed as graduation work.

Prototype note: the prototype declares one shared environment for all
field groups, so an out-of-scope binding fails at evaluation rather
than compilation.

### Status matching

Matchers keep their family: `byPhase`, `byConditions`, `byExpression`.
Within one matcher the clauses AND; across a status's matchers they OR.
The precedence order when several statuses match is fixed and now
documented: Resuming, Suspending, Suspended, Running, Failed,
Completed, Initializing, Degraded.

`byExpression` becomes a boolean predicate: the expression must
evaluate to a CEL boolean, enforced at validation, and `expectedResult`
is removed. Prototype note: the prototype compares
`fmt.Sprintf("%v", result)` against the `expectedResult` string, which
lets a boolean `true` and a string `"true"` match the same value; the
catalog already writes boolean predicates with `expectedResult:
"true"`, so its migration is dropping one line per matcher.

### Condition normalization

`conditionsDefinition.expression` reads the condition collection and is
never writable. The `*FieldName` settings name literal keys inside each
condition object, defaulting to `type`, `status`, `message`, `reason`.
A missing, null, or empty collection means no observed conditions.
Extracted types must be non-empty and unique; status is one of `True`,
`False`, `Unknown`; missing optional fields produce no text; a
wrong-shaped collection is an error, not an empty result.

### The CRD, at a high level

```yaml
apiVersion: run.ai/v1alpha2
kind: Karta
metadata:
  name: batch-job-v1
spec:
  variables: [ ... ]
  structureDefinition:
    references: [ ... ]      # non-normative preview; owned by the references KEP
    rootComponent:
      name: job
      apiVersion: batch/v1
      kind: Job
      specDefinition:
        podTemplateSpec:
          expression: ...
          patch: { patchType: MergePatch, expression: ... }
          patchStrategy: Replace
      scaleDefinition:
        replicas: { expression: ... }
      statusDefinition:
        conditionsDefinition: { expression: ..., typeFieldName: type }
        statusMappings:
          running:
            - byExpression: { expression: <boolean CEL> }
      suspendDefinition:
        suspendActions: [ { patch: { patchType: MergePatch, expression: '{"spec": {"suspend": true}}' } } ]
        resumeActions:  [ { patch: { patchType: MergePatch, expression: '{"spec": {"suspend": false}}' } } ]
    childComponents: [ ... ]
  scheduling:
    podGroup: { ... }
```

Removed or changed outright: every `*Path` field (24 across the spec),
`filters`, `groupByKeyPaths`, `expressionLanguage`, the path-and-value
suspend actions, the `gvk`/`kind` wrapper structs (now flat `apiVersion`
+ `kind`), the deprecated `podGroups` plural, and `expectedResult`. The
complete field-by-field mapping ships in the release notes; migration
is reviewable, not blindly mechanical (jq streams, null-versus-false
defaults, and write addressing all require a human decision).

### Collection semantics

`childComponents`, `references`, and variable-like named lists are
list-map keyed by `name`; component names are unique across root and
children; `members` are keyed by `componentName` and must name a
declared component. `additionalChildKinds` is keyed by `(apiVersion,
kind)`. `patches`, suspend and resume actions, `matchConditions`,
`groupByExpressions` (an ordered tuple, not a set), and selector
requirements are ordered atomic lists. The `scheduling` section is a
pointer with omitempty, so definitions without it carry no empty
stanza.

### Decoupling the scheduling section

`optimizationInstructions` was named for one consumer. The semantics it
carries - pod grouping for gang scheduling - are consumed by KAI
Scheduler, Kueue, and Volcano alike. The new version renames the
section to `scheduling` and promotes the model v1alpha1 already
declared as its successor:

- `scheduling.podGroup` (singular) with `subGroups` replaces the
  deprecated `podGroups` plural and drops the `gangScheduling` wrapper.
- The topology fields (`topologyName`, `preferredTopologyLevel`,
  `requiredTopologyLevel`) stay: their values are definition-supplied
  strings, no scheduler CRD is referenced by kind, and the concepts are
  scheduler-neutral. Any field that cannot be stated
  scheduler-agnostically is dropped.
- Grouping keys: each `groupByExpressions` entry evaluates once per
  candidate Pod and contributes one element to an ordered tuple; group
  equality compares tuple elements without concatenation or coercion,
  scoped by workload and group name. An absent list falls back to
  owner-reference grouping. Result types and null handling are settled
  at implementation with the same errors-are-loud rule as everywhere
  else.
- Translating a Karta pod group into a scheduler's own object is the
  consumer's job, exactly like resolving references.

### CEL vs jq, the whole difference

| Concern | jq (v1alpha1) | CEL (v1alpha2) |
|---|---|---|
| Read a field | `.spec.template` | `object[?"spec"][?"template"].orValue(null)` |
| Default | `.status.ready // 0` (also fires on `false`) | `object.?status.?ready.orValue(0)` (absence only) |
| Iterate | `.spec.jobs[].name` (stream) | `object.spec.jobs.map(x, x[?"name"].orValue(null))` (list) |
| Filter and test | `.status.conditions[] \| select(.type == "Ready") \| .status == "True"` | `object.status.conditions.exists(c, c.type == "Ready" && c.status == "True")` |
| Wrong-typed question | `[...] > 0` answers `true` | typed error: no matching overload |
| Write | the read path is the l-value | a declared patch constructs the change |
| Conditional write | not expressible | `patches` with `matchConditions` |
| Compose | `$variables.name` (engine-specific) | `variables.<name>`, the VAP shape |
| Cost | unbounded | compiled once, cached, per-program budget |

The `//` default is the sharpest migration trap: jq falls through on
`false` as well as null. Where a definition relied on that, the CEL
spelling states it:
`([dyn(X.orValue(null))].filter(v, v != null && v != false) + [D])[0]`.

## Examples

The same suspend action, both versions:

```yaml
# v1alpha1
suspendActions:
  - path: .spec.suspend
    value: "true"

# v1alpha2 (target)
suspendActions:
  - patch: { patchType: MergePatch, expression: '{"spec": {"suspend": true}}' }
```

A list-instanced component:

```yaml
# v1alpha1
instanceIdPath: .spec.replicatedJobs[].name
podTemplateSpecPath: .spec.replicatedJobs[].template.spec.template

# v1alpha2 (target)
instanceIDs:
  expression: object.spec.replicatedJobs.map(x, x[?"name"].orValue(null))
podTemplateSpec:
  expression: object.spec.replicatedJobs.map(x, x[?"template"][?"spec"][?"template"].orValue(null))
  patch:
    patchType: JSONPatch
    expression: '[{"op": "add", "path": "/spec/replicatedJobs/" + string(index) + "/template/spec/template", "value": value}]'
```

An instanced accessor returns one outer entry per `instanceIDs` entry,
in the same order; ids are unique non-empty strings. The prototype
catalog under `docs/catalog/` demonstrates the CEL expressions in the
prototype's `v1alpha1` single-patch shape and is the richest expression
reference; target-shape examples are labeled as such above.

## Migration and versioning

CRD versioning:

- `run.ai/v1alpha2` is added as the served and storage version; no
  conversion webhook ships (`conversion.strategy: None`). Alpha-to-alpha
  carries no compatibility promise, which is exactly why the change
  lands now rather than at beta.
- A stored version cannot simply vanish. The documented operator
  procedure: export existing definitions; pause definition writers and
  Karta consumers; apply the transitional CRD (v1alpha1 served,
  v1alpha2 storage); apply reviewed v1alpha2 rewrites of every
  definition; verify nothing remains stored as v1alpha1; remove
  v1alpha1 from `status.storedVersions` and from served versions;
  restart compatible consumers. Rollback is the same procedure in
  reverse using the exported originals.
- The CRD, catalog, admission validation, and consuming binaries roll
  out as a compatible set; old-language consumers and new-language
  definitions are not supported concurrently against one definition
  store.

Definitions: every path field maps to an accessor per the table and
examples above; the built-in catalog ships converted, and
`hack/karta-verify` validates a rewritten definition against a real
manifest before it is applied. Safety net: the recorded fixtures under
`test/e2e/recorded_data/` replay every captured operator state through
the engine offline, so a conversion mistake in the catalog fails a
test, not a cluster - regression evidence for recorded states, not
proof of the unimplemented target pieces.

## Validation stages

Three distinct stages, never conflated:

- Structural validation (CRD schema plus `KartaValidator`): required
  fields, the one-ofs, list keys, name rules, the boolean-predicate
  markers.
- Compilation: every `expression`, patch expression, and condition
  compiles against its declared environment at admission time. This is
  part of the target design; the prototype does not compile expressions
  at validation, so compile errors currently surface on first
  evaluation.
- Evaluation: dynamically-typed workload fields, result types, cost,
  and constructed patches are checked at run time. Diagnostics name the
  definition, component, field path, and entry or instance index,
  without embedding workload contents.

## Test plan

- Unit: absence versus null versus false versus empty; the accessor
  one-of matrix; first-match selection including error-is-not-false
  guards; `patchStrategy: Replace` two-pass behavior; RFC 6902 parent
  creation and pointer escaping; instance alignment and ordering;
  rollback boundaries; variable dependency selection and shadowing
  rejection.
- Integration: the generated v1alpha2 schema, defaults, list keys, and
  validation with and without admission compilation; definitions loaded
  from the API server and from raw YAML behave identically.
- Migration: start from stored v1alpha1 definitions, run the documented
  procedure, verify data preservation and `storedVersions` retirement.
- Catalog: every definition converted, replayed against the recorded
  fixtures, and spot-verified live (CronJob nested template and
  suspend; JobSet instance-to-template alignment and grouping).
- Results recorded with the tested commit and command.

## Enablement and compatibility

There is no per-definition language switch and no feature gate: the
language is selected by the API version of the definition. Disabling
CEL means rolling back the version migration, not flipping a field.
Minimum supported Kubernetes and consumer versions are recorded before
beta. Go source compatibility covers callers of the factory, accessor,
component, and tree interfaces; importers of the versioned API package
must move to `v1alpha2`.

## Graduation criteria

- v1alpha2 (this KEP): the target schema and semantics above
  implemented and validated; the catalog converted; the migration
  procedure demonstrated on a real cluster; the naming table applied.
- v1beta1: at least one release of v1alpha2 feedback from at least two
  independent consumers; measured limits (aggregate budgets, size
  caps) chosen and enforced; no unresolved correctness or data-loss
  issues; a decided conversion story for the beta bump.
- v1: two beta releases of feedback and a frozen surface.

KEP status moves to `implemented` only when the scoped work is merged
and released.

## User stories

- A catalog author defines a CronJob's reads and writes independently:
  the read tolerates a missing `jobTemplate`, the write replaces the
  nested template wholesale, suspend and resume are two one-line merge
  patches.
- A platform consumer updates one JobSet instance's template without
  touching its siblings, addressed by `index`, all-or-nothing.
- An operator migrates a cluster's stored definitions to v1alpha2 with
  the documented procedure and a verified way back.

## Drawbacks

- Definitions get longer: explicit absence handling and separate write
  declarations cost lines that jq paths did not.
- Reads and writes can disagree; nothing forces an accessor's
  expression and patch to address the same field, and only review and
  replay catch a mismatch.
- Indexed JSONPatch writes depend on stable instance ordering.
- Compiled programs and constructed patches consume memory beyond the
  per-evaluation cost budget; the cache is unbounded until the beta
  limits land.

## Risks and mitigations

- A definition is trusted configuration that controls consumer
  behavior; admission validation and the verify tool gate what enters
  the store.
- A hot reconcile loop can hit the per-evaluation budget; the failure
  is a typed, named error rather than a stall, and budgets become
  tunable at beta.
- A constructed patch can be one the workload's controller fights;
  recording and replay make the write's effect observable before it
  ships in a definition.
- Diagnostics separate schema, compilation, evaluation, patch
  application, and (later) reference resolution failures, so an
  on-call reader knows which layer to look at.

## Alternatives considered

- Keep jq. Rejected: untyped total ordering produces silently wrong
  statuses, the path-as-l-value model blocks references, and jq is not
  the language the ecosystem reviews in.
- Support both engines behind `spec.expressionLanguage`. Rejected after
  being built: every definition doubles its verification surface, the
  API grows a mode switch forever, and the engines disagree exactly in
  the corners that matter (defaults on `false`, list typing).
- Conditional writes through CEL ternaries only. Rejected: a nested
  ternary is control flow smuggled into a string - a mini-language on
  top of CEL - and stops reading at three branches. `patches` with
  `matchConditions` adds no new grammar the ecosystem lacks.
- A single untyped `patch` string with result-type dispatch (the
  prototype's shape). Rejected for the target API: inferring semantics
  from a value's runtime type is the undiscriminated-union pattern the
  conventions warn against; MAP's `patchType` is the precedent.
- `when` (Kyverno) or `enabledIf` (cluster-api) for the condition
  field. Rejected in favor of `matchConditions`: the named-entry shape
  is the admissionregistration precedent this KEP models itself on and
  gives addressable failure messages.
- Renaming `suspendDefinition`, `structureDefinition`, and the
  `Definition` suffix family now. Deferred to a dedicated pre-v1
  skeleton-rename KEP so this KEP's diff stays reviewable; recorded
  here so the debt is visible.
- A boolean `replace` (the prototype's shape). Rejected: conventions
  prefer strategy enums, and the word collides with the RFC 6902
  operation.
- Keeping `optimizationInstructions` through the version bump.
  Rejected: a breaking release is the one cheap moment to remove a
  consumer-specific name from the API.

## Future work

- Resource references as their own KEP: `references[{name, apiVersion,
  kind, nameExpression | selector}]` with the selector split decided
  here, a `notFoundAction` enum per `paramRef`, and the reader
  interface; namespace rules, freshness, and authorization behavior are
  owned there.
- Declaring `variables.<name>` and `references.<name>` per definition
  in the CEL environment, the way VAP gates `params` on `paramKind`, so
  an undeclared name fails at validation rather than evaluation.
- Per-context CEL environments so out-of-scope bindings fail at
  compilation.
- The skeleton-rename KEP for the `Definition` suffix family.

## Implementation history

- 2026-09-08: CEL engine, accessors, catalog, and docs prototyped on
  the `cel-native` branch; recorded fixtures replay green.
- 2026-09-09: references prototyped on top (`cel-references` branch).
- 2026-09-10: KEP written; status `implementable`. Reviewed against
  Kubernetes API conventions by a multi-reviewer naming and gap audit;
  the naming table, the discriminated patch union, boolean status
  predicates, `matchConditions`, `patchStrategy`, the `podGroup`
  singular, and the v1alpha2 versioning and migration sections came out
  of that review. None of the target-only pieces are implemented; see
  Implementation status.
