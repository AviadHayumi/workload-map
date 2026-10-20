# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

import io
import json
import unittest
import urllib.error
import urllib.parse
import urllib.request

from runtime_scale import CREATED, PHASES, LoopbackAPI, read_metrics


class APIFixtureTests(unittest.TestCase):
    def setUp(self):
        self.fixture = LoopbackAPI(3, 2, page_size=2)
        self.endpoint = self.fixture.start()
        self.addCleanup(self.fixture.close)

    def get(self, path, metadata=False):
        request = urllib.request.Request(self.endpoint + path)
        if metadata:
            request.add_header("Accept", "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1")
        with urllib.request.urlopen(request, timeout=2) as response:
            return json.load(response)

    def test_discovery_and_paginated_informer_objects(self):
        self.assertEqual(self.get("/api")["versions"], ["v1"])
        self.assertEqual(self.get("/apis")["groups"][0]["name"], "batch")
        page = self.get("/apis/batch/v1/namespaces/scale/jobs?limit=100")
        self.assertEqual(len(page["items"]), 2)
        self.assertIn("spec", page["items"][0])
        token = page["metadata"]["continue"]
        final = self.get("/apis/batch/v1/namespaces/scale/jobs?continue=" + urllib.parse.quote(token))
        self.assertEqual([item["metadata"]["name"] for item in final["items"]], ["job-2"])
        self.assertEqual(final["metadata"]["continue"], "")
        self.assertEqual(page["metadata"]["resourceVersion"], final["metadata"]["resourceVersion"])

    def test_metadata_snapshot_survives_changes_and_explicit_expiry(self):
        page = self.get("/api/v1/namespaces/scale/pods", metadata=True)
        self.assertEqual(page["kind"], "PartialObjectMetadataList")
        self.assertNotIn("spec", page["items"][0])
        self.assertEqual(page["items"][0]["metadata"]["ownerReferences"][0]["uid"], "job-0-g0")
        self.fixture.change(1, delete=True)
        stable = self.get("/api/v1/namespaces/scale/pods?continue=" + urllib.parse.quote(page["metadata"]["continue"]), metadata=True)
        self.assertEqual(stable["metadata"]["resourceVersion"], page["metadata"]["resourceVersion"])
        self.assertEqual([item["metadata"]["name"] for item in stable["items"]], ["pod-1-0", "pod-1-1"])
        self.fixture.snapshots.clear()
        with self.assertRaises(urllib.error.HTTPError) as failure:
            self.get("/api/v1/namespaces/scale/pods?continue=" + urllib.parse.quote(page["metadata"]["continue"]))
        self.assertEqual(failure.exception.code, 410)
        failure.exception.close()

    def test_snapshot_bound_and_collection_token_binding(self):
        page = self.get("/api/v1/namespaces/scale/pods")
        self.get("/apis/batch/v1/namespaces/scale/jobs")
        with self.assertRaises(urllib.error.HTTPError) as failure:
            self.get("/apis/batch/v1/namespaces/scale/jobs?continue=" + urllib.parse.quote(page["metadata"]["continue"]))
        self.assertEqual(failure.exception.code, 400)
        failure.exception.close()
        for _ in range(20):
            self.fixture.change(0)
            self.get("/api/v1/namespaces/scale/pods")
        self.assertLessEqual(len(self.fixture.snapshots), 16)
        with self.assertRaises(urllib.error.HTTPError) as failure:
            self.get("/api/v1/namespaces/scale/pods?continue=" + urllib.parse.quote(page["metadata"]["continue"]))
        self.assertEqual(failure.exception.code, 410)
        failure.exception.close()

    def test_unsupported_watchlist_falls_back_with_bad_request(self):
        with self.assertRaises(urllib.error.HTTPError) as failure:
            self.get("/api/v1/namespaces/scale/pods?watch=true&sendInitialEvents=true&resourceVersion=0")
        self.assertEqual(failure.exception.code, 400)
        self.assertEqual(json.load(failure.exception)["reason"], "BadRequest")
        failure.exception.close()
        self.assertEqual(self.fixture.active_watches, 0)

    def test_real_watch_stream_and_object_get(self):
        request = urllib.request.Request(self.endpoint + "/apis/batch/v1/namespaces/scale/jobs?watch=true&resourceVersion=1")
        with urllib.request.urlopen(request, timeout=2) as response:
            self.fixture.change(1)
            event = json.loads(response.readline())
        self.assertEqual(event["type"], "MODIFIED")
        self.assertEqual(event["object"]["metadata"]["name"], "job-1")
        self.assertEqual(event["object"]["metadata"]["resourceVersion"], "2")
        page = self.get("/apis/batch/v1/namespaces/scale/jobs?limit=2", metadata=True)
        self.get("/apis/batch/v1/namespaces/scale/jobs?continue=" + urllib.parse.quote(page["metadata"]["continue"]), metadata=True)
        self.assertEqual(self.fixture.snapshot()["completed_metadata_lists"]["jobs"], self.fixture.revision)
        obj = self.get("/apis/batch/v1/namespaces/scale/jobs/job-1")
        self.assertEqual(obj, event["object"])
        self.fixture.change(1, delete=True)
        with self.assertRaises(urllib.error.HTTPError) as failure:
            self.get("/apis/batch/v1/namespaces/scale/jobs/job-1")
        self.assertEqual(failure.exception.code, 404)
        failure.exception.close()
        page = self.get("/api/v1/namespaces/scale/pods")
        self.assertTrue(all(item["metadata"]["ownerReferences"][0]["uid"] != "job-1-g0" for item in page["items"]))


