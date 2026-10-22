# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Run finite SkyPilot association probes against the isolated test API server."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time

os.environ.setdefault("SKY_RUNTIME_DIR", "/private/tmp/sky-ref-runtime")
os.environ.setdefault("SKYPILOT_DISABLE_USAGE_COLLECTION", "true")
os.environ.setdefault("SKYPILOT_USER", "sky-ref-user")
os.environ.setdefault("SKYPILOT_USER_ID", "a1b2c3d4")
os.environ.setdefault("SKYPILOT_API_SERVER_ENDPOINT", "http://127.0.0.1:46581")

import sky  # noqa: E402

ROOT = Path(__file__).resolve().parent
EVIDENCE = ROOT / "evidence"
NAMESPACE = "test-ref"


def save(name, value):
    EVIDENCE.mkdir(exist_ok=True)
    (EVIDENCE / name).write_text(json.dumps(value, indent=2, default=str) + "\n")


def pods():
    result = subprocess.run(
        ["kubectl", "--context", os.environ["KUBE_CONTEXT"], "--request-timeout=20s",
         "-n", NAMESPACE, "get", "pods", "-l", "research.karta.dev/suite=skypilot",
         "-o", "json"], check=True, capture_output=True, text=True)
    allowed_labels = {"parent", "component", "skypilot-cluster", "skypilot-cluster-name",
                      "skypilot-user", "ray-cluster-name", "ray-node-type",
                      "skypilot-head-node", "research.karta.dev/suite",
                      "kai.scheduler/queue", "runai/queue"}
    allowed_annotations = {"skypilot-cluster-name", "skypilot-user",
                           "skypilot-managed-job-id", "skypilot-managed-job-name",
                           "skypilot-workspace", "pod-group-name"}
    captured = []
    for pod in json.loads(result.stdout)["items"]:
        source = pod["metadata"]
        meta = {key: source[key] for key in ["name", "namespace", "uid", "creationTimestamp"]}
        meta["ownerReferences"] = source.get("ownerReferences", [])
        meta["labels"] = {k: v for k, v in source.get("labels", {}).items() if k in allowed_labels}
        meta["annotations"] = {k: v for k, v in source.get("annotations", {}).items() if k in allowed_annotations}
        for field in ["labels", "annotations"]:
            if "skypilot-user" in meta[field] and meta[field]["skypilot-user"] not in {"sky-ref-user", "skyrefuser"}:
                meta[field]["skypilot-user"] = "<redacted-user>"
        captured.append({"apiVersion": "v1", "kind": "Pod", "metadata": meta,
                         "spec": {"schedulerName": pod["spec"].get("schedulerName"), "containers": [{"name": c["name"], "image": c["image"], "resources": c.get("resources", {})} for c in pod["spec"]["containers"]]},
                         "status": {"phase": pod.get("status", {}).get("phase"), "containerStatuses": [{"name": c["name"], "imageID": c.get("imageID")} for c in pod.get("status", {}).get("containerStatuses", [])]}})
    return captured


def capture(name, captured_pods, targets=None, notes=""):
    value = {"provenance": {"kind": "live_capture", "capturedAtUnix": time.time(),
                            "skyVersion": sky.__version__, "skyCommit": sky.__commit__,
                            "notes": notes}, "pods": captured_pods, "targetSets": targets or {}}
    save(name, value)
    return value


def result(operation, request_id):
    path = EVIDENCE / "requests.json"
    requests = json.loads(path.read_text()) if path.exists() else []
    requests.append({"operation": operation, "requestID": str(request_id),
                     "submittedAtUnix": time.time()})
    save("requests.json", requests)
    print(f"{operation}: request {request_id}", flush=True)
    # A lost client connection does not cancel the server request. Save its ID
    # before waiting so the same request can be recovered without a new launch.
    return sky.get(request_id)


def statuses(cluster, ids):
    return {str(k): getattr(v, "value", str(v)) for k, v in sky.get(sky.job_status(cluster, ids)).items()}


def wait_complete(cluster, ids):
    deadline = time.monotonic() + 240
    while time.monotonic() < deadline:
        current = statuses(cluster, ids)
        if set(current) == {str(job_id) for job_id in ids} and all(value == "SUCCEEDED" for value in current.values()):
            return current
        if any(value in {"FAILED", "FAILED_DRIVER", "FAILED_SETUP", "CANCELLED"} for value in current.values()):
            raise RuntimeError(current)
        time.sleep(5)
    raise TimeoutError(f"Jobs did not finish on {cluster}: {current}")


