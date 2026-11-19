#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Record Job scheduling inputs from a temporary namespace on the explicit context."""
import argparse
import json
from pathlib import Path
import subprocess
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--context", required=True)
parser.add_argument("--output", required=True, type=Path)
args = parser.parse_args()


def kubectl(*command, obj=None):
    output = subprocess.check_output(
        ["kubectl", "--context", args.context, "--request-timeout=30s", *command],
        input=json.dumps(obj) if obj is not None else None, text=True,
    )
    return json.loads(output) if output.strip() else None


version = kubectl("version", "-o", "json")["serverVersion"]["gitVersion"]
namespace = kubectl("create", "-f", "-", "-o", "json", obj={
    "apiVersion": "v1", "kind": "Namespace", "metadata": {"generateName": "cel-groups-"},
})["metadata"]["name"]
try:
    jobs = []
    for name, count in (("train", 2), ("evaluate", 1)):
        job = kubectl("-n", namespace, "create", "-f", "-", "-o", "json", obj={
            "apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": name},
            "spec": {"parallelism": count, "completions": count, "template": {"spec": {
                "restartPolicy": "Never", "automountServiceAccountToken": False,
                "containers": [{"name": "worker", "image": "registry.k8s.io/pause:3.10"}],
            }}},
        })
        jobs.append(job)
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        pods = kubectl("-n", namespace, "get", "pods", "-o", "json")["items"]
        if len(pods) == 3:
            break
        time.sleep(1)
    else:
        raise SystemExit("Job controller did not create the expected three Pods")

    # Only these fields take part in the replay. Node names, addresses, managed
    # fields, service accounts and container environment are outside the fixture.
    inputs = {"serverVersion": version, "jobs": [], "pods": []}
    for job in jobs:
        inputs["jobs"].append({
            "apiVersion": job["apiVersion"], "kind": job["kind"],
            "metadata": {k: job["metadata"][k] for k in ("name", "namespace", "uid")},
            "spec": {"parallelism": job["spec"]["parallelism"]},
        })
    for pod in sorted(pods, key=lambda x: x["metadata"]["name"]):
        meta = pod["metadata"]
        inputs["pods"].append({
            "metadata": {k: meta[k] for k in ("name", "namespace", "uid", "ownerReferences", "labels")},
        })
    args.output.write_text(json.dumps(inputs, indent=2) + "\n")
finally:
    subprocess.run(["kubectl", "--context", args.context, "delete", "namespace",
                    namespace, "--wait=false"], check=True)
