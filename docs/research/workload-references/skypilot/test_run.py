# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Proof-gate tests with a fake SDK and temporary evidence directory."""
import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch


def pod(name, role="head", job_id=None):
    cluster = "sky-ref-managed" if job_id is not None else "sky-ref-multinode"
    annotations = {"skypilot-cluster-name": cluster}
    if job_id is not None:
        annotations.update({"skypilot-managed-job-id": str(job_id),
                            "skypilot-managed-job-name": "sky-ref-managed"})
    return {"apiVersion": "v1", "kind": "Pod", "metadata": {
        "name": name, "namespace": "test-ref", "uid": name + "-uid",
        "ownerReferences": [], "annotations": annotations,
        "labels": {"skypilot-cluster-name": cluster + "-testhash",
                   "ray-cluster-name": cluster + "-testhash", "ray-node-type": role}}}


class ProofGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.sky = types.ModuleType("sky")
        self.sky.__version__ = "test"
        self.sky.__commit__ = "test"
        self.sky.Task = types.SimpleNamespace(from_yaml=Mock(
            return_value=types.SimpleNamespace(name="sky-ref-managed")))
        self.sky.launch = Mock(return_value="fake-request")
        self.sky.get = Mock()
        self.sky.jobs = types.SimpleNamespace(
            launch=Mock(return_value="fake-request"), queue=Mock(return_value="fake-queue"))
        spec = importlib.util.spec_from_file_location("sky_probe_under_test", Path(__file__).with_name("run.py"))
        self.probe = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"sky": self.sky}):
            spec.loader.exec_module(self.probe)
        self.probe.EVIDENCE = Path(self.temp.name)
        self.probe.result = Mock(return_value=(1, None))
        self.probe.wait_complete = Mock(return_value={"1": "SUCCEEDED"})
        self.probe.pods = Mock(return_value=[])
        self.output = io.StringIO()

    def evidence(self, name):
        return json.loads((self.probe.EVIDENCE / name).read_text())

    def multinode(self, captured):
        self.probe.pods.return_value = captured
        with contextlib.redirect_stdout(self.output):
            self.probe.run_multinode()

    def managed(self, captured, ids=(1,), rows=None):
        self.probe.result.return_value = (list(ids), None)
        self.probe.pods.return_value = captured
        self.sky.get.return_value = rows if rows is not None else [
            {"job_id": job_id, "status": "SUCCEEDED"} for job_id in ids]
        with patch.object(self.probe.time, "monotonic", side_effect=[0, 0, 901]), \
             patch.object(self.probe.time, "sleep"), contextlib.redirect_stdout(self.output):
            self.probe.run_managed()

    def test_multinode_empty_or_partial_capture_is_not_success(self):
        for captured in [[], [pod("head")]]:
            with self.subTest(count=len(captured)), self.assertRaisesRegex(RuntimeError, "mapping validation failed"):
                self.multinode(captured)
            result = self.evidence("multinode-result.json")
            self.assertTrue(result["runtime"]["succeeded"])
            self.assertFalse(result["mapping"]["passed"])
        self.assertNotIn("PROBE_SUCCEEDED", self.output.getvalue())

    def test_multinode_rejects_inconsistent_identity_and_relationships(self):
        valid = [pod("head"), pod("worker", "worker")]
        for field in ["uid", "namespace", "role", "cluster", "owner"]:
            captured = copy.deepcopy(valid)
            meta = captured[1]["metadata"]
            if field == "uid":
                meta["uid"] = captured[0]["metadata"]["uid"]
            elif field == "namespace":
                meta["namespace"] = "other-test-namespace"
            elif field == "role":
                meta["labels"]["ray-node-type"] = "head"
            elif field == "cluster":
                meta["labels"]["skypilot-cluster-name"] = "other-cluster"
            else:
                meta["ownerReferences"] = [{"kind": "Job", "name": "other-test-owner"}]
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, "mapping validation failed"):
                self.multinode(captured)
            self.assertFalse(self.evidence("multinode-result.json")["mapping"]["passed"])
        self.assertNotIn("PROBE_SUCCEEDED", self.output.getvalue())

    def test_multinode_valid_capture_and_runtime_succeed(self):
        self.multinode([pod("head"), pod("worker", "worker")])
        result = self.evidence("multinode-result.json")
        self.assertTrue(result["runtime"]["succeeded"])
        self.assertTrue(result["mapping"]["passed"])
        self.assertEqual(2, len(self.evidence("multinode-capture.json")["pods"]))
        self.assertIn("MULTINODE_PROBE_SUCCEEDED", self.output.getvalue())

    def test_multinode_runtime_failure_keeps_mapping_result_separate(self):
        self.probe.wait_complete.side_effect = RuntimeError("fake runtime failure")
        with self.assertRaisesRegex(RuntimeError, "runtime failure"):
            self.multinode([pod("head"), pod("worker", "worker")])
        result = self.evidence("multinode-result.json")
        self.assertFalse(result["runtime"]["succeeded"])
        self.assertTrue(result["mapping"]["passed"])
        self.assertNotIn("PROBE_SUCCEEDED", self.output.getvalue())

    def test_managed_success_without_metadata_is_not_probe_success(self):
        with self.assertRaisesRegex(RuntimeError, "mapping validation failed"):
            self.managed([])
        result = self.evidence("managed-result.json")
        self.assertTrue(result["runtime"]["succeeded"])
        self.assertFalse(result["mapping"]["passed"])
        self.assertFalse(result["metadataCaptured"])
        self.assertNotIn("PROBE_SUCCEEDED", self.output.getvalue())

    def test_managed_requires_metadata_for_every_job(self):
        with self.assertRaisesRegex(RuntimeError, "mapping validation failed"):
            self.managed([pod("job-one", job_id=1)], ids=(1, 2))
        result = self.evidence("managed-result.json")
        self.assertTrue(result["runtime"]["succeeded"])
        self.assertFalse(result["mapping"]["passed"])
        self.assertNotIn("PROBE_SUCCEEDED", self.output.getvalue())

    def test_managed_rejects_incomplete_or_mismatched_metadata(self):
        for field in ["uid", "namespace", "cluster", "task"]:
            captured = pod("managed", job_id=1)
            meta = captured["metadata"]
            if field == "uid":
                del meta["uid"]
            elif field == "namespace":
                meta["namespace"] = "other-test-namespace"
            elif field == "cluster":
                del meta["labels"]["ray-cluster-name"]
            else:
                meta["annotations"]["skypilot-managed-job-name"] = "other-task"
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, "mapping validation failed"):
                self.managed([captured])
            self.assertFalse(self.evidence("managed-result.json")["mapping"]["passed"])
        self.assertNotIn("PROBE_SUCCEEDED", self.output.getvalue())

    def test_managed_partial_runtime_results_are_not_success(self):
        with self.assertRaises(TimeoutError):
            self.managed([pod("job-one", job_id=1), pod("job-two", job_id=2)], ids=(1, 2),
                         rows=[{"job_id": 1, "status": "SUCCEEDED"}])
        result = self.evidence("managed-result.json")
        self.assertFalse(result["runtime"]["succeeded"])
        self.assertTrue(result["mapping"]["passed"])
        self.assertNotIn("PROBE_SUCCEEDED", self.output.getvalue())

    def test_managed_full_metadata_and_runtime_succeed(self):
        self.managed([pod("job-one", job_id=1), pod("job-two", job_id=2)], ids=(1, 2))
        result = self.evidence("managed-result.json")
        self.assertTrue(result["runtime"]["succeeded"])
        self.assertTrue(result["mapping"]["passed"])
        self.assertEqual(["1", "2"], result["mapping"]["observedJobIDs"])
        self.assertIn("MANAGED_PROBE_SUCCEEDED", self.output.getvalue())


if __name__ == "__main__":
    unittest.main()
