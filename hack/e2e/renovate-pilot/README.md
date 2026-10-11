<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Renovate recording pilot

This experiment runs only in `AviadHayumi/workload-map`, on
`codex/e2e-renovate-pilot`. It does not enable scheduled updates or automerge.

Renovate reads the JobSet pin from `hack/e2e/global.env` through a regex manager
and looks up GitHub releases. The eligible range ends at v0.12.0 because its
[versioned support table](https://github.com/kubernetes-sigs/jobset/blob/v0.12.0/README.md)
includes Kubernetes 1.34, matching the pinned kind image. This is a reviewed
candidate bound, not an automatic claim that every new release is supported.

The workflow uses a new Renovate branch for every run attempt and defers PR
creation until checks succeed. The fork's Actions token cannot create PRs under
its current settings. After the experiment succeeds, the recordings can be
committed and a PR opened in the fork through an authorized GitHub session.
No personal token or additional repository secret is required by this workflow.

The recording job has read-only repository permissions. It verifies the only
Renovate change is JobSet v0.10.1 to v0.12.0, installs that pin into a fresh kind
cluster, checks the running controller image, records all five JobSet flows,
and replays the fixtures offline. It rejects existing candidate fixtures,
Kubernetes-version fallback metadata, missing flows, skipped JobSet specs,
failed recordings, and dry runs. The temporary cluster is deleted afterward.

The run uploads the pin diff, all version defaults, controller image digests,
Kubernetes version, source commit IDs, JUnit report, and successful recordings.
Failed runs upload available diagnostics. Kubeconfig and credentials are not
included in the evidence bundle. A successful JobSet pilot establishes this
pipeline for JobSet on this Kubernetes version; other operators still need
release policy review and their own install/record/replay runs.

Local validation:

```sh
PILOT_BASE_BRANCH=codex/e2e-renovate-pilot PILOT_RUN=1-1 \
  renovate-config-validator --strict hack/e2e/renovate-pilot/config.cjs
actionlint .github/workflows/renovate-operator-pilot.yaml
make check
```

The workflow pins Renovate 44.151.0 by container digest and every action by
commit. The existing repository Renovate configuration is ignored for this
isolated run so unrelated dependencies and release commands are unaffected.

## Verified run

[Fork Actions run 38174039025](https://github.com/AviadHayumi/workload-map/actions/runs/38174039025)
succeeded on 2026-10-11 (UTC). Renovate created commit
`4302432479e1a1f0c1342143fc0c5c0e66d503f5`, changing only JobSet v0.10.1 to
v0.12.0. Installation, all five live flows, recording validation, and offline
replay passed on Kubernetes v1.34.0. The temporary cluster was deleted.

The running JobSet controller image resolved to
`registry.k8s.io/jobset/jobset@sha256:e75536f1135b7bb2f19f8c3b620782fbdd9091d73398e3a272f9a5fed322980d`.
The five recordings are committed under
`test/e2e/recorded_data/jobset/v0.12.0/` with license headers added after download.
