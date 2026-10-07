---
name: add-workload-type
description: >-
  Author and validate a Karta definition that teaches Karta a new Kubernetes
  workload type. Use when a user wants to add, register, onboard, or support a
  workload framework or CRD in Karta (for example an Argo Workflow, a Volcano
  Job, a SparkApplication, or any custom operator), or to write, fix, or review
  a Karta YAML that maps a workload's status, pod template, and scale. Covers
  choosing the closest sample, picking the correct specDefinition pattern,
  writing null-safe jq paths, mapping real conditions or phases to Karta
  statuses, and self-checking against the validator. Not for consuming an
  existing definition from Go code or operating a live cluster.
license: Apache-2.0
---
<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Add a workload type to Karta

A Karta definition describes one Kubernetes workload type as a tree of
components. This skill authors a definition, validates it, and ships it with a
recorded flow.

Every path is a jq expression. `specDefinition`, `scaleDefinition`, and
`statusDefinition` paths run against the workload object. `podSelector` and
`optimizationInstructions` paths run against pod manifests. Mixing these up is
the most common mistake, so keep it in mind throughout.

Every rule in the steps is a requirement. The reference files hold the why,
the examples, and the operator notes.

## Bundled references

Load these as needed. Do not guess field names or rules; confirm them here.

- `reference/technical-guide.md` - schema cheatsheet, status mapping patterns,
  karta-verify runs, and the checklist.
- `reference/sample-index.md` - workload shape to closest `docs/catalog/`
  sample, and what not to copy. Start here in step 2.
- `reference/recorded-flow.md` - reading the operator source, and the step 8
  install, flow, recording, and presubmit detail.
- `reference/troubleshooting.md` - every error and symptom mapped to its fix.
- `hack/karta-verify/` - the offline harness for steps 6 and 7.

## Workflow

### 1. Gather the target facts first

Do not write anything until these facts are known, from the target CRD source
or documentation.

Inputs. Ask the user for them when there is a user and a cluster; otherwise use
the offline sources.

- The CRD schema (`kubectl get crd <name> -o yaml`, or the operator's API
  types).
- At least one real CR (`kubectl get <kind> <name> -o yaml`), ideally one
  running and one finished. Optional, but only step 7 on a real CR proves a
  path resolves.

Sources:

- Offline: the release manifest or chart CRDs, the API types (`pkg/apis/`,
  `api/`), and the controller code that writes status. Grep it for the
  condition type constants and phase setters (`markRunning`, `markCompleted`,
  `SetPhase`). When the phase is computed from several inputs, grep for the
  function that returns it (`func Calculate.*Phase`) and copy its order of
  checks into the mapping comments.
- Confirm condition and label names in a shallow clone at the pinned tag
  (`git clone --depth 1 --branch <tag>`), not from documentation alone. The
  controller code that assigns the phase or conditions is the source of truth
  for step 5, not the CRD enum.
- Pin the newest non-prerelease tag
  (`git ls-remote --tags --refs <repo-url> | sort -V -k2`); it becomes
  `<NAME>_VERSION` in step 8. A Helm-installed operator pins the chart version:
  check that its `appVersion` is the tag the source was read at, and note any
  difference next to the pin.
- An operator already in `hack/e2e/global.env`: read the source at that tag and
  leave the pin alone. A bump re-records every flow of that operator.
- A checkout on another branch: `git fetch --depth 1 origin tag <tag>`, then
  `git show <tag>:<path>`, not the working tree.
- A Kubernetes builtin (apps, batch, core): do not clone kubernetes/kubernetes.
  Read the status type from the `k8s.io/api` module cache. Fetch the controller
  and its `util/` counter helpers at the Kubernetes version of
  `KIND_NODE_IMAGE`, and grep it for `Status.Conditions` before mapping any
  condition.

Proceed without a CR if there is none; the definition is then validated, not
exercised. For a catalog definition, take the CR from the step 8 cluster: do
the operator install, export the cluster's kubeconfig (Record, step 8), run
`make e2e-up CLUSTER_NAME=<name> WORKLOADS=<operator>` (`none` for a builtin),
`kubectl apply` a running manifest, and save `kubectl get <kind> <name> -o yaml`
and one pod with `kubectl get pod <pod> -o json`. Delete the hand-applied
object before `make record-e2e`.

Establish:

- The full GVK. All three parts are required; only core `Pod` may omit the
  group.