def run_shared():
    cluster = "sky-ref-shared"
    bootstrap, _ = result("shared launch", sky.launch(sky.Task.from_yaml(str(ROOT / "shared-cluster.yaml")), cluster))
    wait_complete(cluster, [bootstrap])
    before = [p for p in pods() if p["metadata"]["annotations"].get("skypilot-cluster-name") == cluster]
    capture("shared-before.json", before)
    ids = []
    for name in ["job-a", "job-b"]:
        task = sky.Task.from_yaml(str(ROOT / "shared-job.yaml"))
        task.name = f"sky-ref-{name}"
        task.update_envs({"REF_JOB": name})
        job_id, _ = result(name, sky.exec(task, cluster))
        ids.append(job_id)
    deadline = time.monotonic() + 60
    concurrent = False
    while time.monotonic() < deadline:
        current = statuses(cluster, ids)
        if set(current) == {str(job_id) for job_id in ids} and all(value == "RUNNING" for value in current.values()):
            concurrent = True
            break
        time.sleep(3)
    during = pods()
    own = [p for p in during if p["metadata"]["annotations"].get("skypilot-cluster-name") == cluster]
    if len(before) != 1 or len(own) != 1:
        raise RuntimeError("Shared probe requires exactly one cluster Pod")
    if before[0]["metadata"]["uid"] != own[0]["metadata"]["uid"]:
        raise RuntimeError("Cluster Pod was replaced during the shared-job probe")
    cluster_labels = {p["metadata"]["labels"]["skypilot-cluster-name"] for p in own}
    if len(cluster_labels) != 1:
        raise RuntimeError("Expected one cluster label for shared-cluster Pods")
    cluster_label = next(iter(cluster_labels))
    targets = {"jobs": {"complete": True, "targets": [
        {"namespace": NAMESPACE, "id": str(job_id), "attributes": {"cluster": cluster_label}}
        for job_id in ids]}}
    capture("association-capture.json", own, targets,
            "Jobs are SkyPilot runtime jobs. They are not Kubernetes objects. "
            f"Both jobs observed RUNNING simultaneously: {concurrent}.")
    final = wait_complete(cluster, ids)
    queue = sky.get(sky.queue(cluster))
    safe_queue = [{k: row.get(k) for k in ["job_id", "job_name", "submitted_at", "start_at", "end_at", "resources", "status"]}
                  for row in queue if row["job_id"] in ids]
    save("shared-result.json", {"jobIDs": ids, "concurrentRunningObserved": concurrent,
                                "statusesDuringCapture": current, "finalStatuses": final,
                                "podUIDsBefore": [p["metadata"]["uid"] for p in before],
                                "podUIDsDuring": [p["metadata"]["uid"] for p in own], "jobs": safe_queue})
    if not concurrent:
        raise RuntimeError("Both jobs succeeded but concurrent execution was not observed")
    print("SHARED_PROBE_SUCCEEDED", flush=True)


def pod_identity_errors(pod):
    meta = pod.get("metadata", {})
    labels = meta.get("labels", {})
    annotations = meta.get("annotations", {})
    errors = []
    if meta.get("namespace") != NAMESPACE:
        errors.append("Pod namespace must be test-ref")
    if not meta.get("uid"):
        errors.append("Pod UID is missing")
    cluster_label = labels.get("skypilot-cluster-name")
    if not cluster_label or labels.get("ray-cluster-name") != cluster_label:
        errors.append("Pod cluster labels are missing or inconsistent")
    if not annotations.get("skypilot-cluster-name"):
        errors.append("Pod cluster annotation is missing")
    return errors


