#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Kubeflow training-operator (PyTorchJob) and the standalone kubeflow mpi-operator
# (kubeflow.org/v2beta1 MPIJob).
# shellcheck disable=SC2154  # KUBEFLOW_VERSION/MPI_OPERATOR_VERSION from global.env via _common.sh
set -euo pipefail
MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${MODULE_DIR}/../_common.sh"

main() {
  # Restrict the training-operator to PyTorchJob and hand MPIJob to the standalone
  # mpi-operator (v2beta1) below. Drop the bundled MPIJob CRD before each apply:
  # kube will not re-point it to v2beta1 in place while v1 is still a stored version.
  echo "==> Kubeflow training-operator ${KUBEFLOW_VERSION}"

  # training-operator releases before v1.8 never become ready on newer
  # Kubernetes; they only run on their era clusters.
  case "${KUBEFLOW_VERSION}" in
    v1.[0-7].*) require_k8s_max 28 "training-operator ${KUBEFLOW_VERSION}" || exit 1 ;;
  esac
  kubectl delete crd mpijobs.kubeflow.org --ignore-not-found --timeout=60s
  kubectl apply --server-side --force-conflicts -k "github.com/kubeflow/trainer/manifests/overlays/standalone?ref=${KUBEFLOW_VERSION}"
  kubectl patch deployment training-operator -n kubeflow \
    -p '{"spec":{"template":{"spec":{"containers":[{"name":"training-operator","args":["--enable-scheme=pytorchjob"]}]}}}}'
  rollout_wait kubeflow deploy/training-operator

  echo "==> mpi-operator ${MPI_OPERATOR_VERSION} (kubeflow.org/v2beta1 MPIJob)"
  kubectl delete crd mpijobs.kubeflow.org --ignore-not-found --timeout=60s
  apply_with_retry "https://raw.githubusercontent.com/kubeflow/mpi-operator/${MPI_OPERATOR_VERSION}/deploy/v2beta1/mpi-operator.yaml" \
    5 10 --server-side --force-conflicts
  rollout_wait mpi-operator deploy/mpi-operator
}

main "$@"
