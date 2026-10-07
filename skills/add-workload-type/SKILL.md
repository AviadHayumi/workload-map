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
components. Once written, any controller or platform built on the Karta library
reads status, scale, and pod specs for that workload through one uniform API,
with no per-type code. This skill walks through authoring a correct definition
and validating it before use.

Every path in a Karta is a jq expression. Paths in `specDefinition`,
`scaleDefinition`, and `statusDefinition` run against the workload object. Paths
in `podSelector` and `optimizationInstructions` run against pod manifests.
Mixing these up is the most common mistake, so keep it in mind throughout.

## Bundled references

Load these as needed. Do not guess field names or rules; confirm them here.

- `reference/technical-guide.md` - the full field and schema cheatsheet:
  component model, the three spec patterns, status mapping semantics, jq safety
  rules, scale, suspend, multi-instance, gang scheduling, and the checklist.
- `reference/sample-index.md` - a decision table that maps a workload shape to
  the closest existing definition under `docs/catalog/`. Start here in step 2.
- `reference/troubleshooting.md` - every validator, jq, and runtime error mapped
  to its cause and fix, plus the mistakes that pass validation but behave wrong.
- `hack/karta-verify/` in the repository root - the offline harness. Validates a
  definition (step 6) and, given a real CR, runs it and checks the extraction
  against predicted values (step 7).

## Workflow

### 1. Gather the target facts first

Do not write anything until these facts are known. Read the target CRD source or
documentation to get them right.

Two inputs are needed up front. Ask the user for them when there is a user and
a cluster; otherwise get them from the offline sources below.

