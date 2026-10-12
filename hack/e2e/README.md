<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta e2e cluster provisioner

This directory provisions a local kind cluster for the Karta e2e suite: it builds
and deploys the Karta operator, installs the dependencies the suite needs, and
installs the upstream workload operators it exercises. Each operator is smoke-tested
as it installs, so a broken install fails provisioning rather than a later run.

## Layout

```
hack/e2e/
  up.sh                 orchestrator: base + selected operators (install then verify)
  check-updates.sh       report upstream workload and infrastructure operator tags
  _updates.sh           update-check helpers, independent of cluster provisioning
  infra/
    <name>/             cert-manager, fake-gpu-operator, and KAI Scheduler
      install.sh        standalone: installs the infrastructure operator
      verify.sh         standalone: smoke-tests it via run_smoke
      check-updates.sh   standalone: report upstream tag updates
      smoke.yaml        the throwaway infrastructure smoke resources
    karta-operator/     the system under test, built from this checkout
      install.sh        standalone: installs Karta in the selected webhook route
      verify.sh         standalone: smoke-tests it via run_smoke
      smoke.yaml        the throwaway Karta the smoke test applies
      test.sh           runs the operator e2e against the current cluster
  down.sh               tear the cluster down (and its kubeconfig for named clusters)
  global.env            single source of truth for versions and runtime defaults
  kind-config.yaml      kind cluster shape (1 control-plane + 2 workers)
  operators/            the upstream workload operators it exercises
    _common.sh          shared helpers + GitHub Actions logging, sourced by every script
    <name>/
      install.sh        standalone: installs that workload operator (a subprocess)
      verify.sh         standalone: smoke-tests it via run_smoke
      check-updates.sh   standalone: report upstream tag updates
      smoke.yaml        the throwaway workload the smoke test applies
      <config>.yaml     optional co-located config (e.g. grove/values.yaml)
```

The shared tag checker lives in `hack/operator-updates/`, in the root Go module.
Its [README](../operator-updates/README.md) describes the CLI and release mapping
contract.

## Usage

```sh
make e2e-up                          # base + all operators
make e2e-up WORKLOADS="jobset lws"   # base + a subset (one provision, deps resolved once)
make e2e-up WORKLOADS="jobset"       # base + a single operator
make e2e-up WORKLOADS=none           # base only, no workload operators
make e2e-down                        # tear down
./hack/e2e/up.sh --list dynamo       # print the resolved plan and exit (dynamo pulls grove)
./hack/e2e/up.sh --list none         # print the base-only plan and exit
```

The base is the kind cluster, the fake-gpu-operator, and the Karta operator.
Selecting a subset keeps a run light, and `none` keeps only the base, which is what
the operator e2e wants. Dependencies are added automatically: kserve pulls
knative, dynamo pulls grove. `up.sh` installs and verifies external infrastructure first:
Fake GPU Operator is always included, cert-manager follows `CERT_MANAGER` and
webhook/dependency settings, and KAI Scheduler is included whenever Grove is
selected. Karta is installed and verified last from `infra/karta-operator/`.
`--list` shows both external infrastructure and workloads.

## Check for operator updates

```sh
make e2e-operators-check-latest-supported-versions                         # all workload and infra operators
make e2e-operators-check-latest-supported-versions OPERATORS="jobset infra" # selected operators or infra only
make e2e-operators-check-latest-supported-versions UPDATE_FLAGS=--fail-on-update
make e2e-operators-check-latest-supported-versions UPDATE_FLAGS=--include-prereleases
./hack/e2e/operators/kubeflow/check-updates.sh   # Training Operator and MPI Operator
./hack/e2e/infra/fake-gpu-operator/check-updates.sh
JOBSET_VERSION=v0.11.0 ./hack/e2e/operators/jobset/check-updates.sh
make test-e2e-updates                          # offline tests, also part of make check
make build-operator-updates                    # optional: build bin/operator-updates
```

Support status is not inferred from tag presence. This command currently checks
available release tags. Upstream support-policy evaluation and Karta compatibility
validation through installation, recording, and replay are separate steps.

