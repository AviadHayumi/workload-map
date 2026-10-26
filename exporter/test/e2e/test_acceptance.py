# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

import copy
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from acceptance import CheckFailed, HEALTH, INFO, STATUS, PHASES, ROOT, fixture, inspect, parse_metrics, resource
from faults import inspect_group_retirement


class ContractInspectorTest(unittest.TestCase):
    def setUp(self):
        self.root = {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"namespace": "test", "name": "app", "uid": "root"}}
        self.child = {"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": {"namespace": "test", "name": "child", "uid": "child", "ownerReferences": [{"name": "app", "uid": "root", "controller": True}]}}
        self.pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {"namespace": "test", "name": "worker", "uid": "pod", "ownerReferences": [{"name": "child", "uid": "child", "controller": True}]}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}}
        self.objects = [self.root, self.child, self.pod]
        self.described = {("apps", "Deployment"): "v1", ("apps", "ReplicaSet"): "v1"}
        self.identity = dict(namespace="test", workload="app", workload_group="apps", workload_version="v1", workload_kind="Deployment", workload_uid="root", owner_resolution="resolved")
        self.rows = [{"metric": INFO, "labels": dict(self.identity, pod="worker", pod_uid="pod", component="deployment", component_instance="", replica_group_id=""), "value": 1}]
        self.rows += [{"metric": STATUS, "labels": dict(self.identity, phase=phase, karta="apps-deployment-v1", karta_source="catalog"), "value": int(phase == "Running")} for phase in PHASES]
        self.rows += [{"metric": HEALTH, "labels": dict(scope="workload", namespace="test", group="apps", version="v1", kind="Deployment", workload="app", workload_uid="root", reason="none"), "value": 1}]
        self.expected = {"pods": 1, "mapped": 1, "groups": [{"component": "deployment", "component_instance": "", "replica_group_id": "", "pods": 1}], "phases": ["Running"]}

    def inspect(self, rows=None):
        return inspect(rows if rows is not None else self.rows, self.objects, self.root, self.expected, self.described, "test")

    def test_complete_chain_and_zero_pod_root(self):
        self.assertEqual(self.inspect()["mappedPods"], 1)
        self.objects = [self.root]
        self.rows = [r for r in self.rows if r["metric"] != INFO]
        self.expected.update(pods=0, mapped=0, groups=[])
        self.assertEqual(self.inspect()["ownedPods"], 0)

    def test_kubectl_resource_selection_keeps_grouped_api_version(self):
        self.assertEqual(resource(self.root), "Deployment.v1.apps")
        self.assertEqual(resource(self.pod), "Pod")

    def test_selected_version_is_independent_of_preferred_inventory_version(self):
        self.root["apiVersion"] = "apps/v2"
        self.assertEqual(self.inspect()["mappedPods"], 1)
        self.rows[0]["labels"]["workload_version"] = "v2"
        with self.assertRaisesRegex(CheckFailed, "selected definition"):
            self.inspect()

    def test_extra_described_child_status_is_rejected(self):
        bad = copy.deepcopy(next(r for r in self.rows if r["metric"] == STATUS))
        bad["labels"].update(workload="child", workload_uid="child", workload_kind="ReplicaSet")
        with self.assertRaisesRegex(CheckFailed, "child root"):
            self.inspect(self.rows + [bad])

    def test_old_pod_lifetime_is_rejected(self):
        self.rows[0]["labels"]["pod_uid"] = "old-pod"
        with self.assertRaisesRegex(CheckFailed, "unexpected Pod UID"):
            self.inspect()

    def test_duplicate_pod_attribution_is_rejected(self):
        with self.assertRaisesRegex(CheckFailed, "multiple attributions"):
            self.inspect(self.rows + [self.rows[0]])

    def test_stale_health_reason_cannot_survive_recovery(self):
        old = copy.deepcopy(self.rows[-1])
        old["labels"]["reason"] = "permission_denied"
        old["value"] = 0
        with self.assertRaisesRegex(CheckFailed, "old health reason"):
            self.inspect(self.rows + [old])

    def test_owner_replacement_is_not_accepted_by_name(self):
        self.child["metadata"]["ownerReferences"][0]["uid"] = "replacement"
        with self.assertRaisesRegex(CheckFailed, "unresolved owner"):
            self.inspect()

    def test_incomplete_phase_set_is_rejected(self):
        self.rows = [r for r in self.rows if r["labels"].get("phase") != "Failed"]
        with self.assertRaisesRegex(CheckFailed, "nine dense phase"):
            self.inspect()

    def test_not_ready_is_distinct_from_exporter_health(self):
        self.pod["status"]["conditions"] = []
        with self.assertRaisesRegex(CheckFailed, "not ready"):
            self.inspect()

    def exposition(self, rows):
        types = ["# TYPE " + name + " gauge" for name in {r["metric"] for r in rows}]
        samples = [r["metric"] + "{" + ",".join(k + "=" + json.dumps(v) for k, v in sorted(r["labels"].items())) + "} " + str(r["value"]) for r in rows]
        return "\n".join(types + samples)

    def test_exact_public_label_sets(self):
        self.assertEqual(len(parse_metrics(self.exposition(self.rows))), 11)
        self.rows[0]["labels"]["uid"] = self.rows[0]["labels"].pop("pod_uid")
        with self.assertRaisesRegex(CheckFailed, "incorrect labels"):
            parse_metrics(self.exposition(self.rows))

    def test_legacy_metric_family_is_rejected(self):
        with self.assertRaisesRegex(CheckFailed, "unexpected Karta family"):
            parse_metrics(self.exposition(self.rows) + '\nkarta_workload_info{namespace="test"} 1')

    def test_escaped_definition_labels_parse_without_corruption(self):
        self.rows[0]["labels"]["component_instance"] = 'quote"slash\\newline\n'
        parsed = parse_metrics(self.exposition(self.rows))
        self.assertEqual(parsed[0]["labels"]["component_instance"], self.rows[0]["labels"]["component_instance"])

    def test_all_recorded_workload_cases_have_manifests(self):
        expected = json.loads((ROOT / "expectations.json").read_text())
        self.assertEqual(len(expected), 21)
        for case, want in expected.items():
            doc = fixture(case)
            self.assertIn("apiVersion", doc)
            self.assertNotIn("namespace", doc["metadata"])
            self.assertEqual(sum(g["pods"] for g in want["groups"]), want["mapped"])


