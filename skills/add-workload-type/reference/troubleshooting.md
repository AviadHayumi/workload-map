<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Troubleshooting catalog

Match the error text to a row and apply the fix. Messages come from the
validator (`karta/sdk/pkg/api/runai/v1alpha2/validation.go`), the CEL validator
(`karta/sdk/pkg/cel/evaluator.go`), the Go accessor API at runtime, a
`karta/sdk/hack/karta-verify` run against a CR, or the e2e scripts and recorder. The
prose version is `docs/Troubleshooting.md`.

## Structure validation errors

The validator joins several errors at once, so fix every named component.

| Message | Cause | Fix |
|---|---|---|
| `root component must have full kind (group, version, kind)` | Root is missing group, version, or kind. | Provide all three under `kind`. Use an empty group for core API kinds. |
| `root component must have status definition` | Root has no `statusDefinition`. | Add a `statusDefinition` to the root. It is required. |
| `root component cannot have owner ref` | An `ownerRef` is set on the root. | Remove it. Only child components have owners. |
| `child component '<name>' has no owner ref` | A child is missing `ownerRef`, or it is empty. | Set `ownerRef` to the parent component `name`. |
| `child component '<name>' has owner ref to non-existing component '<owner>'` | `ownerRef` names a component that does not exist. | Correct it to an existing `name`. Watch for typos and renames. |
| `component name <name> is not unique` | Two components share a `name`. | Make every `name` unique. |
| `component name is empty` | A component has no `name`. | Add a non-empty `name`. |
| `component '<name>' has multiple pod spec definitions` | More than one of `podTemplateSpec`, `podSpec`, `fragmentedPodSpecDefinition` is set. | Keep exactly one. They are mutually exclusive. |
| `component '<name>' has instance id path but no pod component instance selector` | `instanceIds.read.expression` is set without a `componentInstanceSelector`. | Add a `componentInstanceSelector`, or remove `instanceIds.read.expression`. |
| `component '<name>' has pod component instance selector but no instance id path` | A `componentInstanceSelector` is set without `instanceIds.read.expression`. | Add `instanceIds.read.expression`, or remove the instance selector. |
| `ownership cycle detected involving component <name>` | Owner refs form a loop instead of reaching the root. | Break the cycle. Every owner chain must terminate at the root. |
| `pod-group member component '<name>' is not defined (should be a root or child component)` | A gang-scheduling member names a missing component. | Make each `componentName` match a defined component. |
| `karta is nil` | The validator got no definition. | Ensure the file parsed and loaded before validation. |

## CEL accessor errors

Use the named component and accessor to find the failing expression. CEL errors
do not include the workload or reference contents.

| Message | Cause | Fix |
| --- | --- | --- |
| `invalid CEL expression for this scope` | Invalid syntax or an identifier unavailable in this scope. | Use `object` for the current input. Pod expressions cannot use `variables` or `references`; only write expressions receive `instance` and `index`. |
| `CEL predicate must return a boolean` | A match, filter or status expression returns another type. | Return a boolean comparison. Remove the old `expectedResult` field. |
| `CEL evaluation failed` | A missing field, wrong type or invalid operation. | Test the expression against the actual object. Use optional access and a schema-appropriate default. |
| `CEL evaluation cost limit exceeded` | The expression exceeds the evaluation budget. | Bound collections and simplify repeated work. |
| `exactly one of path or pathExpression is required` | A write has no destination or has both forms. | Provide one fixed JSON Pointer or one CEL expression returning a pointer. |
| `read accessor does not match the workload` | Its `read.matchExpression` returned false. | Treat `resource.ErrValueNotFound` as an absent field. Whole-workload extraction already skips it. |
| An undefined variable or dependency cycle | Shared expressions have a bad name or circular dependency. | Correct the variable names and remove the cycle. |

A read fallback does not select a write destination. Check `write.path` or
`write.pathExpression` separately. Use the local verification tool or SDK tests;
a jq command does not execute CEL.

## Accessor errors at runtime

Raised by the Go Component API when reading a definition.

| Error type | Example | Cause | Fix |
|---|---|---|---|
| `DefinitionNotFoundError` | `component <name> does not have suspendDefinition` | Code asked for a part the component does not define. | Add the missing definition, or guard the call with `errors.As` against `DefinitionNotFoundError`. |
| `InstanceNotFoundError` | `could not match instance id "<id>". existing instance ids [...]` | A pod's extracted instance id matches no instance from `instanceIds.read.expression`. | Confirm the `componentInstanceSelector` reads the same id the `instanceIds.read.expression` produces. |
| tree build error | `instance ids count (1) does not match results count (N)` | A fragmented or scale path iterates an array on a component with no `instanceIds.read.expression`, so N results meet one implicit instance. Also raised when the instance and value expressions filter different items. | Add `instanceIds.read.expression` and a `componentInstanceSelector`, or make every expression use the same ordered collection so the counts align. Test with a CR that has two array entries; one entry hides the bug. |

## karta-verify output

