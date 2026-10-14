#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Verify real Prometheus pull and Collector-to-OTLP identity preservation."""

import argparse
import json
import subprocess
import sys
import uuid
from pathlib import Path
from urllib.parse import urlencode

from acceptance import HEALTH, INFO, LABELS, STATUS, CheckFailed, Kubernetes, require, wait_for


def identity(row, family):
    return tuple((key, row.get(key, "")) for key in sorted(LABELS[family]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", type=Path, required=True)
    parser.add_argument("--context", required=True)
    parser.add_argument("--namespace", default="karta-exporter-acceptance")
    parser.add_argument("--exporter-namespace", default="karta-system")
    parser.add_argument("--exporter-service", default="karta-exporter")
    parser.add_argument("--exporter-deployment", default="karta-exporter")
    parser.add_argument("--exporter-port", type=int, default=8080)
    parser.add_argument("--prometheus-image", default="quay.io/prometheus/prometheus:v3.14.0")
    parser.add_argument("--collector-image", default="ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.148.0")
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--poll", type=float, default=3)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    kube = Kubernetes(args)
    token = uuid.uuid4().hex[:8]
    namespace = "karta-transport-" + token
    workload = "transport-" + token
    results = []
    created_namespace = False
    created_workload = False
    created_discovery_rbac = False

    def create(obj):
        obj["metadata"]["namespace"] = namespace
        kube.create(obj)

    def deployment(name, image, arguments, config, port, ready=None, discovery=False):
        print("Deploying transport receiver: " + name, flush=True)
        create({"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": name},
                "data": {"config.yaml": json.dumps(config)}})
        container = {"name": name, "image": image, "args": arguments,
                     "ports": [{"containerPort": port}],
                     "resources": {"requests": {"cpu": "50m", "memory": "64Mi"},
                                   "limits": {"memory": "512Mi"}},
                     "volumeMounts": [{"name": "config", "mountPath": "/configuration", "readOnly": True},
                                      {"name": "data", "mountPath": "/data"}]}
        if ready:
            container["readinessProbe"] = {"httpGet": {"path": ready, "port": port}, "periodSeconds": 2}
        create({"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": name},
                "spec": {"replicas": 1, "selector": {"matchLabels": {"app": name}},
                         "template": {"metadata": {"labels": {"app": name}}, "spec": {
                             "automountServiceAccountToken": discovery,
                             "serviceAccountName": "discovery" if discovery else "default",
                             "securityContext": {"fsGroup": 65534}, "containers": [container],
                             "volumes": [{"name": "config", "configMap": {"name": name}},
                                         {"name": "data", "emptyDir": {}}]}}}})
        create({"apiVersion": "v1", "kind": "Service", "metadata": {"name": name},
                "spec": {"selector": {"app": name}, "ports": [{"name": "http", "port": port, "targetPort": port}]}})
        kube.run("rollout", "status", "deployment/" + name, "-n", namespace, "--timeout=60s")

    def query(target, expression):
        route = ("/api/v1/namespaces/" + namespace + "/services/http:" + target +
                 ":9090/proxy/api/v1/query?" + urlencode({"query": expression}))
        response = json.loads(kube.run("get", "--raw", route))
        require(response.get("status") == "success", "Prometheus query failed")
        return response["data"]["result"]

    def expression(family, uid=None, fresh=False):
        selector = family + '{namespace="' + args.namespace + '",workload="' + workload + '"'
        if uid:
            selector += ',workload_uid="' + uid + '"'
        selector += "}"
        if fresh:
            # OTLP backends can retain the last gauge after source deletion.
            # Consumers must reject old timestamps rather than trust last value.
            return selector + " and (timestamp(" + selector + ") > time() - 10)"
        return selector

    def check_parity(uid):
        snapshots = kube.snapshots()
        require(len(snapshots) >= 2, "transport acceptance requires at least two exporter replicas")
        expected = None
        replicas = {}
        for pod, (_, rows) in snapshots.items():
            current = {family: {} for family in LABELS}
            for row in rows:
                label = row["labels"]
                if label["namespace"] == args.namespace and label["workload_uid"] == uid:
                    current[row["metric"]][identity(label, row["metric"])] = row["value"]
            require(len(current[INFO]) == 1 and len(current[STATUS]) == 9 and len(current[HEALTH]) == 1,
                    "source metrics have not converged")
            require(list(current[HEALTH].values()) == [1], "source observation is unhealthy")
            require(expected is None or current == expected, "source replicas disagree")
            expected = current
            replicas[pod] = current
        counts = {}
        for target in ("direct", "bridge"):
            counts[target] = {}
            for family in LABELS:
                observed = {}
                for row in query(target, expression(family, uid, fresh=target == "bridge")):
                    key = identity(row["metric"], family)
                    value = float(row["value"][1])
                    pod = row["metric"].get("exporter_pod", "")
                    samples = observed.setdefault(pod, {})
                    require(key not in samples, "receiver has duplicate identities within one exporter target")
                    samples[key] = value
                require(observed == {pod: rows[family] for pod, rows in replicas.items()},
                        target + " changed exporter targets, labels, values or sample count for " + family)
                counts[target][family] = {pod: len(rows) for pod, rows in observed.items()}
        return counts

    def create_workload():
        nonlocal created_workload
        print("Creating transport workload", flush=True)
        kube.create({"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {
            "name": workload, "namespace": args.namespace}, "spec": {
                "replicas": 1, "selector": {"matchLabels": {"app": workload}},
                "template": {"metadata": {"labels": {"app": workload}}, "spec": {
                    "containers": [{"name": "worker", "image": "busybox:1.36", "command": ["sleep", "3600"],
                                    "resources": {"requests": {"cpu": "1m", "memory": "8Mi"}}}]}}}})
        created_workload = True
        kube.run("rollout", "status", "deployment/" + workload, "-n", args.namespace, "--timeout=60s")
        return kube.get("deployment", workload, "-n", args.namespace)["metadata"]["uid"]

    try:
        kube.get("namespace", args.namespace)
        kube.create({"apiVersion": "v1", "kind": "Namespace", "metadata": {
            "name": namespace, "labels": {"acceptance.karta.test/owned": "true"}}})
        created_namespace = True
        create({"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": "discovery"}})
        created_discovery_rbac = True
        kube.create({"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": {
            "name": namespace, "namespace": args.exporter_namespace}, "rules": [{
                "apiGroups": [""], "resources": ["endpoints", "pods", "services"], "verbs": ["get", "list", "watch"]}]})
        kube.create({"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": {
            "name": namespace, "namespace": args.exporter_namespace}, "roleRef": {
                "apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": namespace},
            "subjects": [{"kind": "ServiceAccount", "name": "discovery", "namespace": namespace}]})
        scrape = {"job_name": "karta", "scrape_interval": "2s", "scrape_timeout": "1s",
                  "honor_labels": True,
                  "kubernetes_sd_configs": [{"role": "endpoints", "namespaces": {"names": [args.exporter_namespace]}}],
                  "relabel_configs": [
                      {"source_labels": ["__meta_kubernetes_service_name"], "action": "keep", "regex": args.exporter_service},
                      {"source_labels": ["__meta_kubernetes_endpoint_port_name"], "action": "keep", "regex": "metrics"},
                      {"source_labels": ["__meta_kubernetes_pod_name"], "target_label": "exporter_pod"}]}
        base = {"global": {"scrape_interval": "2s"}, "scrape_configs": []}
        deployment("direct", args.prometheus_image,
                   ["--config.file=/configuration/config.yaml", "--storage.tsdb.path=/data"],
                   dict(base, scrape_configs=[scrape]), 9090, "/-/ready", discovery=True)
        deployment("bridge", args.prometheus_image,
                   ["--config.file=/configuration/config.yaml", "--storage.tsdb.path=/data", "--web.enable-otlp-receiver"],
                   dict(base, otlp={"translation_strategy": "NoTranslation"}), 9090, "/-/ready")
        deployment("collector", args.collector_image, ["--config=/configuration/config.yaml"], {
            "receivers": {"prometheus": {"config": {"scrape_configs": [scrape]}}},
            "exporters": {"otlphttp": {"endpoint": "http://bridge:9090/api/v1/otlp", "compression": "none"}},
            "service": {"pipelines": {"metrics": {"receivers": ["prometheus"], "exporters": ["otlphttp"]}}}}, 8888, discovery=True)
        uid = create_workload()
        print("Checking direct and bridge parity", flush=True)
        counts = wait_for(args, "pull and OTLP bridge parity", lambda: check_parity(uid))
        results.append({"case": "direct-and-bridge-parity", "passed": True, "counts": counts})
        print("Checking exporter rollout", flush=True)
        kube.run("rollout", "restart", "deployment/" + args.exporter_deployment, "-n", args.exporter_namespace)
        kube.run("rollout", "status", "deployment/" + args.exporter_deployment, "-n", args.exporter_namespace, "--timeout=60s")
        wait_for(args, "replica rollout parity", lambda: check_parity(uid))
        results.append({"case": "replica-rollout-parity", "passed": True})
        kube.run("delete", "deployment", workload, "-n", args.namespace,
                 "--cascade=foreground", "--wait=true", "--timeout=60s")
        created_workload = False

        def absent():
            for target in ("direct", "bridge"):
                for family in LABELS:
                    require(not query(target, expression(family, uid, fresh=target == "bridge")),
                            target + " still exposes old lifetime as fresh")
            return True

        wait_for(args, "deleted lifetime freshness", absent)
        results.append({"case": "deleted-lifetime-freshness", "passed": True,
                        "bridge_freshness_seconds": 10})
        replacement = create_workload()
        require(replacement != uid, "recreated workload reused its UID")
        wait_for(args, "same-name replacement parity", lambda: check_parity(replacement))
        absent()
        results.append({"case": "same-name-new-uid", "passed": True})
        report = {"passed": True, "images": {"prometheus": args.prometheus_image, "collector": args.collector_image},
                  "results": results}
        print(json.dumps(report), flush=True)
        (args.output / "transport.json").write_text(json.dumps(report, indent=2) + "\n")
        return 0
    except (CheckFailed, subprocess.TimeoutExpired) as error:
        (args.output / "transport.json").write_text(json.dumps({"passed": False, "results": results, "error": str(error)}, indent=2) + "\n")
        print(str(error), file=sys.stderr)
        return 1
    finally:
        if created_workload:
            kube.run("delete", "deployment", workload, "-n", args.namespace, "--ignore-not-found", "--wait=false", allow_failure=True)
        if created_namespace:
            kube.run("delete", "namespace", namespace, "--ignore-not-found", "--wait=false", allow_failure=True)
        if created_discovery_rbac:
            kube.run("delete", "role,rolebinding", namespace, "-n", args.exporter_namespace,
                     "--ignore-not-found", "--wait=false", allow_failure=True)


if __name__ == "__main__":
    sys.exit(main())
