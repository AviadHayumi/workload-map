# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Build a portable promtool test from captured Karta and KSM metric families."""

import argparse
import hashlib
import json
from pathlib import Path
import re


LABEL = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)=("(?:[^"\\]|\\.)*")(?:,|$)')
SAMPLE = re.compile(r'([a-zA-Z_:][a-zA-Z0-9_:]*)\{(.*)\}\s+([0-9.eE+-]+)')
PLATFORMS = {
    "osmo": "label_osmo_workflow_uuid",
    "sky": "label_skypilot_cluster_name",
}


def samples(path, metric):
    output = []
    for line in Path(path).read_text().splitlines():
        if not line.startswith(metric + "{"):
            continue
        match = SAMPLE.fullmatch(line)
        if match is None:
            raise ValueError("Malformed sample in " + str(path))
        labels, position = {}, 0
        for item in LABEL.finditer(match[2]):
            if item.start() != position or item[1] in labels:
                raise ValueError("Malformed or repeated label")
            labels[item[1]] = json.loads(item[2])
            position = item.end()
        if position != len(match[2]):
            raise ValueError("Unparsed label text")
        output.append((line.rsplit(None, 1)[0], labels, float(match[3])))
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--karta", required=True)
    parser.add_argument("--ksm", required=True)
    parser.add_argument("--capture", action="append", default=[])
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    karta = samples(args.karta, "karta_pod_workload_info")
    ksm = samples(args.ksm, "kube_pod_labels")
    if not karta or not ksm:
        raise ValueError("Both actual metric families must be present")

    verified_roots = []
    for path in args.capture:
        capture = json.loads(Path(path).read_text())
        for pod in capture["pods"]:
            metadata = pod["metadata"]
            matches = [labels for _, labels, value in karta
                       if labels.get("namespace") == metadata["namespace"]
                       and labels.get("pod_uid") == metadata["uid"]
                       and labels.get("observation_valid") == "true" and value == 1]
            if len(matches) != 1:
                raise ValueError("Expected one valid Karta row for captured Pod " + metadata["name"])
            row = matches[0]
            if row.get("pod_name") != metadata["name"]:
                raise ValueError("Pod UID and name disagree")
            owners = metadata.get("ownerReferences")
            ownerless = owners is not None and not any(owner.get("controller") is True for owner in owners)
            if ownerless:
                expected_root = {"workload_uid": metadata["uid"], "workload_name": metadata["name"],
                                 "workload_kind": "Pod", "workload_group": "", "workload_version": "v1"}
                if any(row.get(key) != value for key, value in expected_root.items()):
                    raise ValueError("An ownerless captured Pod was not preserved as its own root")
            for source_key, metric_key in [("osmo.workflow_uuid", PLATFORMS["osmo"]),
                                           ("skypilot-cluster-name", PLATFORMS["sky"])]:
                source_value = metadata.get("labels", {}).get(source_key)
                if source_value:
                    observed = {labels.get(metric_key) for _, labels, value in ksm
                                if labels.get("namespace") == metadata["namespace"]
                                and labels.get("uid") == metadata["uid"] and value == 1}
                    if observed != {source_value}:
                        raise ValueError("Capture and KSM metadata disagree for " + metadata["name"])
            verified_roots.append({"namespace": metadata["namespace"], "pod": metadata["name"],
                                   "podUID": metadata["uid"], "workloadUID": row.get("workload_uid"),
                                   "ownerlessRootVerified": ownerless})

    checks, counts = [], {}
    for platform, label in PLATFORMS.items():
        expected = []
        for _, root, value in karta:
            if root.get("observation_valid") != "true" or value != 1:
                continue
            candidates = {metadata[label] for _, metadata, sample_value in ksm
                          if metadata.get("namespace") == root.get("namespace")
                          and metadata.get("uid") == root.get("pod_uid")
                          and metadata.get(label) and sample_value == 1}
            if len(candidates) != 1:
                continue
            labels = dict(root)
            labels[label] = next(iter(candidates))
            name = "research:karta_pod_" + platform + "_association"
            encoded = ",".join(key + "=" + json.dumps(val) for key, val in sorted(labels.items()))
            expected.append({"labels": name + "{" + encoded + "}", "value": 1})
        counts[platform] = len(expected)
        checks.append({"expr": "research:karta_pod_" + platform + "_association", "eval_time": "1m", "exp_samples": expected})
    if sum(counts.values()) == 0:
        raise ValueError("No platform Pod matched across the actual scrapes")

    inputs = [{"series": series, "values": str(value) + " " + str(value)} for series, _, value in karta + ksm]
    test = {"rule_files": ["../promql/associations.rules.yaml"], "evaluation_interval": "1m",
            "tests": [{"name": "Captured live Karta roots joined to actual isolated KSM metadata",
                       "interval": "1m", "input_series": inputs, "promql_expr_test": checks}]}
    output = Path(args.output)
    output.write_text(json.dumps(test, indent=2) + "\n")
    report = {"kind": "live_scrape_replay", "kartaSource": Path(args.karta).name,
              "ksmSource": Path(args.ksm).name,
              "sourceSHA256": {Path(path).name: hashlib.sha256(Path(path).read_bytes()).hexdigest()
                               for path in [args.karta, args.ksm] + args.capture},
              "matchedRows": counts,
              "capturedPodRootChecks": verified_roots,
              "note": "Actual captured samples replayed locally; this is not a running Prometheus scrape test."}
    output.with_suffix(".report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