| Message | Cause | Fix |
|---|---|---|
| `extracted a pod spec with no containers`, with `replicas=<none>` on the same line | A fixed child for a role the CR omits (TFJob PS or Evaluator, PyTorchJob Worker). Or, expected, a `fragmentedPodSpecDefinition` child with no container path and no `scaleDefinition`. | For an omitted role, model the roles as one multi-instance child keyed by the map (step 3 in `SKILL.md`). For the fragmented child, run without `--strict` (step 7). |
| `extracted a pod spec with no containers`, with a replica count | The spec path points at the wrong level, or a `fragmentedPodSpecDefinition` has no `containers`. | Check the CEL expression against the CR through the SDK. For a fragmented spec with no container field, run without `--strict` (step 7). |
| `status: Running,Initializing` (two or more statuses) | Two rules match the same object, often one on a condition the controller never clears. | Make the rules exclusive (step 5 in `SKILL.md`). Do not widen the prediction. |
| `predicted but not extracted` for the root key | The read side does not extract the root's scale and spec paths. | Never list the root. Predict `status:` alone for a root-only definition and check root accessors through the Component API. |
| `replicas=<none>` with no warning | The spec carries no count, an `omitempty` field is absent, or the component has bounds only. | Expected. Leave `replicas` out of that prediction. Do not invent a constant replica count. |
| Unresolved status warning on the CR with no status | No rule matches, as intended. | Predict `status: [Undefined]` and run without `--strict`. |
| Zero instances warning under `--strict` | The CR references its items (`pipelineRef`, a template reference) instead of defining them inline. | Run without `--strict` and state the expected zero in the summary. |
| Every failure exits 1 | `go run` turns every non-zero exit into 1. | Build with `go build -o <scratch>/verify ./hack/karta-verify`, run `<scratch>/verify` unpiped, and read `$?`: 2 mismatch, 3 warnings. |
| `skipped: no instances extracted` | The component extracted nothing from this CR. | Fix the read side first. |

## Recorder and e2e

| Symptom | Cause | Fix |
|---|---|---|
| `required state ... missing or out of order` | The `observed [...]` list in the error is the real walk. | Fix the mapping when a frame reads the wrong status. Change the journey only when the frame is real and correctly mapped. |
| An action step never reaches its next state | A stale writer (a polling loop) or a wrong mapping. | Dump the frames (Reading fixtures in `recorded-flow.md`) and check for a stale writer (step 5) before dropping the flow. |
| The run times out with no frames | The controller or a webhook does not watch the recorder's generated namespace. | Widen each namespace setting the controller and its webhooks read (a `--namespaces` flag, a `jobNamespaces` chart value, a webhook `namespaceSelector`) to cover the generated namespace (Namespaces the controller watches in `recorded-flow.md`). |
| Every workload pod ends in error | The upstream manifest grants the pods no permissions. | Add a co-located RBAC manifest applied from `install.sh`. |
| The webhook rollout times out | A one-shot Job that generates webhook certs has not finished. | `kubectl wait --for=condition=Complete job.batch/<init-job>` before `rollout_wait`. |
| `install.sh` fails at exit on an unbound variable | The temp dir is `local`; the EXIT trap fires after `main` returns, under `set -u`. | Assign it without `local` before the `trap`. |
| The controller lands in `default` while its ClusterRoleBinding names another namespace | The manifest carries no `kind: Namespace` and no `namespace:` (Argo Rollouts). | Create the namespace and apply with `-n <ns>`. Delete what landed in the wrong namespace before `make e2e-up` again. |
| The operator's own manager pods are blocked by its pod webhook | The chart's Namespace was turned off and its labels were lost (OpenKruise `control-plane: openkruise`). | Let the chart render its Namespace and install as upstream documents. |
| A new training-operator kind never reconciles | `install.sh` does not list it in `--enable-scheme`. | Add one `--enable-scheme=<kind>` per kind. |
| `kind load` fails on a multi-arch image | Docker Desktop's containerd store. | `preload_image "${img}" "${img}" \|\| warn "..."` with the pinned upstream reference, and keep testdata on it. |
| `run_smoke` waits on the wrong object | A bare `job/` resolves to `batch/v1`. | Use `<plural>.<group>/<name>-smoke`. |
| `go vet` fails in `test/e2e/flows` on a new helper | The name collides with dot-imported ginkgo or gomega (`Not`, `And`, `Or`, `Equal`). | Rename it (`Negate`, `AnyOf`). |
| Counter predicates pass or fail on every CR in the offline test | `yaml.Unmarshal` into a map yields float64, and `NestedInt64` reads 0. | Decode with `yaml.YAMLToJSON` then `Unstructured.UnmarshalJSON`. |
| The run stalls until the timeout | A `With()` or `Do()` step whose frame the watch missed. | Gate only the terminal step or a step whose frame always appears. Never pair `With()` with `Optional()`. |
| `Do()` fires before the controller wrote any status | Its predicate reads a spec field. | Gate the step with `With()` on a field only the controller writes. |
| The run ends on the frame that fired the action | The `Do()` state is also the terminal state. | Gate the terminal step with `With()` on a field the action changes and the controller echoes. |
| `succeeded: true`, but the fixture stops one frame early | An in-flight phase maps to the same status as the final one. | Gate the terminal step on the CR field and check the `phase:` values. |
| A late write lands in the checked walk after a `Do()` | `observedGeneration` is a string or absent, so stale frames are not marked. | Gate the action step with `With()` on the controller's first real status. |
| `kubectl apply --dry-run=server` fails with `connection refused` right after install | The webhook Service has no endpoints yet, seconds after `rollout_wait`. | Retry; it is not a rejection. |
| A fixture's last event is the `ACTION`, with `succeeded: true` | The `Do()` state is also terminal and the controller echoes nothing of the action (a status reset). | End the flow on a later state only the action leads to (Actions and gates in `recorded-flow.md`). |
| A flow fails at create with `admission webhook ... denied the request` | The webhook rejects a spec value the Go types declare (CloneSet `OnDelete`). | Drop the rule branch, flow, and testdata for that value, then re-record every flow of the kind. Check manifests with `kubectl apply --dry-run=server` first. |
| Every exit code reads empty or 0 in a loop over frames | A pipeline reports its last command, and zsh has no `PIPESTATUS`. | Run the built `verify` unpiped, redirect its output, and read `$?`. |
| `Ran N of M Specs` with N below the number of `It`s | The flow file is `Ordered`; the first failure skipped the rest. | Re-run the skipped flows with `FLOW="<a>\|<b>"`. |
| A walk frame reads `Running=Initializing,Running` | Two flow predicates matched the frame. They mirror the status rules, so the rules likely overlap too. | Run karta-verify on that frame, fix the overlap (step 5), and re-record. |
| `generated files or module manifests are stale or untracked` | `validate` requires a clean tree and sees untracked files. | Commit the new files, fixtures included, before `make check`. |
| `make check` is silent for minutes | It is downloading the pinned tools missing from `bin/` (golangci-lint, goreleaser, controller-gen, go-licence-detector, setup-envtest and its control-plane binaries). | Expected. Run it in the background with its output in a log file. |

