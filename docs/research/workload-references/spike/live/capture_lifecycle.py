# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Capture one disposable Pod lifecycle stage after both informers converge."""

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import subprocess
import tempfile
import time
from urllib.request import urlopen

from replay_scrapes import samples


def scrape(port, metric):
    with urlopen("http://127.0.0.1:" + str(port) + "/metrics", timeout=15) as response:
        body = response.read().decode()
    lines = [line for line in body.splitlines() if line.startswith(metric + "{")
             and 'namespace="test-ref"' in line
             and ('pod="ref-lifecycle"' in line or 'pod_name="ref-lifecycle"' in line)]
    return "\n".join(lines) + ("\n" if lines else "")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stage", required=True)
    parser.add_argument("--label", default="")
    parser.add_argument("--absent", action="store_true")
    parser.add_argument("--previous-uid")
    parser.add_argument("--ksm-port", type=int, default=18091)
    parser.add_argument("--karta-port", type=int, default=18092)
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z0-9-]+", args.stage):
        raise ValueError("Stage must be a simple lowercase filename")
    output = Path(__file__).resolve().parent
    started = datetime.now(timezone.utc).isoformat()
    read = subprocess.run(["kubectl", "--context", "kmetric-playground-dev", "--request-timeout=30s",
                           "-n", "test-ref", "get", "pod", "ref-lifecycle", "--ignore-not-found", "-o", "json"],
                          capture_output=True, text=True, timeout=40)
    if read.returncode:
        raise RuntimeError("API Pod read failed; no successful stage was recorded")
    pod = json.loads(read.stdout) if read.stdout.strip() else None
    if args.absent != (pod is None):
        raise ValueError("API Pod presence does not match the requested stage")
    capture = {"provenance": {"kind": "live_capture", "startedAt": started,
                              "scope": "disposable test-ref/ref-lifecycle Pod"}, "pods": []}
    uid = None
    if pod:
        metadata = pod["metadata"]
        uid = metadata["uid"]
        label = metadata.get("labels", {}).get("skypilot-cluster-name", "")
        if label != args.label:
            raise ValueError("API label does not match the requested stage")
        if args.previous_uid == uid:
            raise ValueError("Recreated Pod did not receive a new UID")
        capture["pods"].append({"apiVersion": "v1", "kind": "Pod", "metadata": {
            "namespace": metadata["namespace"], "name": metadata["name"], "uid": uid,
            "labels": {"skypilot-cluster-name": label} if label else {},
            "ownerReferences": metadata.get("ownerReferences", [])}})
    with tempfile.TemporaryDirectory() as directory:
        temp = Path(directory)
        for attempt in range(1, 21):
            ksm_text = scrape(args.ksm_port, "kube_pod_labels")
            karta_text = scrape(args.karta_port, "karta_pod_workload_info")
            (temp / "ksm.prom").write_text(ksm_text)
            (temp / "karta.prom").write_text(karta_text)
            ksm = samples(temp / "ksm.prom", "kube_pod_labels")
            karta = samples(temp / "karta.prom", "karta_pod_workload_info")
            valid = [row for _, row, value in karta if row.get("observation_valid") == "true" and value == 1]
            if args.absent:
                converged = not ksm and not valid
            else:
                expected_root = {"pod_uid": uid, "pod_name": "ref-lifecycle", "namespace": "test-ref",
                                 "workload_uid": uid, "workload_name": "ref-lifecycle", "workload_kind": "Pod",
                                 "workload_group": "", "workload_version": "v1"}
                converged = (len(ksm) == 1 and ksm[0][1].get("uid") == uid
                             and ksm[0][1].get("label_skypilot_cluster_name", "") == args.label
                             and len(valid) == 1
                             and all(valid[0].get(key) == value for key, value in expected_root.items()))
            if converged:
                break
            time.sleep(2)
        else:
            raise TimeoutError("Informer snapshots did not converge within 20 attempts; no successful stage was recorded")
    capture["provenance"]["completedAt"] = datetime.now(timezone.utc).isoformat()
    stem = "lifecycle-" + args.stage
    (output / (stem + ".capture.json")).write_text(json.dumps(capture, indent=2) + "\n")
    (output / (stem + ".ksm.prom")).write_text(ksm_text)
    (output / (stem + ".karta.prom")).write_text(karta_text)
    report = {"kind": "live_lifecycle_stage", "stage": args.stage, "podUID": uid,
              "expectedAssociation": args.label or None, "podAbsent": args.absent,
              "ksmSamples": len(ksm), "validKartaSamples": len(valid), "pollAttempts": attempt,
              "startedAt": started, "completedAt": capture["provenance"]["completedAt"],
              "note": "Verified eventual convergence after this operation; no atomic cross-informer timing guarantee."}
    (output / (stem + ".report.json")).write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
