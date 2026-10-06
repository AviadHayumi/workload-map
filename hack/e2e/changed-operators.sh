#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Prints what a change between two refs needs proven on a fresh kind cluster,
# as key=value lines CI appends to its job outputs:
#   operators=["jobset","kserve"]   workload operators to install, one job each
#   karta=true                      the Karta operator install itself changed
#
# An operator's directory, or a global.env pin one of its scripts reads, selects
# it plus the operators installed on top of it (deps_of in up.sh, read from the
# other side). up.sh selects a canary, the operators added to ALL_WORKLOADS and
# every operator its changed lines name. _common.sh and the kind node image
# select every operator, since every install goes through them. Any other file
# under hack/e2e selects the canary, except Markdown.
# karta=true comes from karta-operator/, the chart, the operator Dockerfile,
# the cert-manager pin or a cert-manager line in up.sh, a global.env value the
# karta-operator scripts read, and the kind node image. cert-manager changes
# also select kserve, the one workload whose plan installs it.
# Operators that are not in ALL_WORKLOADS at the head ref are dropped.
#
# Usage: changed-operators.sh <base-ref> [head-ref]
set -euo pipefail

BASE="${1:?base ref required}"
HEAD="${2:-HEAD}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RANGE="${BASE}...${HEAD}"
CANARY="jobset"

up_sh="$(git -C "${REPO_ROOT}" show "${HEAD}:hack/e2e/up.sh")"
all_operators=()
read -r -a all_operators <<<"$(printf '%s\n' "${up_sh}" | sed -nE 's/^ALL_WORKLOADS=\((.*)\)$/\1/p')"
[ "${#all_operators[@]}" -gt 0 ] || { echo "error: ALL_WORKLOADS not found in up.sh at ${HEAD}" >&2; exit 1; }

# One "<operator> <dependency>..." line per deps_of case, e.g. "kserve knative".
# Every line of the block must be scaffold or a case, so a case written in
# another form fails here instead of silently losing a dependency.
deps_block="$(printf '%s\n' "${up_sh}" | sed -n '/^deps_of() {/,/^}/p' \
  | grep -vE '^(deps_of\(\) \{| *case "[$]1" in| *esac|\})$' || true)"
deps_table="$(printf '%s\n' "${deps_block}" | sed -nE 's/^ *([a-z0-9-]+)\) echo "([^"]*)" ;;$/\1 \2/p')"
if [ "$(printf '%s\n' "${deps_block}" | grep -c .)" -ne "$(printf '%s\n' "${deps_table}" | grep -c .)" ]; then
  echo "error: a deps_of case in up.sh is not of the form 'name) echo \"deps\" ;;'" >&2
  exit 1
fi

changed_files="$(git -C "${REPO_ROOT}" diff --name-only --no-renames "${RANGE}" -- hack/e2e charts/karta operator/Dockerfile)"

selected=()
karta=false
while IFS= read -r file; do
  case "$file" in
    hack/e2e/operators/_common.sh)
      selected+=("${all_operators[@]}") ;;
    hack/e2e/operators/*/*)
      dir="${file#hack/e2e/operators/}"
      selected+=("${dir%%/*}") ;;
    hack/e2e/karta-operator/*|charts/karta/*|operator/Dockerfile)
      karta=true ;;
    hack/e2e/up.sh)
      selected+=("${CANARY}")
      # Operators added to ALL_WORKLOADS. A reorder of the kept ones changes
      # the install order of every plan, so it selects them all.
      base_operators=()
      read -r -a base_operators <<<"$(git -C "${REPO_ROOT}" show "${BASE}:hack/e2e/up.sh" 2>/dev/null \
        | sed -nE 's/^ALL_WORKLOADS=\((.*)\)$/\1/p' || true)"
      kept_base=""
      for op in ${base_operators[@]+"${base_operators[@]}"}; do
        printf '%s\n' "${all_operators[@]}" | grep -qxF "${op}" && kept_base="${kept_base} ${op}"
      done
      kept_head=""
      for op in "${all_operators[@]}"; do
        if printf '%s\n' ${base_operators[@]+"${base_operators[@]}"} | grep -qxF "${op}"; then
          kept_head="${kept_head} ${op}"
        else
          selected+=("${op}")
        fi
      done
      [ "${kept_base}" = "${kept_head}" ] || selected+=("${all_operators[@]}")
      # Every operator a changed line names; the ALL_WORKLOADS line names them
      # all and is skipped. cert-manager is provisioned here, and only kserve's
      # plan installs it.
      up_sh_diff="$(git -C "${REPO_ROOT}" diff -U0 --no-renames "${RANGE}" -- hack/e2e/up.sh \
        | grep -E '^[-+][^-+]' | grep -vE '^[-+]ALL_WORKLOADS=' || true)"
      for op in "${all_operators[@]}"; do
        printf '%s\n' "${up_sh_diff}" | grep -qw "${op}" && selected+=("${op}")
      done
      if printf '%s\n' "${up_sh_diff}" | grep -qiE 'cert.manager'; then
        karta=true
        selected+=("kserve")
      fi ;;
    hack/e2e/global.env)
      env_diff="$(git -C "${REPO_ROOT}" diff --no-renames "${RANGE}" -- hack/e2e/global.env)"
      while IFS= read -r var; do
        [ -n "${var}" ] || continue
        case "${var}" in
          KIND_NODE_IMAGE) karta=true; selected+=("${all_operators[@]}") ;;
          CERT_MANAGER_VERSION) karta=true; selected+=("kserve") ;;
          *)
            # Readers at both refs, so a pin removed together with its reader
            # still selects that operator.
            if git -C "${REPO_ROOT}" grep -qw "${var}" "${BASE}" "${HEAD}" -- 'hack/e2e/karta-operator/*.sh'; then
              karta=true
            fi
            consumers="$(git -C "${REPO_ROOT}" grep -lw "${var}" "${BASE}" "${HEAD}" -- 'hack/e2e/operators/*/*.sh' \
              | sed -E 's#^[^:]*:hack/e2e/operators/([^/]+)/.*#\1#' | sort -u || true)"
            if [ -n "${consumers}" ]; then
              while IFS= read -r op; do selected+=("${op}"); done <<<"${consumers}"
            else
              selected+=("${CANARY}")
            fi ;;
        esac
      done <<<"$(printf '%s\n' "${env_diff}" | sed -nE 's/^[-+]([A-Z_][A-Z0-9_]*)=.*/\1/p' | sort -u)" ;;
    *.md) ;;
    hack/e2e/*)
      selected+=("${CANARY}") ;;
  esac
done <<<"${changed_files}"

# A selected operator also selects the operators installed on top of it.
for op in ${selected[@]+"${selected[@]}"}; do
  while IFS= read -r line; do
    [ -n "${line}" ] || continue
    for dep in ${line#* }; do
      [ "${dep}" = "${op}" ] && selected+=("${line%% *}")
    done
  done <<<"${deps_table}"
done

operators=()
for op in "${all_operators[@]}"; do
  if printf '%s\n' ${selected[@]+"${selected[@]}"} | grep -qxF "${op}"; then operators+=("${op}"); fi
done

json=""
for op in ${operators[@]+"${operators[@]}"}; do json="${json:+${json},}\"${op}\""; done
echo "operators=[${json}]"
echo "karta=${karta}"
