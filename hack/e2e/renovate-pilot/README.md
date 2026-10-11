<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Renovate recording pilot

The pilot runs only in `AviadHayumi/workload-map`, on
`codex/e2e-renovate-pilot`. It has no schedule or automerge.

Renovate reads the JobSet pin from `hack/e2e/global.env` through a regex manager
and looks up GitHub releases. The eligible range ends at v0.12.0 because its
[versioned support table](https://github.com/kubernetes-sigs/jobset/blob/v0.12.0/README.md)
includes Kubernetes 1.34, matching the pinned kind image. Other operators need
their own release-policy review and install/record/replay runs.

## CI owns the recordings and commit

GitHub Actions runs pinned Renovate 44.151.0 natively with the pinned Go, kind,
kubectl, and Helm tools. The existing general Renovate configuration is ignored.
Renovate updates the pin, then runs the explicitly allowed `record.sh` through
`postUpgradeTasks` before creating its commit. The hook refuses to run outside
GitHub Actions or against an existing cluster.

The hook provisions a fresh kind cluster, installs JobSet, checks the ready
controller image and installed-version metadata, records all five JobSet flows,
and validates and replays their fixtures. It rejects stale files, version
fallbacks, missing flows, skipped selected specs, failed recordings, and dry runs.
It adds license headers inside CI and runs `make check` in a disposable clean
checkout of the candidate changes. That temporary validation commit stays local
to the runner and is discarded.

Only after all checks and cluster cleanup succeed does the hook write a manifest
of the exact pin and recording file hashes. Renovate includes those recordings
through `postUpgradeTasks.fileFilters` in the same bot commit as the version pin.
A local pre-push hook verifies the committed files against that manifest before
allowing the push. Post-upgrade errors therefore cannot publish an unchecked
pin or partial recordings. The workflow verifies the published commit again.

The workflow then publishes a successful status check on that commit, linking to
the recording run. With `E2E_OPERATOR_CREATE_PR=true` in the fork's Actions
variables, it also opens a draft PR with the temporary Actions token. Otherwise,
the verified bot branch and prepared PR body remain available for publication
through an authorized GitHub session. The
PR contains only the pin and five generated fixtures; its base branch contains
the pilot implementation. The author is `github-actions[bot]` because that is
the authentication used by this self-hosted Renovate run.

The fork must allow GitHub Actions to create pull requests. Its
`E2E_OPERATOR_TRACKING_ISSUE` Actions variable supplies the public issue URL,
which is checked to be open before running the pilot. Default repository
token permissions remain read-only; this workflow explicitly requests content,
PR, and status writes. No personal token or additional secret is used.

## Evidence and validation

The PR body and status check link to the Actions run. Artifacts contain
`recording.log`, `make-check.log`, JUnit, version defaults, controller image
digests, Kubernetes version, source provenance, file hashes, and the generated
recordings. Kubeconfig and credentials are excluded. The cluster is deleted on
success and failure, with a final workflow cleanup as a fallback.

Local checks do not run a cluster or record fixtures:

```sh
PILOT_BASE_BRANCH=codex/e2e-renovate-pilot PILOT_RUN=1-1 \
  renovate-config-validator --strict hack/e2e/renovate-pilot/config.cjs
actionlint .github/workflows/renovate-operator-pilot.yaml
make lint-shell
make check
```

The commit-verification tests use temporary local Git repositories and fabricated
fixtures to check failure handling without a Kubernetes cluster.
