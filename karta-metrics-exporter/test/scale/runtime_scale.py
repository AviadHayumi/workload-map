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
import hashlib
import http.server
import json
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
    def __init__(self, roots, pods, page_size=500):
        self.roots, self.pods, self.page_size = roots, pods, page_size
        self.deleted = bytearray(roots)
        self.failed = bytearray(roots)
        self.changed_pods = {}
        self.root_versions = {}
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
        deleted, failed, root_versions, changed_pods, generations = state if state is not None else (self.deleted, self.failed, self.root_versions, self.changed_pods, self.generations)
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
                obj = {"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": {"name": f"rs-{root}", "namespace": "scale", "uid": f"rs-{root}-g{generation}", "resourceVersion": str(root_versions.get(root,1)), "ownerReferences": [{"apiVersion":"batch/v1", "kind":"Job", "name":f"job-{root}", "uid":f"job-{root}-g{generation}", "controller": True}]}}
            else:
                pod = index % self.pods
                obj = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": f"pod-{root}-{pod}", "namespace": "scale", "uid": f"pod-{root}-{pod}-g{generation}", "resourceVersion": str(changed_pods.get(index, 1)), "creationTimestamp": CREATED_TEXT, "labels": {"app": "worker", "batch.kubernetes.io/job-name": f"job-{root}", "batch.kubernetes.io/controller-uid": f"job-{root}-g{generation}"}, "ownerReferences": [{"apiVersion": "batch/v1", "kind": "Job", "name": f"job-{root}", "uid": f"job-{root}-g{generation}", "controller": True, "blockOwnerDeletion": True}]}, "spec": {"nodeName": "synthetic-node", "restartPolicy": "Never", "containers": [{"name": "worker", "image": "registry.k8s.io/pause:3.10", "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}}}]}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}], "containerStatuses": [{"name": "worker", "ready": True, "restartCount": 0, "image": "registry.k8s.io/pause:3.10", "imageID": "synthetic", "state": {"running": {"startedAt": CREATED_TEXT}}}]}}
        if resource != "namespaces":
            obj["metadata"].setdefault("labels", {})["team"] = "updated-team" if resource == "pods" and index in changed_pods else TEAM
        if resource == "pods":
            obj["metadata"]["ownerReferences"] = [{"apiVersion":"apps/v1", "kind":"ReplicaSet", "name":f"rs-{root}", "uid":f"rs-{root}-g{generation}", "controller":True}]
        if metadata:
            return {"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadata", "metadata": obj["metadata"]}
        return obj

    def total(self, resource):
        return {"namespaces": 1, "jobs": self.roots, "replicasets": self.roots, "pods": self.roots * self.pods}[resource]

    def snapshot(self):
        with self.condition:
            return {"requests": dict(self.counts), "max_page_objects": self.max_page, "active_watches": self.active_watches, "revision": self.revision, "completed_metadata_lists": dict(self.completed_metadata_lists), "delivered_watch_versions": dict(self.delivered_watch_versions)}

    def change(self, root, delete=False):
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
                if self.pods:
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
                else:
                    self.root_versions[root] = self.revision
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
                    offset, revision = 0, fixture.revision
                    if value("continue"):
                        try:
                            token_resource, revision, offset = json.loads(base64.urlsafe_b64decode(value("continue")))
                            if token_resource != resource or not isinstance(offset, int) or not 0 <= offset <= fixture.total(resource):
                                raise ValueError("continuation belongs to another collection or has an invalid offset")
                        except (ValueError, TypeError):
                            self.status(400, "BadRequest")
                            return
                        if (resource, revision) not in fixture.snapshots:
                            fixture.counts["continuation_expirations"] += 1
                            self.status(410, "ResourceExpired")
                            return
                    snapshot_key = (resource, revision)
                    if snapshot_key not in fixture.snapshots:
                        fixture.snapshots[snapshot_key] = (bytes(fixture.deleted), bytes(fixture.failed), dict(fixture.root_versions), dict(fixture.changed_pods), dict(fixture.generations))
                        while len(fixture.snapshots) > 16:
                            fixture.snapshots.popitem(last=False)
                    fixture.snapshots.move_to_end(snapshot_key)
                    state = fixture.snapshots[snapshot_key]
                    requested = int(value("limit", str(fixture.page_size)))
                    limit = min(requested or fixture.page_size, fixture.page_size)
                    items = []
                    while offset < fixture.total(resource) and len(items) < limit:
                        obj = fixture.object(resource, offset, metadata, state)
                        offset += 1
                        if obj is not None:
                            items.append(obj)
                    fixture.max_page = max(fixture.max_page, len(items))
                    fixture.counts[f"{resource}.objects.{encoding}"] += len(items)
                    continuation = base64.urlsafe_b64encode(json.dumps([resource, revision, offset]).encode()).decode() if offset < fixture.total(resource) else ""
                    kind = "PartialObjectMetadataList" if metadata else {"namespaces": "NamespaceList", "jobs": "JobList", "replicasets": "ReplicaSetList", "pods": "PodList"}[resource]
                    api = "meta.k8s.io/v1" if metadata else "batch/v1" if resource == "jobs" else "apps/v1" if resource == "replicasets" else "v1"
                    self.send_json({"apiVersion": api, "kind": kind, "metadata": {"resourceVersion": str(revision), "continue": continuation}, "items": items})
                    if metadata and not continuation:
                        fixture.completed_metadata_lists[resource] = revision

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
                    fixture.active_watches += 1
                    fixture.counts[f"{resource}.watch"] += 1
                started = time.monotonic()
                try:
                    while time.monotonic() - started < 240:
                        with fixture.condition:
                            if fixture.events and version < fixture.events[0][0] - 1:
                                fixture.counts["watch_expirations"] += 1
                                payload = {"type": "ERROR", "object": {"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "Expired", "code": 410}}
                                events = None
                            else:
                                events = [event for event in fixture.events if event[0] > version]
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


def read_metrics(response, fixture, deadline=None):
    seen_created = bytearray(fixture.roots)
    seen_status = [0] * fixture.roots
    seen_pods = bytearray(fixture.roots * fixture.pods)
    counts = collections.Counter()
    samples = {}
    errors = []
    health_resources = set()
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
        if labels.get("observation_valid") == "false":
            counts["invalid_rows"] += 1
        if name == "karta_exporter_observation_valid":
            counts["health_rows"] += 1
            if value != 1 or labels.get("scope") != "resource_type" or labels.get("reason") != "none" or labels.get("workload_uid") or labels.get("workload_name"):
                counts["health_failures"] += 1
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
        if labels.get("namespace") != "scale" or labels.get("workload_name") != f"job-{root}":
            counts["wrong_coordinates"] += 1
        if name in ("karta_workload_status", "karta_pod_workload_info"):
            if name == "karta_workload_status" and labels.get("team") != TEAM:
                counts["wrong_team"] += 1
            expected_labels = {"workload_group": "batch", "workload_version": "v1", "workload_kind": "Job", "owner_resolution": "resolved", "observation_valid": "true"}
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
            if labels.get("team") != ("updated-team" if index in fixture.changed_pods else TEAM):
                counts["wrong_team"] += 1
            if seen_pods[index] or value != 1:
                counts["wrong_pod"] += 1
            seen_pods[index] = 1
        else:
            counts["unexpected_family"] += 1
    live = fixture.roots - sum(fixture.deleted)
    counts["missing_created"] = live - sum(seen_created)
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
    fixture = LoopbackAPI(args.roots, args.pods, args.page_size)
    endpoint = fixture.start()
    config = {"kartaDefinitionSource": {"catalog": {"enabled": True, "include": ["batch-job-v1"], "exclude": []}, "cluster": {"enabled": False}}, "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "scale"}}, "requiredKinds": [{"group":"batch", "version":"v1", "kind":"Job"}], "labelRules":[{"name":"team", "metrics":["karta_workload_status"], "sources":["workload"], "expression":'object.metadata.labels["team"]'}, {"name":"team", "metrics":["karta_pod_workload_info"], "sources":["pod", "workload"], "expression":'object.metadata.labels["team"]'}]}
    (output / "config.json").write_text(json.dumps(config))
    # Loopback HTTP needs no authentication material; this file contains no credentials.
    kube = {"apiVersion": "v1", "kind": "Config", "clusters": [{"name": "synthetic", "cluster": {"server": endpoint}}], "users": [{"name": "synthetic", "user": {}}], "contexts": [{"name": "synthetic", "context": {"cluster": "synthetic", "user": "synthetic"}}], "current-context": "synthetic"}
    (output / "kubeconfig.json").write_text(json.dumps(kube))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        metrics_port = listener.getsockname()[1]
    child_env = dict(os.environ, GOMAXPROCS=str(args.gomaxprocs), GOMEMLIMIT=f"{max(128, args.max_rss_mib * 3 // 4)}MiB")
    log = open(output / "exporter.log", "w")
    process = subprocess.Popen([str(binary), "--config=" + str(output / "config.json"), "--kubeconfig=" + str(output / "kubeconfig.json"), f"--metrics-bind-address=127.0.0.1:{metrics_port}"], stdout=log, stderr=log, env=child_env)
    started = time.monotonic()
    stop = threading.Event()
    rss_samples = []
    limit = []

    def monitor():
        while not stop.wait(1):
            usage = process_usage(process.pid)
            rss = usage["rss_bytes"]
            rss_samples.append(dict(usage, seconds=round(time.monotonic() - started, 3)))
            if rss > report["memory_limit_bytes"] or time.monotonic() - started > args.max_seconds:
                limit.append("rss" if rss > report["memory_limit_bytes"] else "wall_time")
                process.kill()
                return

    threading.Thread(target=monitor, daemon=True).start()

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
                with urllib.request.urlopen(request, timeout=args.scrape_seconds) as response:
                    headers_seconds = time.monotonic() - scrape_started
                    with tempfile.TemporaryFile() as body:
                        shutil.copyfileobj(response,body)
                        http_seconds = time.monotonic() - scrape_started
                        body.seek(0)
                        oracle_started = time.monotonic()
                        last = read_metrics(body, fixture, oracle_started + args.oracle_seconds)
                        last["oracle_seconds"] = round(time.monotonic()-oracle_started,4)
                        last["http_seconds"] = round(http_seconds,4)
                last["response_headers_seconds"] = round(headers_seconds, 4)
                last["scrape_seconds"] = round(time.monotonic() - scrape_started, 4)
                stage["scrapes"].append(last)
                if time.monotonic() - stage_started > args.stage_seconds:
                    raise RuntimeError(f"{name} exceeded stage deadline during scrape")
                if last["complete"]:
                    stage["seconds"] = round(time.monotonic() - stage_started, 4)
                    stage["api_after"] = fixture.snapshot()
                    stage.update(process_usage(process.pid))
                    print(json.dumps({"stage": name, "seconds": stage["seconds"], "rss_bytes": stage["rss_bytes"], "counts": last["counts"]}), flush=True)
                    return
                print(json.dumps({"stage": name, "complete": False, "scrape_seconds": last["scrape_seconds"], "counts": last["counts"]}), flush=True)
            except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
                stage["scrapes"].append({"error": str(exc), "scrape_seconds": round(time.monotonic() - scrape_started, 4)})
            time.sleep(args.poll_seconds)
        raise RuntimeError(f"{name} did not converge within {args.stage_seconds}s; last={last}")

    try:
        converge("cold_start")
        for _ in range(args.steady_scrapes):
            converge("steady_scrape")
        churn_count = max(1, min(args.churn_roots, args.roots))
        if args.pods:
            fixture.change_pod(0, args.pods - 1)
            converge("single_pod_label_update")
            report["stages"][-1]["limitation"] = "Convergence includes polling and full HTTP/oracle time; not per-event publication latency"
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
        if args.settle_seconds:
            time.sleep(args.settle_seconds)
            converge("settled_after_cleanup")
        report["completed_all_stages"] = True
        gaps = [scrape for stage in report["stages"] if stage["name"] == "steady_scrape" for scrape in stage["scrapes"] if not scrape.get("complete")]
        report["steady_validity_gaps"] = len(gaps)
        report["complete"] = not gaps
        if gaps:
            report["failure"] = "steady output lost validity without fixture changes"
    except Exception as exc:
        report["completed_all_stages"] = False
        report["complete"] = False
        report["failure"] = str(exc)
    finally:
        stop.set()
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        report["returncode"] = process.returncode
        completed = [scrape for stage in report["stages"] for scrape in stage["scrapes"] if scrape.get("complete")]
        report["measured_gates"] = {"http_scrapes_under_25s": bool(completed) and all(scrape["http_seconds"] <= 25 for scrape in completed), "oracle_under_deadline": bool(completed) and all(scrape["oracle_seconds"] <= args.oracle_seconds for scrape in completed)}
        report["elapsed_seconds"] = round(time.monotonic() - started, 4)
        report["peak_sampled_rss_bytes"] = max((sample["rss_bytes"] for sample in rss_samples), default=0)
        report["process_samples"] = rss_samples
        report["cpu_seconds_last_sample"] = next((sample["cpu_seconds"] for sample in reversed(rss_samples) if sample["cpu_seconds"] is not None), None)
        report["limit_triggered"] = limit
        report["api"] = fixture.snapshot()
        report["harness_rss_bytes"] = process_rss(os.getpid())
        log.close()
        fixture.close()
        (output / "result.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps({key: report[key] for key in ("complete", "elapsed_seconds", "peak_sampled_rss_bytes", "limit_triggered")}), flush=True)
    return 0 if report["complete"] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
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
    if args.roots < 1 or args.pods < 0 or args.roots * args.pods > 1000000 or args.roots > 100000 or not 1 <= args.page_size <= 5000 or args.max_rss_mib < 256 or args.gomaxprocs < 1 or min(args.max_seconds, args.stage_seconds, args.scrape_seconds, args.oracle_seconds, args.poll_seconds) <= 0 or args.steady_scrapes < 0 or args.churn_roots < 1 or not 1 <= args.delete_percent <= 100 or args.settle_seconds < 0:
        parser.error("invalid scenario or safety bound; maximum 100k roots and 1m Pods")
    raise SystemExit(run(args))


if __name__ == "__main__":
    main()
