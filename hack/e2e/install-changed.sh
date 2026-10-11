#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Report whether installation inputs changed, and the narrowest install plan.
# Shared inputs select all; operator-local changes select those operators.
# Usage: install-changed.sh <base-ref> [head-ref]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
base_ref="$(git -C "${REPO_ROOT}" rev-parse --verify --end-of-options "${1:?base ref required}^{commit}")"
head_ref="$(git -C "${REPO_ROOT}" rev-parse --verify --end-of-options "${2:-HEAD}^{commit}")"
changed_files="$(git -C "${REPO_ROOT}" -c core.quotePath=true diff --name-only --no-renames \
  "${base_ref}...${head_ref}" -- \
  hack/e2e .github/workflows/ci.yaml Makefile charts/karta operator/Dockerfile \
  .dockerignore go.mod go.sum go.work go.work.sum operator/go.mod operator/go.sum cli/go.mod cli/go.sum)"

if [ -z "${changed_files}" ]; then
  printf 'changed=false\nworkloads=\n'
  exit 0
fi

operators=()
workloads=""
while IFS= read -r path; do
  case "${path}" in
    hack/e2e/operators/*/*)
      operator="${path#hack/e2e/operators/}"
      operator="${operator%%/*}"
      # Workload names reach a Make recipe; never pass shell syntax from a path.
      case "${operator}" in
        '' | -* | *[!a-z0-9-]*) workloads=all; break ;;
      esac
      operators+=("${operator}")
      ;;
    *) workloads=all; break ;;
  esac
done <<<"${changed_files}"

if [ "${workloads}" != all ]; then
  workloads="$(printf '%s\n' "${operators[@]}" | LC_ALL=C sort -u | paste -sd ' ' -)"
fi
printf 'changed=true\nworkloads=%s\n' "${workloads}"
