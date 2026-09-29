#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Smoke tests: a throwaway PyTorchJob (training-operator) must reach Running and a
# throwaway v2beta1 MPIJob (mpi-operator) must reach Succeeded.
set -euo pipefail
MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${MODULE_DIR}/../_common.sh"

# training-operator releases before v1.8 only run on their era clusters (see install.sh).
case "${KUBEFLOW_VERSION}" in
  v1.[0-7].*) require_k8s_max 28 "training-operator ${KUBEFLOW_VERSION}" || exit 1 ;;
esac

echo "==> smoke: pytorchjob/pytorch-smoke"
run_smoke "${MODULE_DIR}/smoke.yaml" "pytorchjob/pytorch-smoke" "condition=Running" "240s" default

echo "==> smoke: mpijob/mpi-smoke"
run_smoke "${MODULE_DIR}/mpi-smoke.yaml" "mpijob/mpi-smoke" "condition=Succeeded" "240s" default