- The real statuses: exact condition types with their status and reason
  values, or the phase strings, as the controller sets them. Never invent one.
  A condition named by documentation, a user, or a task brief that the
  controller never sets (a `Running` condition on a CRD that only writes
  `Complete` and `Failed`) is dropped: derive the state from fields the
  controller writes, and say so in the builder comment and the final answer.
- Where the pod template lives, and one role or several. Take the role list
  from the controller loop that walks the roles, not the documentation. It
  includes deprecated aliases, whose pods need a component too.
- How replicas are expressed, if at all.

Detail: Reading the operator source in `reference/recorded-flow.md`.

### 2. Start from the closest sample

Find the row for the workload shape in `reference/sample-index.md` and copy
that definition from `docs/catalog/` as the skeleton. Change the GVK, the
paths, and the status mapping. Copy nothing its Do not copy table names.

### 3. Pick one specDefinition pattern per component

Set exactly one per component:

- `podTemplateSpecPath` for an embedded PodTemplateSpec (a Job's
  `.spec.template`).
- `podSpecPath`, with optional `metadataPath`, for a bare PodSpec.
- `fragmentedPodSpecDefinition` for scattered pod fields. List only paths that
  exist. Each must be assignable, since it is written too; a `//` fallback
  breaks on write. Model a default plus an override as a multi-instance
  component, not a fallback. When an optional full template sits next to the
  scattered fields, use the scattered fields and say in the builder comment
  that the template is not read.

A component may have no spec definition when it only models ownership or scale.

Multi-instance:

- A component whose spec or scale paths can return more than one value, or
  zero, needs `instanceIdPath` plus `componentInstanceSelector`. Otherwise the
  build fails with `instance ids count (1) does not match results count (N)`,
  which a one-entry CR hides.
- Roles that are optional map keys (`.spec.tfReplicaSpecs`): one
  multi-instance child keyed by the map, never a fixed child per role.
- `keys_unsorted` passes the validator but fails at runtime. Check an
  `ascii_downcase` id against the real pod label. The jq CLI walks `.[]` in
  insertion order where Karta sorts, so sort the input
  (`jq -S . cr.json | jq '<expr>'`) or check alignment with karta-verify.

Detail: Spec definitions, Read-only projections, Multi-instance components in
`reference/technical-guide.md`.

### 4. Write null-safe jq paths against the correct resource

- Absolute paths, starting with `.`.
- In status expressions, default every field that can be absent:
  `(.status.active // 0)`.
- Spec and scale paths stay plain (`.spec.parallelism`,
  `.spec.tasks[].replicas`), even for `omitempty` fields, so they can be
  written. Never put a default after an iterator. An absent field reads null
  (`replicas=<none>`, accepted by `--strict`): leave `replicas` out of that
  prediction and name the controller default in the builder comment. A count
  derived from several fields stays a read-only formula.
- No assignment or update operators, `del`, `..`, `range`, `paths`, `recurse`,
  `walk`, or `repeat`. Read-only navigation and standard builtins only.
- Test each path with jq against a real manifest before committing it:
  `kubectl get <resource> -o json | jq '<expr>'`.

Detail: Scale definition and jq safety rules in `reference/technical-guide.md`.

### 5. Map real conditions or phases to Karta statuses

`statusDefinition` is required on the root. It maps the workload's conditions
or phases to `Initializing`, `Running`, `Completed`, `Failed`, `Degraded`,
`Suspended`, `Suspending`, or `Resuming`. A frame no rule matches reads
`Undefined`.

Matchers:

- `byConditions` with a `conditionsDefinition`: all conditions in one entry
  must hold, and each needs a `status` or a `reason`. It matches only a
  condition that exists; for "not yet written or not True", use `byExpression`
  over `.status.conditions // []`.
- `byPhase` with a `phaseDefinition`.
- `byExpression` (jq plus an expected result string) for state in status
  fields, such as a controller that reports only counters. Do not invent a
  phase or condition type the controller never sets.
- Rules under one status are OR'd; one matcher setting several kinds ANDs
  them. Map only the statuses the workload reports.

One status per frame:

- Check every pair that should be exclusive (Running and Suspended, Running and
  Degraded): guard on a field the controller changes between them. When
  suspending changes neither phase nor conditions, AND
  `(.spec.suspend // false) | not` into `Running` and `Initializing`.
