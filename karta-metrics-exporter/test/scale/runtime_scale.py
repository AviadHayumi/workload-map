#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Exercise an exporter binary through a synthetic Kubernetes HTTP API.

This models API responses, not an API server/etcd or a production cluster.
The exporter itself owns all informer, discovery, evaluation and publication work.
"""

import argparse
import base64
import collections
import contextlib
from fractions import Fraction
import hashlib
import http.server
import json
import math
import os
import pathlib
import re
import signal
import shutil
import tempfile
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

PHASES = ("Initializing", "Running", "Completed", "Failed", "Degraded", "Suspended", "Suspending", "Resuming", "Undefined")
CREATED = 1792454400
CREATED_TEXT = "2026-10-20T00:00:00Z"
TEAM = "scale-team"
BASE_LABELS = {"namespace", "workload_name", "workload_group", "workload_version", "workload_kind", "workload_uid", "owner_resolution", "observation_valid", "team"}
LABELS = {
    "karta_pod_workload_info": BASE_LABELS | {"pod_name", "pod_uid", "component", "component_instance", "replica_group_id"},
    "karta_workload_status": BASE_LABELS | {"phase", "karta", "karta_source"},
    "karta_workload_created_timestamp_seconds": {"namespace", "workload_name", "workload_uid"},
    "karta_exporter_observation_valid": {"scope", "namespace", "resource_group", "resource_version", "resource_kind", "workload_name", "workload_uid", "reason"},
}


class LoopbackAPI:
    def __init__(self, roots, pods, page_size=500, intermediate=True):
        self.roots, self.pods, self.page_size = roots, pods, page_size
        self.intermediate = intermediate
        self.deleted = bytearray(roots)
        self.failed = bytearray(roots)
        self.changed_pods = {}
        self.root_versions = {}
        self.intermediate_versions = {}
        self.generations = {}
        self.revision = 1
        self.condition = threading.Condition()
        self.events = collections.deque(maxlen=20000)
        self.snapshots = collections.OrderedDict()
        self.counts = collections.Counter()
        self.max_page = 0
        self.active_watches = 0
        self.completed_metadata_lists = {}
        self.delivered_watch_versions = {}
        self.server = None
        self.watch_faults = {}
        self.change_during_list = False

    def resource(self, path):
        parts = path.strip("/").split("/")
        prefix = ("api", "v1") if parts[0] == "api" else tuple(parts[:3])
        rest = parts[len(prefix):]
        if rest == ["namespaces"]:
            return "namespaces", ""
        if len(rest) == 2 and rest[0] == "namespaces":
            return "namespaces", rest[1]
        if len(rest) >= 3 and rest[:2] == ["namespaces", "scale"]:
            return rest[2], rest[3] if len(rest) > 3 else ""
        if rest and rest[0] in ("pods", "jobs", "replicasets"):
            return rest[0], rest[1] if len(rest) > 1 else ""
        return "", ""

    def object(self, resource, index, metadata=False, state=None):
        deleted, failed, root_versions, changed_pods, generations, intermediate_versions = state if state is not None else (self.deleted, self.failed, self.root_versions, self.changed_pods, self.generations, self.intermediate_versions)
        if resource == "namespaces":
            obj = {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "scale", "uid": "scale-namespace", "resourceVersion": "1", "creationTimestamp": CREATED_TEXT, "labels": {"kubernetes.io/metadata.name": "scale"}}}
        else:
            root = index if resource in ("jobs", "replicasets") else index // self.pods
            generation = generations.get(root, 0)
            if deleted[root]:
                return None
            if resource == "jobs":
                obj = {"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": f"job-{root}", "namespace": "scale", "uid": f"job-{root}-g{generation}", "resourceVersion": str(root_versions.get(root, 1)), "creationTimestamp": CREATED_TEXT, "labels": {"app": "worker"}}, "spec": {"parallelism": max(1, self.pods), "completions": max(1, self.pods), "template": {"metadata": {"labels": {"app": "worker"}}, "spec": {"restartPolicy": "Never", "containers": [{"name": "worker", "image": "registry.k8s.io/pause:3.10", "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}}}]}}}, "status": {"active": 0 if failed[root] else max(1, self.pods), "ready": 0 if failed[root] else max(1, self.pods), "conditions": [{"type": "Failed", "status": "True"}] if failed[root] else []}}
            elif resource == "replicasets":
                obj = {"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": {"name": f"rs-{root}", "namespace": "scale", "uid": f"rs-{root}-g{generation}", "resourceVersion": str(intermediate_versions.get(root,1)), "ownerReferences": [{"apiVersion":"batch/v1", "kind":"Job", "name":f"job-{root}", "uid":f"job-{root}-g{generation}", "controller": True}]}}
            else:
                pod = index % self.pods
                obj = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": f"pod-{root}-{pod}", "namespace": "scale", "uid": f"pod-{root}-{pod}-g{generation}", "resourceVersion": str(changed_pods.get(index, 1)), "creationTimestamp": CREATED_TEXT, "labels": {"app": "worker", "batch.kubernetes.io/job-name": f"job-{root}", "batch.kubernetes.io/controller-uid": f"job-{root}-g{generation}"}, "ownerReferences": [{"apiVersion": "batch/v1", "kind": "Job", "name": f"job-{root}", "uid": f"job-{root}-g{generation}", "controller": True, "blockOwnerDeletion": True}]}, "spec": {"nodeName": "synthetic-node", "restartPolicy": "Never", "containers": [{"name": "worker", "image": "registry.k8s.io/pause:3.10", "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}}}]}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}], "containerStatuses": [{"name": "worker", "ready": True, "restartCount": 0, "image": "registry.k8s.io/pause:3.10", "imageID": "synthetic", "state": {"running": {"startedAt": CREATED_TEXT}}}]}}
        if resource != "namespaces":
            obj["metadata"].setdefault("labels", {})["team"] = f"updated-team-{changed_pods[index]}" if resource == "pods" and index in changed_pods else TEAM
        if resource == "pods" and self.intermediate:
            obj["metadata"]["ownerReferences"] = [{"apiVersion":"apps/v1", "kind":"ReplicaSet", "name":f"rs-{root}", "uid":f"rs-{root}-g{generation}", "controller":True}]
        if metadata:
            return {"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadata", "metadata": obj["metadata"]}
        return obj

    def total(self, resource):
        return {"namespaces": 1, "jobs": self.roots, "replicasets": self.roots, "pods": self.roots * self.pods}[resource]

    def snapshot(self):
        with self.condition:
            return {"requests": dict(self.counts), "max_page_objects": self.max_page, "active_watches": self.active_watches, "revision": self.revision, "completed_metadata_lists": dict(self.completed_metadata_lists), "delivered_watch_versions": dict(self.delivered_watch_versions)}

    def change(self, root, delete=False, change_pods=True):
        with self.condition:
            if self.deleted[root]:
                return
            if delete:
                for resource, index in [("pods", root * self.pods + pod) for pod in range(self.pods)] + [("replicasets", root), ("jobs", root)]:
                    obj = self.object(resource, index)
                    self.revision += 1
                    obj["metadata"]["resourceVersion"] = str(self.revision)
                    self.events.append((self.revision, resource, "DELETED", obj))
                self.deleted[root] = 1
            else:
                self.failed[root] = 1
                self.revision += 1
                self.root_versions[root] = self.revision
                self.events.append((self.revision, "jobs", "MODIFIED", self.object("jobs", root)))
                if self.pods and change_pods:
                    self.revision += 1
                    self.changed_pods[root * self.pods] = self.revision
                    self.events.append((self.revision, "pods", "MODIFIED", self.object("pods", root * self.pods)))
            self.condition.notify_all()

    def change_pod(self, root, pod=0):
        with self.condition:
            if self.deleted[root] or not self.pods:
                return
            index = root * self.pods + pod
            self.revision += 1
            self.changed_pods[index] = self.revision
            self.events.append((self.revision, "pods", "MODIFIED", self.object("pods", index)))
            self.condition.notify_all()

    def interrupt_watch(self, resource, expired=False):
        with self.condition:
            generation,_ = self.watch_faults.get(resource,(0,False))
            self.watch_faults[resource]=(generation+1,expired)
            self.condition.notify_all()

    def recreate(self, root):
        with self.condition:
            if not self.deleted[root]:
                raise ValueError("recreate requires a deleted root")
            self.generations[root] = self.generations.get(root, 0) + 1
            self.deleted[root] = self.failed[root] = 0
            for resource, index in [("jobs", root), ("replicasets", root)] + [("pods",root*self.pods+pod) for pod in range(self.pods)]:
                self.revision += 1
                if resource == "pods":
                    self.changed_pods[index] = self.revision
                elif resource == "jobs":
                    self.root_versions[root] = self.revision
                else:
                    self.intermediate_versions[root] = self.revision
                obj = self.object(resource,index)
                obj["metadata"]["resourceVersion"] = str(self.revision)
                self.events.append((self.revision,resource,"ADDED",obj))
            self.condition.notify_all()

    def start(self):
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def handle(self):
                # Informers can close a keep-alive socket after negotiation.
                with contextlib.suppress(ConnectionResetError):
                    super().handle()

            def send_json(self, obj, code=200):
                data = json.dumps(obj, separators=(",", ":")).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                with contextlib.suppress(BrokenPipeError, ConnectionResetError):
                    self.wfile.write(data)
                with fixture.condition:
                    fixture.counts["response_bytes"] += len(data)

            def status(self, code, reason):
                self.send_json({"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": reason, "code": code, "message": reason}, code)

            def do_GET(self):
                url = urllib.parse.urlparse(self.path)
                query = urllib.parse.parse_qs(url.query)
                value = lambda key, default="": query.get(key, [default])[0]
                path = url.path
                with fixture.condition:
                    fixture.counts["all_requests"] += 1
                if path == "/version":
                    self.send_json({"major": "1", "minor": "36", "gitVersion": "v1.36.0-synthetic"})
                    return
                if path == "/api":
                    self.send_json({"apiVersion": "v1", "kind": "APIVersions", "versions": ["v1"], "serverAddressByClientCIDRs": []})
                    return
                group = {"name": "batch", "versions": [{"groupVersion": "batch/v1", "version": "v1"}], "preferredVersion": {"groupVersion": "batch/v1", "version": "v1"}}
                if path == "/apis":
                    self.send_json({"apiVersion": "v1", "kind": "APIGroupList", "groups": [group, {"name":"apps", "versions":[{"groupVersion":"apps/v1", "version":"v1"}], "preferredVersion":{"groupVersion":"apps/v1", "version":"v1"}}]})
                    return
                if path == "/apis/apps":
                    self.send_json({"apiVersion":"v1", "kind":"APIGroup", "name":"apps", "versions":[{"groupVersion":"apps/v1", "version":"v1"}], "preferredVersion":{"groupVersion":"apps/v1", "version":"v1"}})
                    return
                if path == "/apis/batch":
                    self.send_json(dict(group, apiVersion="v1", kind="APIGroup"))
                    return
                if path in ("/api/v1", "/apis/batch/v1", "/apis/apps/v1"):
                    resources = [("namespaces", "Namespace", False), ("pods", "Pod", True)] if path == "/api/v1" else [("replicasets", "ReplicaSet", True)] if path == "/apis/apps/v1" else [("jobs", "Job", True)]
                    self.send_json({"apiVersion": "v1", "kind": "APIResourceList", "groupVersion": "v1" if path == "/api/v1" else "apps/v1" if path == "/apis/apps/v1" else "batch/v1", "resources": [{"name": name, "singularName": kind.lower(), "namespaced": namespaced, "kind": kind, "verbs": ["get", "list", "watch"]} for name, kind, namespaced in resources]})
                    return
                resource, name = fixture.resource(path)
                if resource not in ("namespaces", "jobs", "pods", "replicasets"):
                    with fixture.condition:
                        fixture.counts["unexpected_requests"] += 1
                    self.status(404, "NotFound")
                    return
                metadata = "as=PartialObjectMetadata" in self.headers.get("Accept", "")
                encoding = "metadata" if metadata else "full"
                if value("watch") == "true":
                    self.watch(resource, int(value("resourceVersion", "0")), metadata)
                    return
                with fixture.condition:
                    fixture.counts[f"{resource}.{'get' if name else 'list'}.{encoding}"] += 1
                if name:
                    with fixture.condition:
                        try:
                            if resource == "namespaces":
                                index = 0 if name == "scale" else -1
                            elif resource == "replicasets":
                                index = int(name.removeprefix("rs-"))
                            elif resource == "jobs":
                                index = int(name.removeprefix("job-"))
                            else:
                                root, pod = map(int, name.removeprefix("pod-").split("-"))
                                index = root * fixture.pods + pod
                            obj = fixture.object(resource, index, metadata) if 0 <= index < fixture.total(resource) else None
                        except (ValueError, IndexError):
                            obj = None
                    if obj is None:
                        self.status(404, "NotFound")
                    else:
                        self.send_json(obj)
                    return
                prepared, error = self.list_snapshot(resource, value("continue"))
                if error:
                    self.status(*error)
                    return
                offset, revision, state = prepared
                requested = int(value("limit", str(fixture.page_size)))
                limit = min(requested or fixture.page_size, fixture.page_size)
                items = []
                while offset < fixture.total(resource) and len(items) < limit:
                    obj = fixture.object(resource, offset, metadata, state)
                    offset += 1
                    if obj is not None:
                        items.append(obj)
                with fixture.condition:
                    fixture.max_page = max(fixture.max_page, len(items))
                    fixture.counts[f"{resource}.objects.{encoding}"] += len(items)
                    if resource == "pods" and metadata and not value("continue") and fixture.change_during_list:
                        fixture.change_during_list = False
                        fixture.change_pod(fixture.roots-1,fixture.pods-1)
                continuation = base64.urlsafe_b64encode(json.dumps([resource, revision, offset]).encode()).decode() if offset < fixture.total(resource) else ""
                kind = "PartialObjectMetadataList" if metadata else {"namespaces": "NamespaceList", "jobs": "JobList", "replicasets": "ReplicaSetList", "pods": "PodList"}[resource]
                api = "meta.k8s.io/v1" if metadata else "batch/v1" if resource == "jobs" else "apps/v1" if resource == "replicasets" else "v1"
                self.send_json({"apiVersion": api, "kind": kind, "metadata": {"resourceVersion": str(revision), "continue": continuation}, "items": items})
                if metadata and not continuation:
                    with fixture.condition:
                        fixture.completed_metadata_lists[resource] = revision

            def list_snapshot(self, resource, token):
                with fixture.condition:
                    offset, revision = 0, fixture.revision
                    if token:
                        try:
                            token_resource, revision, offset = json.loads(base64.urlsafe_b64decode(token))
                            if token_resource != resource or not isinstance(offset, int) or not 0 <= offset <= fixture.total(resource):
                                raise ValueError("continuation belongs to another collection or has an invalid offset")
                        except (ValueError, TypeError):
                            return None, (400, "BadRequest")
                        if (resource, revision) not in fixture.snapshots:
                            fixture.counts["continuation_expirations"] += 1
                            return None, (410, "ResourceExpired")
                    snapshot_key = (resource, revision)
                    if snapshot_key not in fixture.snapshots:
                        fixture.snapshots[snapshot_key] = (bytes(fixture.deleted), bytes(fixture.failed), dict(fixture.root_versions), dict(fixture.changed_pods), dict(fixture.generations), dict(fixture.intermediate_versions))
                        while len(fixture.snapshots) > 16:
                            fixture.snapshots.popitem(last=False)
                    fixture.snapshots.move_to_end(snapshot_key)
                    return (offset, revision, fixture.snapshots[snapshot_key]), None

            def watch(self, resource, version, metadata):
                if urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("sendInitialEvents") == ["true"]:
                    self.status(400, "BadRequest")
                    return
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.flush()
                with fixture.condition:
                    fault_generation = fixture.watch_faults.get(resource,(0,False))[0]
                    fixture.active_watches += 1
                    fixture.counts[f"{resource}.watch"] += 1
                started = time.monotonic()
                try:
                    while time.monotonic() - started < 240:
                        with fixture.condition:
                            current_fault,expired = fixture.watch_faults.get(resource,(0,False))
                            if current_fault != fault_generation and not expired:
                                return
                            if current_fault != fault_generation or (fixture.events and version < fixture.events[0][0] - 1):
                                fixture.counts["watch_expirations"] += 1
                                payload = {"type": "ERROR", "object": {"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "Expired", "code": 410}}
                                events = None
                            else:
                                events = []
                                for event in reversed(fixture.events):
                                    if event[0] <= version:
                                        break
                                    events.append(event)
                                events.reverse()
                                if not events:
                                    fixture.condition.wait(timeout=1)
                                    continue
                                version = events[-1][0]
                        if events is None:
                            self.wfile.write(json.dumps(payload).encode() + b"\n")
                            self.wfile.flush()
                            return
                        for event_version, event_resource, kind, obj in events:
                            if event_resource != resource:
                                continue
                            if metadata:
                                obj = {"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadata", "metadata": obj["metadata"]}
                            data = json.dumps({"type": kind, "object": obj}, separators=(",", ":")).encode() + b"\n"
                            self.wfile.write(data)
                            self.wfile.flush()
                            with fixture.condition:
                                fixture.delivered_watch_versions[resource] = event_version
                                fixture.counts[f"{resource}.watch_events"] += 1
                                fixture.counts["response_bytes"] += len(data)
                except (BrokenPipeError, ConnectionResetError):
                    pass
                finally:
                    with fixture.condition:
                        fixture.active_watches -= 1
                    self.close_connection = True

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        return f"http://127.0.0.1:{self.server.server_port}"

    def close(self):
        if self.server:
            self.server.shutdown()
            self.server.server_close()


class FixedSchedule:
    def __init__(self, seconds, rate, lateness_limit):
        if not math.isfinite(rate) or rate <= 0 or not math.isfinite(seconds) or seconds < 0:
            raise ValueError("duration must be finite and nonnegative; rate must be finite and positive")
        self.target = math.ceil(Fraction(str(seconds))*Fraction(str(rate)))
        self.rate = rate
        self.lateness_limit = lateness_limit
        self.emitted = 0

    def due(self, elapsed, blocked=False):
        if self.emitted >= self.target or elapsed < self.emitted/self.rate:
            return False
        if elapsed-self.emitted/self.rate > self.lateness_limit:
            raise RuntimeError("emitter exceeded the scheduling lateness limit: " + ("pending publication blocks rotation" if blocked else "emitter scheduling delay"))
        return not blocked

    def record(self, elapsed):
        if not self.due(elapsed):
            raise AssertionError("event emitted before its scheduled offset")
        scheduled = self.emitted/self.rate
        self.emitted += 1
        return scheduled, elapsed-scheduled


def churn_root_count(roots, rate, publication_limit, lateness_limit, poll_seconds):
    return min(roots,max(100,math.ceil(rate*(publication_limit+2*lateness_limit+2*poll_seconds))))


class CollectionGate:
    """Exclude client metrics HTTP requests from forced GC, not ACKs or emission."""
    def __init__(self):
        self.lock = threading.Lock()

    def run(self, action, scheduled=None):
        waiting = time.monotonic()
        with self.lock:
            started = time.monotonic()
            result = action()
            completed = time.monotonic()
        return result, {"scheduled_at": scheduled, "wait_started_at": waiting,
                        "request_started_at": started, "completed_at": completed,
                        "gate_wait_seconds": started-waiting}


def heap_window(samples, duration):
    coverage = samples[-1]["completed_at"]-samples[0]["completed_at"] if len(samples)>1 else 0
    growth = samples[-1]["heap_alloc_bytes"]/samples[0]["heap_alloc_bytes"]-1 if len(samples)>1 else None
    complete = coverage >= 180 and samples[0]["completed_at"] <= duration-180 and samples[-1]["completed_at"] >= duration
    return coverage, growth, complete


class ChurnIntervals:
    """Only roots with emitted, not yet verified changes may be temporarily invalid."""
    def __init__(self):
        self.lock = threading.Lock()
        self.pending = {}
        self.completed = collections.deque()

    def begin(self, root, at):
        with self.lock:
            if root in self.pending:
                raise AssertionError("change before previous publication acknowledgment")
            self.pending[root] = at

    def acknowledge(self, root, at):
        with self.lock:
            sent = self.pending.pop(root)
            self.completed.append((root,sent,at))

    def overlapping(self, start, end):
        # One continuity reader calls this in increasing scrape-start order.
        with self.lock:
            while self.completed and self.completed[0][2] < start:
                self.completed.popleft()
            return {root for root,sent,ack in self.completed if sent <= end and ack >= start} | {root for root,sent in self.pending.items() if sent <= end}

    def overdue(self, now, limit):
        with self.lock:
            return {root for root,sent in self.pending.items() if now-sent > limit}


def verified_publication(publication, fixture, root, version):
    if publication.get("resource_version") != version or publication.get("valid") is not True:
        return False
    workload = publication.get("workload", {})
    if workload.get("UID") != f"job-{root}-g{fixture.generations.get(root,0)}" or not workload.get("HasStatus") or workload.get("CreatedTimestamp") != CREATED:
        return False
    expected_gvk = {"Group":"batch","Version":"v1","Kind":"Job"}
    ref = workload.get("Ref",{})
    if ref.get("GVK") != expected_gvk or ref.get("Namespace") != "scale" or ref.get("Name") != f"job-{root}":
        return False
    if workload.get("OwnerResolution") != "resolved" or workload.get("Karta") != "batch-job-v1" or workload.get("KartaSource") != "catalog" or workload.get("StatusLabels",{}).get("team") != TEAM:
        return False
    if workload.get("Phases") != ["Failed" if fixture.failed[root] else "Running"]:
        return False
    pods = publication.get("pods", {})
    if len(pods) != fixture.pods:
        return False
    for pod in range(fixture.pods):
        index = root*fixture.pods+pod
        uid = f"pod-{root}-{pod}-g{fixture.generations.get(root,0)}"
        observed = pods.get(uid,{})
        record = observed.get("record",{})
        expected = {"WorkloadGVK":expected_gvk,"UID":uid,"Name":f"pod-{root}-{pod}","Namespace":"scale","Component":"job","Instance":"","Group":"","OwnerResolution":"resolved"}
        if observed.get("valid") is not True or any(record.get(key) != value for key,value in expected.items()):
            return False
        team = f"updated-team-{fixture.changed_pods[index]}" if index in fixture.changed_pods else TEAM
        if record.get("Labels",{}).get("team") != team:
            return False
    return True


def read_metrics(response, fixture, deadline=None, changing_roots=frozenset()):
    seen_created = bytearray(fixture.roots)
    seen_status = [0] * fixture.roots
    seen_pods = bytearray(fixture.roots * fixture.pods)
    counts = collections.Counter()
    samples = {}
    errors = []
    health_resources = set()
    invalid_status_roots = set()
    invalid_health_roots = set()
    validity = {}
    for number, raw in enumerate(response):
        if deadline is not None and number % 1024 == 0 and time.monotonic() > deadline:
            raise TimeoutError("independent oracle deadline exceeded")
        counts["bytes"] += len(raw)
        line = raw.decode().strip()
        if not line or line.startswith("#"):
            continue
        name = line.split("{", 1)[0].split(" ", 1)[0]
        if not name.startswith("karta_"):
            if name in ("process_resident_memory_bytes", "process_cpu_seconds_total", "go_memstats_heap_alloc_bytes", "go_memstats_alloc_bytes_total", "go_goroutines"):
                samples[name] = float(line.rsplit(" ", 1)[1])
            continue
        left, text_value = line.rsplit(" ", 1)
        value = float(text_value)
        labels = dict(re.findall(r'(\w+)="([^"\\]*)"', left))
        if name in LABELS and set(labels) != LABELS[name]:
            counts["wrong_labels"] += 1
            if len(errors) < 5:
                errors.append(f"{name} labels={sorted(labels)}")
        identity = re.fullmatch(r"job-(\d+)-g(\d+)", labels.get("workload_uid", ""))
        changing = identity is not None and int(identity[1]) in changing_roots and int(identity[2]) == fixture.generations.get(int(identity[1]),0)
        invalid = labels.get("observation_valid") == "false"
        if invalid and not changing:
            counts["invalid_rows"] += 1
            if len(errors) < 5:
                errors.append(line[:1000])
        if name == "karta_exporter_observation_valid":
            counts["health_rows"] += 1
            if changing and labels.get("scope") == "workload" and value == 0 and labels.get("reason") == "api_unavailable" and labels.get("workload_name") == f"job-{int(identity[1])}" and all(labels.get(key)==value for key,value in {"namespace":"scale","resource_group":"batch","resource_version":"v1","resource_kind":"Job"}.items()):
                if int(identity[1]) in invalid_health_roots:
                    counts["health_failures"] += 1
                invalid_health_roots.add(int(identity[1]))
                continue
            if value != 1 or labels.get("scope") != "resource_type" or labels.get("reason") != "none" or labels.get("workload_uid") or labels.get("workload_name"):
                counts["health_failures"] += 1
                if len(errors) < 5:
                    errors.append(line[:1000])
            resource = (labels.get("namespace"), labels.get("resource_group"), labels.get("resource_version"), labels.get("resource_kind"))
            if resource in health_resources:
                counts["health_failures"] += 1
            health_resources.add(resource)
            continue
        match = re.fullmatch(r"job-(\d+)-g(\d+)", labels.get("workload_uid", ""))
        if not match or int(match[1]) >= fixture.roots or fixture.deleted[int(match[1])] or int(match[2]) != fixture.generations.get(int(match[1]),0):
            counts["unexpected_identity"] += 1
            if len(errors) < 5:
                errors.append(line[:500])
            continue
        root = int(match[1])
        if name in ("karta_workload_status", "karta_pod_workload_info"):
            if root in validity and validity[root] != invalid:
                counts["wrong_coordinates"] += 1
            validity[root] = invalid
            if name == "karta_workload_status" and invalid and changing:
                invalid_status_roots.add(root)
        if labels.get("namespace") != "scale" or labels.get("workload_name") != f"job-{root}":
            counts["wrong_coordinates"] += 1
        if name in ("karta_workload_status", "karta_pod_workload_info"):
            if name == "karta_workload_status" and labels.get("team") != TEAM:
                counts["wrong_team"] += 1
            expected_labels = {"workload_group": "batch", "workload_version": "v1", "workload_kind": "Job", "owner_resolution": "resolved", "observation_valid": "false" if invalid and changing else "true"}
            if any(labels.get(key) != expected for key, expected in expected_labels.items()):
                counts["wrong_coordinates"] += 1
        if name == "karta_workload_created_timestamp_seconds":
            counts["created"] += 1
            if seen_created[root] or value != CREATED:
                counts["wrong_created"] += 1
            seen_created[root] = 1
        elif name == "karta_workload_status":
            counts["status"] += 1
            if labels.get("karta") != "batch-job-v1" or labels.get("karta_source") != "catalog":
                counts["wrong_definition"] += 1
            phase = labels.get("phase")
            if phase not in PHASES:
                counts["wrong_phase"] += 1
                continue
            bit = 1 << PHASES.index(phase)
            if seen_status[root] & bit:
                counts["duplicate_status"] += 1
            seen_status[root] |= bit
            expected = "Failed" if fixture.failed[root] else "Running"
            if value != int(phase == expected):
                counts["wrong_phase"] += 1
        elif name == "karta_pod_workload_info":
            counts["pods"] += 1
            match = re.fullmatch(r"pod-(\d+)-(\d+)-g(\d+)", labels.get("pod_uid", ""))
            if not match or int(match[1]) != root or int(match[2]) >= fixture.pods or int(match[3]) != fixture.generations.get(root,0):
                counts["wrong_pod"] += 1
                continue
            expected_labels = {"pod_name": f"pod-{root}-{int(match[2])}", "component": "job", "component_instance": "", "replica_group_id": ""}
            if any(labels.get(key) != expected for key, expected in expected_labels.items()):
                counts["wrong_pod"] += 1
            index = root * fixture.pods + int(match[2])
            if labels.get("team") != (f"updated-team-{fixture.changed_pods[index]}" if index in fixture.changed_pods else TEAM):
                counts["wrong_team"] += 1
            if seen_pods[index] or value != 1:
                counts["wrong_pod"] += 1
            seen_pods[index] = 1
        else:
            counts["unexpected_family"] += 1
    counts["health_failures"] += len(invalid_status_roots ^ invalid_health_roots)
    live = fixture.roots - sum(fixture.deleted)
    counts["missing_created"] = sum(not seen_created[root] and root not in invalid_status_roots for root in range(fixture.roots) if not fixture.deleted[root])
    counts["missing_pods"] = live * fixture.pods - sum(seen_pods)
    counts["missing_status_roots"] = sum(mask != 511 for index, mask in enumerate(seen_status) if not fixture.deleted[index])
    counts["missing_health"] = len({("scale", "", "v1", "Pod"), ("scale", "batch", "v1", "Job")} - health_resources)
    failures = ("wrong_labels", "wrong_team", "wrong_definition", "missing_health", "missing_created", "missing_pods", "missing_status_roots", "invalid_rows", "health_failures", "unexpected_identity", "wrong_coordinates", "wrong_created", "wrong_phase", "duplicate_status", "wrong_pod", "unexpected_family")
    return {"complete": all(counts[key] == 0 for key in failures), "counts": dict(counts), "process_metrics": samples, "examples": errors}


def process_rss(pid):
    result = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)], capture_output=True, text=True, check=False)
    return int(result.stdout.strip() or 0) * 1024


def process_usage(pid):
    result = subprocess.run(["ps", "-o", "rss=,pcpu=,time=", "-p", str(pid)], capture_output=True, text=True, check=False)
    fields = result.stdout.split()
    if len(fields) != 3:
        return {"rss_bytes": 0, "cpu_percent": None, "cpu_seconds": None}
    seconds = 0.0
    for part in fields[2].split(":"):
        seconds = seconds * 60 + float(part)
    return {"rss_bytes": int(fields[0])*1024, "cpu_percent":float(fields[1]), "cpu_seconds":seconds}


def run(args):
    output = pathlib.Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    binary = pathlib.Path(args.binary).resolve()
    with open(binary, "rb") as binary_file:
        binary_hash = hashlib.file_digest(binary_file, "sha256").hexdigest()
    report = {"model": "loopback HTTP API fixture; actual exporter binary/informers/workqueue/SDK/publication/OTel", "custom_label": "team", "expected_team": TEAM, "harness_sha256": hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(), "roots": args.roots, "pods_per_root": args.pods, "page_size": args.page_size, "poll_seconds": args.poll_seconds, "settle_seconds": args.settle_seconds, "delete_percent": args.delete_percent, "scrape_timing": "HTTP response transfer to disk; oracle timed separately", "gomaxprocs": args.gomaxprocs, "memory_limit_bytes": args.max_rss_mib * 1024 ** 2, "max_seconds": args.max_seconds, "stage_seconds": args.stage_seconds, "scrape_seconds": args.scrape_seconds, "oracle_seconds": args.oracle_seconds, "binary_sha256": binary_hash, "unavailable_measurements": ["per-event publication latency", "queue backlog", "queue drain time", "live heap if no diagnostic metrics"], "stages": []}
    report["measurement_protocol"] = "3: latency-budgeted hot set; bounded full-round ACK batches; protocol-2 heap/drain sampling"
    report["heap_profiles_enabled"] = args.heap_profiles
    report["source_revision"] = args.source_revision
    report["publication_latency_limit_seconds"] = args.publication_latency_seconds
    report["queue_drain_limit_seconds"] = args.queue_drain_seconds
    report["binary_build_info"] = subprocess.run(["go", "version", "-m", str(binary)], capture_output=True, text=True, check=False).stdout
    fixture = LoopbackAPI(args.roots, args.pods, args.page_size, intermediate=not args.direct_owner)
    report["topology"] = "Job->Pod" if args.direct_owner else "Job->ReplicaSet->Pod"
    endpoint = fixture.start()
    config = {"kartaDefinitionSource": {"catalog": {"enabled": True, "include": ["batch-job-v1"], "exclude": []}, "cluster": {"enabled": False}}, "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "scale"}}, "requiredKinds": ["batch/v1/Job"], "labelRules":[{"name":"team", "metrics":["karta_workload_status"], "sources":["workload"], "expression":'object.metadata.labels["team"]'}, {"name":"team", "metrics":["karta_pod_workload_info"], "sources":["pod", "workload"], "expression":'object.metadata.labels["team"]'}]}
    if args.refresh_seconds:
        config["refreshInterval"]=f"{args.refresh_seconds}s"
    if args.api_qps is not None:
        config["apiQPS"] = args.api_qps
    if args.api_burst is not None:
        config["apiBurst"] = args.api_burst
    if args.workers is not None:
        config["workers"] = args.workers
    config["maxAge"] = "120s"
    report["exporter_config"] = config
    report["client_budget"] = {"qps":args.api_qps,"burst":args.api_burst,"workers":args.workers,"source":"explicit config values when supplied; null means executable defaults, verify its source"}
    report["refresh_seconds"] = args.refresh_seconds or 45
    report["quiet_seconds"] = args.quiet_seconds
    (output / "config.json").write_text(json.dumps(config))
    # Loopback HTTP needs no authentication material; this file contains no credentials.
    kube = {"apiVersion": "v1", "kind": "Config", "clusters": [{"name": "synthetic", "cluster": {"server": endpoint}}], "users": [{"name": "synthetic", "user": {}}], "contexts": [{"name": "synthetic", "context": {"cluster": "synthetic", "user": "synthetic"}}], "current-context": "synthetic"}
    (output / "kubeconfig.json").write_text(json.dumps(kube))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        metrics_port = listener.getsockname()[1]
    diagnostics_port = None
    if args.diagnostics:
        with socket.socket() as listener:
            listener.bind(("127.0.0.1",0))
            diagnostics_port = listener.getsockname()[1]
    diagnostic_flags = [f"--diagnostics-bind-address=127.0.0.1:{diagnostics_port}"] if diagnostics_port else []
    report["test_diagnostics_wrapper"] = args.diagnostics
    child_env = dict(os.environ, GOMAXPROCS=str(args.gomaxprocs), GOMEMLIMIT=f"{max(128, args.max_rss_mib * 3 // 4)}MiB")
    log = open(output / "exporter.log", "w")
    process = subprocess.Popen([str(binary), "--config=" + str(output / "config.json"), "--kubeconfig=" + str(output / "kubeconfig.json"), f"--metrics-bind-address=127.0.0.1:{metrics_port}"] + diagnostic_flags, stdout=log, stderr=log, env=child_env)
    started = time.monotonic()
    stop = threading.Event()
    rss_samples = []
    limit = []
    diagnostic_samples = []
    continuity_stop = None
    continuity_thread = None
    emission_stop = threading.Event()
    emission_thread = None
    gc_thread = None
    gc_stop = threading.Event()
    collection_gate = CollectionGate()

    diagnostic_traffic = collections.Counter()
    diagnostic_lock = threading.Lock()
    heap_profile_number = 0

    def diagnostic(path="/stats", method="GET"):
        nonlocal heap_profile_number
        if path == "/gc" and args.heap_profiles:
            path = "/gc?heap=1"
        request=urllib.request.Request(f"http://127.0.0.1:{diagnostics_port}{path}", method=method)
        request_started = time.monotonic()
        with urllib.request.urlopen(request,timeout=5) as response:
            payload = response.read()
        with diagnostic_lock:
            diagnostic_traffic["requests"] += 1
            diagnostic_traffic["response_bytes"] += len(payload)
            diagnostic_traffic["http_seconds"] += time.monotonic()-request_started
        result=json.loads(payload)
        encoded=result.pop("heap_profile_base64",None)
        if encoded is not None:
            data=base64.b64decode(encoded,validate=True)
            heap_profile_number+=1
            profile_path=output / f"heap-{heap_profile_number:03d}.pprof"
            profile_path.write_bytes(data)
            result["heap_profile"]={"file":profile_path.name,"bytes":len(data),"sha256":hashlib.sha256(data).hexdigest()}
        return result


    def monitor():
        while not stop.wait(1):
            usage = process_usage(process.pid)
            rss = usage["rss_bytes"]
            rss_samples.append(dict(usage, seconds=round(time.monotonic() - started, 3)))
            if diagnostics_port:
                try:
                    diagnostic_samples.append(dict(diagnostic(),seconds=round(time.monotonic()-started,3)))
                except (OSError,ValueError):
                    pass
            if rss > report["memory_limit_bytes"] or time.monotonic() - started > args.max_seconds:
                limit.append("rss" if rss > report["memory_limit_bytes"] else "wall_time")
                process.kill()
                return

    monitor_thread=threading.Thread(target=monitor, daemon=True)
    monitor_thread.start()

    churn_intervals = ChurnIntervals()

    def converge(name):
        stage_started = time.monotonic()
        stage = {"name": name, "scrapes": [], "api_before": fixture.snapshot()}
        report["stages"].append(stage)
        last = None
        while time.monotonic() - stage_started < args.stage_seconds:
            if process.poll() is not None:
                raise RuntimeError(f"exporter exited {process.returncode}, limit={limit}")
            scrape_started = time.monotonic()
            try:
                request = urllib.request.Request(f"http://127.0.0.1:{metrics_port}/metrics", headers={"Accept": "text/plain; version=0.0.4"})
                with tempfile.TemporaryFile() as body:
                    def transfer():
                        request_started=time.monotonic()
                        with urllib.request.urlopen(request, timeout=args.scrape_seconds) as response:
                            headers=time.monotonic()-request_started
                            shutil.copyfileobj(response,body)
                        return headers
                    headers_seconds, http_timing = collection_gate.run(transfer)
                    scrape_started=http_timing["request_started_at"]
                    http_seconds=http_timing["completed_at"]-scrape_started
                    body.seek(0)
                    oracle_started = time.monotonic()
                    changing = churn_intervals.overlapping(scrape_started, http_timing["completed_at"]) if name == "churn_continuity" else frozenset()
                    last = read_metrics(body, fixture, oracle_started + args.oracle_seconds, changing)
                    last["changing_roots"] = sorted(changing)
                    last["oracle_seconds"] = round(time.monotonic()-oracle_started,4)
                    last["http_seconds"] = round(http_seconds,4)
                    last["http_gate_wait_seconds"] = http_timing["gate_wait_seconds"]
                last["started_seconds"] = round(scrape_started-started,6)
                last["finished_seconds"] = round(time.monotonic()-started,6)
                last["response_headers_seconds"] = round(headers_seconds, 4)
                last["scrape_seconds"] = round(time.monotonic() - scrape_started, 4)
                stage["scrapes"].append(last)
                if time.monotonic() - stage_started > args.stage_seconds:
                    raise RuntimeError(f"{name} exceeded stage deadline during scrape")
                if last["complete"]:
                    stage["seconds"] = round(time.monotonic() - stage_started, 4)
                    stage["api_after"] = fixture.snapshot()
                    stage["api_read_rates_per_second"] = {key:(value-stage["api_before"]["requests"].get(key,0))/stage["seconds"] for key,value in stage["api_after"]["requests"].items() if ".get." in key or ".list." in key}
                    stage.update(process_usage(process.pid))
                    if diagnostics_port:
                        stage["diagnostics"] = diagnostic()
                    print(json.dumps({"stage": name, "seconds": stage["seconds"], "rss_bytes": stage["rss_bytes"], "counts": last["counts"]}), flush=True)
                    return
                print(json.dumps({"stage": name, "complete": False, "scrape_seconds": last["scrape_seconds"], "counts": last["counts"], "examples": last.get("examples", [])}), flush=True)
            except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
                stage["scrapes"].append({"error": str(exc), "scrape_seconds": round(time.monotonic() - scrape_started, 4)})
            time.sleep(args.poll_seconds)
        raise RuntimeError(f"{name} did not converge within {args.stage_seconds}s; last={last}")

    try:
        converge("cold_start")
        for _ in range(args.steady_scrapes):
            converge("steady_scrape")
        quiet_started=time.monotonic()
        while time.monotonic()-quiet_started < args.quiet_seconds:
            converge("quiet_continuity")
            time.sleep(min(args.continuity_seconds,max(0,args.quiet_seconds-(time.monotonic()-quiet_started))))
        if args.churn_seconds:
            if not diagnostics_port:
                raise RuntimeError("sustained churn requires --diagnostics")
            for root in range(churn_root_count(args.roots,args.churn_rate,args.publication_latency_seconds,args.max_scheduling_lateness_seconds,args.diagnostic_poll_seconds)):
                fixture.change(root,change_pods=False)
            converge("churn_baseline")
            continuity_stop=threading.Event()
            continuity_errors=[]
            def continuity():
                while not continuity_stop.wait(args.continuity_seconds):
                    try:
                        converge("churn_continuity")
                    except Exception as exc:
                        continuity_errors.append(str(exc))
                        return
            continuity_thread=threading.Thread(target=continuity,daemon=True)
            continuity_thread.start()
            churn_started=time.monotonic()
            pending={}
            latencies=[]
            schedule=FixedSchedule(args.churn_seconds,args.churn_rate,args.max_scheduling_lateness_seconds)
            report["sustained_churn"] = {"requested_seconds":args.churn_seconds,"target_event_count":schedule.target,"target_events_per_second":args.churn_rate,"emitted":0,"observed":0,"publication_upper_bounds_seconds":latencies}
            report["sustained_churn"]["emissions"] = []
            emission_condition=threading.Condition()
            emission_done=threading.Event()
            emission_errors=[]
            report["sustained_churn"]["scheduling_lateness_limit_seconds"]=args.max_scheduling_lateness_seconds
            report["sustained_churn"]["max_scheduling_lateness_seconds"]=0
            pool=churn_root_count(args.roots,args.churn_rate,args.publication_latency_seconds,args.max_scheduling_lateness_seconds,args.diagnostic_poll_seconds)
            report["sustained_churn"].update({"hot_root_count":pool,"hot_root_rotation_seconds":pool/args.churn_rate,
                "hot_root_formula":"min(roots,max(100,ceil(rate*(publication_limit+2*lateness_limit+2*poll_seconds))))",
                "hot_root_population_capped":pool==args.roots,"publication_limit_seconds":args.publication_latency_seconds,
                "diagnostic_poll_seconds":args.diagnostic_poll_seconds,"ack_batch_limit":100,"ack_poll_rounds":[]})
            gc_samples=[]
            report["sustained_churn"]["gc_samples"]=gc_samples
            gc_errors=[]
            def sample_gc():
                try:
                    offset=max(0,args.churn_seconds-180-args.gc_sample_seconds)
                    while True:
                        scheduled=churn_started+offset
                        if gc_stop.wait(max(0,scheduled-time.monotonic())):
                            return
                        sample,timing=collection_gate.run(lambda: diagnostic("/gc",method="POST"),scheduled)
                        sample.update({key:value-churn_started if key.endswith("_at") and value is not None else value for key,value in timing.items()})
                        sample["seconds"]=sample["completed_at"]
                        gc_samples.append(sample)
                        if offset >= args.churn_seconds:
                            return
                        offset=min(args.churn_seconds,offset+args.gc_sample_seconds)
                except Exception as exc:
                    gc_errors.append(str(exc))
            gc_thread=threading.Thread(target=sample_gc,daemon=True)
            gc_thread.start()
            def emit_changes():
                try:
                    while schedule.emitted < schedule.target and not emission_stop.is_set():
                        with emission_condition:
                            sent=time.monotonic()
                            root=schedule.emitted % pool
                            if not schedule.due(sent-churn_started,root in pending):
                                delay=max(0.001,min(0.05,churn_started+schedule.emitted/args.churn_rate-sent))
                                emission_condition.wait(timeout=delay)
                                continue
                            scheduled,lateness=schedule.record(sent-churn_started)
                            churn_intervals.begin(root,sent)
                            fixture.change(root,change_pods=False)
                            pending[root]=(str(fixture.root_versions[root]),sent)
                            report["sustained_churn"]["emissions"].append({"root":root,"resource_version":str(fixture.root_versions[root]),"seconds":round(sent-started,6),"scheduled_seconds":scheduled,"lateness_seconds":lateness})
                            report["sustained_churn"]["emitted"]=schedule.emitted
                            report["sustained_churn"]["last_emission_seconds"]=sent-churn_started
                            report["sustained_churn"]["max_scheduling_lateness_seconds"]=max(report["sustained_churn"]["max_scheduling_lateness_seconds"],lateness)
                except Exception as exc:
                    emission_errors.append(str(exc))
                    report["sustained_churn"]["scheduler_error"]=str(exc)
                finally:
                    report["sustained_churn"]["emitter_completed_seconds"]=time.monotonic()-churn_started
                    emission_done.set()

            emission_thread=threading.Thread(target=emit_changes,daemon=True)
            emission_thread.start()
            while not emission_done.is_set() or pending or time.monotonic()-churn_started < args.churn_seconds:
                now=time.monotonic()
                if emission_errors:
                    raise RuntimeError(emission_errors[0])
                if now-churn_started >= args.churn_seconds+args.stage_seconds:
                    raise RuntimeError("churn publication drain deadline exceeded")
                if process.poll() is not None:
                    raise RuntimeError("exporter exited during sustained churn")
                if churn_intervals.overdue(now,args.publication_latency_seconds):
                    raise RuntimeError("changed root did not recover a verified publication within the latency bound")
                with emission_condition:
                    pending_snapshot=dict(pending)
                if pending_snapshot:
                    round_started=time.monotonic()
                    batch_timings=[]
                    rejections=collections.Counter()
                    entries=list(pending_snapshot.items())
                    for offset in range(0,len(entries),100):
                        if churn_intervals.overdue(time.monotonic(),args.publication_latency_seconds):
                            raise RuntimeError("changed root did not recover a verified publication within the latency bound")
                        batch=dict(entries[offset:offset+100])
                        query=urllib.parse.urlencode([("uid",f"job-{root}-g{fixture.generations.get(root,0)}") for root in batch])
                        batch_started=time.monotonic()
                        publications=diagnostic("/publications?"+query)
                        observed=time.monotonic()
                        batch_timings.append({"started_seconds":batch_started-churn_started,"completed_seconds":observed-churn_started,"requested_roots":len(batch)})
                        with emission_condition:
                            for root,(version,sent) in batch.items():
                                uid=f"job-{root}-g{fixture.generations.get(root,0)}"
                                publication=publications.get(uid,{})
                                if verified_publication(publication,fixture,root,version):
                                    if observed-sent > args.publication_latency_seconds:
                                        raise RuntimeError("publication acknowledgment exceeded the latency bound")
                                    churn_intervals.acknowledge(root,observed)
                                    latencies.append(observed-sent)
                                    del pending[root]
                                    report["sustained_churn"]["observed"]=len(latencies)
                                    report["sustained_churn"]["last_verified_ack_seconds"]=observed-churn_started
                                elif not publication:
                                    rejections["absent"]+=1
                                elif publication.get("valid") is not True:
                                    rejections["invalid_proof"]+=1
                                elif publication.get("resource_version") != version:
                                    rejections["different_resource_version"]+=1
                                else:
                                    rejections["identity_or_content_mismatch"]+=1
                            emission_condition.notify_all()
                    report["sustained_churn"]["ack_poll_rounds"].append({"started_seconds":round_started-churn_started,"completed_seconds":time.monotonic()-churn_started,"pending_roots":len(entries),"batches":batch_timings,"rejections":dict(rejections)})
                time.sleep(args.diagnostic_poll_seconds)
            emission_thread.join(timeout=1)
            if emission_errors:
                raise RuntimeError(emission_errors[0])
            emitted=schedule.emitted
            report["sustained_churn"]["emission_and_observation_seconds"]=time.monotonic()-churn_started
            report["sustained_churn"]["observed_events_per_elapsed_second"]=len(latencies)/report["sustained_churn"]["emission_and_observation_seconds"]
            continuity_stop.set()
            drain_started=time.monotonic()
            drain_polls=[]
            queue_idle_samples=0
            while queue_idle_samples < 2:
                poll_started=time.monotonic()
                queue_stats=diagnostic()["queues"]
                poll_completed=time.monotonic()
                depth=sum(metric["value"] for name,metric in queue_stats.items() if name.endswith(".depth"))
                starts=sum(metric["count"] for name,metric in queue_stats.items() if name.endswith(".queue_latency_seconds"))
                finishes=sum(metric["count"] for name,metric in queue_stats.items() if name.endswith(".work_duration_seconds"))
                queue_idle_samples=queue_idle_samples+1 if queue_stats and depth==0 and starts==finishes else 0
                drain_polls.append({"started_seconds":poll_started-churn_started,"completed_seconds":poll_completed-churn_started,"depth":depth,"active":starts-finishes})
                if poll_completed-drain_started > args.queue_drain_seconds:
                    break
                if queue_idle_samples < 2:
                    time.sleep(args.diagnostic_poll_seconds)
            drain_completed=time.monotonic()
            report["sustained_churn"]["drain_started_seconds"]=drain_started-churn_started
            report["sustained_churn"]["drain_completed_seconds"]=drain_completed-churn_started
            report["sustained_churn"]["drain_polls"]=drain_polls
            continuity_thread.join(timeout=args.stage_seconds+args.scrape_seconds+args.oracle_seconds)
            gc_thread.join(timeout=args.scrape_seconds+10)
            if continuity_thread.is_alive() or continuity_errors:
                raise RuntimeError(f"churn continuity failed: {continuity_errors}")
            if gc_thread.is_alive() or gc_errors:
                raise RuntimeError(f"GC sampling failed: {gc_errors}")
            heap_coverage,heap_growth,heap_complete=heap_window(gc_samples,args.churn_seconds)
            report["sustained_churn"]["heap_sampling_coverage_seconds"]=heap_coverage
            report["sustained_churn"]["heap_sampling_full_window"]=heap_complete
            latencies.sort()
            report["sustained_churn"].update({"requested_seconds":args.churn_seconds,"target_events_per_second":args.churn_rate,"emitted":emitted,"observed":len(latencies),"achieved_events_per_second":emitted/args.churn_seconds,"publication_upper_bound_p99_seconds":latencies[min(len(latencies)-1,int(len(latencies)*0.99))] if latencies else None,"publication_upper_bound_max_seconds":max(latencies,default=None),"drain_seconds":drain_completed-drain_started,"gc_samples":gc_samples,"gc_sample_seconds":args.gc_sample_seconds,"final_window_heap_growth_fraction":heap_growth,"poll_seconds":args.diagnostic_poll_seconds,"measurement":"event emission to first captured valid creation/status and current Pod mapping at exact root RV; polling upper bound","all_queues_drained":queue_idle_samples>=2,"queue_drain_snapshot":queue_stats})
            converge("after_sustained_churn")
        churn_count = max(1, min(args.churn_roots, args.roots))
        if args.pods:
            fixture.change_pod(0, args.pods - 1)
            converge("single_pod_label_update")
            report["stages"][-1]["limitation"] = "Convergence includes polling and full HTTP/oracle time; not per-event publication latency"
        if args.watch_faults:
            before=fixture.snapshot()["requests"]
            fixture.interrupt_watch("pods")
            fixture.interrupt_watch("jobs",expired=True)
            reconnect_started=time.monotonic()
            while time.monotonic()-reconnect_started < args.stage_seconds:
                counts=fixture.snapshot()["requests"]
                if all(counts.get(resource+".watch",0)>before.get(resource+".watch",0) for resource in ("pods","jobs")):
                    break
                time.sleep(0.05)
            else:
                raise RuntimeError("WATCH reconnect deadline exceeded")
            if args.pods:
                fixture.change_pod(0)
            converge("watch_eof_and_410_recovery")
            if args.pods:
                with fixture.condition:
                    fixture.change_during_list = True
                list_started=time.monotonic()
                while fixture.change_during_list:
                    if time.monotonic()-list_started > args.stage_seconds:
                        raise RuntimeError("metadata LIST mutation was not exercised")
                    time.sleep(0.05)
                converge("watch_update_during_metadata_list")
        for root in range(churn_count):
            fixture.change(root)
        converge("status_and_pod_churn")
        for root in range(max(1, args.roots * args.delete_percent // 100)):
            fixture.change(root, delete=True)
        converge("deletion_cleanup")
        fixture.recreate(0)
        converge("same_name_recreation")
        for root in range(args.roots):
            fixture.change(root, delete=True)
        converge("delete_all_cleanup")
        if diagnostics_port:
            report["post_delete_gc"] = diagnostic("/gc",method="POST")
            peak_heap=max([sample["heap_alloc_bytes"] for sample in diagnostic_samples]+[report["post_delete_gc"]["heap_alloc_bytes"]])
            report["post_delete_heap_gate"] = report["post_delete_gc"]["heap_alloc_bytes"] <= report["post_delete_gc"]["baseline_heap_bytes"] + max(256*1024**2,peak_heap*0.1)
        if args.settle_seconds:
            time.sleep(args.settle_seconds)
            converge("settled_after_cleanup")
        intermediate_streams={key:value for key,value in fixture.snapshot()["requests"].items() if value and key=="replicasets.watch"}
        report["intermediate_streams"] = intermediate_streams
        if intermediate_streams:
            raise RuntimeError(f"intermediate owner WATCH is forbidden: {intermediate_streams}")
        report["completed_all_stages"] = True
        gaps = [scrape for stage in report["stages"] if stage["name"] in ("steady_scrape","quiet_continuity","churn_continuity") for scrape in stage["scrapes"] if not scrape.get("complete")]
        report["steady_validity_gaps"] = len(gaps)
        report["complete"] = not gaps
        if gaps:
            report["failure"] = "steady/continuity output lost validity without expected public-value changes"
    except Exception as exc:
        report["completed_all_stages"] = False
        report["complete"] = False
        report["failure"] = str(exc)
    finally:
        gc_stop.set()
        emission_stop.set()
        if emission_thread is not None:
            emission_thread.join(timeout=2)
        stop.set()
        if continuity_stop is not None:
            continuity_stop.set()
        monitor_thread.join(timeout=6)
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        if continuity_thread is not None:
            continuity_thread.join(timeout=args.poll_seconds+5)
        if gc_thread is not None:
            gc_thread.join(timeout=6)
        report["returncode"] = process.returncode
        completed = [scrape for stage in report["stages"] for scrape in stage["scrapes"] if scrape.get("complete")]
        report["measured_gates"] = {"http_scrapes_under_25s": bool(completed) and all(scrape["http_seconds"] <= 25 for scrape in completed), "oracle_under_deadline": bool(completed) and all(scrape["oracle_seconds"] <= args.oracle_seconds for scrape in completed)}
        if "sustained_churn" in report:
            churn=report["sustained_churn"]
            events=churn["emissions"]
            span=events[-1]["seconds"]-events[0]["seconds"] if len(events)>1 else 0
            churn["actual_emission_span_seconds"]=span
            churn["actual_emission_events_per_second"]=(len(events)-1)/span if span else None
            report["measured_gates"]["sustained_churn_event_count"]=churn["emitted"]==churn["target_event_count"]
            report["measured_gates"]["scheduler_lateness"]=not churn.get("scheduler_error") and churn["max_scheduling_lateness_seconds"]<=churn["scheduling_lateness_limit_seconds"]
        if "sustained_churn" in report and "drain_seconds" in report["sustained_churn"]:
            churn=report["sustained_churn"]
            report["measured_gates"].update({"sustained_churn_full_duration":churn["requested_seconds"]>=300,"sustained_churn_event_count":churn["emitted"]==churn["target_event_count"],"publication_p99_upper_bound":churn["publication_upper_bound_p99_seconds"] is not None and churn["publication_upper_bound_p99_seconds"] <= args.publication_latency_seconds,"queue_drain":churn.get("all_queues_drained",False) and churn["drain_seconds"] <= args.queue_drain_seconds,"heap_sampling_full_window":churn["heap_sampling_full_window"],"live_heap_growth":churn["heap_sampling_full_window"] and churn["final_window_heap_growth_fraction"] is not None and churn["final_window_heap_growth_fraction"]<=0.05})
        if "post_delete_heap_gate" in report:
            report["measured_gates"]["delete_all_heap"] = report["post_delete_heap_gate"]
        report["elapsed_seconds"] = round(time.monotonic() - started, 4)
        report["peak_sampled_rss_bytes"] = max((sample["rss_bytes"] for sample in rss_samples), default=0)
        report["diagnostic_samples"] = diagnostic_samples
        report["diagnostic_traffic"] = dict(diagnostic_traffic)
        report["diagnostic_poll_seconds"] = args.diagnostic_poll_seconds
        report["diagnostic_cost_scope"] = "wrapper CPU/RSS/TotalAlloc include diagnostic work; not separately attributed from concurrent production work"
        if diagnostics_port:
            report["unavailable_measurements"] = ["exact event-to-publication timestamp (diagnostic polling gives an upper bound)"]
        report["process_samples"] = rss_samples
        report["cpu_seconds_last_sample"] = next((sample["cpu_seconds"] for sample in reversed(rss_samples) if sample["cpu_seconds"] is not None), None)
        report["limit_triggered"] = limit
        report["api"] = fixture.snapshot()
        report["api_read_rates_per_second"] = {key:value/report["elapsed_seconds"] for key,value in report["api"]["requests"].items() if ".get." in key or ".list." in key}
        report["harness_rss_bytes"] = process_rss(os.getpid())
        log.close()
        fixture.close()
        (output / "result.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps({key: report[key] for key in ("complete", "elapsed_seconds", "peak_sampled_rss_bytes", "limit_triggered", "measured_gates")}), flush=True)
    return 0 if report["complete"] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--source-revision", help="Exact checkout revision used to build the binary; build info also records dirty state")
    parser.add_argument("--api-qps", type=float)
    parser.add_argument("--api-burst", type=int)
    parser.add_argument("--workers", type=int)
    parser.add_argument("--direct-owner", action="store_true", help="Use Job->Pod for a matched no-intermediate control")
    parser.add_argument("--quiet-seconds", type=float, default=0)
    parser.add_argument("--continuity-seconds", type=float, default=5)
    parser.add_argument("--refresh-seconds", type=float, default=0)
    parser.add_argument("--watch-faults", action="store_true")
    parser.add_argument("--heap-profiles", action="store_true", help="Diagnostic-only: save sampled heap profiles after existing GC requests; adds profile serialization overhead")
    parser.add_argument("--diagnostics", action="store_true", help="Binary is the test/scale/cmd wrapper")
    parser.add_argument("--churn-seconds", type=float, default=0)
    parser.add_argument("--churn-rate", type=float, default=100, help="Positive events/s; fractional rates are preserved")
    parser.add_argument("--publication-latency-seconds", type=float, help="Predeclared verified-publication bound; default 5s, or 10s for >=10 Pods/root")
    parser.add_argument("--queue-drain-seconds", type=float, help="Predeclared drain bound; default 30s, or 60s for >=10 Pods/root")
    parser.add_argument("--max-scheduling-lateness-seconds", type=float, default=1, help="Fail instead of catch-up bursts after a longer emitter stall")
    parser.add_argument("--gc-sample-seconds", type=float, default=30)
    parser.add_argument("--diagnostic-poll-seconds", type=float, help="Default 250ms for >=10k roots or >=100k Pods; otherwise 10ms")
    parser.add_argument("--output", required=True)
    parser.add_argument("--roots", type=int, required=True)
    parser.add_argument("--pods", type=int, required=True)
    parser.add_argument("--page-size", type=int, default=500)
    parser.add_argument("--gomaxprocs", type=int, default=4)
    parser.add_argument("--max-rss-mib", type=int, default=8192)
    parser.add_argument("--max-seconds", type=int, default=480)
    parser.add_argument("--stage-seconds", type=int, default=180)
    parser.add_argument("--scrape-seconds", type=int, default=60)
    parser.add_argument("--oracle-seconds", type=int, default=60)
    parser.add_argument("--poll-seconds", type=float, default=5)
    parser.add_argument("--steady-scrapes", type=int, default=2)
    parser.add_argument("--churn-roots", type=int, default=100)
    parser.add_argument("--delete-percent", type=int, default=10)
    parser.add_argument("--settle-seconds", type=int, default=30)
    args = parser.parse_args()
    if args.heap_profiles and not args.diagnostics:
        parser.error("heap profiles require the test diagnostics wrapper")
    if args.publication_latency_seconds is None:
        args.publication_latency_seconds = 10 if args.pods>=10 else 5
    if args.queue_drain_seconds is None:
        args.queue_drain_seconds = 60 if args.pods>=10 else 30
    if any(not math.isfinite(value) or value <= 0 for value in (args.churn_rate,args.publication_latency_seconds,args.queue_drain_seconds)):
        parser.error("rate, publication latency and queue drain bounds must be finite and positive")
    if args.diagnostic_poll_seconds is None:
        args.diagnostic_poll_seconds = 0.25 if args.roots >= 10000 or args.roots*args.pods >= 100000 else 0.01
    if args.roots < 1 or args.pods < 0 or args.roots * args.pods > 1000000 or args.roots > 100000 or not 1 <= args.page_size <= 5000 or args.max_rss_mib < 256 or args.gomaxprocs < 1 or min(args.max_seconds, args.stage_seconds, args.scrape_seconds, args.oracle_seconds, args.poll_seconds) <= 0 or args.steady_scrapes < 0 or args.churn_roots < 1 or not 1 <= args.delete_percent <= 100 or args.settle_seconds < 0 or args.churn_seconds < 0 or args.churn_rate <= 0 or args.max_scheduling_lateness_seconds <= 0 or args.diagnostic_poll_seconds <= 0 or args.gc_sample_seconds <= 0 or args.quiet_seconds < 0 or args.continuity_seconds <= 0 or args.refresh_seconds < 0:
        parser.error("invalid scenario or safety bound; maximum 100k roots and 1m Pods")
    if (args.api_qps is not None and args.api_qps<=0) or (args.api_burst is not None and args.api_burst<=0) or (args.workers is not None and args.workers<1):
        parser.error("explicit API budgets and worker counts must be positive")
    raise SystemExit(run(args))


if __name__ == "__main__":
    main()
