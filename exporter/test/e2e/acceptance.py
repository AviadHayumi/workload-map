#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Exercise real operator fixtures against the final exporter contract."""

import argparse
import collections
import json
import math
import re
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent
PHASES = {"Initializing", "Running", "Completed", "Failed", "Degraded",
          "Suspended", "Suspending", "Resuming", "Undefined"}
INFO = "karta_pod_workload_info"
STATUS = "karta_workload_status"
HEALTH = "karta_exporter_observation_healthy"
IDENTITY = {"namespace", "workload", "workload_group", "workload_version",
            "workload_kind", "workload_uid", "owner_resolution"}
LABELS = {
    INFO: IDENTITY | {"pod", "pod_uid", "component", "component_instance", "replica_group_id"},
    STATUS: IDENTITY | {"phase", "karta", "karta_source"},
    HEALTH: {"scope", "namespace", "group", "version", "kind", "workload", "workload_uid", "reason"},
}
REASONS = {"none", "permission_denied", "api_unavailable", "invalid_definition",
           "evaluation_failed", "unresolved_owner"}
BASE_RESOURCES = {"pods", "deployments.apps", "replicasets.apps", "statefulsets.apps",
                  "jobs.batch", "cronjobs.batch"}
RELATED = {
    "dynamo": {"dynamocomponentdeployments.nvidia.com", "podcliquesets.grove.io",
               "podcliques.grove.io", "podcliquescalinggroups.grove.io",
               "leaderworkersets.leaderworkerset.x-k8s.io"},
    "grove": {"podcliques.grove.io", "podcliquescalinggroups.grove.io"},
    "kserve": {"services.serving.knative.dev", "configurations.serving.knative.dev", "revisions.serving.knative.dev"},
    "knative": {"configurations.serving.knative.dev", "revisions.serving.knative.dev"},
    "rayjob": {"rayclusters.ray.io"},
    "rayservice": {"rayclusters.ray.io"},
}
LABEL_PATTERN = re.compile(r'([a-zA-Z_][a-zA-Z0-9_]*)="((?:\\.|[^"\\])*)"')


class CheckFailed(Exception):
    pass


def require(condition, message):
    if not condition:
        raise CheckFailed(message)


def fixture(case):
    text = (ROOT / "fixtures" / (case + ".yaml")).read_text()
    return json.loads("\n".join(line for line in text.splitlines() if not line.startswith("#")))


def group_kind(obj):
    return (obj["apiVersion"].split("/")[0] if "/" in obj["apiVersion"] else "", obj["kind"])


def resource(obj):
    group, kind = group_kind(obj)
    version = obj["apiVersion"].split("/")[-1]
    return kind + "." + version + "." + group if group else kind


def parse_metrics(raw):
    rows = []
    seen = set()
    types = {}
    for line in raw.splitlines():
        if line.startswith("# TYPE karta_"):
            _, _, name, kind = line.split()
            require(name in LABELS, "unexpected Karta family " + name)
            require(kind == "gauge", name + " must be a gauge")
            types[name] = kind
        if not line.startswith("karta_"):
            continue
        match = re.fullmatch(r'(\w+)\{(.*)\} ([^ ]+)(?: .*)?', line)
        require(match is not None, "malformed Karta sample")
        name, text, value = match.groups()
        require(name in LABELS, "unexpected Karta family " + name)
        pairs = list(LABEL_PATTERN.finditer(text))
        require(",".join(p.group(0) for p in pairs) == text, "malformed sample labels")
        labels = {p[1]: json.loads('"' + p[2] + '"') for p in pairs}
        require(len(pairs) == len(labels), "duplicate label key")
        require(set(labels) == LABELS[name], name + " has incorrect labels: " + str(sorted(labels)))
        value = float(value)
        require(math.isfinite(value) and value in (0, 1), name + " has non-boolean value")
        if name == INFO:
            require(value == 1, "attribution value must be 1")
        if name in (INFO, STATUS):
            require(labels["owner_resolution"] in ("resolved", "ignored_owner"), "invalid owner resolution")
            require(labels["workload_uid"] != "", "missing workload UID")
        if name == STATUS:
            require(labels["phase"] in PHASES, "invalid phase")
            require(labels["karta"] and labels["karta_source"] in ("catalog", "cluster"), "missing definition provenance")
        if name == HEALTH:
            require(labels["scope"] in ("workload", "resource_type"), "invalid health scope")
            require(labels["reason"] in REASONS, "unbounded health reason")
            require((value == 1) == (labels["reason"] == "none"), "health value and reason disagree")
            if labels["scope"] == "resource_type":
                require(not labels["workload"] and not labels["workload_uid"], "API health contains workload identity")
            else:
                require(labels["workload"] and labels["workload_uid"], "workload health lacks identity")
        key = (name, tuple(sorted(labels.items())))
        require(key not in seen, "duplicate metric row")
        seen.add(key)
        rows.append({"metric": name, "labels": labels, "value": value})
    require(rows, "exporter exposes no Karta observations")
    require(all(row["metric"] in types for row in rows), "missing gauge type declaration")
    return rows


