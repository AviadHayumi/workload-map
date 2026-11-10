#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
CLUSTER_NAME="${CEL_PLUGINS_CLUSTER:-karta-cel-e2e}"
KEEP_CLUSTER="${KEEP_CLUSTER:-0}"
REUSE_CLUSTER="${REUSE_CLUSTER:-0}"
# shellcheck source=hack/e2e/global.env
source "${REPO_ROOT}/hack/e2e/global.env"

case "${CLUSTER_NAME}" in
  karta-cel-*) ;;
  *) echo "CEL_PLUGINS_CLUSTER must start with karta-cel-" >&2; exit 1 ;;
esac
for tool in docker kind kubectl go make; do
  command -v "${tool}" >/dev/null || { echo "missing required tool: ${tool}" >&2; exit 1; }
done

RUN_DIR="$(mktemp -d "${TMPDIR:-/tmp}/karta-cel-e2e.XXXXXX")"
ARTIFACTS="${REPO_ROOT}/.artifacts/cel-plugins/$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "${ARTIFACTS}"
created=0
cleanup() {
  local result=$?
  if [[ ${result} != 0 ]]; then
    echo "Failure diagnostics: ${ARTIFACTS}" >&2
    kubectl --request-timeout=10s --kubeconfig "${RUN_DIR}/kubeconfig" -n "${CEL_PLUGINS_E2E_NAMESPACE:-default}" \
      get deployments,cronjobs,pods,services,pvc,endpointslices,events -o yaml \
      > "${ARTIFACTS}/resources.yaml" 2>&1 || true
    kubectl --request-timeout=10s --kubeconfig "${RUN_DIR}/kubeconfig" -n "${CEL_PLUGINS_E2E_NAMESPACE:-default}" \
      logs deployment/cel-plugins --all-containers --tail=300 --pod-running-timeout=5s \
      > "${ARTIFACTS}/controller.log" 2>&1 || true
  fi
  if [[ ${created} == 1 && ${KEEP_CLUSTER} != 1 ]]; then
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
  rm -rf "${RUN_DIR}"
  exit "${result}"
}
trap cleanup EXIT

if kind get clusters | grep -Fxq "${CLUSTER_NAME}"; then
  [[ ${REUSE_CLUSTER} == 1 ]] || {
    echo "cluster ${CLUSTER_NAME} exists; choose another name or explicitly set REUSE_CLUSTER=1" >&2
    exit 1
  }
  kind get kubeconfig --name "${CLUSTER_NAME}" > "${RUN_DIR}/kubeconfig"
else
  [[ ${REUSE_CLUSTER} == 0 ]] || { echo "requested reuse cluster does not exist" >&2; exit 1; }
  created=1
  kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_NODE_IMAGE}" \
    --config "${SCRIPT_DIR}/kind.yaml" --kubeconfig "${RUN_DIR}/kubeconfig" --wait 120s
fi
[[ "$(kubectl --kubeconfig "${RUN_DIR}/kubeconfig" config current-context)" == "kind-${CLUSTER_NAME}" ]]
[[ "$(kubectl --kubeconfig "${RUN_DIR}/kubeconfig" get nodes -l plugins.karta.dev/pool=blue -o name | wc -l | tr -d ' ')" == 1 ]]
[[ "$(kubectl --kubeconfig "${RUN_DIR}/kubeconfig" get nodes -l plugins.karta.dev/pool=green -o name | wc -l | tr -d ' ')" == 1 ]]

ARCH="$(kubectl --kubeconfig "${RUN_DIR}/kubeconfig" get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')"
{
  kind version
  kubectl --kubeconfig "${RUN_DIR}/kubeconfig" version
  docker version --format '{{.Server.Version}}'
} > "${ARTIFACTS}/versions.txt"
docker image inspect --format '{{json .RepoTags}} {{.Id}} {{json .RepoDigests}}' \
  "${KIND_NODE_IMAGE}" > "${ARTIFACTS}/images.txt"
make -C "${REPO_ROOT}" cel-plugins-image CEL_PLUGINS_ARCH="${ARCH}"
for image in karta-cel-plugins:local busybox:1.37.0; do
  if ! docker image inspect "${image}" >/dev/null 2>&1; then
    docker pull --platform "linux/${ARCH}" "${image}"
  fi
  docker image inspect --format '{{json .RepoTags}} {{.Id}} {{json .RepoDigests}}' \
    "${image}" >> "${ARTIFACTS}/images.txt"
  docker image save --platform "linux/${ARCH}" "${image}" -o "${RUN_DIR}/image.tar"
  kind load image-archive "${RUN_DIR}/image.tar" --name "${CLUSTER_NAME}"
done

export CEL_PLUGINS_KUBECONFIG="${RUN_DIR}/kubeconfig"
export CEL_PLUGINS_CLUSTER="${CLUSTER_NAME}"
CEL_PLUGINS_E2E_NAMESPACE="cel-plugins-e2e-$(date -u +%s)-$$"
export CEL_PLUGINS_E2E_NAMESPACE
export CEL_PLUGINS_ARTIFACTS="${ARTIFACTS}"
cd "${REPO_ROOT}/docs/examples/cel-plugins"
GOWORK=off go test -tags=e2e -run '^TestPluginsE2E$' -count=1 -v -timeout=20m ./... 2>&1 | tee "${ARTIFACTS}/test.log"
echo "End-to-end checks passed; evidence: ${ARTIFACTS}/test.log"
