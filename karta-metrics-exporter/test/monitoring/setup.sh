#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
export KUBECONFIG=${KUBECONFIG:?Set a dedicated monitoring kubeconfig path}
context=kind-exporter-monitoring
assets=${MONITORING_ASSETS:-/tmp/exporter-monitoring-assets}
kube() { kubectl --kubeconfig "$KUBECONFIG" --context "$context" "$@"; }
download() {
  mkdir -p "$assets"
  curl -fsSL --retry 3 https://raw.githubusercontent.com/projectcalico/calico/v3.31.0/manifests/calico.yaml -o "$assets/calico.yaml"
  curl -fsSL --retry 3 https://github.com/prometheus-operator/prometheus-operator/releases/download/v0.86.2/bundle.yaml -o "$assets/operator.yaml"
  (cd "$assets" && printf '%s\n' \
    '1990b1cec14002790718bb960b1b8445eb7b896cd16c32e952309f35a9026981  calico.yaml' \
    '2179f514c364b64d81f1ca0347274715b2982efe2d76a4fa65cb1271b05b8ff4  operator.yaml' | shasum -a 256 -c -)
}
case ${1:-} in
  cluster)
    download
    if kind get clusters | grep -Fxq exporter-monitoring; then
      echo 'exporter-monitoring already exists; reuse it or remove it explicitly' >&2; exit 1
    fi
    kind create cluster --name exporter-monitoring --kubeconfig "$KUBECONFIG" --config "$here/kind.yaml" --image kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a
    kube apply --server-side -f "$assets/calico.yaml"
    kube -n kube-system rollout status daemonset/calico-node --timeout=300s
    kube wait node --all --for=condition=Ready --timeout=300s
    ;;
  operator)
    test "$(kube get nodes -o jsonpath='{.items[*].metadata.name}')" = exporter-monitoring-control-plane
    download
    kube apply --server-side -f "$assets/operator.yaml"
    kube wait crd/servicemonitors.monitoring.coreos.com --for=condition=Established --timeout=120s
    kube -n default rollout status deployment/prometheus-operator --timeout=300s
    ;;
  stop)
    nodes=$(kind get nodes --name exporter-monitoring)
    test -n "$nodes"
    while IFS= read -r node; do docker stop "$node"; done <<< "$nodes"
    ;;
  *) echo 'usage: setup.sh cluster|operator|stop' >&2; exit 2 ;;
esac