def selected_root(obj, objects, described):
    candidate = None
    seen = set()
    for _ in range(9):
        uid = obj["metadata"]["uid"]
        require(uid not in seen, "owner cycle")
        seen.add(uid)
        if group_kind(obj) in described:
            candidate = obj
        refs = [r for r in obj["metadata"].get("ownerReferences", []) if r.get("controller")]
        require(len(refs) <= 1, "multiple controller owners")
        if not refs:
            return candidate
        owner = objects.get(refs[0]["uid"])
        require(owner is not None, "unresolved owner of " + obj["metadata"]["name"])
        require(owner["metadata"]["name"] == refs[0]["name"], "owner name/UID mismatch")
        if refs[0].get("kind"):
            require(owner["kind"] == refs[0]["kind"], "owner kind/UID mismatch")
        obj = owner
    raise CheckFailed("owner walk exceeds eight edges")


def inspect(rows, objects, root, expected, described, namespace):
    """Check the entire namespace, including child status the old audit missed."""
    objects = {obj["metadata"]["uid"]: obj for obj in objects}
    root_uid = root["metadata"]["uid"]
    roots = {}
    owned_pods = set()
    for uid, obj in objects.items():
        if obj["metadata"].get("deletionTimestamp"):
            continue
        chosen = selected_root(obj, objects, described)
        if chosen is not None:
            roots[chosen["metadata"]["uid"]] = chosen
            if obj["kind"] == "Pod" and chosen["metadata"]["uid"] == root_uid:
                owned_pods.add(uid)
                status = obj.get("status", {})
                require(status.get("phase") in ("Succeeded", "Failed") or any(c.get("type") == "Ready" and c.get("status") == "True" for c in status.get("conditions", [])), "owned Pod is not ready or terminal")
    mappings, phases, health = {}, collections.defaultdict(dict), {}
    for row in rows:
        label = row["labels"]
        if label["namespace"] != namespace:
            continue
        uid = label["workload_uid"]
        if row["metric"] == HEALTH and label["scope"] == "resource_type":
            continue
        require(uid in roots, "metric names an unconfirmed or child root: " + label["workload"])
        if row["metric"] in (INFO, STATUS):
            require(label["owner_resolution"] == "resolved", "unexpected ignored-owner boundary in operator matrix")
        chosen = roots[uid]
        group, kind = group_kind(chosen)
        version = described[(group, kind)]
        prefix = "" if row["metric"] == HEALTH else "workload_"
        require(label[prefix + "group"] == group and label[prefix + "version"] == version and label[prefix + "kind"] == kind,
                "root GVK differs from selected definition")
        require(label["workload"] == chosen["metadata"]["name"], "root name differs from Kubernetes")
        if row["metric"] == INFO:
            pod_uid = label["pod_uid"]
            require(pod_uid not in mappings, "multiple attributions for one Pod UID")
            require(pod_uid in objects and objects[pod_uid]["kind"] == "Pod", "unexpected Pod UID")
            pod = objects[pod_uid]
            require(label["pod"] == pod["metadata"]["name"], "Pod name differs from Kubernetes")
            selected = selected_root(pod, objects, described)
            require(selected is not None and selected["metadata"]["uid"] == uid, "incorrect highest described root")
            require(label["component"] != "<unknown>" and label["component_instance"] != "<unknown>", "unknown component identity")
            mappings[pod_uid] = label
        elif row["metric"] == STATUS:
            require(label["phase"] not in phases[uid], "multiple definitions/versions for root status")
            phases[uid][label["phase"]] = row["value"]
        else:
            require(uid not in health, "old health reason row survived replacement")
            require(row["value"] == 1 and label["reason"] == "none", "unhealthy root " + label["workload"] + ": " + label["reason"])
            health[uid] = label
    for uid in roots:
        require(set(phases[uid]) == PHASES, "root lacks nine dense phase rows: " + roots[uid]["metadata"]["name"])
        require(uid in health, "root lacks fresh observation health")
        require(any(phases[uid].values()), "root has no matched phase, including Undefined")
        if phases[uid]["Undefined"]:
            require(sum(phases[uid].values()) == 1, "Undefined coexists with a matching phase")
    if root["kind"] == "Pod":
        require(not mappings and not roots, "bare Pod was attributed without opt-in")
        return {"ownedPods": 1, "mappedPods": 0, "roots": 0}
    require(len(owned_pods) == expected["pods"], "owned Pod count differs from fixture expectation")
    selected = {uid: label for uid, label in mappings.items() if label["workload_uid"] == root_uid}
    require(set(selected) == owned_pods and len(selected) == expected["mapped"], "missing or unexpected attributed Pod UID")
    groups = collections.Counter((label["component"], label["component_instance"], label["replica_group_id"]) for label in selected.values())
    want = {(g["component"], g["component_instance"], g["replica_group_id"]): g["pods"] for g in expected["groups"]}
    require(dict(groups) == want, "component/instance/group identities differ: " + str(dict(groups)))
    active = {phase for phase, value in phases[root_uid].items() if value == 1}
    require(set(expected["phases"]) <= active <= set(expected["phases"] + expected.get("optionalPhases", [])), "unexpected active root phases: " + str(sorted(active)))
    return {"ownedPods": len(owned_pods), "mappedPods": len(selected), "roots": len(roots), "activePhases": sorted(active)}


