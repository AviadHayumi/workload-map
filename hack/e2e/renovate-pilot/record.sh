#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
#
# Renovate post-upgrade hook: record and validate the reviewed JobSet candidate
# before Renovate creates its update commit.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${REPO_ROOT}"

if [ "${GITHUB_ACTIONS:-}" != true ] || [ "${GITHUB_REPOSITORY:-}" != AviadHayumi/workload-map ]; then
  echo "error: the recording pilot runs only in the fork's GitHub Actions" >&2
  exit 1
fi

# Renovate may keep processing after a post-upgrade task fails. Gate the later
# Git push even when this hook exits before recording or validation starts.
push_hook=$(git rev-parse --git-path hooks/pre-push)
if [ -e "${push_hook}" ] || [ -L "${push_hook}" ]; then
  echo "error: refusing to replace an existing Git pre-push hook" >&2
  exit 1
fi
mkdir -p "$(dirname "${push_hook}")"
printf '#!/usr/bin/env bash\nset -euo pipefail\ncd %q\nexec python3 hack/e2e/renovate-pilot/verify_commit.py --manifest %q --base-sha %q\n' \
  "${REPO_ROOT}" "${PILOT_ARTIFACT_DIR:-}/commit-files.sha256" "${PILOT_BASE_SHA:-}" \
  > "${push_hook}"
chmod +x "${push_hook}"

: "${PILOT_ARTIFACT_DIR:?set an absolute evidence directory outside the Renovate checkout}"
: "${PILOT_BASE_SHA:?set the reviewed pilot source commit}"
if [[ ! "${PILOT_RUN:-}" =~ ^[0-9]+-[0-9]+$ ]]; then
  echo "error: PILOT_RUN must identify a GitHub run and attempt" >&2
  exit 1
fi

python3 - "${PILOT_ARTIFACT_DIR}" "${PILOT_BASE_SHA}" <<'PY'
import pathlib
import subprocess
import sys

root = pathlib.Path.cwd().resolve()
artifacts = pathlib.Path(sys.argv[1])
assert artifacts.is_absolute(), 'Evidence directory must be absolute'
artifacts = artifacts.resolve()
assert not artifacts.is_relative_to(root), 'Evidence must be outside the Renovate checkout'
assert not (artifacts / 'jobset-junit.xml').exists(), 'JUnit evidence must be fresh'
assert not (artifacts / 'commit-files.sha256').exists(), 'Success evidence must be fresh'
base = sys.argv[2]
assert subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip() == base
before = subprocess.check_output(['git', 'show', base + ':hack/e2e/global.env'], text=True)
old = 'JOBSET_VERSION="${JOBSET_VERSION:-v0.10.1}"'
new = 'JOBSET_VERSION="${JOBSET_VERSION:-v0.12.0}"'
assert before.count(old) == 1, 'Expected the reviewed JobSet baseline'
assert pathlib.Path('hack/e2e/global.env').read_text() == before.replace(old, new), 'Unexpected version changes'
changed = subprocess.check_output(['git', 'diff', '--name-only', base], text=True).splitlines()
assert changed == ['hack/e2e/global.env'], 'Expected only the JobSet pin to change'
assert not pathlib.Path('test/e2e/recorded_data/jobset/v0.12.0').exists(), 'Recordings must be fresh'
PY

# Read the updated file instead of inheriting the old pin from Renovate's runner.
unset JOBSET_VERSION
# shellcheck source=/dev/null
source "${REPO_ROOT}/hack/e2e/global.env"
test "${JOBSET_VERSION}" = v0.12.0
test "${KIND_NODE_IMAGE}" = kindest/node:v1.34.0
export JOBSET_VERSION
export CLUSTER_NAME="renovate-jobset-${PILOT_RUN}"
RECORDING_DIR="test/e2e/recorded_data/jobset/${JOBSET_VERSION}"

