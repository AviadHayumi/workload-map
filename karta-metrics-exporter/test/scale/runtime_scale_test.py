# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

import ast
import copy
import inspect
import io
import json
import threading
import time
import unittest
from unittest import mock
from types import SimpleNamespace
import runtime_scale
import urllib.error
import urllib.parse
import urllib.request

from runtime_scale import CREATED, PHASES, LoopbackAPI, ChurnIntervals, FixedSchedule, CollectionGate, heap_window, churn_root_count, read_metrics, verified_publication


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
        self.assertEqual(page["items"][0]["metadata"]["ownerReferences"][0]["uid"], "rs-0-g0")
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

    def test_intermediate_chain_and_recreation_have_new_uids(self):
        pod = self.get("/api/v1/namespaces/scale/pods/pod-0-0")
        rs = self.get("/apis/apps/v1/namespaces/scale/replicasets/rs-0")
        self.assertEqual(pod["metadata"]["ownerReferences"][0]["uid"],rs["metadata"]["uid"])
        self.assertEqual(rs["metadata"]["ownerReferences"][0]["uid"],"job-0-g0")
        self.fixture.change(0,delete=True)
        self.fixture.recreate(0)
        pod = self.get("/api/v1/namespaces/scale/pods/pod-0-0")
        rs = self.get("/apis/apps/v1/namespaces/scale/replicasets/rs-0")
        self.assertEqual(pod["metadata"]["uid"],"pod-0-0-g1")
        self.assertEqual(pod["metadata"]["ownerReferences"][0]["uid"],"rs-0-g1")
        self.assertEqual(rs["metadata"]["ownerReferences"][0]["uid"],"job-0-g1")

    def test_healthy_watch_label_update_needs_no_list(self):
        request = urllib.request.Request(self.endpoint+"/api/v1/namespaces/scale/pods?watch=true&resourceVersion=1")
        before = self.fixture.snapshot()["requests"]
        with urllib.request.urlopen(request,timeout=2) as response:
            self.fixture.change_pod(0)
            event=json.loads(response.readline())
        self.assertEqual(event["object"]["metadata"]["labels"]["team"],"updated-team-2")
        after = self.fixture.snapshot()["requests"]
        self.assertEqual({k:v for k,v in before.items() if ".list." in k},{k:v for k,v in after.items() if ".list." in k})

    def test_explicit_watch_eof_and_expiration(self):
        url=self.endpoint+"/api/v1/namespaces/scale/pods?watch=true&resourceVersion=1"
        with urllib.request.urlopen(url,timeout=2) as response:
            self.fixture.interrupt_watch("pods")
            self.assertEqual(response.readline(),b"")
        with urllib.request.urlopen(url,timeout=2) as response:
            self.fixture.interrupt_watch("pods",expired=True)
            event=json.loads(response.readline())
            self.assertEqual(event["type"],"ERROR")
            self.assertEqual(event["object"]["code"],410)

    def test_watch_update_during_paginated_metadata_list_keeps_snapshot(self):
        self.fixture.change_during_list=True
        first=self.get("/api/v1/namespaces/scale/pods",metadata=True)
        token=first["metadata"]["continue"]
        last=None
        while token:
            last=self.get("/api/v1/namespaces/scale/pods?continue="+urllib.parse.quote(token),metadata=True)
            token=last["metadata"]["continue"]
        self.assertEqual(last["items"][-1]["metadata"]["labels"]["team"],"scale-team")
        current=self.get("/api/v1/namespaces/scale/pods/pod-2-1",metadata=True)
        self.assertEqual(current["metadata"]["labels"]["team"],"updated-team-2")

    def test_resource_versions_change_only_for_mutated_objects(self):
        before=self.get("/apis/apps/v1/namespaces/scale/replicasets/rs-0")
        self.fixture.change(0,change_pods=False)
        self.assertEqual(self.get("/apis/apps/v1/namespaces/scale/replicasets/rs-0"),before)
        self.fixture.change(0,delete=True)
        self.fixture.recreate(0)
        job=self.get("/apis/batch/v1/namespaces/scale/jobs/job-0")
        job_event=next(obj for _,resource,event,obj in self.fixture.events if resource=="jobs" and event=="ADDED")
        self.assertEqual(job,job_event)


