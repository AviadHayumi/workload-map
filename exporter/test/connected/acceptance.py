#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Run the compiled exporter against a disposable, explicitly selected cluster."""

import argparse
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

POD = "karta_pod_workload_info"
STATUS = "karta_workload_status"
CREATED = "karta_workload_created_timestamp_seconds"
HEALTH = "karta_exporter_observation_valid"
PHASES = {"Initializing", "Running", "Completed", "Failed", "Degraded",
          "Suspended", "Suspending", "Resuming", "Undefined"}
SCHEMAS = {
    POD: set("namespace workload_name workload_group workload_version workload_kind "
             "workload_uid owner_resolution pod_name pod_uid component component_instance "
             "replica_group_id observation_valid submitted_by".split()),
    STATUS: set("namespace workload_name workload_group workload_version workload_kind "
                "workload_uid phase owner_resolution karta karta_source observation_valid "
                "submitted_by".split()),
    CREATED: {"namespace", "workload_name", "workload_uid"},
    HEALTH: set("scope namespace resource_group resource_version resource_kind workload_name "
                "workload_uid reason".split()),
}
SAMPLE = re.compile(
    r'(?P<name>[a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(?P<labels>(?:[^"{}]|"(?:\\.|[^"\\])*")*)\})?'
    r'\s+(?P<value>[^\s]+)(?:\s+[0-9]+)?$')
LABEL = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)=("(?:\\.|[^"\\])*")')
RUN_LABEL = "example.com/karta-connected-run"
HIDDEN_LABEL = "example.com/karta-connected-hidden"
SUBMITTER = "example.com/karta-connected-submitter"
JOBSET_KARTA = "jobset-x-k8s-io-jobset-v1alpha2"
LWS_KARTA = "leaderworkerset-x-k8s-io-leaderworkerset-v1"
STATEFULSET_KARTA = "apps-statefulset-v1"


def parse_metrics(text):
    """Parse only Karta families, rejecting duplicate or malformed samples."""
    rows, seen, types = [], set(), {}
    for line in text.splitlines():
        if line.startswith("# TYPE karta_"):
            _, _, name, kind = line.split()
            types[name] = kind
        if not line.startswith("karta_"):
            continue
        match = SAMPLE.fullmatch(line)
        if not match:
            raise AssertionError(f"Malformed Karta sample: {line}")
        name, raw = match["name"], match["labels"] or ""
        labels, offset = {}, 0
        while offset < len(raw):
            label = LABEL.match(raw, offset)
            if label is None or label[1] in labels:
                raise AssertionError(f"Malformed or duplicate label: {line}")
            labels[label[1]] = json.loads(label[2])
            offset = label.end()
            if offset < len(raw):
                if raw[offset] != "," or offset + 1 == len(raw):
                    raise AssertionError(f"Malformed label separator: {line}")
                offset += 1
        key = (name, tuple(sorted(labels.items())))
        if key in seen:
            raise AssertionError(f"Duplicate sample: {key}")
        seen.add(key)
        value = float(match["value"])
        if not math.isfinite(value):
            raise AssertionError(f"Nonfinite Karta value: {line}")
        rows.append((name, labels, value))
    for name, labels, value in rows:
        if name not in SCHEMAS or set(labels) != SCHEMAS[name]:
            raise AssertionError(f"Wrong fixed-KEP family/labels: {name} {sorted(labels)}")
        if types.get(name) != "gauge":
            raise AssertionError(f"Missing gauge type for {name}")
        if name == POD and value != 1:
            raise AssertionError("Pod identity must equal 1")
        if name in (STATUS, HEALTH) and value not in (0, 1):
            raise AssertionError(f"Nonbinary {name}")
        if name in (POD, STATUS) and labels["observation_valid"] not in ("true", "false"):
            raise AssertionError("Invalid observation_valid")
        if name == HEALTH and labels["scope"] == "workload":
            if value != 0 or labels["reason"] == "none":
                raise AssertionError("Healthy workload health row is forbidden")
    return rows


def selected(rows, family, **labels):
    return [(attrs, value) for name, attrs, value in rows
            if name == family and all(attrs.get(k) == v for k, v in labels.items())]