class CLIFaultTest(unittest.TestCase):
    def test_group_retirement_checks_every_replica_and_family(self):
        group = "acceptance-test.karta.test"
        row = {"metric": HEALTH, "labels": {"group": group, "scope": "resource_type"}, "value": 0}
        snapshots = {"replica-a": ("", [row]), "replica-b": ("", [row])}
        inspect_group_retirement(snapshots, group, 2, removed=False)
        snapshots["replica-a"] = ("", [])
        with self.assertRaisesRegex(CheckFailed, "has not observed"):
            inspect_group_retirement(snapshots, group, 2, removed=False)
        for metric, label in ((HEALTH, "group"), (INFO, "workload_group"), (STATUS, "workload_group")):
            snapshots["replica-b"] = ("", [{"metric": metric, "labels": {label: group}, "value": 1}])
            with self.subTest(metric=metric), self.assertRaisesRegex(CheckFailed, "replica-b retains rows"):
                inspect_group_retirement(snapshots, group, 2, removed=True)
        snapshots["replica-b"] = ("", [])
        inspect_group_retirement(snapshots, group, 2, removed=True)
        with self.assertRaisesRegex(CheckFailed, "endpoint count"):
            inspect_group_retirement({"replica-a": ("", [])}, group, 2, removed=True)

    def test_fault_helper_failure_is_reported_by_script_entrypoint(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            kubectl = root / "kubectl"
            kubectl.write_text("#!/bin/sh\nexit 0\n")
            kubectl.chmod(0o755)
            output = root / "result"
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ.get("PATH", ""))
            result = subprocess.run([sys.executable, str(ROOT / "acceptance.py"),
                                     "--kubeconfig", str(root / "unused"), "--context", "unused",
                                     "--suite", "faults", "--image", "example.invalid/exporter:test",
                                     "--output", str(output)], env=env, text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 1)
            self.assertNotIn("Traceback", result.stderr)
            summary = json.loads((output / "summary.json").read_text())
            self.assertFalse(summary["passed"])
            self.assertIn("acceptance namespace already exists", summary["error"])


if __name__ == "__main__":
    unittest.main()