- A phase derived from a spec field: let `Suspended` also match the field with
  an empty phase, and AND the field's negation into `Initializing`. A flag read
  before the phase switch, with the controller's own Suspending and Resuming
  phases: map a frame with the flag set and a phase it will suspend from to
  `Suspending`, and the paused phase with the flag cleared to `Resuming`, and
  mirror both in the flow with `AnyOf`.
- A rule that negates a phase list (`IN(...) | not`) also matches the empty
  phase; say so in the builder comment.
- A condition set once and never cleared: AND the absence of every later
  condition into its rule, and keep the condition itself required. Do not add
  a no-status rule only to fill the no-status frame. Do not copy the PyTorchJob
  or MPIJob `Initializing` rule.
- Trace every condition one reconcile writes together. When two terminal
  conditions can be True at once, guard `Completed` with the absence of
  `Failed=True` (or the reverse, per what the controller treats as final) and
  mirror it in the flow.
- A polling loop next to the reconcile: check whether the reconcile writes its
  copy of the conditions after starting the loop, and whether the loop writes a
  cached copy. Declare the one-frame dip `Optional()` in the flow between the
  advanced state and the next one, with a comment. When the pause intent lives
  in a field the controller only reads, match `Suspended` on that field and the
  condition together, parse the field as the controller does, and AND the
  negation into every other rule. Do not drop the resumed flow to work around
  it. Drop a flow only when no field tells the states apart; keep the action
  and name it unproven in the builder comment.

Failed and Degraded:

- Reserve `Failed` for a state the controller does not leave without a spec
  change. An error the next good poll clears is `Degraded`. When one condition
  carries both, split it by reason and mirror the split in the flow with a
  status-plus-reason predicate.
- A progress deadline (`Progressing=False/ProgressDeadlineExceeded`) is
  `Degraded`; read how the controller times it. Do not copy the Deployment
  sample's `Failed` rule.
- Map `Degraded` only on a field that a fault sets, normal progress does not,
  and the controller clears on recovery. Leave a never-cleared condition
  unmapped and say in the builder comment that a consumer wanting the fault
  reads it directly.
- A counters-only controller maps some-but-not-all ready to `Initializing`, as
  `kubectl rollout status` does. Do not copy the StatefulSet sample's
  `Degraded` rule or its `Optional()` dip.
- Every builder comment says whether `Degraded` is mapped. If not, name the
  fault signals the controller writes and the status each reads; if it writes
  none to status, say so and name any counter a reader could take for one.
  Read the controller branch before saying why it is not one.
- A condition written while pods are recreated needs no rule when a guarded
  rule already maps the frame. Say in the builder comment which rule carries it
  and that it is unrecorded.

Spec-driven branches:

- A settled state that owes no pods reads `Running`; do not add a desired > 0
  guard. Record it when the kind cluster can reach it, else name it unrecorded
  in the builder comment.
- Read the spec fields that choose how the controller progresses (update
  strategy, partition, pause, restart policy) and check every rule under each
  value, with the default for an absent field. Say where the default comes
  from: API defaulting, a webhook, or the controller. Under `OnDelete`, updated
  below desired is settled. Record one flow per value a rule branches on that
  the kind cluster can reach, and name unrecorded branches in the builder
  comment. Check feature gates the same way and name any no rule covers.

In-flight phases:

- Only `Suspending` and `Resuming` are in-flight. A phase of a transition the
  controller always completes maps to the status it ends in; say so in a
  comment. Never leave it unmapped.
- An in-flight phase whose exit is picked from fields on the object: one
  matcher per exit, `byPhase` plus a `byExpression` on those fields (with
  defaults), each under its end status. Mirror the split in the flow predicates
  and record each exit the kind cluster can reach. Do not absorb the frame with
  an extra `Optional()` step.
- A pause through a separate object, not a spec field: map the resumable
  paused phase to `Suspended` and its draining phase to `Suspending` without a
  `suspendDefinition`, and say why in a comment. Leave `Resuming` unmapped when
  its phase is one another transition also writes. The recorder only patches
  the workload, so reach the paused state through an in-CR trigger.

Scale and suspend:

- No count in the spec: no `scaleDefinition`. Never write `replicasPath: 1`;
  `replicas=<none>` is not a warning. Leave out an implied count and a
  status-only count the same way.
- Bounds on how many child objects run go on that child with no
  `replicasPath`. Never put bounds in one unit next to a `replicasPath` in
  another.

