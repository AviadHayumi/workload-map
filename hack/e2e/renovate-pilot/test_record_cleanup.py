#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Run the real recording hook with mocked cluster/build tools and real Git/files."""

import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import textwrap
import unittest


SCRIPT = Path(__file__).with_name("record.sh")
MOCK_TOOLS = r'''#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

name = Path(sys.argv[0]).name
args = sys.argv[1:]
artifacts = Path(os.environ['PILOT_ARTIFACT_DIR'])
failure = os.environ.get('MOCK_FAILURE', '')
if name == 'kind':
    if args[:2] == ['delete', 'cluster']:
        (artifacts / 'deleted-kubeconfig').write_text(os.environ['KUBECONFIG'])
        sys.exit(failure == 'cleanup')
    if args[:2] == ['export', 'logs']:
        Path(args[2]).mkdir(exist_ok=True)
elif name == 'make':
    if args[0] == 'e2e-up':
        p = Path('hack/e2e/operators') / ('.installed-versions-' + os.environ['CLUSTER_NAME'])
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text('jobset=v0.12.0\n')
    elif args[0] == 'record-e2e':
        root = Path('test/e2e/recorded_data/jobset/v0.12.0/jobset-x-k8s-io-jobset-v1alpha2')
        root.mkdir(parents=True)
        for flow in ('running', 'completed', 'failed', 'resumed', 'suspended'):
            (root / (flow + '.yaml')).write_text('flow: ' + flow + '\n')
        (artifacts / 'jobset-junit.xml').write_text('<mock/>')
        sys.exit(failure == 'record')
    elif args[0] == '-C':
        clone = Path(args[1])
        assert not subprocess.check_output(['git', '-C', str(clone), 'status', '--porcelain'])
        assets = clone / 'bin/k8s/1.31.0-linux-amd64'
        assets.mkdir(parents=True)
        for name in ('etcd', 'kubectl', 'kube-apiserver'):
            binary = assets / name
            binary.write_text('mock envtest binary\n')
            binary.chmod(0o444)
        assets.chmod(0o555)
        (artifacts / 'readonly-assets').write_text(str(assets))
        sys.exit(failure == 'check')
    elif args[0] != 'verify-recordings':
        sys.exit('unexpected make invocation')
elif name == 'kubectl':
    if 'deployment' in args:
        print('registry.k8s.io/jobset/jobset:v0.12.0', end='')
    else:
        print('{}')
elif name == 'jq':
    # Provenance inspection is unrelated to this filesystem cleanup regression.
    sys.stdin.read() if not any(arg.endswith('.json') for arg in args) and '-n' not in args else None
    print('{}')
elif name == 'sha256sum':
    for path in args:
        print(hashlib.sha256(Path(path).read_bytes()).hexdigest() + '  ' + path)
elif name != 'go':
    sys.exit('unexpected mock executable')
'''


class RecordCleanupTests(unittest.TestCase):
    def test_readonly_envtest_assets_and_failure_gates(self):
        for failure in ("", "record", "check", "cleanup"):
            with self.subTest(failure=failure or "success"), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                repo, artifacts, scratch, tools = (root / part for part in ("repo", "artifacts", "scratch", "tools"))
                for directory in (repo, artifacts, scratch, tools):
                    directory.mkdir()
                env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
                env.update({
                    "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                    "PATH": str(tools) + os.pathsep + env["PATH"],
                    "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "AviadHayumi/workload-map",
                    "PILOT_ARTIFACT_DIR": str(artifacts), "PILOT_RUN": "12345-1",
                    "TMPDIR": str(scratch), "MOCK_FAILURE": failure,
                })
                for name in ("kind", "make", "kubectl", "jq", "go", "sha256sum"):
                    path = tools / name
                    path.write_text(MOCK_TOOLS)
                    path.chmod(0o755)
                script = repo / "hack/e2e/renovate-pilot/record.sh"
                script.parent.mkdir(parents=True)
                shutil.copyfile(SCRIPT, script)
                pin = repo / "hack/e2e/global.env"
                pin.write_text(textwrap.dedent('''\
                    JOBSET_VERSION="${JOBSET_VERSION:-v0.10.1}"
                    KIND_NODE_IMAGE="kindest/node:v1.34.0"
                    FAKE_GPU_VERSION="0.0.72"
                    KARTA_WEBHOOK_MODE="auto"
                '''))
                recordings = repo / "test/e2e/recorded_data/jobset"
                recordings.mkdir(parents=True)
                (recordings / ".keep").touch()
                (repo / ".gitignore").write_text(".installed-versions-*\n")
                for args in (
                    ("init", "-q"), ("config", "user.name", "Example"),
                    ("config", "user.email", "example@example.com"),
                    ("config", "commit.gpgsign", "false"), ("add", "."),
                    ("commit", "-qm", "test(e2e): cleanup regression fixture"),
                ):
                    subprocess.run(["git", *args], cwd=repo, env=env, check=True, capture_output=True)
                env["PILOT_BASE_SHA"] = subprocess.check_output(
                    ["git", "rev-parse", "HEAD"], cwd=repo, env=env, text=True,
                ).strip()
                pin.write_text(pin.read_text().replace("v0.10.1", "v0.12.0"))
                result = subprocess.run(
                    ["bash", str(script)], cwd=repo, env=env, text=True,
                    capture_output=True, timeout=30,
                )
                self.assertEqual(result.returncode == 0, not failure, result.stdout + result.stderr)
                self.assertEqual((recordings / "v0.12.0").exists(), not failure)
                marker = artifacts / "commit-files.sha256"
                self.assertEqual(marker.exists(), not failure)
                self.assertFalse(list(scratch.glob("karta-*")), "scratch files were not removed")
                kubeconfig = Path((artifacts / "deleted-kubeconfig").read_text())
                self.assertFalse(kubeconfig.exists())
                if failure != "record":
                    self.assertTrue((artifacts / "readonly-assets").exists())
                    self.assertFalse(Path((artifacts / "readonly-assets").read_text()).exists())
                if not failure:
                    lines = marker.read_text().splitlines()
                    self.assertEqual(len(lines), 6)
                    for line in lines:
                        digest, relative = line.split("  ", 1)
                        self.assertEqual(digest, hashlib.sha256((repo / relative).read_bytes()).hexdigest())


if __name__ == "__main__":
    unittest.main()