class SchedulerTests(unittest.TestCase):
    def test_fractional_giant_root_rate_emits_exactly_fifteen_events(self):
        schedule=FixedSchedule(300,0.05,1)
        self.assertEqual(schedule.target,15)
        for index in range(15):
            offset=index*20
            if index:
                self.assertFalse(schedule.due(offset-0.001))
            self.assertEqual(schedule.record(offset),(offset,0))
        self.assertFalse(schedule.due(300))

    def test_decimal_rate_does_not_add_a_rounded_boundary_event(self):
        self.assertEqual(FixedSchedule(100,0.07,1).target,7)
        for rate in (0,-1,float("nan"),float("inf")):
            with self.assertRaises(ValueError):
                FixedSchedule(300,rate,1)

    def test_fractional_rate_preserves_pending_guard_and_lateness_bound(self):
        schedule=FixedSchedule(300,0.05,1)
        schedule.record(0)
        self.assertFalse(schedule.due(20,blocked=True))
        with self.assertRaisesRegex(RuntimeError,"lateness"):
            schedule.due(21.001,blocked=True)
        self.assertEqual(schedule.emitted,1)

    def test_last_due_tick_is_not_dropped_at_duration_boundary(self):
        schedule=FixedSchedule(0.03,100,1)
        self.assertEqual(schedule.target,3)
        self.assertEqual(schedule.record(0),(0,0))
        self.assertFalse(schedule.due(0.009))
        self.assertEqual(schedule.record(0.01),(0.01,0))
        self.assertTrue(schedule.due(0.030001))
        scheduled,lateness=schedule.record(0.030001)
        self.assertEqual(scheduled,0.02)
        self.assertAlmostEqual(lateness,0.010001)
        self.assertEqual(schedule.emitted,3)
        self.assertFalse(schedule.due(10))

    def test_pending_root_backpressure_never_advances_or_fabricates_ticks(self):
        schedule=FixedSchedule(1,100,1)
        self.assertFalse(schedule.due(0.5,blocked=True))
        self.assertEqual(schedule.emitted,0)
        self.assertEqual(schedule.record(0.5),(0,0.5))
        self.assertEqual(schedule.emitted,1)
        self.assertFalse(schedule.due(0.7,blocked=True))
        self.assertEqual(schedule.emitted,1)

    def test_long_stalls_fail_before_a_catchup_burst(self):
        schedule=FixedSchedule(300,100,1)
        self.assertEqual(schedule.target,30000)
        schedule.record(0)
        with self.assertRaisesRegex(RuntimeError,"lateness"):
            schedule.due(1.02)
        self.assertEqual(schedule.emitted,1)
        with self.assertRaisesRegex(RuntimeError,"lateness"):
            schedule.due(1.02,blocked=True)
        with self.assertRaises(AssertionError):
            FixedSchedule(1,100,1).record(-0.1)


