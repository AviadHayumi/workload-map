<!--
SPDX-License-Identifier: Apache-2.0
Copyright (c) 2026 NVIDIA Corporation
-->

# KEP-0002: Resource references

- Status: implementable
- Authors: @AviadHayumi
- Created: 2026-09-14
- Depends on: [KEP-0001](../0001-cel-expressions/README.md) (CEL expressions)
- Tracking issue: to be opened before this KEP merges

## Summary

Some workloads keep half their spec in another object. A Kubeflow Trainer v2
TrainJob holds only overrides; the base pod template lives in the
ClusterTrainingRuntime its `runtimeRef` names. A definition that only sees the
workload cannot answer "what is the image" for such a kind.

This KEP lets a definition declare references: named pointers to other cluster
objects, resolved by name or by label selector, exposed to expressions as
`references.<name>`. The definition declares what it needs; it never fetches.
Consumers feed the data through one of four doors: a Kubernetes client behind
a shipped adapter, a set of plain objects, a query plan the consumer executes
with any client, or fully pre-resolved values. All four end in identical
bindings.

![one definition, four doors](kep-refs-doors.png)

Diagram sources sit next to this file as `.excalidraw`; open them at
excalidraw.com to edit.

## Motivation

The catalog's TrainJob definition needs three values that do not live in the
TrainJob: the trainer image, the per-node resources, and the node count
default. All three live in the runtime object. Without references the
definition has two bad options: return null and push the join onto every
consumer, or hardcode runtime conventions into consumer code, which is exactly
the per-CRD adapter this project exists to remove.

The same shape repeats across the ecosystem: admission policies join their
params objects, composition functions join required resources, policy engines
join the objects their rules mention. The pattern has a settled grain, and
this KEP follows it: declarations stay in the document, fetching stays with
the host.

### Goals

- A definition declares references in its CRD: which kind, which one (a name
  recipe) or which ones (a selector), nothing else.
- Expressions read a reference like any other data, with explicit absence
  semantics.
- Karta core gains no Kubernetes client. The module keeps depending on
  apimachinery only, and keeps compiling to wasm.
- A consumer with a controller-runtime client wires in with one line.
- A consumer with no cluster at all (CLI verify, recorded replay, a wasm
  host) feeds the same definitions without importing a client.
- A consumer who wants to fetch personally gets the definition's needs as
  data: concrete queries out, checked answers in.
- Reads that do not mention references stay free: a status-only pass makes
  zero fetches.

### Non-goals

- Writing through a reference. Patches target the workload document only; a
  referenced object is read-only input.
- Reference chains: a reference whose recipe reads another reference's
  content. Ruled out by construction in this KEP; if ever needed it arrives
  as a separately designed protocol.
- Watching references for changes, retry policy, or cache lifecycle. Those
  belong to the consumer process, not to a library call.
- Cross-namespace reads. A reference resolves in the workload's own
  namespace, always.

## Proposal part 1: declaring a reference

A definition lists its references under `structureDefinition.references`.
Each entry answers three questions: what kind, which object or objects, and
what name expressions use for it.

```yaml
spec:
  structureDefinition:
    references:

      # one object, found by name
      - name: trainingRuntime
        gvk:
          group: trainer.kubeflow.org
          version: v1alpha1
          kind: ClusterTrainingRuntime
        lookup:
          nameExpression: object.spec.runtimeRef.name

      # a set of objects, found by a structured selector
      - name: pods
        gvk: {group: "", version: v1, kind: Pod}
        list:
          matchLabels:
            jobset.sigs.k8s.io/jobset-name:
              expression: object.metadata.name
          matchExpressions:
            - key: batch.kubernetes.io/job-name
              operator: Exists
            - key: trainer.kubeflow.org/trainjob-ancestor-step
              operator: In
              values:
                - value: trainer
```

The rules, all enforced at admission:

- `name` is the identifier expressions use, as `references.<name>`. It must
  be a valid CEL identifier, unique across the list, and may not shadow a
  reserved word (`object`, `value`, `instance`, `index`, `variables`,
  `references`).
- Exactly one of `lookup` or `list` is set.
- `lookup.nameExpression` is a CEL expression over the workload that must
  return a non-empty string: the referenced object's name.
- `list` maps onto a standard Kubernetes label selector. `matchLabels` values
  and `matchExpressions` values each take one of `value` (a literal) or
  `expression` (CEL over the workload). Operators are the selector's own:
  `In`, `NotIn`, `Exists`, `DoesNotExist`. At least one of `matchLabels` or
  `matchExpressions` is set.