mkdir -p "${PILOT_ARTIFACT_DIR}"
git diff "${PILOT_BASE_SHA}" -- hack/e2e/global.env > "${PILOT_ARTIFACT_DIR}/renovate.patch"
cp hack/e2e/global.env "${PILOT_ARTIFACT_DIR}/global.env"

clusters=$(kind get clusters)
if printf '%s\n' "${clusters}" | grep -Fxq "${CLUSTER_NAME}"; then
  echo "error: ${CLUSTER_NAME} already exists; the pilot requires a fresh cluster" >&2
  exit 1
fi
export KUBECONFIG
KUBECONFIG=$(mktemp "${TMPDIR:-/tmp}/karta-renovate-kubeconfig.XXXXXX")
check_dir=""

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [ "${status}" -ne 0 ]; then
    kind export logs "${PILOT_ARTIFACT_DIR}/kind" --name "${CLUSTER_NAME}" \
      > "${PILOT_ARTIFACT_DIR}/kind-export.log" 2>&1 || true
  fi
  kind delete cluster --name "${CLUSTER_NAME}" \
    > "${PILOT_ARTIFACT_DIR}/kind-cleanup.log" 2>&1 || status=1
  rm -f "${KUBECONFIG}" || status=1
  if [ -n "${check_dir}" ]; then
    rm -rf "${check_dir}" || status=1
  fi
  if [ "${status}" -eq 0 ]; then
    sha256sum hack/e2e/global.env "${RECORDING_DIR}"/*/*.yaml \
      > "${PILOT_ARTIFACT_DIR}/commit-files.sha256.tmp" || status=1
    if [ "${status}" -eq 0 ]; then
      mv "${PILOT_ARTIFACT_DIR}/commit-files.sha256.tmp" \
        "${PILOT_ARTIFACT_DIR}/commit-files.sha256" || status=1
    fi
  fi
  if [ "${status}" -ne 0 ]; then
    # Renovate can continue after a hook failure. Never leave partial recordings
    # available for its commit, or a success marker for the publishing step.
    rm -rf "${RECORDING_DIR}"
    rm -f "${PILOT_ARTIFACT_DIR}/commit-files.sha256" \
      "${PILOT_ARTIFACT_DIR}/commit-files.sha256.tmp" \
      "${PILOT_ARTIFACT_DIR}/jobset-recordings.tar.gz"
  else
    printf 'JobSet %s: five fresh recordings, replay, make check, and cluster cleanup passed.\n' "${JOBSET_VERSION}"
  fi
  exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "Installing the JobSet candidate in a fresh kind cluster"
if ! make e2e-up WORKLOADS=jobset CLUSTER_NAME="${CLUSTER_NAME}" \
  > "${PILOT_ARTIFACT_DIR}/install.log" 2>&1; then
  tail -n 100 "${PILOT_ARTIFACT_DIR}/install.log"
  exit 1
fi
grep -Fx "jobset=${JOBSET_VERSION}" "hack/e2e/operators/.installed-versions-${CLUSTER_NAME}"
image=$(kubectl -n jobset-system get deployment jobset-controller-manager \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}')
test "${image}" = "registry.k8s.io/jobset/jobset:${JOBSET_VERSION}"
kubectl -n jobset-system get pods -l control-plane=controller-manager -o json |
  jq --arg expected "${image}" \
    '[.items[].status.containerStatuses[] | select(.name == "manager") | {image, imageID, ready}] |
     if length > 0 and all(.[]; .ready and .image == $expected and (.imageID | length > 0))
     then . else error("No ready JobSet controller with the expected image") end' \
    > "${PILOT_ARTIFACT_DIR}/controller-images.json"
kubectl version -o json | jq '{serverVersion}' > "${PILOT_ARTIFACT_DIR}/kubernetes.json"
jq -e '.serverVersion.gitVersion == "v1.34.0"' "${PILOT_ARTIFACT_DIR}/kubernetes.json" >/dev/null
jq -n \
  --arg base "${PILOT_BASE_SHA}" \
  --arg branch "codex/renovate-pilot/${PILOT_RUN}/jobset" \
  --arg version "${JOBSET_VERSION}" \
  --arg run "https://github.com/AviadHayumi/workload-map/actions/runs/${PILOT_RUN%-*}" \
  --arg kindNodeImage "${KIND_NODE_IMAGE}" \
  --arg fakeGpuVersion "${FAKE_GPU_VERSION}" \
  --arg webhookMode "${KARTA_WEBHOOK_MODE}" \
  '{base: $base, branch: $branch, operator: "jobset", version: $version, run: $run,
    kindNodeImage: $kindNodeImage, fakeGpuVersion: $fakeGpuVersion, kartaWebhookMode: $webhookMode}' \
  > "${PILOT_ARTIFACT_DIR}/provenance.json"

echo "Recording all five JobSet flows"
if ! make record-e2e WORKLOADS=jobset CLUSTER_NAME="${CLUSTER_NAME}" E2E_TIMEOUT=15m \
  E2E_JUNIT_REPORT="${PILOT_ARTIFACT_DIR}/jobset-junit.xml" \
  > "${PILOT_ARTIFACT_DIR}/recording.log" 2>&1; then
  tail -n 100 "${PILOT_ARTIFACT_DIR}/recording.log"
  exit 1
fi

python3 - <<'PY'
import pathlib

root = pathlib.Path('test/e2e/recorded_data/jobset/v0.12.0')
header = '# SPDX-License-Identifier: Apache-2.0\n# Copyright (c) 2026 NVIDIA Corporation\n\n'
for path in root.glob('*/*.yaml'):
    data = path.read_text()
    if not data.startswith(header):
        path.write_text(header + data)