class OracleTests(unittest.TestCase):
    def setUp(self):
        self.fixture = LoopbackAPI(1, 1)
        coordinates = 'namespace="scale",workload_name="job-0",workload_uid="job-0-g0"'
        root_labels = coordinates + ',workload_group="batch",workload_version="v1",workload_kind="Job",owner_resolution="resolved",observation_valid="true",team="scale-team"'
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
        self.assertFalse(self.check(self.exposition.replace('team="scale-team"', 'team=""'))["complete"])
        self.assertFalse(self.check(self.exposition.replace('owner_resolution="resolved"', 'owner_resolution="unresolved"'))["complete"])
        self.assertFalse(self.check(self.exposition.replace('observation_valid="true"', 'observation_valid="false"'))["complete"])
        self.assertFalse(self.check(self.exposition + 'karta_exporter_observation_valid{scope="workload",reason="evaluation_failed"} 0\n')["complete"])

    def test_invalid_examples_preserve_health_reasons_and_are_bounded(self):
        result=self.check(self.exposition.replace('observation_valid="true"', 'observation_valid="false"'))
        self.assertTrue(result["examples"])
        self.assertLessEqual(len(result["examples"]),5)
        result=self.check(self.exposition.replace('reason="none"','reason="unresolved_owner"'))
        self.assertIn("unresolved_owner",result["examples"][0])

    def test_only_inflight_roots_may_be_coherently_invalid(self):
        invalid="\n".join(line.replace('observation_valid="true"','observation_valid="false"') for line in self.exposition.splitlines() if not line.startswith("karta_workload_created_timestamp_seconds"))
        invalid+='\nkarta_exporter_observation_valid{scope="workload",namespace="scale",resource_group="batch",resource_version="v1",resource_kind="Job",workload_name="job-0",workload_uid="job-0-g0",reason="api_unavailable"} 0\n'
        read=lambda text, changing:read_metrics(io.BytesIO(text.encode()),self.fixture,changing_roots=changing)
        self.assertTrue(read(invalid,{0})["complete"])
        self.assertFalse(read(invalid,set())["complete"])
        self.assertFalse(read(invalid,{1})["complete"])
        self.assertFalse(read(invalid.replace("job-0-g0","job-0-g1"),{0})["complete"])
        self.assertFalse(read(invalid.replace('phase="Running"} 1','phase="Running"} 0'),{0})["complete"])
        mixed=invalid.replace('observation_valid="false"','observation_valid="true"',1)
        self.assertFalse(read(mixed,{0})["complete"])
        self.assertTrue(read(self.exposition,set())["complete"])

    def test_inflight_intervals_end_only_after_ack_and_detect_never_recovery(self):
        intervals=ChurnIntervals()
        intervals.begin(0,10)
        self.assertEqual(intervals.overlapping(9,11),{0})
        self.assertEqual(intervals.overdue(16,5),{0})
        intervals.acknowledge(0,17)
        self.assertEqual(intervals.overlapping(16,18),{0})
        self.assertEqual(intervals.overlapping(18,19),set())
        self.assertEqual(intervals.overdue(20,5),set())
        intervals.begin(1,20)
        self.assertEqual(intervals.overlapping(20,21),{1})

    def test_ack_rejects_stale_version_invalid_proof_and_wrong_pod(self):
        p={"resource_version":"new","valid":True,"workload":{"UID":"job-0-g0","HasStatus":True,"CreatedTimestamp":CREATED,"Phases":["Running"]},"pods":{"pod-0-0-g0":{"valid":True,"record":{"UID":"pod-0-0-g0","Name":"pod-0-0","Namespace":"scale","Component":"job","Instance":"","Group":"","OwnerResolution":"resolved","Labels":{"team":"scale-team"}}}}}
        gvk={"Group":"batch","Version":"v1","Kind":"Job"}
        p["workload"].update({"Ref":{"GVK":gvk,"Namespace":"scale","Name":"job-0"},"OwnerResolution":"resolved","Karta":"batch-job-v1","KartaSource":"catalog","StatusLabels":{"team":"scale-team"}})
        p["pods"]["pod-0-0-g0"]["record"]["WorkloadGVK"]=gvk
        self.assertTrue(verified_publication(p,self.fixture,0,"new"))
        self.assertFalse(verified_publication(p,self.fixture,0,"newer"))
        bad=copy.deepcopy(p);bad["valid"]=False
        self.assertFalse(verified_publication(bad,self.fixture,0,"new"))
        bad=copy.deepcopy(p);bad["pods"]["pod-0-0-g0"]["record"]["UID"]="old-pod"
        self.assertFalse(verified_publication(bad,self.fixture,0,"new"))
        bad=copy.deepcopy(p);bad["pods"]["pod-0-0-g0"]["valid"]=False
        self.assertFalse(verified_publication(bad,self.fixture,0,"new"))
        bad=copy.deepcopy(p);bad["pods"]["pod-0-0-g0"]["record"]["Labels"]["team"]="stale"
        self.assertFalse(verified_publication(bad,self.fixture,0,"new"))
        bad=copy.deepcopy(p);bad["workload"]["Phases"]=["Failed"]
        self.assertFalse(verified_publication(bad,self.fixture,0,"new"))

    def test_oracle_requires_visible_pod_update_and_rejects_old_generation(self):
        self.fixture.change_pod(0)
        self.assertFalse(self.check(self.exposition)["complete"])
        lines=self.exposition.splitlines()
        updated="\n".join(line.replace('team="scale-team"','team="updated-team-2"') if line.startswith("karta_pod_workload_info") else line for line in lines)
        self.assertTrue(self.check(updated)["complete"])
        self.fixture.change(0,delete=True)
        self.assertFalse(self.check(updated)["complete"])
        self.fixture.recreate(0)
        self.assertFalse(self.check(updated)["complete"])
        self.assertTrue(self.check(updated.replace("-g0","-g1").replace("updated-team-2", f"updated-team-{self.fixture.changed_pods[0]}"))["complete"])




