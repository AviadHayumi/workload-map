#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
set -euo pipefail

MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/e2e/_updates.sh
source "${MODULE_DIR}/../../_updates.sh"

check_updates "$@" \
  --operator "kuberay" "${KUBERAY_VERSION}" "ray-project/kuberay-helm" "kuberay-operator-" "stable" "Tracks the published operator chart, not Ray runtime or application tags."
