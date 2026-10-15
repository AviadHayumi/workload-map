# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Copy reference inputs into a working directory with explicit cluster settings."""

import argparse
import json
import shutil
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--node", required=True)
parser.add_argument("--storage-class", required=True)
parser.add_argument("--output", required=True, type=Path)
args = parser.parse_args()
source = Path(__file__).resolve().parent
destination = args.output.resolve()
if destination == source or source in destination.parents:
    parser.error("--output must be outside the recorded evidence directory")
if destination.exists():
    parser.error("--output must be a new directory")


def configure(value):
    if isinstance(value, dict):
        return {
            key: args.storage_class if key == "storageClassName" else configure(item)
            for key, item in value.items()
        }
    if isinstance(value, list):
        return [configure(item) for item in value]
    if isinstance(value, str):
        return value.replace("__GPU_NODE__", args.node)
    return value


shutil.copytree(source, destination)
for path in destination.rglob("*"):
    if not path.is_file():
        continue
    if path.suffix == ".json":
        path.write_text(json.dumps(configure(json.loads(path.read_text())), indent=2) + "\n")
    elif path.suffix == ".yaml":
        path.write_text(path.read_text().replace("__GPU_NODE__", args.node))
print(f"Rendered reference inputs in {destination}. No cluster changes were made.")