def owner_paths(objects):
    """Resolve the API objects' controller UID paths independently of metrics."""
    by_uid = {obj["metadata"]["uid"]: obj for obj in objects}
    paths = {}
    for pod in objects:
        if pod["kind"] != "Pod":
            continue
        path, seen, current = [], set(), pod
        while True:
            uid = current["metadata"]["uid"]
            if uid in seen:
                raise AssertionError("Cycle in live fixture owner references")
            seen.add(uid)
            path.append(current)
            owners = [owner for owner in current["metadata"].get("ownerReferences", [])
                      if owner.get("controller")]
            if not owners:
                break
            if len(owners) != 1 or owners[0]["uid"] not in by_uid:
                raise AssertionError("Incomplete or ambiguous live fixture owner path")
            owner = owners[0]
            current = by_uid[owner["uid"]]
            if (current["kind"], current["metadata"]["name"]) != (owner["kind"], owner["name"]):
                raise AssertionError("Owner UID/name/kind mismatch")
        paths[pod["metadata"]["uid"]] = path
    return paths


def check_root(rows, root, paths, mapping, phase, karta, source, submitter="", hidden=()):
    meta = root["metadata"]
    uid = meta["uid"]
    expected_pods = {pod_uid: path for pod_uid, path in paths.items()
                     if path[-1]["metadata"]["uid"] == uid and pod_uid not in hidden}
    actual = selected(rows, POD, workload_uid=uid)
    if len(actual) != len(expected_pods) or {a["pod_uid"] for a, _ in actual} != set(expected_pods):
        raise AssertionError(f"Wrong Pod UID membership for {root['kind']}: "
                             f"expected {sorted(expected_pods)}, got {[a['pod_uid'] for a, _ in actual]}")
    api = root["apiVersion"].split("/", 1)
    group, version = (api[0], api[1]) if len(api) == 2 else ("", api[0])
    for attrs, _ in actual:
        pod = expected_pods[attrs["pod_uid"]][0]
        expected = {"namespace": meta["namespace"], "workload_name": meta["name"],
                    "workload_uid": uid, "workload_group": group, "workload_version": version,
                    "workload_kind": root["kind"], "pod_name": pod["metadata"]["name"],
                    "owner_resolution": "resolved", "observation_valid": "true",
                    "submitted_by": submitter}
        expected.update(mapping(pod))
        if any(attrs[k] != v for k, v in expected.items()):
            raise AssertionError(f"Wrong mapping: expected {expected}, got {attrs}")
    timestamps = selected(rows, CREATED, workload_uid=uid)
    wanted_time = datetime.datetime.fromisoformat(meta["creationTimestamp"].replace("Z", "+00:00")).timestamp()
    if len(timestamps) != 1 or timestamps[0] != (
            {"namespace": meta["namespace"], "workload_name": meta["name"], "workload_uid": uid}, wanted_time):
        raise AssertionError(f"Wrong creation timestamp for UID {uid}: {timestamps}")
    statuses = selected(rows, STATUS, workload_uid=uid)
    if phase is None:
        if statuses:
            raise AssertionError("Generic root unexpectedly has status")
    else:
        if len(statuses) != len(PHASES) or {a["phase"] for a, _ in statuses} != PHASES:
            raise AssertionError(f"Wrong dense phases for {uid}: {statuses}")
        for attrs, value in statuses:
            expected = {"namespace": meta["namespace"], "workload_name": meta["name"],
                        "workload_group": group, "workload_version": version,
                        "workload_kind": root["kind"], "owner_resolution": "resolved",
                        "observation_valid": "true", "karta": karta, "karta_source": source,
                        "submitted_by": submitter}
            if any(attrs[k] != v for k, v in expected.items()) or value != int(attrs["phase"] == phase):
                raise AssertionError(f"Wrong status/provenance for {uid}: {attrs}={value}")
    collection = selected(rows, HEALTH, scope="resource_type", namespace=meta["namespace"],
                          resource_group=group, resource_version=version, resource_kind=root["kind"])
    if len(collection) != 1 or collection[0][1] != 1 or collection[0][0]["reason"] != "none":
        raise AssertionError(f"Missing healthy resource coverage for {root['kind']}: {collection}")
    if collection[0][0]["workload_name"] or collection[0][0]["workload_uid"]:
        raise AssertionError("Resource coverage unexpectedly names a workload")
    if selected(rows, HEALTH, workload_uid=uid):
        raise AssertionError(f"Unexpected workload failure for verified UID {uid}")