class CollectionGateTests(unittest.TestCase):
    def test_http_and_gc_exclude_each_other_in_both_directions(self):
        for first, second in (("http", "gc"), ("gc", "http")):
            with self.subTest(first=first):
                gate=CollectionGate()
                entered=threading.Event()
                release=threading.Event()
                second_entered=threading.Event()
                def hold():
                    entered.set()
                    release.wait(2)
                a=threading.Thread(target=lambda: gate.run(hold))
                b=threading.Thread(target=lambda: gate.run(second_entered.set))
                a.start()
                self.assertTrue(entered.wait(1))
                b.start()
                try:
                    self.assertFalse(second_entered.wait(0.02))
                finally:
                    release.set()
                    a.join(1)
                    b.join(1)
                self.assertTrue(second_entered.is_set())

    def test_exception_releases_gate(self):
        gate=CollectionGate()
        def fail():
            raise OSError("HTTP read failed")
        with self.assertRaises(OSError):
            gate.run(fail)
        self.assertTrue(gate.lock.acquire(timeout=0.1))
        gate.lock.release()

    def test_ack_and_multiple_emitter_rotations_continue_while_gc_waits(self):
        gate=CollectionGate()
        gate.lock.acquire()
        waiting=threading.Event()
        samples=[]
        def gc():
            waiting.set()
            samples.append(gate.run(lambda: {"heap_alloc_bytes":100},time.monotonic()))
        thread=threading.Thread(target=gc)
        thread.start()
        self.assertTrue(waiting.wait(1))
        schedule=FixedSchedule(0.06,100,1)
        intervals=ChurnIntervals()
        try:
            for i in range(6):
                root=i%2
                schedule.record(i/100)
                intervals.begin(root,i/100)
                intervals.acknowledge(root,i/100+0.001)
            self.assertEqual(schedule.emitted,6)
            self.assertEqual(samples,[])
        finally:
            gate.lock.release()
            thread.join(1)
        timing=samples[0][1]
        self.assertLessEqual(timing["scheduled_at"],timing["wait_started_at"])
        self.assertLessEqual(timing["wait_started_at"],timing["request_started_at"])
        self.assertLessEqual(timing["request_started_at"],timing["completed_at"])

    def test_heap_window_uses_actual_completion_not_scheduled_offsets(self):
        samples=[{"scheduled_at":90,"completed_at":130,"heap_alloc_bytes":100},
                 {"scheduled_at":300,"completed_at":305,"heap_alloc_bytes":104}]
        coverage,growth,complete=heap_window(samples,300)
        self.assertFalse(complete)
        self.assertEqual(coverage,175)
        self.assertLess(coverage,180)
        self.assertAlmostEqual(growth,0.04)
        samples[0]["completed_at"]=120
        self.assertEqual(heap_window(samples,300)[0],185)
        self.assertTrue(heap_window(samples,300)[2])
        samples[0]["completed_at"]=130
        samples[-1]["completed_at"]=320
        self.assertFalse(heap_window(samples,300)[2])