Detail: Status definition, Status mapping patterns, Scale definition, and
Suspend definition (string values, a field cleared on resume, a hold honored
only before start) in `reference/technical-guide.md`.

### 6. Validate the definition

Always run the validator. Do not hand back a definition that has not passed.

```bash
go run ./hack/karta-verify --karta <definition.yaml>
```

It exits 0 when well-formed, else non-zero with the message. Look the message
up in `reference/troubleshooting.md`, fix, and run again. The structural rules
it enforces are in the Validation checklist; confirm by hand what it cannot:

- Pod selectors reference pod fields. Selectors of the same kind are mutually
  exclusive across components; different kinds may coexist. Verify role-label
  keys against the controller's real pod labels. Two roles sharing a label are
  told apart by a key only one carries (key existence). Prefer a label set at
  pod creation over one written later by status reconciliation. Selectors stay
  unproven until the step 7 pod check.
- Conditions and phases match the real API.
- Every gang `componentName` names a defined component; the validator checks
  only the deprecated `podGroups`, so check `podGroup.subGroups` by hand. Never
  gang a role with the role whose running pod creates it.
- Replica counts describe the right tree level, and siblings agree.

Detail: Pod selectors, Scale definition, Optimization instructions,
Validation checklist in `reference/technical-guide.md`.

### 7. Run the definition against a real CR (optional)

Do this whenever the user supplied a real CR. With no CR, skip it, say so in
the final answer, and state what is unverified. A CR written from the
controller source (one per mapped phase plus one with no status) and a pod
built from its pod label code are stand-ins; every run on them counts as
unverified.

- The CR with no status, and each step 8 testdata manifest as written, must
  read `Initializing` or `Undefined`, never `Running`. Predict a no-match frame
  as `status: [Undefined]` and run it without `--strict`. Every settled rule
  requires a field only the controller writes, such as
  `(.status.observedGeneration // 0) > 0`.
- Predict before running: the status, and per component instance the replicas
  and container names, from the CR's own numbers, never from an existing
  definition. Keys are `name`, `name[instanceId]`, `owner/child`. Never list
  the root; a root-only definition predicts `status:` alone, and root read
  paths are checked with jq. Predict the one status the frame should read; an
  extra status is an overlap to fix in step 5, never a prediction to widen.
- Run from the repository root with absolute paths:

  ```bash
  go run ./hack/karta-verify --karta <definition.yaml> \
    --workload <real-cr.yaml> --predict <predictions.yaml> --strict
  ```

- Reconcile every mismatch and warning. Decide whether the path or the
  understanding of the CRD is wrong first. Never edit the prediction just to
  pass, and never adjust the checklist; look the symptom up in
  `reference/troubleshooting.md`, fix the path, and rerun.
- Done means exit 0 with `--strict`.
- Use a CR that defines its items inline for `--strict`. A CR that only
  references them reports zero instances: run it without `--strict` and state
  the expected zero in the final answer.
- A `fragmentedPodSpecDefinition` with no `containersPath` or `containerPath`
  goes on the component whose pods it describes, not on the root only because
  the root is not extracted. Check its paths with jq, run without `--strict`
  (`--write` too), and predict `podSpec: true` with no `containers`. One
  `no containers` warning per such child is the only expected warning; any
  other is a defect. Do not point `containerPath` at the role spec to silence
  it.
- Multi-instance: run against a CR whose array has two entries (a hand-written
  scratch CR is fine). Run against a CR in another state when one exists.
- Run `--write --strict` against a CR that omits each optional role.
- Pod selectors: evaluate every `podSelector` path and `groupByKeyPaths` entry
  with jq on a real pod (`kubectl get pod -l <owner label> -o json`). The
  `componentTypeSelector` must match, and the `idPath` must return an instance
  id karta-verify printed. With no selectors, check the owner chain; the
  component `ownerRef` names the `controller=true` owner.

Then prove the writes:

```bash
go run ./hack/karta-verify --karta <definition.yaml> \
  --workload <real-cr.yaml> --write --strict
```

Each unexpected change is a warning. A `{}` or `null` identity-write change for
a field the CR did not carry is the write engine's fault; say so in the final
answer. For a field the CR did carry, the read and write paths address
different locations: fix the path. A probe that lands short or elsewhere, or
a suspend or resume that changes a path outside its actions, is a formula
path: rewrite it plain. On
`skipped: no instances extracted`, fix the read side first.

Output and scratch:

