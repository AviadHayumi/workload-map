#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Record one operator version for the real-data matrix: install the operator at
# that version on the current cluster, verify it, record its flows, and file
# the fixtures under real-data/<operator>/<version>/. A version that cannot
# install or record is appended to real-data/SKIPPED.md with the reason.
#
#   hack/e2e/record-matrix.sh [--fresh-cluster] <operator> <version> [<kourier-version>]
#   hack/e2e/record-matrix.sh [--fresh-cluster] <operator> all
#   hack/e2e/record-matrix.sh <operator> pin
#
# "all" walks the operator's list in supported-versions.env, newest first, and
# reinstalls the pin at the end. "pin" reinstalls the version pinned in
# global.env without recording, to leave the cluster back on the supported set
# after a matrix run. --fresh-cluster provisions a throwaway
# karta-matrix-<operator> kind cluster for the run and deletes it afterwards,
# so no state from other operators can leak into the recordings. Operators
# whose old releases need an older Kubernetes fail fast with the era-cluster
# hint (see require_k8s_max in operators/_common.sh); point KUBECONFIG and
# CLUSTER_NAME at that cluster and re-run.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
OPERATORS_DIR="hack/e2e/operators"
# shellcheck source=/dev/null
source "${OPERATORS_DIR}/../global.env"
# shellcheck source=/dev/null
source "${OPERATORS_DIR}/../supported-versions.env"

FRESH=0
if [ "${1:-}" = "--fresh-cluster" ]; then
  FRESH=1
  shift
fi
OP="${1:?usage: record-matrix.sh [--fresh-cluster] <operator> <version> [kourier-version]}"
VER="${2:?usage: record-matrix.sh [--fresh-cluster] <operator> <version> [kourier-version]}"
KOURIER="${3:-}"
VFILE="${OPERATORS_DIR}/.installed-versions-${CLUSTER_NAME}"
SKIP="real-data/SKIPPED.md"

# Label, version variable, and matrix list per operator (bash 3.2, so no
# associative arrays).
case "${OP}" in
  jobset)   LABEL=jobset   VAR=JOBSET_VERSION          PIN="${JOBSET_VERSION}"          MATRIX="${JOBSET_MATRIX}" ;;
  lws)      LABEL=lws      VAR=LWS_VERSION             PIN="${LWS_VERSION}"             MATRIX="${LWS_MATRIX}" ;;
  kuberay)  LABEL=kuberay  VAR=KUBERAY_VERSION         PIN="${KUBERAY_VERSION}"         MATRIX="${KUBERAY_MATRIX}" ;;
  kubeflow) LABEL=kubeflow VAR=KUBEFLOW_VERSION        PIN="${KUBEFLOW_VERSION}"        MATRIX="${KUBEFLOW_MATRIX}" ;;
  knative)  LABEL=knative  VAR=KNATIVE_VERSION         PIN="${KNATIVE_VERSION}"         MATRIX="${KNATIVE_MATRIX}" ;;
  kserve)   LABEL=kserve   VAR=KSERVE_VERSION          PIN="${KSERVE_VERSION}"          MATRIX="${KSERVE_MATRIX}" ;;
  milvus)   LABEL=milvus   VAR=MILVUS_OPERATOR_VERSION PIN="${MILVUS_OPERATOR_VERSION}" MATRIX="${MILVUS_MATRIX}" ;;
  grove)    LABEL=grove    VAR=GROVE_VERSION           PIN="${GROVE_VERSION}"           MATRIX="${GROVE_MATRIX}" ;;
  dynamo)   LABEL=dynamo   VAR=DYNAMO_VERSION          PIN="${DYNAMO_VERSION}"          MATRIX="${DYNAMO_MATRIX}" ;;
  nim)      LABEL=nim      VAR=NIM_OPERATOR_VERSION    PIN="${NIM_OPERATOR_VERSION}"    MATRIX="${NIM_MATRIX}" ;;
  *) echo "error: unknown operator ${OP}" >&2; exit 1 ;;
esac

if [ "${FRESH}" = "1" ]; then
  export CLUSTER_NAME="karta-matrix-${OP}"
  export KUBECONFIG="${HOME}/.kube/kind-${CLUSTER_NAME}.kubeconfig"
  echo "==> ${OP}: fresh cluster ${CLUSTER_NAME}"
  if ! make e2e-up CLUSTER_NAME="${CLUSTER_NAME}" WORKLOADS="${OP}" > "/tmp/matrix-${OP}-cluster-up.log" 2>&1; then
    echo "error: fresh cluster provisioning failed (/tmp/matrix-${OP}-cluster-up.log)" >&2
    make e2e-down CLUSTER_NAME="${CLUSTER_NAME}" >/dev/null 2>&1
    exit 1
  fi
  rc=0
  "$0" "${OP}" "${VER}" "${KOURIER}" || rc=$?
  make e2e-down CLUSTER_NAME="${CLUSTER_NAME}" >/dev/null 2>&1
  exit "${rc}"
fi

if [ "${VER}" = "all" ]; then
  rc=0
  for entry in ${MATRIX}; do
    v="${entry%%:*}"
    kourier=""
    [ "${entry}" != "${v}" ] && kourier="${entry#*:}"
    "$0" "${OP}" "${v}" "${kourier}" || rc=$?
  done
  "$0" "${OP}" pin || rc=$?
  exit "${rc}"
