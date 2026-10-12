#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Print changed operator names, all for shared operator files, or nothing.
# Usage: install-changed.sh <base-ref> [head-ref]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
base_ref="$(git -C "${REPO_ROOT}" rev-parse --verify --end-of-options "${1:?base ref required}^{commit}")"
head_ref="$(git -C "${REPO_ROOT}" rev-parse --verify --end-of-options "${2:-HEAD}^{commit}")"
changed_files="$(git -C "${REPO_ROOT}" -c core.quotePath=true diff --name-only --no-renames \
  "${base_ref}...${head_ref}" -- hack/e2e/operators)"

if [ -z "${changed_files}" ]; then
  exit 0
fi

operators=()
while IFS= read -r path; do
  case "${path}" in
    hack/e2e/operators/*/*)
      operator="${path#hack/e2e/operators/}"
      operator="${operator%%/*}"
      # Workload names reach a Make recipe; never pass shell syntax from a path.
      case "${operator}" in
        '' | -* | *[!a-z0-9-]*) echo all; exit 0 ;;
      esac
      git -C "${REPO_ROOT}" cat-file -e "${head_ref}:hack/e2e/operators/${operator}/install.sh" 2>/dev/null ||
        { echo all; exit 0; }
      operators+=("${operator}")
      ;;
    *) echo all; exit 0 ;;
  esac
done <<<"${changed_files}"

printf '%s\n' "${operators[@]}" | LC_ALL=C sort -u | paste -sd ' ' -