- Show the user the run output alongside the definition.
- Keep predictions and scratch copies outside every checkout (`mktemp -d`).
  When the editing tool cannot write there, use a git-ignored directory that is
  not a checkout (`<workspace>/.context/<name>-scratch`), never one inside the
  worktree being changed. That is `<scratch>` below.
- When the exit code matters (2 mismatch, 3 warnings), build with
  `go build -o <scratch>/verify ./hack/karta-verify`; `go run` reports every
  failure as 1.

Detail: karta-verify runs in `reference/technical-guide.md`.

### 8. Ship the recorded flow

The definition is not done until a recorded flow proves it. Catalog
Definitions in `CONTRIBUTING.md` lists the deliverables. Why, commands, and
examples: `reference/recorded-flow.md`.

Catalog entry:

- The source is a Go builder, `pkg/catalog/kartas/<name>.go`, registered in
  `pkg/catalog/catalog.go`. `make generate-samples` writes `docs/catalog/`;
  never hand-edit it. Rerun steps 6 and 7 on the generated file.
- The builder comment holds what code cannot show: the controller's order of
  checks, why each guard exists, what is unproven, what an action does not do.
  Do not restate paths. Stay within 3 to 26 lines; to fit, first cut what the
  fixtures and flow comments show and merge sentences. Keep the unproven
  branches, the unmapped fault signals, and what an action does not do.
- Add a Pre-built Karta Definitions row in `README.md` for an operator-backed
  kind; none for a builtin.

Operator install under `hack/e2e/`:

- A builtin needs no install scripts, pin, or `up.sh` entry: `make e2e-up`
  with `WORKLOADS=none`, `make record-e2e` with `WORKLOADS=<label>`.
  `Fixture.Operator` equals the first `Label`; the second label is `builtin`.
- A new operator follows Adding an operator in `hack/e2e/README.md`:
  `hack/e2e/operators/<name>/{install.sh,verify.sh,smoke.yaml}`, a
  `<NAME>_VERSION` pin in `global.env`, a `version_of` case and an
  `ALL_WORKLOADS` entry (install order) in `up.sh`, and `deps_of` only when it
  needs another operator first. With no `deps_of` and no dependents, it goes
  last in `ALL_WORKLOADS`.
- A new kind for an installed operator reuses its directory and keeps the pin,
  `version_of`, and `ALL_WORKLOADS`. Extend any `install.sh` flag that gates
  which kinds the controller serves. Add `<kind>-smoke.yaml` and a second
  `run_smoke` line in `verify.sh`.
- `verify.sh` drives the smoke manifest to a terminal or stable state with
  `run_smoke`. Give it the fully qualified resource,
  `<plural>.<group>/<name>-smoke`, which `kubectl wait` always resolves. It is
  required when the kind collides with a builtin: a bare `job/` resolves to
  `batch/v1` and waits on the wrong object, so a Volcano Job uses
  `jobs.batch.volcano.sh/<name>-smoke` and a builtin Job `job.batch/<name>`. A
  workload that never settles and proves pods on an object it creates gets a
  hand-written sequence (`apply_with_retry`, `kubectl wait` on the condition,
  `retry` until the child exists, `kubectl wait` on the child by owner label,
  `kubectl delete`), each step captured in `rc` so the delete always runs, and
  a comment saying why `run_smoke` is not used.
- Check which namespaces the controller and its webhooks watch (a
  `--namespaces` flag, a `jobNamespaces` chart value, a webhook
  `namespaceSelector`) and widen each to cover the recorder's generated
  namespace (chart: a co-located `values.yaml`). Otherwise the run times out
  with no frames.
- Pods with no permissions upstream: a co-located RBAC manifest applied from
  `install.sh`, with a comment that it is scoped to the test cluster. Other
  cluster-scoped or shared objects the flows need: same way, with
  `apply_with_retry` and a comment naming their users. Namespaced objects the
  pods need: a ClusterRole in the RBAC manifest, the objects created in the
  flow's `BeforeAll` through a helper in `test/e2e/flows/setup_test.go`, and
  any new API group registered in `suite_test.go`. A CR with no pod template
  takes the Manifests pod conventions in that object.
