#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

# shellcheck disable=SC2154  # TRAINER_VERSION comes from global.env via _common.sh
set -euo pipefail
MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${MODULE_DIR}/../_common.sh"

main() {
  echo "==> kubeflow trainer ${TRAINER_VERSION}"

  # The chart bundles a JobSet subchart; the cluster already runs the pinned jobset operator,
  # so the subchart is disabled and the trainer module depends on the jobset module instead.
  helm upgrade --install kubeflow-trainer oci://ghcr.io/kubeflow/charts/kubeflow-trainer \
    --namespace kubeflow-system \
    --create-namespace \
    --version "${TRAINER_VERSION#v}" \
    --set jobset.install=false \
    --wait --timeout 5m

  kubectl -n kubeflow-system wait --for=condition=Available deploy --all --timeout=300s

  # A tiny runtime the flows and the smoke reference, so no default runtime images are pulled.
  apply_with_retry "${MODULE_DIR}/runtime.yaml"
}

main "$@"
