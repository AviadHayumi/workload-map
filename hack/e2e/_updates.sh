# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
# shellcheck shell=bash

_UPDATES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/e2e/global.env
source "${_UPDATES_DIR}/global.env"

check_updates() {
  python3 "${_UPDATES_DIR}/check_updates.py" "$@"
}