- Pin the manifest by preference: release asset, raw manifest at the tag,
  kustomize base at the tag, Helm chart. Tags as upstream spells them; chart
  versions without `v`. An asset name without the `v` is derived with
  `${<NAME>_VERSION#v}`, noted next to the pin. List assets with
  `gh release view` before writing a URL, then `curl -fsSIL` the listed one. A
  remote kustomize resource must be a directory; copy a single file locally.
  Never install a bundled operator the suite installs on its own twice: drop it
  from a co-located kustomization or the chart values, and add it to
  `deps_of`. A one-shot webhook cert Job: `kubectl wait` for it to complete
  before `rollout_wait`.
- Grep the manifest (chart: `helm template`) for `kind: Namespace` and
  `namespace:`. With neither, create the namespace and apply with `-n <ns>`;
  after a failed install on a reused cluster, delete what landed in the wrong
  namespace first. A chart that renders its own Namespace owns it: install as
  upstream documents, usually without `-n` or `--create-namespace`, and do not
  turn that Namespace off.
- Preload a large image with the pinned upstream reference as both arguments,
  `preload_image "${img}" "${img}" || warn "..."`, and keep testdata on it. A
  local tag works only when the load does.
- A temp dir in `install.sh` must not be `local`; assign it before
  `trap 'rm -rf "${tmp}"' EXIT`.
- While fixing, run `install.sh` and `verify.sh` directly with `KUBECONFIG`
  exported. Run `make e2e-up` once at the end; it writes
  `.installed-versions-<cluster>`.
- `make lint-shell` must pass on the new scripts.

Flow under `test/e2e/flows/` (read `test/e2e/recorder/README.md` first):

- One Ginkgo file whose `recorder.Fixture` `Operator` equals the directory name
  under `hack/e2e/operators/`; a mismatch silently files the recording under
  the Kubernetes version. Check the path the recorder prints on save. New
  fixtures go under the operator's `version_of` string (possibly composite),
  not `v1.34.0` like the existing ones; a second version directory beside it
  is expected.
- The operator name is the first `Label`; a multi-kind operator adds the kind
  as the second. The testdata directory and object names use the lowercase kind
  (a single-kind operator may use its name). When that collides with a builtin
  or another catalog entry, use the upstream short name for the second label,
  testdata directory, object names, and root component name alike.
- Predicates read the CR's fields, never Karta. Each `AddState` predicate holds
  on exactly the frames its `statusMappings` rule matches, step 5 guards
  included.
- Reuse the helpers in `test/e2e/flows/predicates.go`; add a named predicate
  only when none fits. `CondReason` requires True; `CondNotTrue` also matches
  Unknown. A missing shape (status plus reason, absent, any-of, negation,
  at-most with absent as 0) is added as a generic helper, not a
  workload-specific one. Never name a helper `Not`, `And`, `Or`, or `Equal`. To
  reuse a named predicate on another path, add a path parameter, keep existing
  callers on the old path, and compose extra guards with `AllOf`. A state
  judged by comparing several counters gets one named predicate per state.
- Prove the predicates offline first: a scratch `func TestX(t *testing.T)` in
  `test/e2e/flows` decodes each step 7 CR with `yaml.YAMLToJSON` then
  `Unstructured.UnmarshalJSON` (never plain `yaml.Unmarshal`) and asserts
  exactly one predicate holds, naming the status karta-verify printed. Run
  `GOWORK=off go test -run '^TestX$' ./flows` from `test/e2e`, then delete it.
- A reason no recording can show stays mapped, stays out of the predicate, and
  is named unproven in the builder comment.
- `AddState` order is precedence: least to most advanced, last match
  strongest. Declare `Suspended` first when the controller leaves the condition
  after a resume, last when it flips it to False.
- Mark a step `Optional()` when the controller may skip it. Check each
  fixture's first frame after the first run. If a no-status rule exists and the
  first frame already carries status, keep the rule and say in the builder
  comment no recording proves it; with no such rule, say an object with no
  status reads `Undefined`.
- Declare a revisit only when the controller source can produce it, described
  as allowed, not predicted. Do not copy a sibling flow's revisit.
- Raise the 3 minute deadline with `SetTimeout` only when a run hits it, with a
  comment saying why.
- Actions are generic merge-patch helpers in `test/e2e/flows/actions.go`, not
  named after the workload; suspend and resume share them, side by side. An
  action other than suspend, resume, or scale needs an `ActionType` constant in
  `test/e2e/recorder/flow.go`. A pod template rollout uses
  `AnnotatePodTemplate(key, value)` with `ActionRollout` (`"Rollout"`); add
  them when missing.
