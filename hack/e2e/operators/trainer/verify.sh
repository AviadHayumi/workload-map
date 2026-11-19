#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

# shellcheck disable=SC2154  # TRAINER_VERSION comes from global.env via _common.sh
set -euo pipefail
MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${MODULE_DIR}/../_common.sh"

main() {
  run_smoke "${MODULE_DIR}/smoke.yaml" "trainjob/trainer-smoke" "condition=Complete" "240s" default
}

main "$@"