- The CRD schema (`kubectl get crd <name> -o yaml`, or the operator's API types).
  This is what the definition is written from.
- At least one real example CR (`kubectl get <kind> <name> -o yaml`), ideally one
  that is running and one that has finished. This is optional but valuable: it
  unlocks step 7, which is the only way to prove the paths resolve. A jq path can
  be structurally valid and still point at a field no real object carries.

Offline sources, when no cluster or user is at hand:

- The operator's release manifest (the `install.yaml` or Helm chart CRDs). It
  carries the CRD schemas, often in full.
- The API types in the operator repository, usually under `pkg/apis/` or
  `api/`. They name every status field and the condition type constants.
- The controller code that writes status. Grep the repository for the condition
  type constants and the sites that set the phase (`markRunning`,
  `markCompleted`, `SetPhase`, and the like). This is where the real condition
  types, reason strings, and pod labels come from. When the phase is computed
  from several inputs rather than set in one place, grep for the function that
  returns the phase type (`func Calculate.*Phase` in Argo Rollouts) and copy its
  order of checks into the mapping comments. That order is what keeps the
  mapped statuses apart.

A shallow clone of the operator repository at the pinned release tag
(`git clone --depth 1 --branch <tag>`) is the normal way to confirm condition
and label names. Do not guess them from documentation alone. The controller code
that assigns the phase or conditions is the source of truth for step 5, not the
enum in the CRD schema. Pin the newest release tag that is not a prerelease
(`git ls-remote --tags --refs <repo-url> | sort -V -k2`); it becomes the
`<NAME>_VERSION` in step 8. When the operator is already in
`hack/e2e/global.env` (a new kind for Kubeflow), read the source at that pinned
tag instead and leave the pin alone: a bump re-records every flow of that
operator. A checkout already on another branch gets the tag
with `git fetch --depth 1 origin tag <tag>`; read files with
`git show <tag>:<path>`, not from the working tree.

Proceed either way. Without a CR the definition can still be written and
validated; it just cannot be exercised, which step 7 covers. When the definition
will ship to the catalog, step 8 needs a cluster with the operator anyway, so get
the CR from it: do the operator install side of step 8 first, run
`make e2e-up CLUSTER_NAME=<name> WORKLOADS=<operator>`, `kubectl apply` a
running manifest by hand, and save the CR with `kubectl get <kind> <name> -o yaml`
and one of its pods with `kubectl get pod <pod> -o json`. Export the cluster's
kubeconfig first (see Record on an isolated cluster in step 8), and delete the
hand-applied object before `make record-e2e`: it lives in `default`, the
recorder uses its own namespace, and its pods compete with the recording.

From those inputs, establish:

- The full GVK: group, version, and kind. All three are required (the core
  `Pod` kind is the only one allowed to omit the group).
- The real statuses the controller reports: the exact condition types and their
  status and reason values, or the phase strings it writes to `.status`. Use the
  names the controller actually sets. Inventing condition types produces a
  definition that validates but never resolves a status. When documentation, a
  user, or a task brief names a condition the controller never sets (a
  `Running` condition on a CRD that only writes `Complete` and `Failed`), drop
  it, derive the state from the fields the controller does write (counters,
  conditions copied from a child), and say so in the builder comment and the
  final answer.
- Where the pod template lives in the spec, and whether the workload has one
  role or several (for example master and worker, or head and worker groups).
  Take the role list from the controller loop that walks the roles (TFJob's
  `allTypes` in `UpdateJobStatus`), not from the documentation. It includes
  deprecated aliases the API still accepts (TFJob `Master`), and the pods of
  those roles need a component too.
- How replicas are expressed, if at all.

### 2. Start from the closest sample

Open `reference/sample-index.md`, find the row that matches the workload shape,
and copy that definition from `docs/catalog/` as the starting skeleton. Adapting a
working sample is faster and safer than starting from an empty file. Change the
GVK, the paths, and the status mapping to fit the target.

### 3. Pick one specDefinition pattern per component

The three patterns are mutually exclusive. Set exactly one per component:

- `podTemplateSpecPath` when the CRD embeds a full PodTemplateSpec (metadata and
  spec), for example a Job at `.spec.template`.
- `podSpecPath`, with optional `metadataPath`, when the CRD embeds a bare PodSpec
  and optionally a separate metadata object.
- `fragmentedPodSpecDefinition` when pod fields are scattered across the spec.
  List only the field paths that exist (labels, annotations, resources,
  containers, nodeAffinity, and so on). Each fragmented path is used to mutate
  the field, not only read it, so it must be a path jq can assign through: a `//`
  fallback reads fine but breaks on write. When a field has a default plus an
  override, model the varying items as a multi-instance component
  (`instanceIdPath`) rather than reaching for a fallback. See
  `reference/technical-guide.md`.

Any path that iterates an array (`.spec.templates[]`, `.spec.workerGroupSpecs[]`)
can return several values. A component whose spec or scale paths can return more
than one value, or zero, needs an `instanceIdPath` plus a
`componentInstanceSelector`. Without them the tree build fails at runtime with
`instance ids count (1) does not match results count (N)`, and a CR with one
array entry hides the problem. See Multi-instance components in
`reference/technical-guide.md`.

When the roles are optional keys of a map (`.spec.tfReplicaSpecs`,
`.spec.pytorchReplicaSpecs`), model them as one multi-instance child keyed by
the map, not one fixed child per role. A fixed child for a key the CR omits
extracts an empty pod spec, and a write through it creates the key with no
containers, which the operator's webhook rejects. The keyed child also covers
deprecated aliases with no extra component:

```yaml
- name: replica
  instanceIdPath: .spec.tfReplicaSpecs | keys[] | ascii_downcase
  specDefinition:
    podTemplateSpecPath: .spec.tfReplicaSpecs[].template
  scaleDefinition:
    replicasPath: .spec.tfReplicaSpecs[].replicas
  podSelector:
    componentInstanceSelector:
      idPath: .metadata.labels["training.kubeflow.org/replica-type"]
```

`keys` and `.[]` both walk the map in sorted key order, so ids and templates
line up. `keys_unsorted` passes the validator but fails at runtime with
`function not defined`. `ascii_downcase` matches a controller that lowercases
the key into the pod label; check the real label first.

A component may also have no spec definition when it exists only to model
ownership or scale. See `reference/technical-guide.md` for the full field list.

### 4. Write null-safe jq paths against the correct resource

- Use absolute paths from the resource root, starting with `.`.
- Supply a default for any field that can be absent in a status expression, so
  evaluation never fails on null, for example `(.status.active // 0)`. Spec and
  scale paths are the exception: they stay plain paths (`.spec.parallelism`,
  `.spec.tasks[].replicas`) so they can be written through. An absent field
  reads as null, which is the honest value.
- Confirm the resource: spec, scale, and status paths read the workload object;
  selector and optimization paths read a pod manifest.
- Karta rejects jq that can mutate or explode. Do not use assignment or update
  operators, `del`, the recursive descent `..`, or `range`, `paths`, `recurse`,
  `walk`, or `repeat`. Read-only navigation and standard builtins only.
- Test a path with the jq CLI against a real manifest before committing it:
  `kubectl get <resource> -o json | jq '<expr>'`.

### 5. Map real conditions or phases to Karta statuses

`statusDefinition` is required on the root component. It translates the
workload's own conditions or phases into Karta's normalized statuses:
`Initializing`, `Running`, `Completed`, `Failed`, `Degraded`, `Suspended`,
`Suspending`, `Resuming`. A workload that matches no rule resolves to
`Undefined`.

- To match conditions, add `conditionsDefinition` (its path plus field names),
  then use `byConditions`. All conditions in one `byConditions` entry must hold
  (AND). Each entry needs at least a `status` or a `reason`.
- To match a phase string, add `phaseDefinition` (its path), then use `byPhase`.
- When the state lives in status fields rather than conditions or a phase, use
  `byExpression` with a jq expression and an expected result string. Some
  controllers report only status fields (for example replica counts) and no
  aggregate phase; match those with `byExpression`. Do not invent a phase value
  or condition type the controller never sets.
- Rules listed under the same status are OR'd; any one matching resolves that
  status. A single matcher may also combine `byPhase`, `byConditions`, and
  `byExpression`, in which case all of them must hold (AND). Map only the
  statuses the workload actually reports.
- `byConditions` matches only a condition that exists. To match "not yet written
  or not True", use `byExpression` over `.status.conditions // []`:
  `([.status.conditions // [] | .[] | select(.type == "PodRunning" and .status == "True")] | length) == 0`.
- The same holds for a phase not yet written. When the controller derives the
  phase from a spec field (it reports Paused whenever `.spec.paused` is set),
  the frames before the first phase write still carry that field. Mirror the
  controller: let `Suspended` also match the field with an empty phase,
  `(.spec.paused // false) and (.status.phase // "") == ""`, and AND
  `(.spec.paused // false) | not` into `Initializing`.
- When suspending does not change the phase or conditions (the controller keeps
  the phase at Running while `.spec.suspend` is true), AND an expression such as
  `(.spec.suspend // false) | not` into the `Running` and `Initializing`
  matchers. Otherwise `Running` and `Suspended` both match on every suspended
  frame and the recorded flow cannot tell them apart. Check every pair that
  should be exclusive (Running and Suspended, Running and Degraded) this way:
  find what the controller leaves unchanged in the other state and guard on a
  field it does change. A controller that hibernates by annotation can keep its
  healthy phase and `Ready=True` while it clears the ready count, so `Running`
  needs `(.status.readyInstances // 0) >= (.spec.instances // 1)` as well.
- Aim for one status per frame. Every status that matches lands in the
  workload's phases list, and a consumer cannot tell which one is current. A
  condition the controller sets once and never clears (Kubeflow `Created`)
  holds on every later frame, so a rule on it alone overlaps every other
  status. AND the absence of each later condition into that rule:
  `[.status.conditions // [] | .[] | select((.type == "Running" or .type == "Succeeded" or .type == "Failed" or .type == "Suspended") and .status == "True")] | length == 0`.
  The PyTorchJob and MPIJob samples still carry this overlap; do not copy their
  `Initializing` rule.
- Trace every condition one reconcile can write, not one condition at a time.
  The training-operator loops over all roles in one status pass, so a
  succeeded Chief and a failed PS set `Succeeded=True` and `Failed=True`
  together. Guard `Completed` with the absence of `Failed=True` (or the
  reverse, matching what the controller treats as final) and mirror it in the
  flow.
- A condition the controller writes while it recreates pods needs no rule of
  its own when the guarded rule above already maps the frame (Kubeflow
  `Restarting` evicts `Running`, and the frame reads `Initializing`). Say which
  rule carries it in the builder comment, and that it is unrecorded.
- Karta has no in-flight status except `Suspending` and `Resuming`. A phase the
  controller writes while it finishes a transition it always completes (draining
  pods before Completed, Aborted, or Terminated) maps to the status it ends in.
  Say so in a comment. Do not leave it unmapped: the workload then reads
  `Undefined` mid-transition.
- An in-flight phase can have two exits. When the controller picks the exit
  from fields already on the object (Volcano's Restarting goes to Failed when
  `status.retryCount >= spec.maxRetry`, else back to Pending), split the phase:
  one matcher per exit, `byPhase` plus a `byExpression` on those fields, each
  under the status it ends in. Use the controller's default for an absent
  field. Mirror the split in the flow predicates. Do not absorb the frame with
  an extra `Optional()` step instead.
- A controller can pause a workload without a spec field: Volcano suspends
  through a separate Command object (AbortJob, ResumeJob) and reports Aborting,
  then Aborted. Map the resumable paused phase to `Suspended` and its draining
  phase to `Suspending` even though no `suspendDefinition` can be written, and
  say why in a comment. Leave `Resuming` unmapped when its phase is the same
  one another transition writes (a resume that writes Restarting, like a
  restart policy does). The recorder only patches the workload, so reach the
  paused state through an in-CR trigger such as a lifecycle policy.
- A component whose spec carries no count (a template that runs any number of
  pods) gets no `scaleDefinition`. Do not write `replicasPath: 1` to fill the
  gap; karta-verify prints `replicas=<none>` for it, and that is not a warning.
  A count the API only implies (one TaskRun per task) is left out the same way.
- A suspend that writes a string or clears a field on resume, or a hold honored
  only before start, is covered under Suspend definition in
  `reference/technical-guide.md`.

### 6. Validate the definition

Always run the validator on the definition just written. Do not hand back a
definition that has not passed it.

```bash
go run ./hack/karta-verify --karta <definition.yaml>
```

It exits 0 when the definition is well-formed, and non-zero with the validator's
message otherwise. Look any failure up in `reference/troubleshooting.md` by the
message text, fix it, and run again.

The validator enforces these, so there is no need to check them by eye:

- All kinds use a full GVK (only `Pod` may omit the group).
- The root component has a `statusDefinition` and no `ownerRef`.
- Every child component has an `ownerRef` naming an existing component, with no
  ownership cycles.
- Component names are unique and non-empty.
- No component sets more than one of the three spec patterns.
- `instanceIdPath` and a `componentInstanceSelector` are either both present or
  both absent on a component.
- Every jq expression parses and uses no rejected construct.

The validator cannot check these. Confirm each one:

- Pod selectors reference pod fields, not workload fields. Selectors of the same
  kind must be mutually exclusive across components so a pod maps to one component
  of that kind; different selector kinds may coexist on a component. Verify
  role-label keys against the controller's real pod labels (they are
  operator-specific), and when two roles share a label, disambiguate by matching
  a key only one role carries (key existence). Prefer a label the controller
  sets when it creates the pod. A label written later by status reconciliation
  (a primary or replica role) leaves new pods unmapped until it appears and
  moves pods between components on failover. karta-verify takes only the
  workload object, so selector paths stay unproven until the pod check in step 7.