PY

(
  cd test/e2e
  GOWORK=off go run ./cmd/verify-operator-pilot -expected-version "${JOBSET_VERSION}" \
    -junit "${PILOT_ARTIFACT_DIR}/jobset-junit.xml"
)
echo "Validating and replaying the generated recordings"
if ! make verify-recordings test-replay > "${PILOT_ARTIFACT_DIR}/replay.log" 2>&1; then
  tail -n 100 "${PILOT_ARTIFACT_DIR}/replay.log"
  exit 1
fi
tar -czf "${PILOT_ARTIFACT_DIR}/jobset-recordings.tar.gz" \
  "test/e2e/recorded_data/jobset/${JOBSET_VERSION}"

# make check requires a clean tree. Validate the exact candidate in a temporary
# local commit while leaving Renovate to create the published update commit.
echo "Running make check against the candidate pin and recordings"
check_dir=$(mktemp -d "${TMPDIR:-/tmp}/karta-renovate-check.XXXXXX")
git clone --quiet --shared "${REPO_ROOT}" "${check_dir}"
cp hack/e2e/global.env "${check_dir}/hack/e2e/global.env"
cp -R "test/e2e/recorded_data/jobset/${JOBSET_VERSION}" \
  "${check_dir}/test/e2e/recorded_data/jobset/${JOBSET_VERSION}"
git -C "${check_dir}" config user.name 'github-actions[bot]'
git -C "${check_dir}" config user.email '41898282+github-actions[bot]@users.noreply.github.com'
git -C "${check_dir}" add -- hack/e2e/global.env "test/e2e/recorded_data/jobset/${JOBSET_VERSION}"
git -C "${check_dir}" -c commit.gpgsign=false commit --quiet -s \
  -m 'test(e2e): verify the JobSet pilot candidate'
if make -C "${check_dir}" check VERSION="0.0.0-main-${PILOT_BASE_SHA:0:7}" \
  > "${PILOT_ARTIFACT_DIR}/make-check.log" 2>&1; then
  echo "make check passed for the candidate pin and generated recordings"
else
  tail -n 100 "${PILOT_ARTIFACT_DIR}/make-check.log"
  exit 1
fi