fi

# Version jumps need a cleanup the plain installs never do: controller
# deployment selectors are immutable across some releases, helm refuses to
# adopt resources another mechanism created, and grove alphas conflict on CRD
# ownership. Each cleanup leaves the cluster ready for a fresh install of any
# version of that operator.
cleanup_for_install() {
  case "${OP}" in
    jobset)   kubectl delete deploy jobset-controller-manager -n jobset-system --ignore-not-found ;;
    lws)      kubectl delete deploy lws-controller-manager -n lws-system --ignore-not-found ;;
    kubeflow) kubectl delete deploy --all -n kubeflow --ignore-not-found ;;
    nim)      helm uninstall k8s-nim-operator -n nim-operator 2>/dev/null || true ;;
    dynamo)
      local r
      for r in $(helm list -n dynamo-system --short 2>/dev/null); do
        helm uninstall "${r}" -n dynamo-system 2>/dev/null || true
      done ;;
    grove)
      local r
      for r in $(helm list -n grove-system --short 2>/dev/null); do
        helm uninstall "${r}" -n grove-system 2>/dev/null || true
      done
      kubectl delete ns grove-system --ignore-not-found --wait=true --timeout=120s
      kubectl get crd -o name 2>/dev/null | { grep 'grove\.io' || true; } | xargs -r kubectl delete ;;
    kserve)
      # KServe installs switch between manifests and charts across releases, so
      # wipe both forms: webhooks first (a dead webhook blocks CR finalizers),
      # then the namespace and the cluster-scoped leftovers.
      kubectl get validatingwebhookconfigurations,mutatingwebhookconfigurations -o name 2>/dev/null \
        | { grep -i 'kserve\|inferenceservice\|servingruntime' || true; } | xargs -r kubectl delete
      local cr
      for cr in $(kubectl get llminferenceserviceconfigs -n kserve -o name 2>/dev/null); do
        kubectl patch "${cr}" -n kserve --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
      done
      helm uninstall kserve -n kserve 2>/dev/null || true
      helm uninstall kserve-crd -n kserve 2>/dev/null || true
      kubectl delete ns kserve --ignore-not-found --wait=true --timeout=180s
      # A namespace can outlive the delete wait on slow clusters, and a fresh
      # install into a still-terminating namespace is rejected; poll it gone.

      for _ in $(seq 1 36); do
        kubectl get ns kserve >/dev/null 2>&1 || break
        sleep 5
      done
      kubectl get clusterrole,clusterrolebinding -o name 2>/dev/null | { grep kserve || true; } | xargs -r kubectl delete
      kubectl get crd -o name 2>/dev/null | { grep 'serving\.kserve\.io' || true; } | xargs -r kubectl delete --timeout=60s ;;
    milvus|knative|kuberay) : ;;
  esac
}

note_skip() {
  mkdir -p real-data
  printf -- "- %s %s: %s\n" "${OP}" "${VER}" "$1" >> "${SKIP}"
  echo "SKIP ${OP} ${VER}: $1"
  rm -rf "test/e2e/recorded_data/${OP}/${VER}"
  git checkout -q -- test/e2e/recorded_data 2>/dev/null
  rm -f "${VFILE}"
  exit 2
}

if [ "${VER}" = "pin" ]; then
  echo "==> ${OP}: reinstall the pinned ${PIN}"
  cleanup_for_install >/dev/null 2>&1
  "${OPERATORS_DIR}/${OP}/install.sh"
  exit $?
fi

env_args=("${VAR}=${VER}")
[ -n "${KOURIER}" ] && env_args+=("KOURIER_VERSION=${KOURIER}")

echo "==> ${OP} ${VER}: cleanup + install"
cleanup_for_install >/dev/null 2>&1
env "${env_args[@]}" "${OPERATORS_DIR}/${OP}/install.sh" > "/tmp/matrix-${OP}-${VER}-install.log" 2>&1 \
  || note_skip "install failed (/tmp/matrix-${OP}-${VER}-install.log)"
if [ -x "${OPERATORS_DIR}/${OP}/verify.sh" ]; then
  env "${env_args[@]}" "${OPERATORS_DIR}/${OP}/verify.sh" > "/tmp/matrix-${OP}-${VER}-verify.log" 2>&1 \
    || note_skip "operator not ready (/tmp/matrix-${OP}-${VER}-verify.log)"
fi

echo "==> ${OP} ${VER}: record ${LABEL}"
echo "${OP}=${VER}" > "${VFILE}"
make record-e2e E2E_LABELS="${LABEL}" > "/tmp/matrix-${OP}-${VER}-record.log" 2>&1 \
  || note_skip "recording failed (/tmp/matrix-${OP}-${VER}-record.log)"

echo "==> ${OP} ${VER}: file under real-data/${OP}/${VER}"
[ -d "test/e2e/recorded_data/${OP}/${VER}" ] || note_skip "no fixtures at the expected version dir"
mkdir -p "real-data/${OP}"
rm -rf "real-data/${OP:?}/${VER:?}"
mv "test/e2e/recorded_data/${OP}/${VER}" "real-data/${OP}/${VER}"
git checkout -q -- test/e2e/recorded_data 2>/dev/null
rm -f "${VFILE}"
echo "OK ${OP} ${VER}"