- Status conditions and phases match the workload's real API.
- Every gang-scheduling `componentName` names a defined component. The validator
  checks this only for the deprecated `podGroups` format; references under
  `podGroup.subGroups` are not checked, so verify those by hand.
- Replica counts describe the right level of the tree, and siblings at the same
  level agree. See the scale section of `reference/technical-guide.md`.

A valid definition is still an unproven one: validation says nothing about
whether a path resolves against a real object. Step 7 is what proves that.

### 7. Run the definition against a real CR (optional)

Do this whenever the user supplied a real CR. It is the only step that proves a
path resolves: a definition can pass step 6 in full, resolve to null against the
real object, and report nothing.

When no CR is available, skip the step and say so in the final answer. The
definition is structurally valid and never exercised, and which parts are
unverified should be stated plainly rather than left for someone to discover.
A CR written by hand from the controller source (the status its first sync
writes, the fields the admission webhook defaults) is a useful stand-in, one
per mapped phase plus one with no status. Every run on such a CR counts as
unverified. A catalog definition runs this step again in step 8 on a CR the
controller wrote.

The same command does it, with `--workload` added. It builds the workload tree
from the manifest and prints the extracted status, replica counts, and containers
per component instance, with no cluster involved. Its flags and the predictions
format are documented in `hack/karta-verify/README.md`.