Requires Bash, Git, the Go version from the root `go.mod`, and access to public
GitHub repositories. The checker uses only the Go standard library.
No cluster, Docker, Helm, GitHub CLI, or GitHub token is required. Version pins
come from `global.env` and keep their environment overrides. The checks only
read remote tags. They do not install anything or rewrite pins.

`_updates.sh` builds `bin/operator-updates` once per aggregate invocation and
shares that binary with the per-operator Bash wrappers. A standalone wrapper
builds it before running. Set `E2E_OPERATOR_UPDATES_BIN` to an absolute path to an
existing executable to reuse a build; that route does not require Go at runtime.
The wrappers run the binary directly so exit code 2 remains distinguishable.

The report shows `NEW`, `CURRENT`, `AHEAD`, or `ERROR`, the pin, the latest
matching tag, and links to tags and changes. `AHEAD` means the pin is newer than
the latest matching tag in the selected channel. The final summary names groups
with newer tags. A Kubeflow group includes MPI Operator; a Knative group includes
Kourier. Infrastructure checks include cert-manager, fake-gpu-operator, and
KAI Scheduler, which `up.sh` installs before Grove.

Stable tags are the default. Grove also includes alpha releases because it has
no stable release line; its release candidates require `--include-prereleases`.
The source mapping filters unrelated module and chart tags and excludes known
retracted KAI Scheduler tags. See [upstream release conventions](UPSTREAM_RELEASES.md)
for the evidence from cloned source repositories and compatibility notes.

Script exit codes are 0 for a successful report, 1 for a lookup or input error,
and 2 when `--fail-on-update` is set and newer tags exist. A lookup failure takes
precedence over detected updates. The aggregate continues other checks after an
error and marks its report incomplete. Make itself returns its standard nonzero
failure code when a script fails. Live network checks are opt-in; `make check`
runs only the offline detection tests.

A new tag is an update candidate, not proof that its release artifacts are
available or that the existing installer and fixtures support it. Review changes,
update pins deliberately, then install, verify, and record the affected flows.
The report covers upstream operators; Karta is built from this checkout. It does
not check CLI tools, Kubernetes node images, Ray runtime, or sidecar image pins.

## How up.sh runs an operator

For each selected external infrastructure operator, `up.sh` runs `infra/<name>/install.sh`
then `infra/<name>/verify.sh`. It uses the same runner for workload operators
under `operators/<name>/`. Both phases run as `bash <script>` subprocesses. The only contract is
the exit code: any non-zero exit is a failed install/smoke and fails provisioning
fast (the `main()` wrapper in the scripts is just convention). up.sh groups each
operator in the CI log (`::group::`) and writes an install summary table to the run's
Summary page (operator, version, install time, smoke time). Successful infrastructure
and workload versions are recorded in the per-cluster installed-version file.

Karta uses the same install/verify pattern under `infra/karta-operator/`, after
all external operators. It has no external version pin or update checker.
`make test-operator-e2e` runs its `test.sh` against the provisioned cluster.

The infrastructure smoke checks prove certificate issuance, a pod starting with
a simulated GPU, and a pod scheduled by KAI. They use `run_smoke` to clean up
throwaway resources after success or failure. `go test ./hack/e2e` exercises
planning, install/verify ordering, and failure propagation offline.

## Shared helpers (`operators/_common.sh`)

Sourcing `_common.sh` also loads `global.env`, so any script gets the version
pins in one step. Available helpers:

- `rollout_wait <ns> <resource> [timeout]` - wait for a rollout; resource includes
  the kind, e.g. `deploy/foo` or `statefulset/foo`.
- `apply_with_retry <file|url> [tries] [sleep] [kubectl args...]` - kubectl apply,
  retried past a warming webhook.
- `retry <tries> <sleep> <command...>` - run a command, retried past a transient.
- `run_smoke <manifest> <target> <wait-expr> [timeout] [ns]` - apply a throwaway
  resource, wait for the state, delete it. Used by every verify.sh.