class Kubernetes:
    def __init__(self, args):
        self.args = args
        self.prefix = ["kubectl", "--kubeconfig", str(args.kubeconfig), "--context", args.context, "--request-timeout=45s"]

    def run(self, *args, obj=None, allow_failure=False):
        result = subprocess.run(self.prefix + list(args), input=json.dumps(obj) if obj is not None else None,
                                text=True, capture_output=True, timeout=90)
        if result.returncode and not allow_failure:
            error = re.sub(r'https?://\S+', '<API-address>', result.stderr.strip())
            raise CheckFailed(error[-2000:])
        return result.stdout if not result.returncode else None

    def get(self, *args):
        return json.loads(self.run("get", *args, "-o", "json"))

    def apply(self, obj):
        return self.run("apply", "-f", "-", obj=obj)

    def create(self, obj):
        return self.run("create", "-f", "-", obj=obj)

    def snapshots(self, service=None, namespace=None):
        namespace = namespace or self.args.exporter_namespace
        service = self.get("service", service or self.args.exporter_service, "-n", namespace)
        selector = ",".join(k + "=" + v for k, v in sorted(service["spec"]["selector"].items()))
        pods = self.get("pods", "-n", namespace, "-l", selector)["items"]
        pods = [p for p in pods if p["status"].get("phase") == "Running" and not p["metadata"].get("deletionTimestamp")]
        require(pods, "exporter has no running endpoints")
        snapshots = {}
        for pod in pods:
            name = pod["metadata"]["name"]
            raw = self.run("get", "--raw", "/api/v1/namespaces/" + namespace + "/pods/" + name + ":" + str(self.args.exporter_port) + "/proxy/metrics")
            snapshots[name] = (raw, parse_metrics(raw))
        return snapshots


def wait_for(args, name, check):
    deadline = time.monotonic() + args.timeout
    stable, last = 0, "not observed"
    while time.monotonic() < deadline:
        try:
            result = check()
            stable += 1
            if stable >= 2:
                return result
        except (CheckFailed, subprocess.TimeoutExpired, KeyError, ValueError) as error:
            stable, last = 0, str(error)
        time.sleep(args.poll)
    raise CheckFailed(name + " timed out: " + last)