Predict before running. Writing down the expected values first is the point of
this step: reading the output afterwards invites accepting whatever appears,
while a prediction that disagrees with the extraction is a defect that cannot be
talked away.

1. From the CR, write the values the definition should produce into a predictions
   file: the status, and per component instance the replica count and container
   names. Derive them from the CR's own numbers, never by reading them back out
   of an existing definition. A component key is `name`, `name[instanceId]` for
   a multi-instance component, and `owner/child` when nested. The root component
   is never listed: its status is the `status:` line, and its scale and spec
   paths are not extracted, so a prediction keyed on the root fails as `predicted
   but not extracted`. Check root paths with jq against the CR instead (for a
   Deployment-shaped root, `.spec.replicas` and
   `.spec.template.spec.containers[].name`). `status` is compared as a set
   against every status that matched, so predict the one status the frame
   should read. An extra status in the output (`Running,Initializing`) is an
   overlap to fix in step 5, not a prediction to widen:

   ```yaml
   status: [Running]
   components:
   - key: task[worker]
     replicas: 2
     containers: [worker]
     podSpec: true
   ```
2. Run it, from the repository root:

   ```bash
   go run ./hack/karta-verify --karta <definition.yaml> \
     --workload <real-cr.yaml> --predict <predictions.yaml> --strict
   ```

3. Reconcile every mismatch and warning. A mismatch means either the path is
   wrong or the understanding of the CRD is wrong. Decide which before changing
   anything, and never edit the prediction just to make the run pass.