class ConvergeWiringTests(unittest.TestCase):
    def test_response_read_and_close_exclude_gc_but_oracle_does_not(self):
        # Execute the actual nested function without starting an exporter process.
        tree=ast.parse(inspect.getsource(runtime_scale.run))
        node=next(n for n in tree.body[0].body if isinstance(n,ast.FunctionDef) and n.name=="converge")
        read_entered,close_entered,oracle_entered=(threading.Event() for _ in range(3))
        release_read,release_close,release_oracle=(threading.Event() for _ in range(3))
        gc_entered=threading.Event()
        gate=CollectionGate()
        errors=[]
        class Response:
            sent=False
            def __enter__(self): return self
            def read(self, size):
                if self.sent: return b""
                self.sent=True
                read_entered.set()
                if not release_read.wait(2): raise TimeoutError("read barrier")
                return b"fixture"
            def __exit__(self, *args):
                close_entered.set()
                if not release_close.wait(2): raise TimeoutError("close barrier")
        def oracle(body, *args):
            self.assertEqual(body.read(),b"fixture")
            oracle_entered.set()
            if not release_oracle.wait(2): raise TimeoutError("oracle barrier")
            return {"complete":True,"counts":{}}
        namespace=dict(vars(runtime_scale),args=SimpleNamespace(stage_seconds=5,scrape_seconds=2,oracle_seconds=2,poll_seconds=0.001),
                       fixture=SimpleNamespace(snapshot=lambda:{"requests":{}}),report={"stages":[]},
                       process=SimpleNamespace(poll=lambda:None,pid=0),limit=[],metrics_port=1,
                       diagnostics_port=None,started=time.monotonic(),collection_gate=gate,
                       churn_intervals=ChurnIntervals(),read_metrics=oracle,
                       process_usage=lambda pid:{"rss_bytes":0},print=lambda *a,**kw:None)
        exec(compile(ast.Module(body=[node],type_ignores=[]),"actual_converge","exec"),namespace)
        def converge():
            try: namespace["converge"]("churn_continuity")
            except Exception as exc: errors.append(exc)
        worker=threading.Thread(target=converge)
        gc=threading.Thread(target=lambda:gate.run(gc_entered.set))
        with mock.patch.object(runtime_scale.urllib.request,"urlopen",return_value=Response()):
            worker.start()
            try:
                self.assertTrue(read_entered.wait(1))
                gc.start()
                self.assertFalse(gc_entered.wait(0.02))
                release_read.set()
                self.assertTrue(close_entered.wait(1))
                self.assertFalse(gc_entered.wait(0.02))
                release_close.set()
                self.assertTrue(oracle_entered.wait(1))
                self.assertTrue(gc_entered.wait(1))
                self.assertTrue(worker.is_alive())
            finally:
                release_read.set(); release_close.set(); release_oracle.set()
                worker.join(2)
                if gc.ident is not None: gc.join(2)
        self.assertFalse(worker.is_alive())
        self.assertEqual(errors,[])
        self.assertTrue(namespace["report"]["stages"][0]["scrapes"][0]["complete"])


class HotRootTests(unittest.TestCase):
    def test_declared_shapes_preserve_fractional_giant_rate(self):
        self.assertEqual(churn_root_count(100000,100,5,1,0.25),750)
        self.assertEqual(churn_root_count(100000,100,10,1,0.25),1250)
        self.assertEqual(churn_root_count(1,0.05,15,1,0.25),1)
        self.assertEqual(churn_root_count(10,100,5,1,0.25),10)

    def test_three_second_acks_survive_multiple_rotations_with_five_second_budget(self):
        pool=churn_root_count(100000,100,5,1,0.25)
        schedule=FixedSchedule(30,100,1)
        pending={}
        intervals=ChurnIntervals()
        acknowledged=0
        for tick in range(3301):
            now=tick/100
            for root,sent in list(pending.items()):
                if now-sent >= 3:
                    intervals.acknowledge(root,now)
                    del pending[root]
                    acknowledged+=1
            self.assertFalse(intervals.overdue(now,5))
            if tick < schedule.target:
                root=tick%pool
                self.assertTrue(schedule.due(now,root in pending))
                schedule.record(now)
                intervals.begin(root,now)
                pending[root]=now
        self.assertEqual(schedule.emitted,3000)
        self.assertEqual(acknowledged,3000)
        self.assertFalse(pending)

    def test_overbudget_publication_and_real_emitter_delay_still_fail(self):
        intervals=ChurnIntervals()
        intervals.begin(0,0)
        self.assertEqual(intervals.overdue(5.001,5),{0})
        with self.assertRaisesRegex(RuntimeError,"emitter scheduling delay"):
            FixedSchedule(300,100,1).due(1.001)
        with self.assertRaisesRegex(RuntimeError,"pending publication blocks rotation"):
            FixedSchedule(300,100,1).due(1.001,blocked=True)


if __name__ == "__main__":
    unittest.main()
