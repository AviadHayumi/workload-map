# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Replay only completed disposable lifecycle stages as PromQL rule tests."""

import argparse
import json
from pathlib import Path

from replay_scrapes import samples


def labelset(name, labels):
    return name + "{" + ",".join(key + "=" + json.dumps(value) for key, value in sorted(labels.items())) + "}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("stages", nargs="+")
    args = parser.parse_args()
    base = Path(__file__).resolve().parent
    snapshots, checks, uids = [], [], []
    for index, stage in enumerate(args.stages):
        stem = base / ("lifecycle-" + stage)
        report = json.loads(stem.with_suffix(".report.json").read_text())
        if report["kind"] != "live_lifecycle_stage":
            raise ValueError("Stage has no verified live result")
        karta = samples(stem.with_suffix(".karta.prom"), "karta_pod_workload_info")
        ksm = samples(stem.with_suffix(".ksm.prom"), "kube_pod_labels")
        snapshots.append({series: value for series, _, value in karta + ksm})
        expected = []
        for _, root, value in karta:
            if root.get("observation_valid") != "true" or value != 1:
                continue
            matches = {row["label_skypilot_cluster_name"] for _, row, value in ksm
                       if value == 1 and row.get("namespace") == root.get("namespace")
                       and row.get("uid") == root.get("pod_uid") and row.get("label_skypilot_cluster_name")}
            if len(matches) == 1:
                result = dict(root)
                result["label_skypilot_cluster_name"] = next(iter(matches))
                expected.append({"labels": labelset("research:karta_pod_lifecycle_association", result), "value": 1})
        assert len(expected) == (1 if report["expectedAssociation"] and not report["podAbsent"] else 0)
        uids.append(report["podUID"])
        checks.append({"expr": "research:karta_pod_lifecycle_association", "eval_time": str(index) + "m",
                       "exp_samples": expected})
    if len({uid for uid in uids if uid}) < 2:
        raise ValueError("The lifecycle run must contain a recreated Pod with a new observed UID")
    all_series = set().union(*(set(snapshot) for snapshot in snapshots))
    input_series = [{"series": series, "values": " ".join(str(snapshot[series]) if series in snapshot else "stale"
                                                           for snapshot in snapshots)}
                    for series in sorted(all_series)]
    test = {"rule_files": ["lifecycle.rules.yaml"], "evaluation_interval": "1m", "tests": [{
        "name": "Actual lifecycle snapshots with scrape staleness at missing series",
        "interval": "1m", "input_series": input_series, "promql_expr_test": checks}]}
    first = samples(base / ("lifecycle-" + args.stages[0] + ".karta.prom"), "karta_pod_workload_info")
    last = samples(base / ("lifecycle-" + args.stages[-1] + ".ksm.prom"), "kube_pod_labels")
    assert first and last and first[0][1]["pod_uid"] != last[0][1]["uid"]
    test["tests"].append({"name": "Deliberately stale old root cannot join new same-name Pod metadata",
                          "interval": "1m", "input_series": [{"series": series, "values": str(value)}
                                                                 for series, _, value in first + last],
                          "promql_expr_test": [{"expr": "research:karta_pod_lifecycle_association",
                                               "eval_time": "0m", "exp_samples": []}]})
    (base / "lifecycle-replay.test.json").write_text(json.dumps(test, indent=2) + "\n")
    report = {"kind": "live_lifecycle_replay", "stages": args.stages, "observedPodUIDs": uids,
              "checks": len(checks) + 1,
              "note": "Each stage was captured after both informers converged. Minute spacing is local replay time, not measured operation latency. The last check intentionally combines captured old-root and new-Pod samples."}
    (base / "lifecycle-replay.report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
