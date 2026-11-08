#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
set -euo pipefail

LAB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$LAB_DIR/../../.." && pwd)"
CLUSTER=kyverno-exporter-tutorial
ARTIFACTS="$ROOT/.artifacts/$CLUSTER"
export KUBECONFIG="$ARTIFACTS/kubeconfig"
KIND_IMAGE=kindest/node:v1.34.3
KYVERNO_CHART_VERSION=3.9.1
EXPORTER_IMAGE=dsx-workload-exporter:kyverno-tutorial

for tool in docker kind kubectl helm; do
  command -v "$tool" >/dev/null || { printf 'Missing required tool: %s\n' "$tool" >&2; exit 1; }
done
mkdir -p "$ARTIFACTS"
if kind get clusters | grep -Fxq "$CLUSTER"; then
  kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
else
  kind create cluster --name "$CLUSTER" --image "$KIND_IMAGE" \
    --kubeconfig "$KUBECONFIG" --wait 120s
fi

kubectl --request-timeout=30s get --raw /readyz >/dev/null

GO_VERSION="$(awk '/^go / {print $2; exit}' "$ROOT/dsx-workload-exporter/go.mod")"
docker build --build-arg "GO_VERSION=$GO_VERSION" \
  -f "$ROOT/dsx-workload-exporter/Dockerfile" -t "$EXPORTER_IMAGE" "$ROOT"
# Save one platform: containerd image stores can hold incomplete multi-arch indexes.
PLATFORM="linux/$(docker version --format '{{.Server.Arch}}')"
for image in "$EXPORTER_IMAGE" quay.io/prometheus/prometheus:v3.14.0 busybox:1.37.0; do
  docker image inspect --platform "$PLATFORM" "$image" >/dev/null 2>&1 ||
    docker pull --platform "$PLATFORM" "$image"
  docker image save --platform "$PLATFORM" "$image" -o "$ARTIFACTS/image.tar"
  kind load image-archive "$ARTIFACTS/image.tar" --name "$CLUSTER"
done
rm -f "$ARTIFACTS/image.tar"

kubectl apply -f "$LAB_DIR/manifests/namespaces.yaml"
helm upgrade --install dsx-workload "$ROOT/charts/karta" \
  --namespace karta-system --create-namespace --skip-crds \
  -f "$LAB_DIR/manifests/exporter-values.yaml" --wait --timeout 5m
# A reused local image tag does not change the Deployment template.
kubectl -n karta-system rollout restart deployment/dsx-workload-exporter
kubectl -n karta-system rollout status deployment/dsx-workload-exporter --timeout=180s
kubectl apply -f "$LAB_DIR/manifests/prometheus-rules.yaml"
kubectl apply -f "$LAB_DIR/manifests/prometheus.yaml"
# Reload ConfigMap changes when repeating setup. Lab history is ephemeral.
kubectl -n monitoring rollout restart deployment/prometheus
kubectl -n monitoring rollout status deployment/prometheus --timeout=180s
helm upgrade --install kyverno kyverno --repo https://kyverno.github.io/kyverno/ \
  --version "$KYVERNO_CHART_VERSION" --namespace kyverno --create-namespace \
  -f "$LAB_DIR/manifests/kyverno-values.yaml" --wait --timeout 5m
kubectl apply -f "$LAB_DIR/manifests/kyverno-rbac.yaml"
printf '\nCluster is ready. Use this isolated kubeconfig:\nexport KUBECONFIG=%q\n' "$KUBECONFIG"