The definition is done when the command exits 0 with `--strict`: the status
resolved, every child component declaring a spec pattern extracted a pod spec
with containers, every `instanceIdPath` produced the instance keys the CR
contains, and every predicted number matched. A child declared only for
ownership (no spec or scale definition, like the Deployment's `replicaset`)
prints `replicas=<none> podSpec=n/a containers=<none>`; `--strict` accepts it.

Run `--strict` against a CR that defines its items inline. A CR that only
references them (a PipelineRun by `pipelineRef`, a Workflow by template
reference) correctly reports zero instances, which `--strict` counts as a
warning. Run that case without `--strict` and state the expected zero in the
final answer. The same holds for a `fragmentedPodSpecDefinition` with no
`containersPath` or `containerPath` (a CRD that only exposes image and
resources overrides): on a child it always warns `extracted a pod spec with no
containers`. Put the spec on the component whose pods it describes, not on the
root only because the root is not extracted, run without `--strict`, state the
expected warning, and check its paths with jq against the CR.

Also run `--write --strict` against a CR that omits each optional role, not only
one that carries every role. A fixed child for an absent role warns `extracted
a pod spec with no containers` next to `replicas=<none>`, and its probe write
creates the role. That is the shape problem from step 3, not a CR to skip.

Show the user the run output alongside the definition. Keep the predictions file
and any scratch copies out of the repository, in a directory outside the
checkout or one the clone ignores through `.git/info/exclude`; karta-verify
takes absolute paths. `go run` turns every non-zero exit into 1, so build once with
`go build -o <scratch>/verify ./hack/karta-verify` when the exit code matters
(2 mismatch, 3 warnings). When something comes back empty or
wrong, do not adjust the checklist; look the symptom up in
`reference/troubleshooting.md`, fix the path, and run again. If a second example
CR in a different state is available (completed or failed), run against it too to
confirm the other status rules fire.

Two more runs are needed when they apply:

- Multi-instance components: run against a CR whose array has two entries, not
  only one. One entry makes a missing `instanceIdPath` look correct; two entries
  expose it as `instance ids count (1) does not match results count (2)`. A
  scratch CR written by hand is fine for this.
- Pod selectors: karta-verify never sees a pod, so check the pod side with jq.
  Fetch one pod of the running workload (`kubectl get pod -l <owner label> -o json`,
  or a pod manifest saved earlier) and evaluate every `podSelector` path and
  every gang-scheduling `groupByKeyPaths` entry against it with `jq`. The
  `componentTypeSelector` must match, and the `componentInstanceSelector`
  `idPath` must return one of the instance ids karta-verify printed for the
  component. A selector that returns null here maps the pod to nothing.

Then prove the writes. Reading is half of a definition; a consumer also writes
through it, and a path that reads fine can write somewhere else or drop fields.
Run the same command with `--write`:

```bash
go run ./hack/karta-verify --karta <definition.yaml> \
  --workload <real-cr.yaml> --write --strict
```

Per component it writes the pod spec back unchanged (nothing may change), sets
one probe field and writes again (exactly one leaf per instance may change),
and applies the suspend actions then the resume actions (only the action paths
may change). It prints every changed path with its before and after value, and
each unexpected change is a warning. Read the output this way:

- A changed path on the identity write names a field the write path drops or
  materializes. When it is `{}` or `null` for a field the CR did not carry, the
  write engine is at fault, not the definition; say so in the final answer. When
  it is a field the CR did carry, the read path and the write path do not
  address the same location.
- A probe that landed in fewer places than there are instances, or in another
  path, means the spec path is a formula: a `//` fallback, arithmetic, or a
  filter after an iterator. Rewrite it as a plain path (see the assignable path
  rules in `reference/technical-guide.md`).
- A suspend or resume that changed a path outside its actions means an action
  path is a formula too.
- `skipped: no instances extracted` means the component extracted nothing from
  this CR; fix the read side first.

Scale paths are read today and `--write` does not probe them, but the same
rules apply: write them as plain paths so they stay writable, even when the
field is `omitempty`. Use `.spec.tasks[].replicas`, not
`.spec.tasks[] | .replicas // 0`. A count derived from several fields
(LeaderWorkerSet workers, JobSet replicas times parallelism) has no plain
path; it stays a formula, read-only.

### 8. Ship the recorded flow

The definition is not done until a recorded flow proves it. The run in step 7
checks one object once; the recording checks every status frame the controller
writes, on every CI run. Catalog Definitions in `CONTRIBUTING.md` lists the
deliverables; the conventions each one follows are below.

Catalog entry:

- The source of a catalog definition is a Go builder under
  `pkg/catalog/kartas/<name>.go`, registered in `pkg/catalog/catalog.go`.
  `make generate-samples` writes the YAML under `docs/catalog/`; never hand-edit
  that file. Port the YAML validated in steps 6 and 7 into the builder, generate,
  and run step 6 and step 7 again on the generated file.
- Add a row for the workload to the Pre-built Karta Definitions table in
  `README.md`.

Operator install under `hack/e2e/`:

- Follow Adding an operator in `hack/e2e/README.md`. The wiring is:
  `hack/e2e/operators/<name>/{install.sh,verify.sh,smoke.yaml}`, a
  `<NAME>_VERSION` pin in `hack/e2e/global.env`, a `version_of` case and an
  `ALL_WORKLOADS` entry (in install order) in `hack/e2e/up.sh`, plus a `deps_of`
  entry only when the operator needs another one installed first. An operator
  with no `deps_of` entry, which no other operator depends on, goes at the end
  of `ALL_WORKLOADS`.
- A new kind for an operator the suite already installs reuses
  `hack/e2e/operators/<operator>/`. Keep the pin, `version_of`, and
  `ALL_WORKLOADS` as they are. Check `install.sh` for flags that gate which
  kinds the controller serves and extend them (the training-operator runs with
  one `--enable-scheme=<kind>` per kind and silently ignores the rest). Add a
  `<kind>-smoke.yaml` next to `smoke.yaml` (as `mpi-smoke.yaml` is) and a second
  `run_smoke` line in `verify.sh`.
- `verify.sh` drives `smoke.yaml` to its terminal state through `run_smoke`. That
  is its purpose: it proves the controller, its RBAC, and the pod path end to
  end. When the upstream release manifest grants the workload's pods no
  permissions (every pod ends in error until a role exists), add a co-located
  RBAC manifest, apply it from `install.sh`, and say in a comment that it is
  scoped to the test cluster. Any other cluster-scoped or shared object the
  flows need (a runtime, a class, a template the CR references by name) goes
  the same way: the recorder creates only the one flow object, in its own
  namespace. Apply it with `apply_with_retry` and name its users in a comment.
  When the CR has no pod template of its own, the pod-template conventions
  below (`automountServiceAccountToken: false`, requests and limits, pinned
  image, `sleep 300`) go into that object instead.
- When the CRD kind collides with a builtin (a `Job` outside `batch`), use the
  fully qualified resource, for example `jobs.batch.volcano.sh/<name>-smoke` as
  the `run_smoke` target and `job.batch/<name>` for a builtin Job. A bare `job/`
  resolves to `batch/v1` and waits on the wrong object.
- Pin the install manifest in this order of preference: the GitHub release
  asset `https://github.com/<org>/<repo>/releases/download/<tag>/<asset>`
  (jobset, lws, kserve), a raw manifest at the tag
  `https://raw.githubusercontent.com/<org>/<repo>/<tag>/<path>` (mpi-operator),
  a kustomize base at the tag,
  `kubectl apply --server-side -k "github.com/<org>/<repo>/<path>?ref=<tag>"`
  (kubeflow), then a Helm chart. Project storage buckets often lag the newest
  tag. Run `curl -fsSIL <url>` before writing `install.sh`. A remote kustomize
  resource must be a directory, so a single upstream file (a Namespace) is
  copied locally. When the upstream overlay or chart bundles an operator the
  suite installs on its own (a JobSet), do not install it twice: render a
  co-located kustomization that lists the same upstream bases minus the bundled
  one (`sed` the version pin in), or turn it off in the chart values, and add
  the dependency to `deps_of`. If the manifest ships a one-shot Job that generates
  webhook certs, `kubectl wait --for=condition=Complete job.batch/<init-job>`
  before `rollout_wait` on the webhook, or the rollout times out on a pod waiting
  for the secret.
- Grep the manifest for `kind: Namespace` and `namespace:`. Some release
  manifests (Argo Rollouts) carry neither, so a plain `kubectl apply -f` lands
  the controller in `default` while its ClusterRoleBinding names a ServiceAccount
  in the intended namespace. Create the namespace and apply with `-n <ns>`, as
  upstream documents. After a failed install on a reused cluster, delete what
  landed in the wrong namespace before running `make e2e-up` again.
- A temp dir in `install.sh` must not be `local`: the EXIT trap fires after
  `main` returns, and under `set -u` it fails on an unbound variable. Assign it
  without `local` before `trap 'rm -rf "${tmp}"' EXIT`, as `dynamo/install.sh`
  and `nim/install.sh` do.
- `make e2e-up` on a reused cluster re-runs every selected install and smoke,
  dependencies included. While fixing one operator, run
  `bash hack/e2e/operators/<name>/install.sh` and `verify.sh` directly (with the
  test cluster's `KUBECONFIG` exported); they are standalone. Run `make e2e-up`
  once at the end: it writes the `.installed-versions-<cluster>` entry the
  recorder needs.
- `make lint-shell` must pass on the new scripts.

Flow under `test/e2e/flows/`:

- One Ginkgo file, `Label("<name>")`, with a `recorder.Fixture` whose `Operator`
  equals the directory name under `hack/e2e/operators/`. `operatorVersion()`
  reads `hack/e2e/operators/.installed-versions-<cluster>` keyed by that name; a
  mismatch silently files the recording under the Kubernetes version instead of
  the operator version. Recordings land under
  `test/e2e/recorded_data/<operator>/<version>/<kartaName>/<flow>.yaml`; check
  the path the recorder prints on save. The fixtures already in the repository
  all sit under the Kubernetes version (`v1.34.0`), operators included. Do not
  copy that: new fixtures go under the operator's `version_of` string from
  `up.sh`, which is what `.installed-versions-<cluster>` records. It can be
  composite (`kubeflow` is `v1.9.0+mpiv0.8.2`). A new kind for an operator with
  older fixtures therefore lands in a second version directory next to
  `v1.34.0`; that split is expected until the older fixtures are re-recorded.
- The operator name is the first `Label` (it is what `WORKLOADS` selects). When
  the operator ships several kinds, add the kind as a second label
  (`Label("kubeflow", "mpijob")`). Name the testdata directory after the kind in
  lowercase (`pytorch`, `mpijob`, `rayjob`); single-kind operators may use the
  operator name (`nim`, `milvus`). Use that same name in the object names.
  When the lowercase kind collides with a builtin or another catalog entry (a
  Volcano `Job`), use the upstream short name (`vcjob`) for the second label,
  the testdata directory, the object names, and the root component name
  (otherwise the lowercase kind, as in every catalog builder) alike.
- State predicates read the CR's own fields, never Karta, and mirror the status
  mapping: each `AddState` predicate must hold on exactly the frames the
  corresponding `statusMappings` rule matches, including any not-suspended AND
  from step 5. Reuse the helpers in `test/e2e/flows/predicates.go` (`CondTrue`,
  `CondNotTrue`, `CondStatus`, `CondReason`, `PhaseEq`, `PhaseAny`, `IntAtLeast`,
  `BoolTrue`, `Absent`, `AllOf`) and add a named predicate only when none fits.
  `CondReason` requires status True and `CondNotTrue` also matches Unknown. A
  controller that reports one condition and tells states apart by reason while
  it is Unknown needs generic helpers (status plus reason, condition absent,
  any-of); add them to `predicates.go` rather than writing a workload-specific
  one. The same goes for a state reported two ways (any-of), a negation, or a
  count that `omitempty` drops at zero (at-most, absent read as 0). The flow
  files dot-import ginkgo and gomega, so a helper named `Not`, `And`, `Or`, or
  `Equal` fails vet; pick another name such as `Negate` or `AnyOf`. When a
  named predicate already reads the same fields under another path (a CR that
  copies the JobSet counters `JobsetRunning` reads), give it the path as a
  parameter, keep the existing callers on the old path, and compose any extra
  guard (the not-suspended AND from step 5) with `AllOf` rather than copying it.
- A reason mapped from the controller source that no recording can show (the
  controller overwrites it within the same reconcile, or a kind cluster cannot
  reach it) stays mapped. Leave it out of the predicate and name it as unproven
  in a comment in the builder.
- `AddState` order is the precedence: declare states least to most advanced,
  and the last match is the strongest. Order only decides a frame where two
  predicates hold, so with exclusive mappings (step 5) it never changes the
  walk. The batch Job and JobSet flows put `Suspended` first in case the
  Suspended condition lingers after a resume; the Kubeflow flows keep it last
  because the training-operator flips it to False on resume. Read which one
  the target controller does.
- Mark a step `Optional()` when the controller may skip it: a watch can miss a
  short frame, and a fast pod can go from Initializing straight to Completed.
  The create response is recorded only when it already reaches the terminal
  state. Otherwise the first frame is the controller's first write, often a
  label or finalizer patch with no status, so a no-conditions `Initializing`
  rule matches it. Check the first frame of each fixture after the first run.
  When it already carries a phase or condition, no recording proves the
  no-status rule; keep it and say so in the builder comment. Read
  `test/e2e/recorder/README.md` before writing the first flow.
- A step whose state was already declared earlier in the journey may be absent
  from the walk, like an `Optional()` step (`order.go`). Declare such a revisit
  only when the controller source can produce it, and describe it as allowed,
  not as a predicted frame. A revisit copied from a sibling flow documents a
  frame the target controller may never write.
- When a run fails with `required state ... missing or out of order`, the
  `observed [...]` list in the error is the real walk. Fix the mapping (step 5)
  when a frame reads the wrong status. Change the journey only when the frame
  is real and correctly mapped.
- Actions are merge patches. A `Do()` step fires on the first frame judged to
  be its state, which can be that pre-status frame when the predicate reads a
  spec field (a `Suspended` that matches `spec.paused`). Gate such a step on a
  field only the controller writes, for example
  `Reaches(kartav1alpha1.SuspendedStatus).With(PhaseEq("Paused", "status", "phase")).Do(...)`.
- A step with `With()` or `Do()` is one the run must reach, in order: the run
  ends only after every such step was reached (`actionSteps` in
  `recorder.go`), and a gated step whose frame the watch misses stalls the run
  until the timeout. Gate only the terminal step or a step whose frame is
  certain, and never pair `With()` with `Optional()`.
- The recorder skips frames written before the controller observed the current
  spec, but only when `status.observedGeneration` is an integer. A controller
  that stores it as a string (Argo Rollouts writes `"1"`) disables that guard,
  and a late write computed from the old spec lands in the checked walk after a
  `Do()`. The `With()` gate above is then the only protection: it makes the
  action land after the controller's first real status, so a late write of the
  same state stays in order.
- The run ends on the first frame that matches the terminal state. When an
  in-flight phase maps to the same status as the final one (step 5), gate the
  terminal step on the CR field, for example
  `Reaches(kartav1alpha1.FailedStatus).With(PhaseEq("Aborted", "status", "state", "phase"))`,
  so the recording holds both frames. After the first run, check the `phase:`
  values in the fixture: a flow that stops one frame early still reports
  `succeeded: true`.

Manifests under `test/e2e/flows/testdata/<workload>/` (`<workload>` as chosen
above):

- Name objects `karta-e2e-<workload>-<flow>` and set `namespace: default`, as
  every existing manifest does. The recorder overrides the namespace with its
  own generated one; `default` keeps the manifest usable by hand with a plain
  `kubectl apply` while debugging.
- Pin image tags, declare resource requests and limits, add the SPDX header,
  and keep the pod alive well past the Running check (`sleep 300`) so the state
  is stable when the watch sees it.
- Set `automountServiceAccountToken: false` on the pod template unless the
  workload's pods call the API server, as the pod, batch-job, deployment, and
  statefulset manifests do.

Record on an isolated cluster:

```bash
make e2e-up CLUSTER_NAME=<name> WORKLOADS=<operator>
make record-e2e CLUSTER_NAME=<name> WORKLOADS=<operator>
make e2e-down CLUSTER_NAME=<name>
```

`hack/e2e/up.sh` always runs `kind export kubeconfig` and `kubectl config
use-context`, against whatever `KUBECONFIG` resolves to. An explicit
`KUBECONFIG=<file>` wins. Otherwise a non-default `CLUSTER_NAME` resolves to
`~/.kube/kind-<name>.kubeconfig`, and the default (`karta-e2e`) resolves to the
shared `~/.kube/config`, which switches the shell's context away from whatever
was selected. Export `KUBECONFIG=~/.kube/kind-<name>.kubeconfig` once for the
whole session, alongside `CLUSTER_NAME`, so hand-run `kubectl` commands hit the
test cluster too. Use the same `CLUSTER_NAME` on every `make` call.
`WORKLOADS` on `record-e2e` selects flows by label. For a new kind on an
operator that ships several, pass the kind label (`WORKLOADS=tfjob`) to
`record-e2e`; the operator name would re-record every sibling flow too.
`e2e-up` still takes the operator name. `E2E_LABELS` takes a raw Ginkgo label
expression. `FLOW` is a Ginkgo focus regex:
`FLOW=<name>` narrows to one flow, and `FLOW="aborted|terminated"` re-records just
those two and leaves the other fixtures untouched, which is the normal loop after
fixing a flow. See Record in `test/e2e/README.md`.

With `KUBECONFIG` exported, `make e2e-down` deletes the cluster but leaves the
kubeconfig file; remove it yourself.

After recording, run step 7 again, `--write` included, on a CR the controller
wrote. Each fixture holds one: extract the last Running frame with
`yq '[.events[] | select(.state == "Running")] | .[-1].object' <fixture> > /tmp/cr.yaml`
and pass it as `--workload`. Read each fixture with yq:
`yq '.result.succeeded' <fixture>` for the outcome,
`yq '.events[0].object.status.conditions' <fixture>` for the first frame, and
`yq '[.events[] | .state + "=" + ((.phases // []) | join(","))] | join(" -> ")' <fixture>`
for the walk. Action frames show an empty state. Every `STATE` frame must list
one status in `phases`; `Running=Initializing,Running` is the overlap from
step 5. Fix the rule and re-record. Before `make e2e-down`, also check the
`podSelector` and `groupByKeyPaths` paths with jq against a real pod
(`kubectl get pod -l <owner label> -o json`), as step 7 describes.

Before `make check`:

- Run `make lint-shell`, `make test-replay`, and `make verify-recordings` first;
  they take seconds. The first `make check` on a fresh checkout downloads
  golangci-lint, then goreleaser in the cli and operator phases, and stays
  silent for minutes in each. The whole check can outlast a tool timeout, so
  run it in the background with its output in a log file and poll the log.
- Commit the new files, fixtures included, before `make check`. The `validate`
  target requires a clean tree and reports untracked files as `generated files
  or module manifests are stale or untracked`, which reads like a broken
  generator but is not.
- The recorded fixtures are recorder output and carry no SPDX header, like
  every existing fixture. Do not add one.
- `make test-replay` and `make verify-recordings` must be green, and every
  fixture must end with `succeeded: true`. `make test-replay` prints only `ok`.
  To see the new fixtures replayed, run
  `cd test/e2e && GOWORK=off go test -count=1 -v ./replay_tests/... -args -ginkgo.v`
  and grep the output for the `kartaName`.
