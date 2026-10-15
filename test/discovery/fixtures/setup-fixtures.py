# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Create the seven-root acceptance fixture in an explicitly selected kind cluster."""

import json
import time

from fixture_common import BASE, NS, run, validate_environment


def apply(value):
    return run("apply", "-f", "-", body=json.dumps(value))


def wait_for_pods(selector, expected):
    deadline = time.monotonic() + 300
    while time.monotonic() < deadline:
        pods = json.loads(run("get", "pods", "-n", NS, "-l", selector, "-o", "json"))["items"]
        if len(pods) == expected and all(
            any(condition.get("type") == "Ready" and condition.get("status") == "True"
                for condition in pod.get("status", {}).get("conditions", []))
            for pod in pods
        ):
            return
        time.sleep(2)
    raise SystemExit(f"Timed out waiting for {expected} ready Pods matching {selector}")


validate_environment()
for crd in ["jobsets.jobset.x-k8s.io", "leaderworkersets.leaderworkerset.x-k8s.io"]:
    run("wait", "--for=condition=Established", "--timeout=180s", f"crd/{crd}")
for namespace, deployment in [
    ("jobset-system", "jobset-controller-manager"),
    ("lws-system", "lws-controller-manager"),
]:
    run("rollout", "status", f"deployment/{deployment}", "-n", namespace, "--timeout=300s")

apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": NS}})
crd, bundle = (BASE / "generic-crd.yaml").read_text().split("\n---\n", 1)
run("apply", "-f", "-", body=crd)
run("wait", "--for=condition=Established", "--timeout=180s", "crd/bundles.discovery-test.example.com")
run("apply", "-f", "-", body=bundle)
run("apply", "-f", str(BASE / "workloads.yaml"))
bundle_uid = run("get", "bundle", "sdk-bundle", "-n", NS, "-o", "jsonpath={.metadata.uid}").strip()
for name, replicas in [("sdk-generic-a", 2), ("sdk-generic-b", 1), ("sdk-zero", 0)]:
    metadata = {"name": name, "namespace": NS}
    if name != "sdk-zero":
        metadata["ownerReferences"] = [{
            "apiVersion": "discovery-test.example.com/v1", "kind": "Bundle",
            "name": "sdk-bundle", "uid": bundle_uid, "controller": True,
        }]
    apply({"apiVersion": "apps/v1", "kind": "Deployment", "metadata": metadata, "spec": {
        "replicas": replicas, "selector": {"matchLabels": {"app": name}},
        "template": {"metadata": {"labels": {"app": name}}, "spec": {"containers": [{
            "name": "work", "image": "busybox:1.37", "command": ["sh", "-c", "sleep 3600"],
        }]}},
    }})
apply({"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": "sdk-finite", "namespace": NS}, "spec": {
    "template": {"spec": {"restartPolicy": "Never", "containers": [{
        "name": "work", "image": "busybox:1.37", "command": ["sh", "-c", "exit 0"],
    }]}},
}})
for name, count in [("sdk-deployment", 2), ("sdk-generic-a", 2), ("sdk-generic-b", 1)]:
    run("rollout", "status", f"deployment/{name}", "-n", NS, "--timeout=300s")
    wait_for_pods(f"app={name}", count)
run("wait", "--for=condition=complete", "job/sdk-finite", "-n", NS, "--timeout=300s")
run("wait", "--for=condition=Ready", "pod/sdk-standalone", "-n", NS, "--timeout=300s")
wait_for_pods("jobset.sigs.k8s.io/jobset-name=sdk-jobset", 3)
wait_for_pods("leaderworkerset.sigs.k8s.io/name=sdk-lws", 4)
print("Fixtures ready: run build-oracle.py to capture current identities")