## Silent mistakes (valid but wrong)

These pass validation but behave incorrectly. Check them first when a definition
"works" but reports the wrong thing.

- Using `ownerName` instead of `ownerRef`. The field is `ownerRef`. A child with
  `ownerName` decodes with no owner and is only caught where the validator runs.
- Expecting a `referencedComponents` field. It does not exist. Model owned
  resources as child components; list other managed kinds under
  `additionalChildKinds`.
- Status conditions that do not match the workload's real API. The definition
  validates but status never resolves because the controller never sets those
  types. Verify against the CRD source or docs.
- A path evaluated against the wrong resource. Spec, scale, and status paths run
  against the workload object; selector and optimization paths run against pod
  manifests. A selector pointing at a workload field matches nothing.
- Listing a defined component's kind under `additionalChildKinds`. The list is
  for managed kinds, and duplicating a kind already modeled as a component is
  usually redundant. The validator does not reject it, though, and it is
  legitimate when a kind must also be declared for RBAC or owner traversal (for
  example a scaling-group kind that is both a component and an ancestor to walk).
  Duplicate only with that intent, not by accident.
- A role selector key copied from the nearest sample without checking the target
  controller's real pod labels. Role-label keys are operator-specific (PyTorchJob
  `training.kubeflow.org/replica-type` vs MPIJob `training.kubeflow.org/job-role`),
  so a copied key silently matches nothing. Read the controller's actual pod
  labels.
- Two role components matching the same pod label. A plain value match on a
  shared label is not mutually exclusive. Disambiguate by matching a key only one
  role carries, using key existence (omit `value`), as LeaderWorkerSet does for
  leader versus worker.
- Inventing a phase or condition for a controller that reports neither. Some
  controllers expose only status fields such as replica counts. A `byPhase` or
  `byConditions` rule then never matches. Map those states with `byExpression`
  over the real fields instead.
- Mapping to `Undefined`. It is the implicit no-match result, not a target to
  map. Map only the statuses the workload reports.
- `byConditions` with `status: "False"` for a condition the controller has not
  written yet. The matcher only sees conditions that exist, so the early frame
  reads `Undefined`. Use `byExpression` over `object.?status.?conditions.orValue([])` and test
  for the absence of a `True` entry.
- `Running` and `Suspended` both matching on a suspended frame. A controller
  that keeps the phase at Running while `.spec.suspend` is true needs a
  not-suspended expression ANDed into the `Running` and `Initializing` matchers.
- A multi-instance component that works on a one-element CR. A missing
  `instanceIds.read.expression` is invisible until the array has two entries. See the
  runtime error above.
- A recording filed under the Kubernetes version (`recorded_data/<op>/v1.34.0/`)
  instead of the operator version. `Fixture.Operator` does not equal the
  directory name under `hack/e2e/operators/`, so `operatorVersion()` found no
  entry in `.installed-versions-<cluster>` and fell back to the server version.
- A read projection accidentally exposed as writable. A computed total or merged
  fallback may have no safe write location. Omit `write` until the intended layer
  is explicit. When writes are supported, test the pointer and preserve fields
  that the typed SDK view does not contain. Raw Merge Patch maps preserve
  unmentioned fields; replacing an array replaces every entry.
