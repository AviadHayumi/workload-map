#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# k8s-nim-operator with a fictive CPU NIM image (no GPU or NGC token needed). Ships
# an image build context (image/). The chart is fetched from the pinned upstream
# git tag. Its development image default is overridden with the matching release
# so the controller and installed CRDs use the same API schema.
# shellcheck disable=SC2154  # NIM_OPERATOR_VERSION comes from global.env via _common.sh
set -euo pipefail
MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${MODULE_DIR}/../_common.sh"

main() {
  echo "==> fake NIM image + k8s-nim-operator"
  build_and_load_image "${MODULE_DIR}/image" nim-cpu:e2e
  local image_repository="nvcr.io/nvidia/cloud-native/k8s-nim-operator"
  local image_tag="${NIM_OPERATOR_VERSION}"
  # The source chart defaults to mutable release-3.0. Its newer controller writes
  # fields absent from the v3.0.2 CRDs, causing a NIMCache helper recreation loop.
  # Match the published 3.0.2 chart, and pin that release's multi-platform image.
  # https://helm.ngc.nvidia.com/nvidia/charts/k8s-nim-operator-3.0.2.tgz
  if [ "${NIM_OPERATOR_VERSION}" = v3.0.2 ]; then
    image_tag="${image_tag}@sha256:28a83f7f3f0322ac721b81385b0a7e1639286226564035809e07802f424fb7b8"
  fi
  # The dev chart ships incomplete RBAC (cannot list computedomains/ingress/hpa/lws ->
  # cache-sync crashloop), so grant the operator broad access on this throwaway cluster.
  kubectl create clusterrolebinding k8s-nim-operator-admin \
    --clusterrole=cluster-admin --serviceaccount=nim-operator:k8s-nim-operator \
    --dry-run=client -o yaml | kubectl apply -f -
  # Install only when absent: the chart's pre-upgrade CRD-migration hook Job
  # crashloops when re-run, so plain "helm upgrade -i" is not idempotent here.
  if helm status k8s-nim-operator -n nim-operator >/dev/null 2>&1; then
    echo "    k8s-nim-operator release already present; skipping reinstall"
  else
    # Fetch the chart and CRDs from the pinned source tag instead of vendoring
    # them. The tag tarball extracts to k8s-nim-operator-<version>/.
    # src is not local so the EXIT trap can resolve it under set -u (cleans up even
    # if a step below fails).
    local chart="" d
    src="$(mktemp -d)"
    trap 'rm -rf "${src}"' EXIT
    curl -fsSL "https://github.com/NVIDIA/k8s-nim-operator/archive/refs/tags/${NIM_OPERATOR_VERSION}.tar.gz" \
      | tar -xz -C "${src}"
    for d in "${src}"/k8s-nim-operator-*/deployments/helm/k8s-nim-operator; do
      [ -d "${d}" ] && chart="${d}"
    done
    if [ -z "${chart}" ]; then
      echo "error: k8s-nim-operator Helm chart not found in the ${NIM_OPERATOR_VERSION} tarball (upstream layout changed?)" >&2
      exit 1
    fi
    helm install k8s-nim-operator "${chart}" -n nim-operator --create-namespace \
      --set-string operator.image.repository="${image_repository}" \
      --set-string operator.image.tag="${image_tag}" >/dev/null
  fi
  # An existing release may have different CRDs. Do not silently replace only
  # its controller and recreate the same schema mismatch in the other direction.
  local installed_image
  installed_image="$(kubectl -n nim-operator get deployment/k8s-nim-operator \
    -o 'jsonpath={.spec.template.spec.containers[?(@.name=="manager")].image}')"
  if [ "${installed_image}" != "${image_repository}:${image_tag}" ]; then
    echo "error: existing NIM operator image does not match ${NIM_OPERATOR_VERSION}; use a fresh e2e cluster or align its chart, CRDs and image before retrying" >&2
    exit 1
  fi
  rollout_wait nim-operator deploy/k8s-nim-operator
  # Dummy NGC secret: the operator injects NGC_API_KEY from it; the fictive image ignores it.
  ensure_secret default ngc-secret NGC_API_KEY=dummy-not-a-real-token
}

main "$@"
