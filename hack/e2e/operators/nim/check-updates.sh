#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
set -euo pipefail

MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/e2e/_updates.sh
source "${MODULE_DIR}/../../_updates.sh"

check_updates "$@" \
  --operator "nim" "${NIM_OPERATOR_VERSION}" "NVIDIA/k8s-nim-operator" "v" "stable" "Tracks the git archive used by install.sh; chart metadata can lag the repository tag."
