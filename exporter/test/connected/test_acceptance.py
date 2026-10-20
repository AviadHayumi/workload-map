# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

import copy
import json
import unittest

import acceptance as a


def exposition(rows):
    kinds = sorted({name for name, _, _ in rows})
    lines = [f"# TYPE {name} gauge" for name in kinds]
    for name, labels, value in rows:
        fields = ",".join(f"{key}={json.dumps(value)}" for key, value in sorted(labels.items()))
        lines.append(f"{name}{{{fields}}} {value}")
    return "\n".join(lines)


def live_fixture():
    root = {"apiVersion": "example.com/v1", "kind": "Parent",
            "metadata": {"namespace": "test", "name": "root", "uid": "root-uid",
                         "creationTimestamp": "2026-01-01T00:00:00Z"}}
    pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {
        "namespace": "test", "name": "pod", "uid": "pod-uid", "ownerReferences": [
            {"apiVersion": "example.com/v1", "kind": "Parent", "name": "root",
             "uid": "root-uid", "controller": True}]}}
    attrs = {key: "" for key in a.SCHEMAS[a.POD]}
    attrs.update(namespace="test", workload_name="root", workload_group="example.com",
                 workload_version="v1", workload_kind="Parent", workload_uid="root-uid",
                 owner_resolution="resolved", pod_name="pod", pod_uid="pod-uid",
                 observation_valid="true", **a.generic_mapping(pod))
    health = {key: "" for key in a.SCHEMAS[a.HEALTH]}
    health.update(scope="resource_type", namespace="test", resource_group="example.com",
                  resource_version="v1", resource_kind="Parent", reason="none")
    rows = [(a.POD, attrs, 1), (a.CREATED, {"namespace": "test", "workload_name": "root",
            "workload_uid": "root-uid"}, 1767225600), (a.HEALTH, health, 1)]
    return root, pod, rows


class ExpositionTests(unittest.TestCase):
    def test_escaped_values_and_other_exporters(self):
        _, _, rows = live_fixture()
        rows[0][1]["submitted_by"] = 'comma, brace} quote" slash\\ newline\n'
        parsed = a.parse_metrics(exposition(rows) + "\ngo_threads 12\n")
        self.assertEqual(rows, parsed)

    def test_duplicate_sample_rejected(self):
        _, _, rows = live_fixture()
        with self.assertRaisesRegex(AssertionError, "Duplicate sample"):
            a.parse_metrics(exposition(rows + [rows[0]]))

    def test_old_kep_names_rejected(self):
        _, _, rows = live_fixture()
        rows[0][1]["pod"] = rows[0][1].pop("pod_name")
        with self.assertRaisesRegex(AssertionError, "fixed-KEP"):
            a.parse_metrics(exposition(rows))

    def test_duplicate_label_rejected(self):
        with self.assertRaisesRegex(AssertionError, "duplicate label"):
            a.parse_metrics('karta_pod_workload_info{pod_uid="a",pod_uid="b"} 1')

    def test_malformed_separator_rejected(self):
        for raw in ('pod_uid="a",', 'pod_uid="a"pod_name="b"'):
            with self.subTest(raw=raw), self.assertRaises(AssertionError):
                a.parse_metrics(f'karta_pod_workload_info{{{raw}}} 1')

    def test_nonfinite_and_wrong_type_rejected(self):
        _, _, rows = live_fixture()
        for text in (exposition(rows).replace("} 1", "} NaN", 1),
                     exposition(rows).replace(" gauge", " counter", 1)):
            with self.subTest(text=text), self.assertRaises(AssertionError):
                a.parse_metrics(text)

    def test_healthy_workload_row_rejected(self):
        _, _, rows = live_fixture()
        rows[-1][1].update(scope="workload", workload_uid="root-uid", workload_name="root")
        with self.assertRaisesRegex(AssertionError, "Healthy workload"):
            a.parse_metrics(exposition(rows))


class LiveOracleTests(unittest.TestCase):
    def test_generic_root_uses_uid_creation_and_coverage(self):
        root, pod, rows = live_fixture()
        a.check_root(rows, root, a.owner_paths([root, pod]), a.generic_mapping, None, "", "")

    def test_same_name_wrong_uid_does_not_pass(self):
        root, pod, rows = live_fixture()
        rows[0][1]["pod_uid"] = "retired-pod-uid"
        with self.assertRaisesRegex(AssertionError, "UID membership"):
            a.check_root(rows, root, a.owner_paths([root, pod]), a.generic_mapping, None, "", "")

    def test_wrong_timestamp_and_absent_coverage_do_not_pass(self):
        root, pod, rows = live_fixture()
        for broken in (rows[:-1], [rows[0], (a.CREATED, rows[1][1], 1), rows[2]]):
            with self.subTest(rows=broken), self.assertRaises(AssertionError):
                a.check_root(broken, root, a.owner_paths([root, pod]), a.generic_mapping, None, "", "")

    def test_hidden_pod_must_not_have_a_row(self):
        root, pod, rows = live_fixture()
        with self.assertRaisesRegex(AssertionError, "UID membership"):
            a.check_root(rows, root, a.owner_paths([root, pod]), a.generic_mapping, None, "", "", hidden={"pod-uid"})
        a.check_root(rows[1:], root, a.owner_paths([root, pod]), a.generic_mapping, None, "", "", hidden={"pod-uid"})

    def test_actual_pod_can_be_an_intermediate_owner(self):
        root, leader, _ = live_fixture()
        worker = copy.deepcopy(leader)
        worker["metadata"].update(name="worker", uid="worker-uid", ownerReferences=[
            {"apiVersion": "v1", "kind": "Pod", "name": "pod", "uid": "pod-uid", "controller": True}])
        paths = a.owner_paths([root, leader, worker])
        self.assertEqual([obj["kind"] for obj in paths["worker-uid"]], ["Pod", "Pod", "Parent"])

    def test_missing_or_ambiguous_owner_fails(self):
        root, pod, _ = live_fixture()
        with self.assertRaisesRegex(AssertionError, "Incomplete"):
            a.owner_paths([pod])
        pod["metadata"]["ownerReferences"].append(copy.deepcopy(pod["metadata"]["ownerReferences"][0]))
        with self.assertRaisesRegex(AssertionError, "ambiguous"):
            a.owner_paths([root, pod])

    def test_cycle_and_wrong_owner_identity_fail(self):
        root, pod, _ = live_fixture()
        root["metadata"]["ownerReferences"] = [{"kind": "Pod", "name": "pod", "uid": "pod-uid", "controller": True}]
        with self.assertRaisesRegex(AssertionError, "Cycle"):
            a.owner_paths([root, pod])
        root["metadata"].pop("ownerReferences")
        root["metadata"]["name"] = "other"
        with self.assertRaisesRegex(AssertionError, "mismatch"):
            a.owner_paths([root, pod])


if __name__ == "__main__":
    unittest.main()