- There is no namespace field, on purpose. A namespaced reference resolves in
  the workload's own namespace; a cluster-scoped kind ignores it. A
  definition can never reach outside the namespace its workload lives in.

Everything in the block is a recipe, not an answer. `nameExpression` does not
say a name; it says where the name is found on each concrete workload. One
definition serves every TrainJob, each pointing at its own runtime.

Just as deliberate is what the block cannot say: no fetch strategy, no cache
or consistency hints, no refresh intervals, no endpoint or API-call entries.
A definition is portable data; the same file must mean the same thing against
a live cluster, a recording, and an in-memory store. A mechanics field is
meaningful to at most one of those and dead weight or a lie for the rest.
Kyverno's policies carry in-document API calls (URL path, method, POST body),
and the cost is instructive: path sanitizing, response size caps, an HTTP
mock universe for offline runs, and staleness documented as a known
limitation ([Kyverno external data sources](https://kyverno.io/docs/policy-types/cluster-policy/external-data-sources/)).
Kyverno's own later designs moved mechanics out of the document. Karta starts
where they ended up.

## Proposal part 2: how expressions read a reference

A resolved reference is one more bound name, next to `object` and
`variables`. The catalog's TrainJob image, verbatim:

```yaml
specDefinition:
  fragmentedPodSpecDefinition:
    image:
      expression: >-
        object.?spec.?trainer.?image.orValue(
          references.trainingRuntime.spec.template.spec.replicatedJobs
            .filter(j, j.?template.?metadata.?labels
              ["trainer.kubeflow.org/trainjob-ancestor-step"]
              .orValue("") == "trainer")[0]
            .template.spec.template.spec.containers
            .filter(c, c[?"name"].orValue("") == "node")[0]
            [?"image"].orValue(null))
```

The override wins when set; otherwise the expression walks into the runtime,
picks the trainer job by its label and the container by its name, and takes
its image. No new syntax: the reference is a document, CEL reads documents.

![one lookup, end to end](kep-refs-lookup.png)

Absence semantics, precisely:

- A lookup that found nothing stays unbound. Plain access
  (`references.trainingRuntime.spec`) fails naming the reference; optional
  access (`references.?trainingRuntime`) lets the expression supply a
  default. Not found is data, not an error.
- A list that matched nothing binds `[]`, never null: comprehensions over an
  empty match work unchanged.
- A reference nobody declared is a compile-time unknown; a declared reference
  the consumer cannot serve fails only the expressions that read it, with the
  reference's name in the error.
- List order is deterministic: sorted by namespace then name, so recordings,
  live runs, and in-memory data bind byte-identical values.

Resolution is lazy and memoized. No expression mentions `references.` in a
read: no fetch happens. The first expression that does triggers resolution of
every declared reference, once; every later read in the same factory reuses
the same values. One factory therefore sees one coherent picture. The frame
rule follows: a factory is one evaluation snapshot over one workload
observation. A new observation means a new factory; nothing refreshes a bound
reference in place.

![laziness and the frame rule](kep-refs-lazy.png)

## Proposal part 3: feeding the data, four doors

Karta core owns the reading contract but no client:

```go
// ListQuery grows by fields, never variadic options.
type ListQuery struct {
    Namespace string
    Selector  labels.Selector
}

type ResourceReader interface {
    Get(ctx context.Context, gvk schema.GroupVersionKind,
        namespace, name string) (*unstructured.Unstructured, error)
    List(ctx context.Context, gvk schema.GroupVersionKind,
        query ListQuery) ([]unstructured.Unstructured, error)
}

var ErrNotFound = errors.New("resource not found")
var ErrPermissionDenied = errors.New("resource read denied")

// PermissionChecker is optional. When the reader implements it, resolution
// probes before fetching and a denial names the reference and the verb.
type PermissionChecker interface {
    CheckRead(ctx context.Context, gvk schema.GroupVersionKind,
        namespace, verb string) error
}
```

Two methods over unstructured, plain arguments, sentinel errors. This is the
same read surface controller-runtime itself settled on
([client.Reader](https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/client/interfaces.go)),
without importing it: implementing the ecosystem interface requires typed
out-params, scheme plumbing, and fabricated status errors, which is exactly
the weight a no-cluster implementer should not carry.

The factory keeps two options, and the four doors compose them:

```go
// exactly one of the two
func WithReferenceReader(reader references.ResourceReader) FactoryOption
func WithReferences(resolved references.ResolvedReferences) FactoryOption
```

### Door 1: a client, behind the shipped adapter

A separate Go module, `adapters/controllerruntime`, so the core module and
every non-cluster consumer stay client-free:

```go
import ctrlreader "github.com/run-ai/karta/adapters/controllerruntime"

reader := ctrlreader.FromReader(mgr.GetClient())      // cache-backed
reader  = ctrlreader.FromReader(mgr.GetAPIReader())   // direct reads
reader  = ctrlreader.FromClientWithAccessReviews(c)   // plus RBAC preflight

factory := resource.NewComponentFactoryFromObject(trainJobKarta, trainJob,
    resource.WithReferenceReader(reader))
```

Cache or live is the consumer's choice of client; the seam does not know.
The adapter owns the two translations where adapter bugs live: not-found to
`ErrNotFound`, forbidden to `ErrPermissionDenied` (dual-wrapped, so both
`errors.Is(err, ErrPermissionDenied)` and `apierrors.IsForbidden(err)` hold).
The access-review variant answers denials with the reference name and verb
before fetching; it is best-effort, since a SelfSubjectAccessReview cannot
model name-scoped RBAC rules, and the real API answer stays authoritative.

This is the headline door in the docs: it is the only lazy one. A controller
that polls status all day never fetches a reference it does not read.

### Door 2: the objects, no cluster

```go
reader := references.NewObjectsReader(objects...)
```

An in-memory reader over plain unstructured objects, shipped in core. The
recorded replay tests, the CLI verify path, and a wasm guest all use it. It
exercises the full resolution machinery: the name recipe still evaluates, the
lookup still happens, a wrong `nameExpression` still misses. Resolution
logic stays testable without a cluster, which is the point.

### Door 3: the plan, any client

The definition can hand the consumer its needs as data. Three pure functions
in core, no new factory option, no CRD change:

```go
// Query is one concrete fetch. Exactly one of Name or Selector is set.
// Namespace is the workload's own; a cluster-scoped target ignores it.
type Query struct {
    Reference string
    GVK       schema.GroupVersionKind
    Namespace string
    Name      string
    Selector  labels.Selector
}

// QueryResult answers one query. A get that found nothing answers with a
// nil Object: not found is an explicit answer, never an omission.
type QueryResult struct {
    Reference string
    Object    *unstructured.Unstructured
    Items     []unstructured.Unstructured
}

// Plan evaluates every declared recipe against the workload and returns
// the concrete queries, in declaration order. It fetches nothing.
func Plan(ctx context.Context, karta *v1alpha1.Karta,
    workload any) ([]Query, error)

// Fulfill matches results to the plan and returns the values
// WithReferences accepts, sorted and bound exactly as the reader path
// binds them.
func Fulfill(plan []Query, results []QueryResult) (ResolvedReferences, error)

// ReferencedKinds returns the distinct GVKs a definition may read, with no
// workload: enough to set up watches and RBAC ahead of time.
func ReferencedKinds(karta *v1alpha1.Karta) []schema.GroupVersionKind
```

For a TrainJob named `bert` in `team-a` pointing at `karta-busybox`, the plan
comes out as data any client can execute:

```yaml
- reference: trainingRuntime
  gvk: trainer.kubeflow.org/v1alpha1 ClusterTrainingRuntime
  namespace: team-a        # cluster-scoped target ignores it
  name: karta-busybox
- reference: pods
  gvk: v1 Pod
  namespace: team-a
  selector: jobset.sigs.k8s.io/jobset-name=bert,
            batch.kubernetes.io/job-name exists,
            trainer.kubeflow.org/trainjob-ancestor-step in (trainer)
```

The consumer fetches however it likes and returns the answers. `Fulfill` is
strict, and every failure is loud and named:

| the consumer's answers | what happens |
|---|---|
| a planned query has no answer | `ErrIncompletePlan`, naming the reference |
| a lookup answered with a nil object | fine: an explicit miss, binds unbound |
| a list answered with empty items | fine: binds `[]` |
| an answer no query asked for, or a duplicate | `ErrUnplannedResult` |
| wrong GVK, wrong name, or a list member outside the selector | `ErrWrongObject` |

The distinction in the first two rows is the whole safety story: "I forgot to
look" and "I looked and it is not there" must never be confused. Without the
check, both are an empty value and the definition silently computes wrong
answers. Over-fetching during acquisition is fine; the consumer partitions
its haul into exact answers before `Fulfill`, and extra members inside a
claimed answer are rejected rather than silently narrowed, so a dropped
selector term is visible.

One-shot is an API invariant, not a current limitation: recipes read the
workload alone, so no fetched value can ever extend the plan. Crossplane's
composition functions need bounded iterative rounds precisely because their
requirements grow from fetched data
([run_function.proto](https://github.com/crossplane/crossplane/blob/main/proto/fn/v1/run_function.proto));
Karta takes the requirements idea and drops the loop, because its recipes
cannot do that by construction.

![the plan door](kep-refs-plan.png)

The trade is stated plainly in the docs: planning is fetch-free, but the
acquisition it drives is eager. A status-polling controller belongs on door 1.

### Door 4: finished values, trusted

```go
factory := resource.NewComponentFactoryFromObject(karta, workload,
    resource.WithReferences(resolved))
```

The escape hatch for values that never came from queries: a batch pipeline
with its own fetch layer, a host that resolved on its side of a boundary.
No checks, no fetches, documented plainly as skipping both, with a pointer
to `Fulfill` for the checked path.

### One pipeline, no drift

Internally there is exactly one resolution pipeline:

```text
Resolve(ctx, reader, karta, workload)
    = Plan(ctx, karta, workload)      # compute the queries
    + execute them through the reader # the only fetch
    + bind the results                # shared with Fulfill
```

The reader path and the plan path share the compiler and the binder, so the
two can never disagree about what a declaration means. A conformance suite
(`referencestest.Conformance`) runs the same scenario set over every reader:
hit, miss, namespace scoping including the empty-namespace rule,
cluster-scoped kinds, selector matching, denial classification, list order.
The shipped adapter, the objects reader, and any consumer's own reader are
provably interchangeable, not conventionally so.

## Prior art

The declaration shape and the host split follow settled upstream patterns:

- ValidatingAdmissionPolicy params: the policy declares `paramKind`, a
  binding declares `paramRef` with name XOR selector, and the plugin owns all
  fetching; the CEL layer receives resolved values only
  ([ValidatingAdmissionPolicy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/)).
  Karta's lookup XOR list mirrors the shape, and `parameterNotFoundAction`
  is the model for a possible future per-reference not-found knob.
- Crossplane composition functions: requirements returned as data, host
  fetches, function computes; the origin of the plan door, minus the
  iteration Karta does not need
  ([run_function.proto](https://github.com/crossplane/crossplane/blob/main/proto/fn/v1/run_function.proto)).
- Gatekeeper: referential data declared in sync configs, replicated by the
  host, evaluated offline by the CLI over user-supplied data
  ([replicating data](https://github.com/open-policy-agent/gatekeeper/blob/master/website/docs/sync.md)).
  Also the cautionary half: sync declarations that only a linter reads drift;
  Karta's declarations are enforced by the engine itself.
- Kyverno: the counter-example for in-document fetch mechanics, discussed in
  part 1
  ([external data sources](https://kyverno.io/docs/policy-types/cluster-policy/external-data-sources/)).
- controller-runtime: the two-method read surface Karta's reader mirrors
  without importing
  ([interfaces.go](https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/client/interfaces.go)).

## Consumer walkthroughs

A controller reconciling TrainJobs:

```go
reader := ctrlreader.FromReader(mgr.GetClient())
factory := resource.NewComponentFactoryFromObject(trainJobKarta, trainJob,
    resource.WithReferenceReader(reader))
root, _ := factory.GetRootComponent()

status, _ := root.GetStatus(ctx)          // zero fetches
spec, _ := root.GetFragmentedPodSpec(ctx) // one Get, memoized
spec["trainjob"].Image                    // "busybox:1.36", from the runtime
```

Recorded replay, same engine, no cluster:

```go
reader := references.NewObjectsReader(recording.References()...)
factory := resource.NewComponentFactoryFromObject(trainJobKarta, recordedJob,
    resource.WithReferenceReader(reader))
```

A consumer that fetches personally, any client at all:

```go
queries, _ := references.Plan(ctx, trainJobKarta, trainJob)
results := fetchWithWhateverYouHave(queries)
resolved, _ := references.Fulfill(queries, results)
factory := resource.NewComponentFactoryFromObject(trainJobKarta, trainJob,
    resource.WithReferences(resolved))
```

A wasm host: `ReferencedKinds` tells the host what it may need,
the host fetches over its bridge, the guest binds through `Fulfill` or wraps
the objects with `NewObjectsReader`.

Setting up ahead of time, no workload yet:

```go
for _, gvk := range references.ReferencedKinds(trainJobKarta) {
    // add a watch, precompute an RBAC rule
}
```

## Migration and versioning

References are additive. They ship in the same `run.ai/v1alpha2` version
KEP-0001 introduces; a definition without a `references` block is unchanged,
and every existing consumer keeps working without touching the new options.
There is no conversion and no storage change beyond KEP-0001's.

Consumers adopt at their own pace: a controller adds one
`WithReferenceReader` line only when it starts using a definition that
declares references. A consumer that passes nothing still works until an
expression actually reads a reference; that read fails with a sentinel
saying the factory was built without a reference source.

## Validation

- Admission: reference names are unique CEL identifiers off the reserved
  list; exactly one of lookup or list; every expression compiles against the
  workload-only environment (a recipe naming `references.` is rejected,
  which is what keeps plans one-shot); label values set exactly one of value
  or expression; selector operators and value counts follow the Kubernetes
  selector rules.
- Definition validation additionally checks that `references.<name>` used
  anywhere in the definition names a declared reference, so a typo fails
  when the definition is applied rather than at first evaluation.
- Run time: a failed recipe names its reference; a denied read names the
  reference and verb; list results are verified sorted.

## Test plan

- Unit: recipe evaluation (empty name, non-string result), selector
  building across all four operators and both value sources, miss and empty
  bindings, deterministic order, plan and fulfill for every row of the
  answer table, reserved-name and one-of validation.
- Conformance: the suite over the shipped adapter (against a fake client),
  the objects reader, and the replay reader; equivalence between
  reader-resolved and plan-fulfilled bindings on the same inputs.
- Live e2e: the TrainJob flow against a real cluster: effective image and
  resources through the runtime, zero fetches on a status-only pass, a
  denial naming the reference, suspend and resume unaffected.
- Replay: recorded trainer lifecycles replay byte-identical to the live
  run's observed statuses.

## Risks and mitigations

- Stale joins. The workload and a reference are two reads; no Kubernetes API
  makes them one instant. Mitigation is the frame rule plus level-based
  reconciliation: one factory per observation, converge on the next event.
- An under-fetching plan consumer. Mitigated by `Fulfill`: a hole is a loud
  error, never an empty value; misses must be said explicitly.
- Recorded data that silently lacks a reference. The objects reader answers
  what it holds; a recording made before a definition declared a new
  reference replays as a miss. Recordings carry their captured reference
  set, and the replay suite fails when a declared reference has no capture.
- Permission preflight is best-effort. SelfSubjectAccessReview cannot see
  name-scoped rules, so the preflight can claim allowed for a read the API
  then denies. The API answer is authoritative; the preflight only improves
  the error text.
- Fan-out on list references. A wide selector is the definition author's
  choice and visible in review; the resolver adds no pagination of its own,
  and a reader may cap results as its environment requires.

## Alternatives considered

- controller-runtime `client.Reader` as the seam, adapter-free. One import
  line cheaper for controllers, and it drags client-go into the CLI, the
  replay tests, and the wasm build, while forcing no-cluster implementers to
  fabricate typed status errors. Rejected; the adapter closes the gap to one
  constructor call.
- Iterative requirements rounds. Needed only when fetched data can extend
  the requirements; Karta's recipes cannot, by validation. Rejected as
  machinery without a driver.
- Fetch mechanics in the CRD (strategy, cache hints, API-call entries).
  Rejected for portability and for handing definition authors a load lever
  over consumer credentials; the Kyverno experience is the cautionary tale.
- A namespace field on references. Rejected: the workload's namespace is the
  boundary, and a definition that can name other namespaces is a definition
  that can read them.
- One merged Get-or-List method with a query union. Rejected: two methods
  with plain arguments are easier to implement correctly, and the union type
  reintroduces the nil-versus-empty ambiguity the sentinels exist to kill.
- An unchecked `Plan() + WithReferences` convenience. Rejected: incomplete
  fetching must not look like success; `Fulfill` exists exactly for that.

## Future work

- A per-reference not-found action, the `parameterNotFoundAction` shape, if
  catalog experience shows misses that should fail fast instead of binding
  unbound.
- Field selectors on list references, as a new `ListQuery` field flowing
  through the same compiler into `Query`.
- A dependency protocol for reference chains, only if a real definition
  needs one; it will not arrive by loosening the workload-only rule quietly.
- Watch helpers built on `ReferencedKinds` for consumers that want cache
  warm-up before the first reconcile.

## Implementation history

- 2026-09-12: reference declarations, resolver, recorder support, and the
  live TrainJob flow prototyped; recorded fixtures replay green.
- 2026-09-14: this KEP; status `implementable`. The plan door and the
  conformance suite are proposal only.
