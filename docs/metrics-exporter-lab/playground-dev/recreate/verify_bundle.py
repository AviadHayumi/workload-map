# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Verify the bundled inputs without contacting a cluster."""
import hashlib
import json
from pathlib import Path

root = Path(__file__).parent
expected = json.loads((root / 'SHA256SUMS.json').read_text())
bad = []
for name, digest in expected.items():
    path = root / name
    if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        bad.append(name)
if bad:
    raise SystemExit('Missing or modified bundle inputs: ' + ', '.join(bad))
print(f'PASS: {len(expected)} bundled files match their captured SHA-256 hashes.')
