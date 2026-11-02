# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Materialize, test and build the saved demo helpers in an empty output directory."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output-dir', type=Path, required=True)
parser.add_argument('--prepare-only', action='store_true', help='Materialize verified source without running Go')
args = parser.parse_args()
base = Path(__file__).resolve().parents[1]
output = args.output_dir.resolve()
if output.exists():
    parser.error('output-dir must not exist; existing files will not be overwritten')
output.mkdir(parents=True)
pins = json.loads((base / 'versions.json').read_text())
helpers = output / 'helpers'
for source in (base / 'sources/helpers').rglob('*.txt'):
    target = helpers / source.relative_to(base / 'sources/helpers')
    target = target.with_suffix('')
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
commit = pins['fake_gpu_operator_source_commit']
url = 'https://codeload.github.com/run-ai/fake-gpu-operator/tar.gz/' + commit
with urllib.request.urlopen(url, timeout=60) as response:
    archive = response.read(20 * 1024 * 1024 + 1)
if hashlib.sha256(archive).hexdigest() != pins['fake_gpu_operator_archive_sha256']:
    raise SystemExit('Pinned source archive checksum mismatch')
source_root = output / 'fake-gpu-source'
source_root.mkdir()
with tarfile.open(fileobj=io.BytesIO(archive)) as archive_file:
    for member in archive_file.getmembers():
        parts = member.name.split('/', 1)
        if len(parts) == 2 and parts[1]:
            member.name = parts[1]
            archive_file.extract(member, source_root, filter='data')
entrypoint = source_root / 'cmd/karta-demo-source'
entrypoint.mkdir()
for source in (base / 'sources/fake-gpu-source').glob('*.go.txt'):
    shutil.copyfile(source, entrypoint / source.name.removesuffix('.txt'))
if args.prepare_only:
    print('Prepared checksum-verified sources in', output)
    raise SystemExit(0)
env = os.environ.copy()
env['GOTOOLCHAIN'] = 'go' + pins['go_version']
for directory, package in [(helpers, './...'), (source_root, './cmd/karta-demo-source')]:
    subprocess.run(['go', 'test', '-race', package], cwd=directory, env=env, check=True)
bin_dir = output / 'bin'
bin_dir.mkdir()
env.update(CGO_ENABLED='0', GOOS='linux', GOARCH='amd64')
for name in ['gpu-demo-randomizer', 'workload-gpu-observer', 'gpu-observer', 'fake-gpu-source']:
    cwd, package = (source_root, './cmd/karta-demo-source') if name == 'fake-gpu-source' else (helpers, './' + name)
    subprocess.run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(bin_dir / name), package],
                   cwd=cwd, env=env, check=True)
with tarfile.open(output / 'helpers-layer.tar', 'w') as layer:
    for binary in sorted(bin_dir.iterdir()):
        layer.add(binary, arcname=binary.name)
    layer.add(source_root / 'LICENSE', arcname='licenses/fake-gpu-operator-LICENSE')
    layer.add(base.parents[3] / 'LICENSE', arcname='licenses/karta-LICENSE')
print('Tests passed. Linux amd64 binaries and helpers-layer.tar are in', output)
print('Runtime base:', pins['helper_runtime_base'])
