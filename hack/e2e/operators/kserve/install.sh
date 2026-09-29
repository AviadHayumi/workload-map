#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# KServe, Serverless on Knative + Kourier. Depends on knative (see deps_of in
# up.sh). Ships a config patch (disable-istio-vh.yaml).
# shellcheck disable=SC2154  # KSERVE_VERSION/KUBE_RBAC_PROXY_VERSION come from global.env via _common.sh
set -euo pipefail
MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${MODULE_DIR}/../_common.sh"

main() {
  echo "==> KServe ${KSERVE_VERSION} (Serverless on Knative + Kourier)"

  # Releases before v0.13 ship CRDs with a placeholder conversion caBundle that
  # newer Kubernetes rejects at apply time; they only run on their era clusters.
  case "${KSERVE_VERSION}" in
    v0.1[0-2].*|v0.[0-9].*) require_k8s_max 28 "KServe ${KSERVE_VERSION}" || exit 1 ;;
  esac
  local base="https://github.com/kserve/kserve/releases/download/${KSERVE_VERSION}"
  # --force-conflicts: cert-manager-cainjector owns the webhook caBundle fields,
  # and on a reused cluster our own set-image/patch steps below already own the
  # rbac-proxy image and inferenceservice-config ingress. Reclaim them here; the
  # set-image and patch steps that follow re-assert those overrides.
  if curl -fsIL -o /dev/null "${base}/kserve.yaml"; then
    kubectl apply --server-side --force-conflicts -f "${base}/kserve.yaml"
  else
    # Some releases (v0.17.x) publish Helm charts only, no kserve.yaml manifest.
    # The resources chart ships the ClusterServingRuntimes the manifest path
    # applies separately below.
    helm upgrade -i kserve-crd "${base}/helm-chart-kserve-crd-${KSERVE_VERSION}.tgz" \
      -n kserve --create-namespace >/dev/null
    helm upgrade -i kserve "${base}/helm-chart-kserve-resources-${KSERVE_VERSION}.tgz" \
      -n kserve >/dev/null
  fi
  # Upstream pins gcr.io/kubebuilder/kube-rbac-proxy:${KSERVE_VERSION}, a tag that
  # registry no longer serves; the sidecar only guards metrics, so repoint it to a
  # maintained image, otherwise the pod never goes Ready and the webhook has no
  # endpoints.
  kubectl set image deployment/kserve-controller-manager -n kserve \
    kube-rbac-proxy="quay.io/brancz/kube-rbac-proxy:${KUBE_RBAC_PROXY_VERSION}"
  # Serverless KServe defaults to creating Istio VirtualServices; without Istio the
  # reconcile errors and PredictorReady/RoutesReady never go True. Route through
  # Knative/Kourier instead.
  kubectl patch cm inferenceservice-config -n kserve --type merge \
    --patch-file "${MODULE_DIR}/disable-istio-vh.yaml"
  rollout_wait kserve deploy/kserve-controller-manager 240s
  # ClusterServingRuntimes are validated by the webhook, so apply them only after
  # the controller pod is Ready; retry briefly in case the webhook is still warming.
  # Releases before v0.12 ship them as kserve-runtimes.yaml, and chart-only
  # releases ship them inside the resources chart instead.
  if curl -fsIL -o /dev/null "${base}/kserve-cluster-resources.yaml"; then
    apply_with_retry "${base}/kserve-cluster-resources.yaml" \
      5 10 --server-side --force-conflicts
  elif curl -fsIL -o /dev/null "${base}/kserve-runtimes.yaml"; then
    apply_with_retry "${base}/kserve-runtimes.yaml" \
      5 10 --server-side --force-conflicts
  fi
}

main "$@"