- `preload_image <src-ref> <local-tag>` - pull an image and load it into kind.
  With Docker Desktop's containerd image store, `kind load docker-image` can fail
  on a multi-arch image with `ctr: content digest ... not found`. Skip the preload
  then and let the nodes pull the pinned image.
- `build_and_load_image <context-dir> <local-tag>` - build an image and load it.
- `ensure_secret <ns> <name> <k=v>...` - idempotently create/update a secret.
- Logging: `group`/`endgroup`, `notice`/`warn`/`fail`, `summary`. When to use each
  is documented at the top of `_common.sh`.

## Adding an operator

Every external workload and infrastructure operator requires installation,
smoke verification, and an executable `check-updates.sh`. The update checker
reports candidate versions; `verify.sh` still proves the installed operator can
run its smoke workload. Kubernetes builtins and Karta built from this checkout
have no upstream operator release to check and are exempt.

Install side (this directory):

1. Create `operators/<name>/install.sh` as a standalone script:

   ```sh
   #!/usr/bin/env bash
   # SPDX-License-Identifier: Apache-2.0
   # Copyright (c) 2026 NVIDIA Corporation
   set -euo pipefail
   MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
   # shellcheck source=/dev/null
   source "${MODULE_DIR}/../_common.sh"

   main() {
     echo "==> <name> ${<NAME>_VERSION}"
     # install with helm/kubectl; wait with rollout_wait; use the shared helpers;
     # reference co-located config via "${MODULE_DIR}/...".
   }

   main "$@"
   ```

   Apply upstream manifests with `kubectl apply --server-side` (plus
   `--force-conflicts` for a reused cluster). Large CRDs exceed the annotation
   size limit of a client-side apply.

2. Create `operators/<name>/smoke.yaml` (a throwaway workload) and
   `operators/<name>/verify.sh`:

   ```sh
   #!/usr/bin/env bash
   # SPDX + copyright, set -euo pipefail, MODULE_DIR, source ../_common.sh
   run_smoke "${MODULE_DIR}/smoke.yaml" "<plural>.<group>/<name>-smoke" "<wait-expr>" "<timeout>" default
   ```

   `<wait-expr>` is passed to `kubectl wait --for=`, so both `condition=Ready` and
   `jsonpath={.status.state}=ready` work.

3. Pin the version(s) in `global.env`, and add a `version_of` case in `up.sh` so
   the operator shows in the install summary.
4. Add an executable `operators/<name>/check-updates.sh` that sources
   `_updates.sh`. Inspect a local clone of each upstream component's release
   workflow or release guide to establish the tag repository, prefix, channel,
   and mapping to the installed chart or manifest. Document the evidence with
   pinned public source links in `UPSTREAM_RELEASES.md`. Register the group in
   `ALL_OPERATORS` in `check-updates.sh` and add offline Go cases under
   `hack/operator-updates/` for its repository, tag namespace, and any release
   exceptions. Run `make test-e2e-updates`.
5. Add `<name>` to `ALL_WORKLOADS` in `up.sh` (in install order), and a `deps_of`
   entry only if it depends on another operator.

Infrastructure follows the same module pattern under `infra/<name>/`: executable
`install.sh`, `verify.sh`, and `check-updates.sh`, plus `smoke.yaml`. Installation
and verification source `../../operators/_common.sh`. Add its version to
`version_of` and select it in `up.sh`'s infrastructure plan before its dependents;
workload installers must not install infrastructure themselves. Register its
update checker in both `ALL_OPERATORS` and the `infra` selector. The same
release-source documentation and offline test requirements apply. A new kind
served by an existing operator reuses its checker; extend it when installation
adds a separately versioned component.

The offline Go tests require checkers for workload installer modules and
infrastructure directories and check their aggregate registration. Include the
executable bit and registration in the same change as a new operator.

Before pushing: run `make test-e2e-updates` and `make lint-shell` (shellcheck), then
provision just that operator to confirm it installs and smoke-tests clean, for
example `make e2e-up WORKLOADS=<name>`. Run `make check` as required by the
contributing guide.