def owned_namespace(kube, namespace):
    require(kube.run("get", "namespace", namespace, allow_failure=True) is None, "acceptance namespace already exists; choose an unused namespace")
    kube.create({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": namespace, "labels": {"app.kubernetes.io/managed-by": "karta-exporter-acceptance"}}})


def run_matrix(kube, args, cases):
    expected = json.loads((ROOT / "expectations.json").read_text())
    described = {group_kind(fixture(case)): fixture(case)["apiVersion"].split("/")[-1]
                 for case in expected if case != "pod"}
    available = set(kube.run("api-resources", "--namespaced=true", "--verbs=list", "-o", "name").split())
    owned_namespace(kube, args.namespace)
    results = []
    cases = [case for case in cases if case != "milvus"] + (["milvus"] if "milvus" in cases else [])
    try:
        helper = fixture("helpers")
        helper["metadata"]["namespace"] = args.namespace
        kube.create(helper)
        for name, key in (("matrix-ngc-placeholder", "NGC_API_KEY"), ("matrix-hf-placeholder", "HF_TOKEN")):
            kube.create({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": name, "namespace": args.namespace}, "stringData": {key: "placeholder-not-a-credential"}})
        kube.create({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "matrix-pull-placeholder", "namespace": args.namespace}, "type": "kubernetes.io/dockerconfigjson", "stringData": {".dockerconfigjson": "{\"auths\":{}}"}})
        for case in cases:
            doc = fixture(case)
            doc["metadata"]["namespace"] = args.namespace
            folder = args.output / case
            folder.mkdir(parents=True, exist_ok=True)
            started = time.monotonic()
            kube.create(doc)
            passed = False
            try:
                def check():
                    root = kube.get(resource(doc), doc["metadata"]["name"], "-n", args.namespace)
                    resources = (BASE_RESOURCES | RELATED.get(case, set())) & available
                    objects = kube.get(",".join(sorted(resources)), "-n", args.namespace)["items"]
                    objects += [root]
                    snapshots = kube.snapshots()
                    require(len(snapshots) == args.exporter_replicas, "exporter endpoint count differs from requested replicas")
                    results = []
                    identities = []
                    for target, (raw, rows) in snapshots.items():
                        (folder / (target + ".prom")).write_text("\n".join(line for line in raw.splitlines() if line.startswith("# TYPE karta_") or ('namespace="' + args.namespace + '"') in line) + "\n")
                        results.append(inspect(rows, objects, root, expected[case], described, args.namespace))
                        identities.append({(row["metric"], tuple(sorted(row["labels"].items())), row["value"]) for row in rows if row["labels"]["namespace"] == args.namespace})
                    require(all(r == results[0] for r in results) and all(rows == identities[0] for rows in identities), "exporter replicas disagree")
                    metadata = [{"apiVersion": obj["apiVersion"], "kind": obj["kind"], "name": obj["metadata"]["name"], "uid": obj["metadata"]["uid"], "ownerReferences": obj["metadata"].get("ownerReferences", [])} for obj in objects]
                    (folder / "ownership.json").write_text(json.dumps(metadata, indent=2) + "\n")
                    return results[0]
                result = wait_for(args, case, check)
                result.update(case=case, passed=True, seconds=round(time.monotonic() - started, 1))
                results.append(result)
                passed = True
                (folder / "result.json").write_text(json.dumps(result, indent=2) + "\n")
                print(json.dumps(result), flush=True)
            except (CheckFailed, subprocess.TimeoutExpired) as error:
                (folder / "result.json").write_text(json.dumps({"case": case, "passed": False, "error": str(error)}, indent=2) + "\n")
                raise
            finally:
                if passed or not args.keep:
                    kube.run("delete", resource(doc), doc["metadata"]["name"], "-n", args.namespace, "--ignore-not-found", "--wait=true", "--timeout=60s")
        return results
    finally:
        if not args.keep:
            kube.run("delete", "namespace", args.namespace, "--wait=false", "--ignore-not-found", allow_failure=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--context", required=True)
    parser.add_argument("--namespace", default="karta-exporter-acceptance")
    parser.add_argument("--exporter-namespace", default="karta-system")
    parser.add_argument("--exporter-service", default="karta-exporter")
    parser.add_argument("--exporter-port", type=int, default=8080)
    parser.add_argument("--exporter-replicas", type=int, default=2)
    parser.add_argument("--cases", default="all", help="Comma-separated case names, or all")
    parser.add_argument("--suite", choices=("all", "matrix", "faults"), default="all")
    parser.add_argument("--image", help="Exporter image for isolated fault Pods; required for faults")
    parser.add_argument("--timeout", type=int, default=600)
    parser.add_argument("--poll", type=float, default=5)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--keep", action="store_true", help="Retain namespaces and failed fixtures for diagnosis")
    args = parser.parse_args()
    known = json.loads((ROOT / "expectations.json").read_text())
    cases = list(known) if args.cases == "all" else args.cases.split(",")
    if set(cases) - set(known):
        parser.error("unknown cases: " + ",".join(sorted(set(cases) - set(known))))
    if args.suite != "matrix" and not args.image:
        parser.error("--image is required when running the fault suite")
    if args.timeout <= 0 or args.poll <= 0 or args.exporter_replicas <= 0:
        parser.error("timeout, poll and exporter-replicas must be positive")
    args.output.mkdir(parents=True, exist_ok=True)
    kube = Kubernetes(args)
    summary = {"matrix": [], "faults": []}
    try:
        if args.suite in ("all", "matrix"):
            summary["matrix"] = run_matrix(kube, args, cases)
        if args.suite in ("all", "faults"):
            from faults import run_faults
            summary["faults"] = run_faults(kube, args)
        summary["passed"] = True
    except (CheckFailed, subprocess.TimeoutExpired) as error:
        summary.update(passed=False, error=str(error))
        print(str(error), file=sys.stderr)
    finally:
        (args.output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    return 0 if summary.get("passed") else 1


if __name__ == "__main__":
    # Fault helpers must share this module's exception type when invoked as a script.
    sys.modules["acceptance"] = sys.modules[__name__]
    sys.exit(main())
