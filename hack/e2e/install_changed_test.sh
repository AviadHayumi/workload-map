#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Exercise the install detector against local Git histories without a cluster.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/karta-install-changed.XXXXXX")"
trap 'rm -rf "${TEST_ROOT}"' EXIT

# Fixture commits must not inherit signing, hooks, identities, or repository paths.
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_COUNT=0
export GIT_AUTHOR_NAME="Detector Test" GIT_AUTHOR_EMAIL="detector@example.com"
export GIT_COMMITTER_NAME="Detector Test" GIT_COMMITTER_EMAIL="detector@example.com"
unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE
unset GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES
mkdir -p "${TEST_ROOT}/empty-template" "${TEST_ROOT}/unrelated"

fixture_count=0
assertion_count=0
repo=""

commit_fixture() {
  git -C "${repo}" add -A
  git -C "${repo}" commit --quiet -m fixture
}

new_fixture() {
  fixture_count=$((fixture_count + 1))
  repo="${TEST_ROOT}/repo ${fixture_count}"
  git init --quiet --template="${TEST_ROOT}/empty-template" "${repo}"
  git -C "${repo}" config core.hooksPath /dev/null
  git -C "${repo}" config commit.gpgSign false
  git -C "${repo}" symbolic-ref HEAD refs/heads/base
  mkdir -p "${repo}/hack/e2e/operators/jobset" "${repo}/hack/e2e/operators/kuberay" "${repo}/docs"
  cp "${SCRIPT_DIR}/install-changed.sh" "${repo}/hack/e2e/install-changed.sh"
  printf 'initial\n' >"${repo}/README.md"
  printf 'initial\n' >"${repo}/docs/notes.md"
  printf 'initial\n' >"${repo}/hack/e2e/global.env"
  printf 'initial\n' >"${repo}/hack/e2e/operators/jobset/install.sh"
  printf 'initial\n' >"${repo}/hack/e2e/operators/jobset/verify.sh"
  printf 'initial\n' >"${repo}/hack/e2e/operators/kuberay/install.sh"
  commit_fixture
  git -C "${repo}" checkout --quiet -b pr
}

change_file() {
  mkdir -p "${repo}/$(dirname "$1")"
  printf 'changed\n' >>"${repo}/$1"
}

assert_plan() {
  local label="$1" changed="$2" workloads="$3"
  shift 3
  if ! (cd "${TEST_ROOT}/unrelated" && bash "${repo}/hack/e2e/install-changed.sh" "$@") \
    >"${TEST_ROOT}/actual" 2>"${TEST_ROOT}/stderr"; then
    printf 'FAIL: %s: detector failed\n' "${label}" >&2
    cat "${TEST_ROOT}/stderr" >&2
    exit 1
  fi
  printf 'changed=%s\nworkloads=%s\n' "${changed}" "${workloads}" >"${TEST_ROOT}/expected"
  if ! cmp -s "${TEST_ROOT}/expected" "${TEST_ROOT}/actual"; then
    printf 'FAIL: %s: unexpected output\n' "${label}" >&2
    diff -u "${TEST_ROOT}/expected" "${TEST_ROOT}/actual" >&2 || true
    exit 1
  fi
  assertion_count=$((assertion_count + 1))
}

assert_failure() {
  local label="$1"
  shift
  if (cd "${TEST_ROOT}/unrelated" && bash "${repo}/hack/e2e/install-changed.sh" "$@") \
    >"${TEST_ROOT}/actual" 2>"${TEST_ROOT}/stderr"; then
    printf 'FAIL: %s: detector accepted invalid arguments\n' "${label}" >&2
    exit 1
  fi
  if [ -s "${TEST_ROOT}/actual" ]; then
    printf 'FAIL: %s: detector emitted a plan on failure\n' "${label}" >&2
    cat "${TEST_ROOT}/actual" >&2
    exit 1
  fi
  assertion_count=$((assertion_count + 1))
}

new_fixture
assert_plan 'unchanged refs' false '' base pr
assert_plan 'default head from an unrelated working directory' false '' base
assert_failure 'missing base'
assert_failure 'unknown base' nonexistent pr
assert_failure 'unknown head' base nonexistent
assert_failure 'option-like base' --help pr
assert_failure 'non-commit object' base 'HEAD:README.md'

# Each shared input independently requires the full installation plan.
while IFS= read -r path; do
  new_fixture
  change_file "${path}"
  commit_fixture
  assert_plan "shared input: ${path}" true all base pr
