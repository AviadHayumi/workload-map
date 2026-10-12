<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Upstream operator releases

Reviewed on 2026-10-11 against local Git clones of every operator repository
and the separate KubeRay chart repository. The pinned public source links below
record the release logic used to select tags. Run the update checks for current
results; this document does not keep a snapshot of the latest versions.

## Selection rules

The checks compare the effective pins in [global.env](global.env) with upstream
Git tags and link to the changes between them. They report candidates for review
without changing pins, installing operators, or contacting Kubernetes.

Stable semantic versions are selected by default. Development, nightly, release
candidate, Go module, and unrelated chart tags are excluded. Pass
`--include-prereleases` to include prereleases in the selected tag namespace.
Grove also includes root `vX.Y.Z-alpha.N` releases by default because that is its
published release channel. Grove release candidates still require the explicit
prerelease option. Numeric prerelease counters such as `rc9` and `rc10` follow
upstream's numeric promotion order. Grove's `alpha.N-rcM` sorts before the
corresponding `alpha.N` release.

KAI Scheduler's retracted `v0.20.0` and `v0.20.1` tags are excluded. Its
[upstream retractions](https://github.com/kai-scheduler/KAI-Scheduler/blob/173748a0b254531a5054374580e34ec8b9d839c8/go.mod#L220)
identify these as accidental releases. They must not outrank valid releases in
semantic version ordering, even if an old chart remains available.

A newer tag does not establish artifact availability, Kubernetes compatibility,
or a safe upgrade path. Some projects tag before publishing release assets or
charts. Review the linked changes and installer assumptions before updating a
pin. Branch commits without a new tag are outside the check's scope.

## Component mapping

`X.Y.Z` is the semantic version after the exact prefix in the tag column.
Serving and Kourier have separate pins, as do Kubeflow Training Operator and MPI
Operator. Their shared installation scripts check both components.

| Component | Pin | Tag repository | Selected tag namespace |
| --- | --- | --- | --- |
| cert-manager | `CERT_MANAGER_VERSION` | `cert-manager/cert-manager` | `vX.Y.Z` |
| Fake GPU Operator | `FAKE_GPU_VERSION` | `run-ai/fake-gpu-operator` | `vX.Y.Z` |
| KAI Scheduler | `KAI_SCHEDULER_VERSION` | `kai-scheduler/KAI-Scheduler` | `vX.Y.Z`, excluding retracted tags |
| Dynamo | `DYNAMO_VERSION` | `ai-dynamo/dynamo` | `vX.Y.Z` |
| Grove | `GROVE_VERSION` | `ai-dynamo/grove` | `vX.Y.Z` and `vX.Y.Z-alpha.N` |
| JobSet | `JOBSET_VERSION` | `kubernetes-sigs/jobset` | `vX.Y.Z` |
| LeaderWorkerSet | `LWS_VERSION` | `kubernetes-sigs/lws` | `vX.Y.Z` |
| KubeRay Operator | `KUBERAY_VERSION` | `ray-project/kuberay-helm` | `kuberay-operator-X.Y.Z` |
| Kubeflow Training Operator | `KUBEFLOW_VERSION` | `kubeflow/trainer` | `vX.Y.Z` |
| MPI Operator | `MPI_OPERATOR_VERSION` | `kubeflow/mpi-operator` | `vX.Y.Z` |
| Knative Serving | `KNATIVE_VERSION` | `knative/serving` | `knative-vX.Y.Z` |
| Kourier | `KOURIER_VERSION` | `knative-extensions/net-kourier` | `knative-vX.Y.Z` |
| KServe | `KSERVE_VERSION` | `kserve/kserve` | `vX.Y.Z` |
| Milvus Operator | `MILVUS_OPERATOR_VERSION` | `zilliztech/milvus-operator` | `milvus-operator-X.Y.Z` |
| NIM Operator | `NIM_OPERATOR_VERSION` | `NVIDIA/k8s-nim-operator` | `vX.Y.Z` |

The scope is installed third-party operators and their serving dependencies.
Ray runtime images, CLI tools, Kubernetes node images, and locally built Karta
are not separate operator release checks.

## Release source evidence

- cert-manager: the [manifest build](https://github.com/cert-manager/cert-manager/blob/9266af4308fc1c7724cb5320e37aded900c94e62/make/manifests.mk)
  produces `cert-manager.yaml`. Installation downloads that asset from the
  selected `vX.Y.Z` release.
- Fake GPU Operator: the [publication workflow](https://github.com/run-ai/fake-gpu-operator/blob/0901c8fc9c235e5b2ef8a2876fb89db1f7af23f4/.github/workflows/ci.yml)
  strips the leading `v` from release tags for the OCI chart version and app
  version. The source chart's placeholder version and branch chart versions
  are not release selectors.
- KAI Scheduler: the [artifact workflow](https://github.com/kai-scheduler/KAI-Scheduler/blob/173748a0b254531a5054374580e34ec8b9d839c8/.github/workflows/push-artifacts.yaml)
  uses the release tag as the chart version and publishes a version alias
  without `v`. The repository moved from the NVIDIA organization to
  `kai-scheduler`.
- Dynamo: the [release artifact guide](https://github.com/ai-dynamo/dynamo/blob/bea55a595a83a84b76183d44516de05f9ea1b2d0/docs/fern/pages/reference/general/release-artifacts.mdx)
  maps stable source releases to `dynamo-platform` chart versions without `v`.
  The [release workflow](https://github.com/ai-dynamo/dynamo/blob/bea55a595a83a84b76183d44516de05f9ea1b2d0/.github/workflows/release.yml)
  also handles release candidate and nightly builds. Source `Chart.yaml` can
  describe an unreleased version.
- Grove: the [release workflow](https://github.com/ai-dynamo/grove/blob/f038137374627c4921b398ab395d8789de686e89/.github/workflows/release.yaml)
  publishes artifacts from the root release tag, then creates separate
  `operator/`, `operator/api/`, and `scheduler/api/` Go module tags. The
  [artifact workflow](https://github.com/ai-dynamo/grove/blob/f038137374627c4921b398ab395d8789de686e89/.github/workflows/push-artifacts.yaml)
  passes the root tag through to the OCI chart version.
- JobSet: the [release checklist](https://github.com/kubernetes-sigs/jobset/blob/1dc6eec96f32943d2d42803d7a5d00aa8934ca23/.github/ISSUE_TEMPLATE/new-release.md)
  distinguishes release tags from `-devel` tags and publishes generated
  `manifests.yaml` as a release asset. Karta installs that asset.
- LeaderWorkerSet: the [release checklist](https://github.com/kubernetes-sigs/lws/blob/47c7659ab4a9820ea24c467b41974357c674b154/.github/ISSUE_TEMPLATE/NEW_RELEASE.md)
  creates version tags and uploads generated artifacts. Its
  [artifact target](https://github.com/kubernetes-sigs/lws/blob/47c7659ab4a9820ea24c467b41974357c674b154/Makefile#L347)
  generates the `manifests.yaml` used by the installer.
- KubeRay Operator: the [operator release process](https://github.com/ray-project/kuberay/blob/fc7532b34f96be619c62dc840e439259647ebbee/docs/development/release.md)
  and [chart release guide](https://github.com/ray-project/kuberay/blob/fc7532b34f96be619c62dc840e439259647ebbee/docs/release/helm-chart.md)
  publish Helm charts through a separate repository. Its
  [chart publication workflow](https://github.com/ray-project/kuberay-helm/blob/51711514ae948c9bea2c2440bdee3708f5625ee5/.github/workflows/chart-release.yaml)
  creates chart-specific releases. Only `kuberay-operator-` tags correspond to
  the installed chart; Ray cluster and API server chart tags are unrelated.
- Kubeflow Training Operator: the [release policy](https://github.com/kubeflow/trainer/blob/658725bdae5c7e3a0854d93ac6ff492e109a22ab/RELEASE.md)
  defines stable and release candidate version tags. The checker reports the
  latest stable version across major versions. The
  [v2 migration guide](https://github.com/kubeflow/trainer/blob/658725bdae5c7e3a0854d93ac6ff492e109a22ab/docs/operator-guides/migration.md)
  explains that Trainer v2 replaces `PyTorchJob`, `TFJob`, and `MPIJob` with
  `TrainJob` and training runtime APIs. Karta's existing standalone installer
  exercises v1 `PyTorchJob`, so a v2 result requires migration work.
- MPI Operator: the [installation guide](https://github.com/kubeflow/mpi-operator/blob/b3b5b84250ddf86c7de58bd166fc506945364743/README.md)
  selects `deploy/v2beta1/mpi-operator.yaml` from a version tag. The
  [image workflow](https://github.com/kubeflow/mpi-operator/blob/b3b5b84250ddf86c7de58bd166fc506945364743/.github/workflows/mpi-operator-docker-image-publish.yml)
  builds release images from `v*` tags. This is independent of the legacy
  Training Operator's bundled MPI API.
- Knative Serving: the [release helper](https://github.com/knative/serving/blob/f2153bddfbb28a9c8719d42ce47bac482b4e8c74/vendor/knative.dev/hack/release.sh#L733)
  creates `knative-` release tags separately from Go module tags. The
  [manifest generator](https://github.com/knative/serving/blob/f2153bddfbb28a9c8719d42ce47bac482b4e8c74/hack/generate-yamls.sh)
  produces `serving-core.yaml` and `serving-crds.yaml` for installation.
- Kourier: the [release script](https://github.com/knative-extensions/net-kourier/blob/37a0ffbefca5eca888c3bbe36c3dcb9e8e1e8332/hack/release.sh)
  generates `kourier.yaml` using the Knative release machinery. Its patch
  releases can differ from Serving's. The old `knative/net-kourier` repository
  redirects to `knative-extensions/net-kourier`.
- KServe: the [release script](https://github.com/kserve/kserve/blob/b81ab1417cde989b31fce242002f635427aa2ad9/hack/release/create-release.sh)
  distinguishes stable tags from release candidates. The
  [preparation workflow](https://github.com/kserve/kserve/blob/b81ab1417cde989b31fce242002f635427aa2ad9/.github/workflows/prepare-release.yml)
  creates tags before publishing the draft release. Its
  [manifest generator](https://github.com/kserve/kserve/blob/b81ab1417cde989b31fce242002f635427aa2ad9/hack/release/generate-install.sh)
  can change the set of installation assets, so newer tags need an installer
  and Knative dependency review.
- Milvus Operator: the [chart release workflow](https://github.com/zilliztech/milvus-operator/blob/3ae2cbf219db098a588552f05902c80fe82ebf29/.github/workflows/release.yml)
  uses chart-releaser. The installed Helm chart is identified by
  `milvus-operator-` tags in the same repository. Application `v` tags and
  `config-tool-` tags do not identify that chart.
- NIM Operator: the [chart packaging target](https://github.com/NVIDIA/k8s-nim-operator/blob/53173fde86c7252215aae44750f2636c26332e73/Makefile#L170)
  derives chart and app versions from `VERSION`, stripping `v`. Karta reads
  chart sources from a Git tag archive. The chart's checked-in version can lag
  the release tag, so `Chart.yaml` alone is not the release selector.

When adding an operator or changing its installer, inspect a local clone of the
upstream release sources, confirm the installed artifact's tag namespace, and
update the component check and this mapping together.