def generic_mapping(_):
    return {"component": "<unknown>", "component_instance": "<unknown>", "replica_group_id": ""}


def jobset_mapping(pod):
    labels = pod["metadata"]["labels"]
    return {"component": "replicatedjob", "component_instance": labels["jobset.sigs.k8s.io/replicatedjob-name"],
            "replica_group_id": labels["jobset.sigs.k8s.io/job-index"]}


def lws_mapping(pod):
    labels = pod["metadata"]["labels"]
    if labels["leaderworkerset.sigs.k8s.io/worker-index"] == "0":
        component = "leader"
    elif pod["metadata"].get("annotations", {}).get("leaderworkerset.sigs.k8s.io/leader-name"):
        component = "worker"
    else:
        raise AssertionError("Live LWS worker lacks its defining leader-name annotation")
    return {"component": component, "component_instance": "",
            "replica_group_id": labels["leaderworkerset.sigs.k8s.io/group-index"]}


class Run:
    def __init__(self, args):
        self.args = args
        self.output = args.output.resolve()
        self.output.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.run_id = uuid.uuid4().hex[:12]
        self.namespace = "karta-connected-" + self.run_id
        self.karta_name = "connected-lws-" + self.run_id
        self.prefix = [args.kubectl, "--kubeconfig", str(args.kubeconfig.resolve()),
                       "--context", args.context, "--request-timeout=30s"]
        self.process = None
        self.process_log = None
        self.process_count = 0
        self.created_namespace = False
        self.created_karta = False
        self.results = []
        self.env = os.environ.copy()
        self.env["KUBECONFIG"] = str(args.kubeconfig.resolve())
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self.config = {
            "kartaDefinitionSource": {
                "catalog": {"enabled": True, "include": [JOBSET_KARTA, LWS_KARTA, STATEFULSET_KARTA],
                            "exclude": ["core-pod-v1"]},
                "cluster": {"enabled": True, "include": [], "exclude": [],
                            "labelSelector": {"matchLabels": {RUN_LABEL: self.run_id}}}},
            "namespaceSelector": {"matchLabels": {RUN_LABEL: self.run_id}},
            "objectSelector": {},
            "podSelector": {"matchExpressions": [{"key": HIDDEN_LABEL, "operator": "NotIn", "values": ["true"]}]},
            "resourceRules": [], "excludeResourceRules": [], "ignoredOwnerKinds": [],
            "requiredKinds": ["jobset.x-k8s.io/v1alpha2/JobSet", "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet"],
        }
        self.fixtures = {}

    def kubectl(self, *args, obj=None, private=False):
        command = self.prefix + list(args)
        result = subprocess.run(command, input=None if obj is None else json.dumps(obj),
                                capture_output=True, text=True, env=self.env, timeout=45)
        with (self.output / "commands.jsonl").open("a") as log:
            log.write(json.dumps({"command": command, "exit_code": result.returncode,
                                  "stdout": "<private kubeconfig>" if private else result.stdout,
                                  "stderr": "<private>" if private else result.stderr}) + "\n")
        if result.returncode:
            raise RuntimeError(f"kubectl {args[0]} failed: " + ("private configuration read" if private else result.stderr))
        return result.stdout

    def prepare(self):
        # Flatten only the explicit supplied context; never mutate its source file.
        private = self.kubectl("config", "view", "--minify", "--flatten", "--raw", private=True)
        path = self.output / "kubeconfig"
        with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as stream:
            stream.write(private)
        for resource in ("jobsets.jobset.x-k8s.io", "leaderworkersets.leaderworkerset.x-k8s.io", "kartas.run.ai"):
            crd = json.loads(self.kubectl("get", "crd", resource, "-o", "json"))
            if not any(c["type"] == "Established" and c["status"] == "True" for c in crd["status"]["conditions"]):
                raise RuntimeError(f"Required CRD is not established: {resource}")
        self.kubectl("create", "-f", "-", obj={"apiVersion": "v1", "kind": "Namespace",
                     "metadata": {"name": self.namespace, "labels": {RUN_LABEL: self.run_id}}})
        self.created_namespace = True
        for name in ("jobset", "lws"):
            raw = (Path(__file__).parent / "fixtures" / (name + ".yaml")).read_text()
            obj = json.loads("\n".join(line for line in raw.splitlines() if not line.startswith("#")))
            obj["metadata"].update(namespace=self.namespace, labels={RUN_LABEL: self.run_id})
            self.fixtures[name] = obj
            self.kubectl("create", "-f", "-", obj=obj)
        self.await_objects()

    def objects(self):
        return json.loads(self.kubectl("-n", self.namespace, "get",
                          "jobsets,jobs,pods,statefulsets,leaderworkersets", "-o", "json"))["items"]

    def await_objects(self):
        deadline, last = time.monotonic() + self.args.timeout, ""
        while time.monotonic() < deadline:
            items = self.objects()
            try:
                roots = {obj["kind"]: obj for obj in items if obj["kind"] in ("JobSet", "LeaderWorkerSet")}
                paths = owner_paths(items)
                for kind, condition, count in (("JobSet", "Completed", 3), ("LeaderWorkerSet", "Available", 6)):
                    root = roots[kind]
                    if not any(c["type"] == condition and c["status"] == "True"
                               for c in root.get("status", {}).get("conditions", [])):
                        raise AssertionError(f"{kind} not {condition}")
                    if sum(p[-1]["metadata"]["uid"] == root["metadata"]["uid"] for p in paths.values()) != count:
                        raise AssertionError(f"{kind} expected {count} live Pods")
                self.roots, self.paths = roots, paths
                (self.output / "live-objects.json").write_text(json.dumps(items, indent=2) + "\n")
                return
            except (AssertionError, KeyError) as error:
                last = str(error)
            time.sleep(1)
        raise AssertionError("Fixture readiness timed out: " + last)

    def start(self):
        self.stop()
        self.process_count += 1
        config = self.output / f"config-{self.process_count}.json"
        config.write_text(json.dumps(self.config, indent=2) + "\n")
        identity = self.output / "identity.json"
        identity.write_text(json.dumps({"submittedByExpression":
                            f'"{SUBMITTER}" in annotations ? annotations["{SUBMITTER}"] : ""'}) + "\n")
        command = [str(self.args.binary.resolve()), "--kubeconfig=" + str(self.output / "kubeconfig"),
                   "--config=" + str(config), "--identity-config=" + str(identity),
                   f"--metrics-bind-address=127.0.0.1:{self.port}", "--metrics-exporter=prometheus", "--workload-phases=all"]
        (self.output / f"exporter-command-{self.process_count}.json").write_text(json.dumps(command, indent=2) + "\n")
        self.process_log = (self.output / f"exporter-{self.process_count}.log").open("w")
        self.process = subprocess.Popen(command, stdout=self.process_log, stderr=subprocess.STDOUT, env=self.env)

    def stop(self):
        if self.process is not None:
            self.process.terminate()
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
            self.process = None
        if self.process_log is not None:
            self.process_log.close()
            self.process_log = None

    def await_metrics(self, label, assertion):
        started, last, successes = time.monotonic(), "", 0
        while time.monotonic() - started < self.args.timeout:
            if self.process.poll() is not None:
                raise RuntimeError(f"Exporter exited {self.process.returncode}; inspect exporter log")
            try:
                with urllib.request.urlopen(self.url + "/readyz", timeout=5) as response:
                    if response.status != 200:
                        raise AssertionError("Exporter not ready")
                with urllib.request.urlopen(self.url + "/metrics", timeout=10) as response:
                    text = response.read().decode()
                rows = parse_metrics(text)
                assertion(rows)
                successes += 1
                if successes >= 3:
                    (self.output / (label + ".prom")).write_text(text)
                    self.results.append({"scenario": label, "passed": True,
                                         "seconds": round(time.monotonic() - started, 3)})
                    print(f"PASS {label}", flush=True)
                    return rows
            except (AssertionError, urllib.error.URLError, TimeoutError, OSError) as error:
                successes, last = 0, str(error)
            time.sleep(1)
        raise AssertionError(f"{label} did not converge: {last}")

    def check(self, rows, lws_mode="catalog", phase="Running", submitter="", hidden=()):
        check_root(rows, self.roots["JobSet"], self.paths, jobset_mapping, "Completed", JOBSET_KARTA, "catalog")
        if lws_mode == "catalog":
            mapping, name, source = lws_mapping, LWS_KARTA, "catalog"
        elif lws_mode == "generic":
            mapping, phase, name, source = generic_mapping, None, "", ""
        else:
            def mapping(pod):
                return {"component": lws_mode, "component_instance": "",
                        "replica_group_id": pod["metadata"]["labels"]["leaderworkerset.sigs.k8s.io/group-index"]}
            name, source = self.karta_name, "cluster"
        check_root(rows, self.roots["LeaderWorkerSet"], self.paths, mapping, phase, name, source, submitter, hidden)
        expected_roots = {root["metadata"]["uid"] for root in self.roots.values()}
        for family, attrs, _ in rows:
            if attrs["namespace"] == self.namespace and family != HEALTH and attrs["workload_uid"] not in expected_roots:
                raise AssertionError("Intermediate owner promoted to workload")

    def definition(self, component, phase):
        return {"apiVersion": "run.ai/v1alpha1", "kind": "Karta",
                "metadata": {"name": self.karta_name, "labels": {RUN_LABEL: self.run_id}},
                "spec": {"structureDefinition": {"rootComponent": {
                    "name": component, "kind": {"group": "leaderworkerset.x-k8s.io", "version": "v1", "kind": "LeaderWorkerSet"},
                    "specDefinition": {"podTemplateSpecPath": ".spec.leaderWorkerTemplate.workerTemplate"},
                    "podSelector": {"replicaSelector": {"keyPath": '.metadata.labels["leaderworkerset.sigs.k8s.io/group-index"]'}},
                    "statusDefinition": {"statusMappings": {phase.lower(): [{"byExpression": {"expression": "true", "expectedResult": "true"}}]}}
                }}}}

    def execute(self):
        self.start()
        self.await_metrics("catalog-jobset-lws", self.check)
        self.config["kartaDefinitionSource"]["catalog"]["include"].remove(LWS_KARTA)
        self.start()
        self.await_metrics("generic-lws-despite-statefulset-karta", lambda rows: self.check(rows, "generic"))
        definition = self.definition("connected-a", "Running")
        self.kubectl("create", "-f", "-", obj=definition)
        self.created_karta = True
        before = json.loads(self.kubectl("get", "karta", self.karta_name, "-o", "json"))
        self.await_metrics("definition-add", lambda rows: self.check(rows, "connected-a"))
        updated = self.definition("connected-b", "Degraded")
        self.kubectl("patch", "karta", self.karta_name, "--type=json", "-p",
                     json.dumps([{"op": "replace", "path": "/spec", "value": updated["spec"]}]))
        after = json.loads(self.kubectl("get", "karta", self.karta_name, "-o", "json"))
        if before["metadata"]["uid"] != after["metadata"]["uid"] or before["metadata"]["resourceVersion"] == after["metadata"]["resourceVersion"]:
            raise AssertionError("Definition update did not preserve UID and change resourceVersion")
        self.await_metrics("definition-change", lambda rows: self.check(rows, "connected-b", "Degraded"))
        self.kubectl("delete", "karta", self.karta_name, "--wait=false")
        self.await_metrics("definition-delete-generic", lambda rows: self.check(rows, "generic"))
        root = self.roots["LeaderWorkerSet"]
        self.kubectl("-n", self.namespace, "annotate", "leaderworkerset", root["metadata"]["name"], SUBMITTER + "=alice", "--overwrite")
        self.await_metrics("root-submitter-agreement", lambda rows: self.check(rows, "generic", submitter="alice"))
        pod_uid, path = next((uid, path) for uid, path in self.paths.items()
                             if path[-1]["metadata"]["uid"] == root["metadata"]["uid"] and
                             path[0]["metadata"]["labels"]["leaderworkerset.sigs.k8s.io/worker-index"] != "0")
        pod_name = path[0]["metadata"]["name"]
        self.kubectl("-n", self.namespace, "patch", "pod", pod_name, "--type=merge", "-p",
                     json.dumps({"metadata": {"labels": {HIDDEN_LABEL: "true"}, "annotations": {SUBMITTER: "bob"}}}))
        self.await_metrics("hidden-pod-conflict", lambda rows: self.check(rows, "generic", hidden={pod_uid}))
        self.kubectl("-n", self.namespace, "patch", "pod", pod_name, "--type=merge", "-p",
                     json.dumps({"metadata": {"annotations": {SUBMITTER: None}}}))
        self.await_metrics("hidden-pod-agreement-recovery", lambda rows: self.check(rows, "generic", submitter="alice", hidden={pod_uid}))
        self.kubectl("-n", self.namespace, "label", "pod", pod_name, HIDDEN_LABEL + "-")
        self.await_metrics("pod-row-restored", lambda rows: self.check(rows, "generic", submitter="alice"))
        self.kubectl("label", "namespace", self.namespace, RUN_LABEL + "-")
        def outside_scope(rows):
            if any(attrs["namespace"] == self.namespace for _, attrs, _ in rows):
                raise AssertionError("Rows survived namespace scope exit")
        self.await_metrics("namespace-exit", outside_scope)
        self.kubectl("label", "namespace", self.namespace, RUN_LABEL + "=" + self.run_id)
        self.await_metrics("namespace-recovery", lambda rows: self.check(rows, "generic", submitter="alice"))
        old_root = self.roots["JobSet"]["metadata"]["uid"]
        old_pods = {uid for uid, path in self.paths.items() if path[-1]["metadata"]["uid"] == old_root}
        self.kubectl("-n", self.namespace, "delete", "jobset", self.roots["JobSet"]["metadata"]["name"], "--cascade=foreground", "--wait=false")
        deadline = time.monotonic() + self.args.timeout
        while any(obj["metadata"]["uid"] in old_pods | {old_root} for obj in self.objects()):
            if time.monotonic() >= deadline:
                raise AssertionError("Old JobSet lifetime did not delete")
            time.sleep(1)
        self.kubectl("create", "-f", "-", obj=self.fixtures["jobset"])
        self.await_objects()
        if self.roots["JobSet"]["metadata"]["uid"] == old_root:
            raise AssertionError("Recreated JobSet did not change UID")
        def recreated(rows):
            self.check(rows, "generic", submitter="alice")
            if any(attrs.get("workload_uid") == old_root or attrs.get("pod_uid") in old_pods for _, attrs, _ in rows):
                raise AssertionError("Deleted lifetime survived recreation")
        self.await_metrics("same-name-new-lifetime", recreated)

    def cleanup(self):
        self.stop()
        errors = []
        if not self.args.keep_resources:
            for created, args in ((self.created_karta, ("delete", "karta", self.karta_name, "--ignore-not-found", "--wait=false")),
                                  (self.created_namespace, ("delete", "namespace", self.namespace, "--ignore-not-found", "--wait=false"))):
                if created:
                    try:
                        self.kubectl(*args)
                    except Exception as error:
                        errors.append(str(error))
        (self.output / "kubeconfig").unlink(missing_ok=True)
        return errors


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--context", required=True)
    parser.add_argument("--binary", type=Path, required=True, help="Compiled assembled exporter cmd")
    parser.add_argument("--output", type=Path, required=True, help="New private evidence directory")
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--timeout", type=float, default=180, help="Per-scenario convergence timeout in seconds")
    parser.add_argument("--keep-resources", action="store_true", help="Keep only this run's namespace/definition for diagnosis")
    args = parser.parse_args()
    if args.timeout <= 0 or not args.binary.is_file() or not args.kubeconfig.is_file():
        parser.error("positive timeout, existing binary and explicit kubeconfig are required")
    run = Run(args)
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "namespace": run.namespace, "context": args.context,
              "binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(),
              "passed": False, "scenarios": run.results}
    try:
        report["server_version"] = json.loads(run.kubectl("version", "-o", "json")).get("serverVersion")
        run.prepare()
        run.execute()
        report["passed"] = True
    except Exception as error:
        report["error"] = str(error)
        print("FAIL " + str(error), file=sys.stderr)
    finally:
        report["cleanup_errors"] = run.cleanup()
        report["kept_resources"] = args.keep_resources
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (run.output / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    return 0 if report["passed"] and not report["cleanup_errors"] else 1


if __name__ == "__main__":
    sys.exit(main())
