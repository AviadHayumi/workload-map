#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Exercise the install selector against local Git histories without a cluster.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/karta-install-changed.XXXXXX")"
trap 'rm -rf "${TEST_ROOT}"' EXIT
repo="${TEST_ROOT}/fixture repo"
assertion_count=0

# Fixture commits must not inherit signing, hooks, identities, or repository paths.
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_COUNT=0
export GIT_AUTHOR_NAME="Detector Test" GIT_AUTHOR_EMAIL="detector@example.com"
export GIT_COMMITTER_NAME="Detector Test" GIT_COMMITTER_EMAIL="detector@example.com"
unset GIT_CONFIG GIT_CONFIG_PARAMETERS GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE
unset GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES
mkdir -p "${TEST_ROOT}/empty-template" "${TEST_ROOT}/unrelated"
git init --quiet --template="${TEST_ROOT}/empty-template" "${repo}"
git -C "${repo}" config core.hooksPath /dev/null
git -C "${repo}" config commit.gpgSign false
git -C "${repo}" symbolic-ref HEAD refs/heads/base

change_file() {
  mkdir -p "${repo}/$(dirname "$1")"
  printf 'changed\n' >>"${repo}/$1"
}

commit_fixture() {
  git -C "${repo}" add -A
  git -C "${repo}" commit --quiet -m fixture
}

reset_fixture() {
  git -C "${repo}" checkout --quiet -B pr fixture
  git -C "${repo}" branch --force base fixture
}

# FAILURE expects a nonzero exit and empty stdout; all other values are exact output.
expect() {
  local label="$1" expected="$2" failed=false expected_failure=false
  shift 2
  if [ "${expected}" = FAILURE ]; then
    expected=""
    expected_failure=true
  fi
  (cd "${TEST_ROOT}/unrelated" && bash "${repo}/hack/e2e/install-changed.sh" "$@") \
    >"${TEST_ROOT}/actual" 2>"${TEST_ROOT}/stderr" || failed=true
  : >"${TEST_ROOT}/expected"
  if [ -n "${expected}" ]; then
    printf '%s\n' "${expected}" >"${TEST_ROOT}/expected"
  fi
  if [ "${failed}" != "${expected_failure}" ] || ! cmp -s "${TEST_ROOT}/expected" "${TEST_ROOT}/actual"; then
    printf 'FAIL: %s (failed=%s, expected_failure=%s)\n' "${label}" "${failed}" "${expected_failure}" >&2
    cat "${TEST_ROOT}/stderr" >&2
    diff -u "${TEST_ROOT}/expected" "${TEST_ROOT}/actual" >&2 || true
    exit 1
  fi
  assertion_count=$((assertion_count + 1))
}

for path in README.md docs/notes.md hack/e2e/global.env \
  hack/e2e/operators/jobset/install.sh hack/e2e/operators/jobset/verify.sh \
  hack/e2e/operators/kuberay/install.sh; do
  change_file "${path}"
done
cp "${SCRIPT_DIR}/install-changed.sh" "${repo}/hack/e2e/install-changed.sh"
commit_fixture
git -C "${repo}" tag fixture
reset_fixture
expect 'unchanged refs' '' base pr
expect 'default head from an unrelated working directory' '' base
expect 'missing base' FAILURE
expect 'unknown base' FAILURE nonexistent pr
expect 'unknown head' FAILURE base nonexistent
expect 'option-like base' FAILURE --help pr
expect 'non-commit object' FAILURE base 'HEAD:README.md'

# Each input is tested independently so broader matching cannot hide omissions.
while IFS='|' read -r expected path; do
  reset_fixture
  change_file "${path}"
  commit_fixture
  expect "input: ${path}" "${expected}" base pr
done <<'CASES'
all|hack/e2e/up.sh
all|hack/e2e/down.sh
all|hack/e2e/README.md
all|hack/e2e/karta-operator/install.sh
all|hack/e2e/operators/_common.sh
all|hack/e2e/global.env
all|hack/e2e/versions.env
all|.github/workflows/ci.yaml
all|Makefile
all|charts/karta/values.yaml
all|charts/karta/templates/deployment.yaml
all|operator/Dockerfile
all|.dockerignore
all|go.mod
all|go.sum
all|go.work
all|go.work.sum
all|cli/go.mod
all|cli/go.sum
all|operator/go.mod
all|operator/go.sum
kuberay|hack/e2e/operators/kuberay/install.sh
new-operator|hack/e2e/operators/new-operator/install.sh
all|hack/e2e/operators/-leading-hyphen/install.sh
all|hack/e2e/operators/MixedCase/install.sh
all|hack/e2e/operators/under_score/install.sh
all|hack/e2e/operators/two words/install.sh
all|hack/e2e/operators/semi;colon/install.sh
all|hack/e2e/operators/$(touch injected)/install.sh
CASES

for path in $'hack/e2e/operators/jobset/line\nbreak.sh' $'hack/e2e/operators/line\nbreak/install.sh'; do
  reset_fixture
  change_file "${path}"
  commit_fixture
  expect 'newline in operator filename or directory' all base pr
done

reset_fixture
for path in README.md docs/notes.md pkg/example.go operator/internal/example.go \
  test/e2e/example_test.go .github/workflows/release.yaml charts/another/values.yaml \
  hack/e2e-other/install.sh hack/e2e.md Makefile.extra go.mod.backup operator/Dockerfile.example; do
  change_file "${path}"
done
commit_fixture
expect 'unrelated code, docs, and similarly named paths' '' base pr

reset_fixture
change_file hack/e2e/operators/jobset/install.sh
commit_fixture
git -C "${repo}" branch selected-head
expect 'one existing operator, default head' jobset base
change_file hack/e2e/operators/jobset/verify.sh
change_file hack/e2e/operators/jobset/README.md
commit_fixture
expect 'multiple files in one operator are deduplicated' jobset base pr
change_file hack/e2e/operators/kuberay/install.sh
commit_fixture
expect 'multiple operators are sorted' 'jobset kuberay' base pr
change_file Makefile
commit_fixture
expect 'shared input overrides selective operators' all base pr
expect 'explicit head does not use current HEAD' jobset base selected-head
expect 'default head includes current HEAD' all base

while IFS='|' read -r expected source destination; do
  reset_fixture
  if [ "${destination}" = DELETE ]; then
    git -C "${repo}" rm --quiet "${source}"
  else
    git -C "${repo}" mv "${source}" "${destination}"
  fi
  commit_fixture
  expect "${source} -> ${destination}" "${expected}" base pr
done <<'CASES'
jobset|hack/e2e/operators/jobset/install.sh|DELETE
all|hack/e2e/global.env|DELETE
jobset|hack/e2e/operators/jobset/install.sh|docs/moved-install.sh
all|hack/e2e/global.env|docs/moved-global.env
jobset|docs/notes.md|hack/e2e/operators/jobset/notes.md
jobset kuberay|hack/e2e/operators/jobset/verify.sh|hack/e2e/operators/kuberay/verify.sh
jobset|hack/e2e/operators/jobset/verify.sh|hack/e2e/operators/jobset/check.sh
CASES

reset_fixture
git -C "${repo}" checkout --quiet base
change_file Makefile
commit_fixture
git -C "${repo}" checkout --quiet pr
change_file docs/notes.md
commit_fixture
expect 'watched change only on base is not part of the PR' '' base pr
change_file hack/e2e/operators/jobset/install.sh
commit_fixture
expect 'divergent PR reports only its own operator change' jobset base pr

printf 'install-changed: %s assertions passed\n' "${assertion_count}"
