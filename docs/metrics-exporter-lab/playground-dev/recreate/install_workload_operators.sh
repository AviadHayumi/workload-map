#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
set -euo pipefail
: "${DEMO_CONTEXT:?Set DEMO_CONTEXT to the target Kubernetes context}"
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
k() { kubectl --context "$DEMO_CONTEXT" "$@"; }
h() { helm --kube-context "$DEMO_CONTEXT" "$@"; }

# Intended for a fresh test cluster; inspect existing operator ownership first.
k apply --server-side -f operators/cert-manager.yaml
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  k -n cert-manager rollout status "deployment/$d" --timeout=5m
done
k apply --server-side -f operators/jobset.yaml
k apply --server-side -f operators/lws.yaml
h upgrade -i kuberay-operator charts/kuberay-operator-1.6.2.tgz -n ray --create-namespace --wait --timeout 5m
k apply --server-side -f operators/training-pytorch-only.yaml
k apply --server-side -f operators/mpi.yaml
k -n jobset-system rollout status deployment/jobset-controller-manager --timeout=5m
k -n lws-system rollout status deployment/lws-controller-manager --timeout=5m
k -n kubeflow rollout status deployment/training-operator --timeout=5m
k -n mpi-operator rollout status deployment/mpi-operator --timeout=5m
h upgrade -i grove-operator charts/grove-charts-v0.1.0-alpha.10-rc1.tgz -n grove-system --create-namespace -f operators/grove-values.yaml --wait --timeout 5m
h upgrade -i dynamo-platform charts/dynamo-platform-1.2.1.tgz -n dynamo-system --create-namespace --set global.etcd.install=true --set global.grove.enabled=true --set global.grove.install=false --set global.kai-scheduler.install=false --set global.kai-scheduler.enabled=false --wait --timeout 8m
h upgrade -i k8s-nim-operator charts/k8s-nim-operator-3.0.2.tgz -n nim-operator --create-namespace --set-string operator.image.tag='v3.0.2@sha256:28a83f7f3f0322ac721b81385b0a7e1639286226564035809e07802f424fb7b8' --wait --timeout 5m
k apply --server-side -f operators/knative-crds.yaml
k apply --server-side -f operators/knative-core.yaml
for d in activator autoscaler controller webhook; do
  k -n knative-serving rollout status "deployment/$d" --timeout=5m
done
k apply --server-side -f operators/kourier.yaml
k -n kourier-system patch service kourier --type merge -p '{"spec":{"type":"ClusterIP"}}'
k -n knative-serving patch configmap/config-network --type merge -p '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'
k apply --server-side -f operators/kserve.yaml
k -n kserve set image deployment/kserve-controller-manager kube-rbac-proxy=quay.io/brancz/kube-rbac-proxy:v0.18.0
k -n kserve patch configmap/inferenceservice-config --type merge --patch-file operators/kserve-no-istio.yaml
k -n kserve rollout status deployment/kserve-controller-manager --timeout=5m
k apply --server-side -f operators/kserve-cluster-resources.yaml
h upgrade -i milvus-operator charts/milvus-operator-1.3.7.tgz -n milvus-operator --create-namespace --wait --timeout 5m