def run_multinode():
    cluster = "sky-ref-multinode"
    job_id, _ = result("multinode launch", sky.launch(sky.Task.from_yaml(str(ROOT / "multinode.yaml")), cluster))
    captured = [p for p in pods() if p.get("metadata", {}).get("annotations", {}).get("skypilot-cluster-name") == cluster]
    errors = []
    if len(captured) != 2:
        errors.append("Expected exactly two cluster Pods")
    for pod in captured:
        errors.extend(pod_identity_errors(pod))
    uids = [p["metadata"].get("uid") for p in captured]
    if len(set(uids)) != 2 or any(not uid for uid in uids):
        errors.append("Expected two distinct nonempty Pod UIDs")
    labels = [p["metadata"].get("labels", {}) for p in captured]
    if sorted(label.get("ray-node-type", "") for label in labels) != ["head", "worker"]:
        errors.append("Expected one head and one worker Pod")
    if len({label.get("skypilot-cluster-name") for label in labels}) != 1:
        errors.append("Expected one shared cluster label")
    if any(p["metadata"].get("ownerReferences") for p in captured):
        errors.append("Expected bare sibling Pods without owner references")
    mapping = {"passed": not errors, "errors": errors, "podUIDs": uids}
    capture("multinode-capture.json", captured,
            notes="Pod snapshot; mapping validation is recorded separately from runtime completion.")
    try:
        final = wait_complete(cluster, [job_id])
    except Exception as error:
        save("multinode-result.json", {"jobID": job_id,
             "runtime": {"succeeded": False, "errorType": type(error).__name__},
             "mapping": mapping})
        raise
    save("multinode-result.json", {"jobID": job_id,
         "runtime": {"succeeded": True, "finalStatuses": final}, "mapping": mapping})
    if errors:
        raise RuntimeError("Multinode mapping validation failed: " + "; ".join(errors))
    print("MULTINODE_PROBE_SUCCEEDED", flush=True)


def managed_mapping(captured, ids, task_name):
    errors = []
    observed_ids = set()
    for pod in captured:
        errors.extend(pod_identity_errors(pod))
        annotations = pod.get("metadata", {}).get("annotations", {})
        observed_ids.add(annotations.get("skypilot-managed-job-id"))
        if annotations.get("skypilot-managed-job-name") != task_name:
            errors.append("Managed Pod task-name annotation does not match the submitted task")
    expected_ids = {str(job_id) for job_id in ids}
    if not expected_ids or observed_ids != expected_ids:
        errors.append("Expected annotated Pod metadata for every submitted managed job ID")
    uids = [p.get("metadata", {}).get("uid") for p in captured]
    return {"passed": not errors, "errors": errors, "podUIDs": uids,
            "observedJobIDs": sorted(observed_ids - {None})}


def run_managed():
    task = sky.Task.from_yaml(str(ROOT / "managed-job.yaml"))
    ids, _ = result("managed launch", sky.jobs.launch(task, name="sky-ref-managed"))
    if not ids or len(set(ids)) != len(ids):
        raise RuntimeError("Expected nonempty distinct managed job IDs")
    expected_ids = {str(job_id) for job_id in ids}
    deadline = time.monotonic() + 900
    captured = {}
    state = []
    while time.monotonic() < deadline:
        current_pods = [p for p in pods() if p.get("metadata", {}).get("annotations", {}).get("skypilot-managed-job-id") in expected_ids]
        for pod in current_pods:
            meta = pod["metadata"]
            captured[(meta.get("namespace"), meta.get("uid") or meta.get("name"))] = pod
        if current_pods:
            capture("managed-capture.json", list(captured.values()),
                    notes="Observed managed-job annotations; mapping validation is recorded separately from runtime completion.")
        queue = sky.get(sky.jobs.queue(refresh=False, job_ids=ids))
        state = [{"jobID": str(row.get("job_id")),
                  "status": getattr(row["status"], "value", str(row["status"]))}
                 for row in queue]
        mapping = managed_mapping(list(captured.values()), ids, task.name)
        complete = ({row["jobID"] for row in state} == expected_ids and
                    all(row["status"] == "SUCCEEDED" for row in state))
        failed = any(row["status"].startswith("FAILED") or row["status"] == "CANCELLED" for row in state)
        if complete or failed:
            save("managed-result.json", {"jobIDs": ids,
                 "runtime": {"succeeded": complete, "finalStatuses": state},
                 "mapping": mapping, "metadataCaptured": bool(captured)})
            if failed:
                raise RuntimeError(f"Managed runtime failed: {state}")
            if not mapping["passed"]:
                raise RuntimeError("Managed mapping validation failed: " + "; ".join(mapping["errors"]))
            print("MANAGED_PROBE_SUCCEEDED", flush=True)
            return
        time.sleep(10)
    save("managed-result.json", {"jobIDs": ids,
         "runtime": {"succeeded": False, "finalStatuses": state, "errorType": "TimeoutError"},
         "mapping": managed_mapping(list(captured.values()), ids, task.name),
         "metadataCaptured": bool(captured)})
    raise TimeoutError("Managed job did not finish within 15 minutes")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("probe", choices=["shared", "multinode", "managed"])
    args = parser.parse_args()
    {"shared": run_shared, "multinode": run_multinode, "managed": run_managed}[args.probe]()