- A `suspendDefinition` that can pause a running workload needs a flow
  `Reaches(Running).Do(<suspend action>)` then `Reaches(Suspended)`; the resume
  is optional.
- A write through an exposed path that makes the controller rerun the
  workload needs a flow that patches it from Running and walks back to Running.
- Gates. A `With()` or `Do()` step must be reached, in order: gate only the
  terminal step or a certain one, and never pair `With()` with `Optional()`. A
  `Do()` whose predicate reads a spec field is gated on a field only the
  controller writes. A terminal status shared with an in-flight phase is gated
  on the CR field. A `Do()` state that is also terminal gets a terminal
  `With()` on a field the action changes and the controller echoes; confirm
  `STATE` frames follow the `ACTION`. With a string or absent
  `observedGeneration`, the `With()` gate is the only protection against a
  late write.

Manifests under `test/e2e/flows/testdata/<workload>/`:

- Name objects `karta-e2e-<workload>-<flow>`; set `namespace: default`.
- Pin image tags, declare requests and limits, add the SPDX header, and keep
  the pod alive past the Running check: `sleep infinity` when it never
  completes, `sleep 300` for a Job that must hold Running.
- Do not tolerate the control-plane taint. To make one pod of a per-node
  workload differ, branch on `spec.nodeName` from the downward API
  (`*-worker2`); keep it unready by failing its readiness probe, not by exit.
  Say in the flow comment that the recording cannot tell that settled partial
  frame from a transient one.
- Set `automountServiceAccountToken: false` unless the pods call the API
  server.

Record on an isolated cluster:

```bash
export CLUSTER_NAME=<name> KUBECONFIG=~/.kube/kind-<name>.kubeconfig
make e2e-up CLUSTER_NAME=<name> WORKLOADS=<operator>
make record-e2e CLUSTER_NAME=<name> WORKLOADS=<operator>
make e2e-down CLUSTER_NAME=<name>
```

- Export both once per session and use the same `CLUSTER_NAME` on every
  `make` call.
- A new kind on a multi-kind operator passes the kind label to `record-e2e`
  (`WORKLOADS=tfjob`); `e2e-up` takes the operator name, or `none`.
  `FLOW="<a>|<b>"` re-records only those flows; `E2E_LABELS` takes a raw
  Ginkgo label expression.
- On `required state ... missing or out of order`, the `observed [...]` list is
  the real walk. Fix the mapping when a frame reads the wrong status; change
  the journey only when the frame is real and correctly mapped. When an action
  step never reaches its next state, dump the frames (Reading fixtures in
  `reference/recorded-flow.md`) and check for a stale writer before dropping
  the flow.
- After a failure, compare `Ran N of M Specs` with the `It` count and re-run
  the skipped flows with `FLOW`.
- `make e2e-down` leaves the kubeconfig file and
  `hack/e2e/operators/.installed-versions-<cluster>`; remove both.

After recording (yq commands for each check: Reading fixtures in
`reference/recorded-flow.md`):

- Rerun step 7, `--write` included, on the last frame of the flow's terminal
  state.
- Every fixture ends with `succeeded: true`, and every `STATE` frame lists one
  status in `phases`; two is an overlap to fix (step 5) and re-record. Check
  the `phase:` values; a flow that stops one frame early still succeeds.
- Check every builder comment claim against the frames; rewrite it as observed
  or mark it unproven. A reset or clear claim needs a frame where the field was
  set before the action. Trace pod claims to the code and check them on the
  hand-applied pod.
- Before `make e2e-down`, `kubectl apply -f` one testdata manifest and wait
  until it settles. A pod that another pod creates appears later: loop on
  `kubectl get` until it exists, then `kubectl wait`. Check selectors and
  `groupByKeyPaths` (or the owner chain) with jq on its pod. Use its CR as a
  second `--write` input, then delete it.

Before `make check`:

- Run `make lint-shell`, `make test-replay`, and `make verify-recordings`, and
  in `test/e2e` `GOWORK=off go vet ./...` and `gofmt -l .`, which `make check`
  does not cover. Then run `make check`; do not skip it. Unless `bin/` holds
  the pinned `golangci-lint` and `goreleaser`, run it in the background with
  its output in a log file and poll the log.
- Commit the new files, fixtures included, before `make check`; `validate`
  needs a clean tree.
- Fixtures carry no SPDX header; do not add one.
- `make test-replay` and `make verify-recordings` must be green.