done <<'PATHS'
hack/e2e/up.sh
hack/e2e/down.sh
hack/e2e/README.md
hack/e2e/karta-operator/install.sh
hack/e2e/operators/_common.sh
hack/e2e/global.env
hack/e2e/versions.env
.github/workflows/ci.yaml
Makefile
charts/karta/values.yaml
charts/karta/templates/deployment.yaml
operator/Dockerfile
.dockerignore
go.mod
go.sum
go.work
go.work.sum
cli/go.mod
cli/go.sum
operator/go.mod
operator/go.sum
PATHS

new_fixture
while IFS= read -r path; do
  change_file "${path}"
done <<'PATHS'
README.md
docs/notes.md
pkg/example.go
operator/internal/example.go
test/e2e/example_test.go
.github/workflows/release.yaml
charts/another/values.yaml
hack/e2e-other/install.sh
hack/e2e.md
Makefile.extra
go.mod.backup
operator/Dockerfile.example
PATHS
commit_fixture
assert_plan 'unrelated code, docs, and similarly named paths' false '' base pr

new_fixture
change_file hack/e2e/operators/jobset/install.sh
commit_fixture
assert_plan 'one existing operator, default head' true jobset base
change_file hack/e2e/operators/jobset/verify.sh
change_file hack/e2e/operators/jobset/README.md
commit_fixture
assert_plan 'multiple files in one operator are deduplicated' true jobset base pr
change_file hack/e2e/operators/kuberay/install.sh
commit_fixture
assert_plan 'multiple operators are sorted' true 'jobset kuberay' base pr
change_file Makefile
commit_fixture
assert_plan 'shared input overrides selective operators' true all base pr

new_fixture
change_file hack/e2e/operators/kuberay/install.sh
commit_fixture
assert_plan 'kuberay alone' true kuberay base pr

new_fixture
change_file hack/e2e/operators/new-operator/install.sh
commit_fixture
assert_plan 'new operator directory' true new-operator base pr

while IFS= read -r operator; do
  new_fixture
  change_file "hack/e2e/operators/${operator}/install.sh"
  commit_fixture
  assert_plan "unsafe operator name: ${operator}" true all base pr
done <<'NAMES'
-leading-hyphen
MixedCase
under_score
two words
semi;colon
$(touch injected)
NAMES

new_fixture
change_file $'hack/e2e/operators/jobset/line\nbreak.sh'
commit_fixture
assert_plan 'newline in an operator filename falls back to all' true all base pr

new_fixture
change_file $'hack/e2e/operators/line\nbreak/install.sh'
commit_fixture
assert_plan 'newline in an operator directory falls back to all' true all base pr

new_fixture
rm "${repo}/hack/e2e/operators/jobset/install.sh"
commit_fixture
assert_plan 'deleted operator file' true jobset base pr

new_fixture
rm "${repo}/hack/e2e/global.env"
commit_fixture
assert_plan 'deleted shared input' true all base pr

new_fixture
git -C "${repo}" mv hack/e2e/operators/jobset/install.sh docs/moved-install.sh
commit_fixture
assert_plan 'operator file moved outside watched tree' true jobset base pr

new_fixture
git -C "${repo}" mv hack/e2e/global.env docs/moved-global.env
commit_fixture
assert_plan 'shared input moved outside watched tree' true all base pr

new_fixture
git -C "${repo}" mv docs/notes.md hack/e2e/operators/jobset/notes.md
commit_fixture
assert_plan 'file moved into operator directory' true jobset base pr

new_fixture
git -C "${repo}" mv hack/e2e/operators/jobset/verify.sh hack/e2e/operators/kuberay/verify.sh
commit_fixture
assert_plan 'rename between operators selects source and destination' true 'jobset kuberay' base pr

new_fixture
git -C "${repo}" mv hack/e2e/operators/jobset/verify.sh hack/e2e/operators/jobset/check.sh
commit_fixture
assert_plan 'rename inside one operator' true jobset base pr

new_fixture
git -C "${repo}" checkout --quiet base
change_file Makefile
commit_fixture
git -C "${repo}" checkout --quiet pr
change_file docs/notes.md
commit_fixture
assert_plan 'watched change only on base is not part of the PR' false '' base pr
change_file hack/e2e/operators/jobset/install.sh
commit_fixture
assert_plan 'divergent PR reports only its own operator change' true jobset base pr

new_fixture
change_file hack/e2e/operators/jobset/install.sh
commit_fixture
git -C "${repo}" branch selected-head
change_file Makefile
commit_fixture
assert_plan 'explicit head does not use current HEAD' true jobset base selected-head
assert_plan 'default head includes current HEAD' true all base

printf 'install-changed: %s assertions passed\n' "${assertion_count}"