class OracleTests(unittest.TestCase):
    def setUp(self):
        self.fixture = LoopbackAPI(1, 1)
        coordinates = 'namespace="scale",workload_name="job-0",workload_uid="job-0-g0"'
        root_labels = coordinates + ',workload_group="batch",workload_version="v1",workload_kind="Job",owner_resolution="resolved",observation_valid="true",submitted_by="scale-user@example.test"'
        lines = [f'karta_workload_created_timestamp_seconds{{{coordinates}}} {CREATED}']
        lines.extend(f'karta_workload_status{{{root_labels},karta="batch-job-v1",karta_source="catalog",phase="{phase}"}} {int(phase == "Running")}' for phase in PHASES)
        lines.append(f'karta_pod_workload_info{{{root_labels},pod_name="pod-0-0",pod_uid="pod-0-0-g0",component="job",component_instance="",replica_group_id=""}} 1')
        for group, kind in (("", "Pod"), ("batch", "Job")):
            lines.append(f'karta_exporter_observation_valid{{scope="resource_type",namespace="scale",resource_group="{group}",resource_version="v1",resource_kind="{kind}",workload_name="",workload_uid="",reason="none"}} 1')
        self.exposition = "\n".join(lines) + "\n"

    def check(self, text):
        return read_metrics(io.BytesIO(text.encode()), self.fixture)

    def test_complete_exposition_and_all_phase_values(self):
        self.assertTrue(self.check(self.exposition)["complete"])
        wrong = self.exposition.replace('phase="Failed"} 0', 'phase="Failed"} 1')
        self.assertFalse(self.check(wrong)["complete"])

    def test_missing_duplicate_obsolete_and_invalid_rows_fail(self):
        lines = self.exposition.splitlines(keepends=True)
        self.assertFalse(self.check("".join(lines[1:]))["complete"])
        self.assertFalse(self.check(self.exposition + lines[1])["complete"])
        self.assertFalse(self.check(self.exposition.replace("job-0-g0", "job-0-g1"))["complete"])
        self.assertFalse(self.check(self.exposition.replace('component="job"', 'component="unknown"'))["complete"])
        self.assertFalse(self.check(self.exposition.replace('submitted_by="scale-user@example.test"', 'submitted_by=""'))["complete"])
        self.assertFalse(self.check(self.exposition.replace('owner_resolution="resolved"', 'owner_resolution="unresolved"'))["complete"])
        self.assertFalse(self.check(self.exposition.replace('observation_valid="true"', 'observation_valid="false"'))["complete"])
        self.assertFalse(self.check(self.exposition + 'karta_exporter_observation_valid{scope="workload",reason="evaluation_failed"} 0\n')["complete"])


if __name__ == "__main__":
    unittest.main()
