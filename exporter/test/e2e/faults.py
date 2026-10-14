# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Isolated API/RBAC/ownership faults; no existing exporter permissions change."""

import copy
import json
import uuid

from acceptance import HEALTH, INFO, STATUS, PHASES, CheckFailed, owned_namespace, require, wait_for


def inspect_group_retirement(snapshots, group, replicas, removed):
    require(len(snapshots) == replicas, "main exporter endpoint count differs from requested replicas")
    for target, (_, rows) in snapshots.items():
        found = [row for row in rows if group in (row["labels"].get("group"), row["labels"].get("workload_group"))]
        if removed:
            require(not found, target + " retains rows for the removed fault definition")
        else:
            require(any(row["metric"] == HEALTH and row["labels"]["scope"] == "resource_type" for row in found),
                    target + " has not observed the fault definition")


def run_faults(kube, args):
    token = uuid.uuid4().hex[:8]
    name = "karta-acceptance-" + token
    namespace = args.namespace + "-faults"
    group = "acceptance-" + token + ".karta.test"
    finalizer = "acceptance.karta.test/hold"
    folder = args.output / "faults"
    folder.mkdir(parents=True, exist_ok=True)
    created = []
    results = []
    held_pods = []
    held_roots = []
    owned_namespace(kube, namespace)

    def create(obj):
        kube.create(obj)
        created.append(obj)

    def crd(kind, plural):
        return {"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
                "metadata": {"name": plural + "." + group}, "spec": {
                    "group": group, "scope": "Namespaced", "names": {"plural": plural, "singular": plural[:-1], "kind": kind},
                    "versions": [{"name": "v1", "served": True, "storage": True,
                                  "schema": {"openAPIV3Schema": {"type": "object", "x-kubernetes-preserve-unknown-fields": True}}}]}}

    def reference(obj, **overrides):
        value = {"apiVersion": obj["apiVersion"], "kind": obj["kind"], "name": obj["metadata"]["name"],
                 "uid": obj["metadata"]["uid"], "controller": True}
        value.update(overrides)
        return value

    def permission(allow):
        resources = ["bridges", "lateapis"] + (["acceptanceroots"] if allow else [])
        kube.apply({"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": {"name": name, "namespace": namespace},
                    "rules": [{"apiGroups": [""], "resources": ["pods"], "verbs": ["get", "list", "watch"]},
                              {"apiGroups": [group], "resources": resources, "verbs": ["get", "list", "watch"]}]})

    def snapshot():
        snapshots = kube.snapshots(name)
        require(len(snapshots) == 1, "fault exporter must have one endpoint")
        raw, rows = next(iter(snapshots.values()))
        return raw, [row for row in rows if row["labels"]["namespace"] == namespace]

    def current_health(rows, uid, healthy=True, reason="none"):
        found = [r for r in rows if r["metric"] == HEALTH and r["labels"]["scope"] == "workload" and r["labels"]["workload_uid"] == uid]
        require(len(found) == 1, "workload must have one current health reason")
        require(found[0]["value"] == int(healthy) and found[0]["labels"]["reason"] == reason,
                "unexpected health: " + json.dumps(found))

    def api_health(rows, kind, healthy, reason=None):
        found = [r for r in rows if r["metric"] == HEALTH and r["labels"]["scope"] == "resource_type" and r["labels"]["kind"] == kind and r["labels"]["group"] == group]
        require(len(found) == 1 and found[0]["value"] == int(healthy), kind + " API health differs")
        if reason:
            require(found[0]["labels"]["reason"] == reason, kind + " API has wrong failure reason")

    def record(case, assertion):
        def check():
            raw, rows = snapshot()
            (folder / (case + ".prom")).write_text("\n".join(line for line in raw.splitlines() if line.startswith("# TYPE karta_") or ('namespace="' + namespace + '"') in line) + "\n")
            assertion(rows)
            return {"case": case, "passed": True}
        result = wait_for(args, case, check)
        results.append(result)
        (folder / "results.json").write_text(json.dumps(results, indent=2) + "\n")
        print(json.dumps(result), flush=True)

    def mappings(rows, uid):
        return [r for r in rows if r["metric"] == INFO and r["labels"]["pod_uid"] == uid]

    def record_main(case, removed):
        def check():
            snapshots = kube.snapshots()
            for target, (raw, _) in snapshots.items():
                (folder / (case + "-" + target + ".prom")).write_text("\n".join(
                    line for line in raw.splitlines() if line.startswith("# TYPE karta_") or group in line
                    or ('namespace="' + args.namespace + '"') in line) + "\n")
            inspect_group_retirement(snapshots, group, args.exporter_replicas, removed)
            return {"case": case, "passed": True, "exporterReplicas": len(snapshots)}
        result = wait_for(args, case, check)
        results.append(result)
        (folder / "results.json").write_text(json.dumps(results, indent=2) + "\n")
        print(json.dumps(result), flush=True)

    try:
        for kind, plural in (("AcceptanceRoot", "acceptanceroots"), ("Bridge", "bridges")):
            doc = crd(kind, plural)
            create(doc)
            kube.run("wait", "crd/" + doc["metadata"]["name"], "--for=condition=Established", "--timeout=60s")
        definition = {"apiVersion": "run.ai/v1alpha1", "kind": "Karta", "metadata": {"name": name}, "spec": {"structureDefinition": {
            "rootComponent": {"name": "application", "kind": {"group": group, "version": "v1", "kind": "AcceptanceRoot"},
                              "statusDefinition": {"phaseDefinition": {"path": ".spec.phase"}, "statusMappings": {"running": [{"byPhase": "Running"}]}}},
            "additionalChildKinds": [{"group": group, "version": "v1", "kind": "Bridge"}],
            "childComponents": [{"name": "worker", "kind": {"group": "", "version": "v1", "kind": "Pod"}, "ownerRef": "application",
                                 "specDefinition": {"podTemplateSpecPath": ".spec.template"},
                                 "podSelector": {"componentTypeSelector": {"keyPath": ".spec.containers[0].image", "value": "busybox:1.36"}}}]
        }}}
        alternate = copy.deepcopy(definition["spec"]["structureDefinition"]["childComponents"][0])
        alternate["name"] = "unused"
        alternate["podSelector"]["componentTypeSelector"]["value"] = "unused:fixture"
        definition["spec"]["structureDefinition"]["childComponents"].append(alternate)
        create(definition)
        for kind in ("ServiceAccount", "ClusterRole", "ClusterRoleBinding", "RoleBinding"):
            metadata = {"name": name}
            if kind == "ServiceAccount":
                metadata["namespace"] = args.exporter_namespace
            if kind == "RoleBinding":
                metadata["namespace"] = namespace
            obj = {"apiVersion": "v1" if kind == "ServiceAccount" else "rbac.authorization.k8s.io/v1", "kind": kind, "metadata": metadata}
            if kind == "ClusterRole":
                obj["rules"] = [{"apiGroups": ["run.ai"], "resources": ["kartas"], "verbs": ["get", "list", "watch"]}]
            if kind.endswith("Binding"):
                obj.update(roleRef={"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole" if kind == "ClusterRoleBinding" else "Role", "name": name},
                           subjects=[{"kind": "ServiceAccount", "name": name, "namespace": args.exporter_namespace}])
            create(obj)
        permission(False)
        template = {"spec": {"containers": [{"name": "worker", "image": "busybox:1.36", "command": ["sleep", "3600"],
                                            "resources": {"requests": {"cpu": "1m", "memory": "4Mi"}}}], "terminationGracePeriodSeconds": 0}}
        roots = []
        for root_name in ("root-a", "root-b"):
            create({"apiVersion": group + "/v1", "kind": "AcceptanceRoot", "metadata": {"name": root_name, "namespace": namespace},
                    "spec": {"phase": "Running", "template": template}})
            roots.append(kube.get("acceptanceroots." + group, root_name, "-n", namespace))
        a, b = roots
        a_uid, b_uid = a["metadata"]["uid"], b["metadata"]["uid"]
        deployment = {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": name, "namespace": args.exporter_namespace},
                      "spec": {"replicas": 1, "selector": {"matchLabels": {"acceptance": name}}, "template": {"metadata": {"labels": {"acceptance": name}},
                               "spec": {"serviceAccountName": name, "containers": [{"name": "exporter", "image": args.image, "imagePullPolicy": "IfNotPresent",
                                        "ports": [{"name": "metrics", "containerPort": args.exporter_port}],
                                        "args": ["--metrics-bind-address=:" + str(args.exporter_port), "--namespace=" + namespace, "--health-check-interval=2s", "--health-max-age=30s",
                                                 "--required-kinds=" + group + "/v1/AcceptanceRoot," + group + "/v1/LateAPI", "--ignored-owner-kinds=" + group + "/v1/Wrapper"]}]}}}}
        create(deployment)
        create({"apiVersion": "v1", "kind": "Service", "metadata": {"name": name, "namespace": args.exporter_namespace},
                "spec": {"publishNotReadyAddresses": True, "selector": {"acceptance": name}, "ports": [{"port": args.exporter_port, "targetPort": args.exporter_port}]}})
        record("startup-permission-denied", lambda rows: api_health(rows, "AcceptanceRoot", False, "permission_denied"))
        record_main("main-replicas-observe-fault-definition", removed=False)
        permission(True)

        def healthy_roots(rows):
            api_health(rows, "AcceptanceRoot", True, "none")
            for uid in (a_uid, b_uid):
                current_health(rows, uid)
                states = [r for r in rows if r["metric"] == STATUS and r["labels"]["workload_uid"] == uid]
                require({r["labels"]["phase"] for r in states} == PHASES and len(states) == len(PHASES), "zero-Pod root has incomplete status")
        record("permission-granted-zero-pod-roots", healthy_roots)
        record("required-api-missing", lambda rows: api_health(rows, "LateAPI", False, "api_unavailable"))
        late = crd("LateAPI", "lateapis")
        create(late)
        kube.run("wait", "crd/" + late["metadata"]["name"], "--for=condition=Established", "--timeout=60s")
        record("required-api-late-discovery", lambda rows: api_health(rows, "LateAPI", True, "none"))
        kube.run("delete", "crd", late["metadata"]["name"], "--wait=true", "--timeout=60s")
        record("required-api-disappeared", lambda rows: api_health(rows, "LateAPI", False, "api_unavailable"))
        kube.create(late)
        kube.run("wait", "crd/" + late["metadata"]["name"], "--for=condition=Established", "--timeout=60s")
        record("required-api-reappeared", lambda rows: api_health(rows, "LateAPI", True, "none"))
        permission(False)

        def denied(rows):
            api_health(rows, "AcceptanceRoot", False, "permission_denied")
            for uid in (a_uid, b_uid):
                current_health(rows, uid, False, "permission_denied")
        record("permission-revoked", denied)
        permission(True)
        record("fresh-recovery-replaces-reason", healthy_roots)
        failing_definition = copy.deepcopy(definition)
        failing_definition["spec"]["structureDefinition"]["rootComponent"]["statusDefinition"]["phaseDefinition"]["path"] = 'error("acceptance evaluation failure")'
        kube.apply(failing_definition)

        def evaluation_failed(rows):
            api_health(rows, "AcceptanceRoot", True, "none")
            for uid in (a_uid, b_uid):
                current_health(rows, uid, False, "evaluation_failed")
                require(not any(r["metric"] == STATUS and r["labels"]["workload_uid"] == uid for r in rows),
                        "failed evaluation retains a status observation")
        record("valid-definition-evaluation-failed", evaluation_failed)
        kube.apply(definition)
        record("definition-evaluation-fresh-recovery", healthy_roots)
        create({"apiVersion": group + "/v1", "kind": "Bridge", "metadata": {"name": "middle", "namespace": namespace, "ownerReferences": [reference(a)]}})
        bridge = kube.get("bridges." + group, "middle", "-n", namespace)
        pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "worker", "namespace": namespace, "ownerReferences": [reference(bridge)]}, "spec": template["spec"]}
        create(pod)
        pod_uid = kube.get("pod", "worker", "-n", namespace)["metadata"]["uid"]

        def attributed_to(rows, uid, resolution="resolved"):
            current_health(rows, uid)
            found = mappings(rows, pod_uid)
            require(len(found) == 1, "expected one mapping for worker")
            label = found[0]["labels"]
            require(label["workload_uid"] == uid and label["owner_resolution"] == resolution and label["component"] == "worker", "wrong root or full-Pod selector result")
        record("full-pod-selector-through-middle-owner", lambda rows: attributed_to(rows, a_uid))
        kube.run("patch", "bridges." + group, "middle", "-n", namespace, "--type=merge", "-p", json.dumps({"metadata": {"ownerReferences": [reference(b)]}}))
        record("existing-pod-reparented-after-middle-owner-update", lambda rows: attributed_to(rows, b_uid))
        child = {"apiVersion": group + "/v1", "kind": "AcceptanceRoot", "metadata": {"name": "child", "namespace": namespace, "ownerReferences": [reference(b)]}, "spec": {"phase": "Running", "template": template}}
        create(child)
        child = kube.get("acceptanceroots." + group, "child", "-n", namespace)
        child_uid = child["metadata"]["uid"]

        def no_child_status(rows):
            attributed_to(rows, b_uid)
            require(not any(r["metric"] == STATUS and r["labels"]["workload_uid"] == child_uid for r in rows), "described child publishes status without Pods")
        record("nested-zero-pod-child-has-no-status", no_child_status)

        for label, owner_name in (("missing-owner", "absent"), ("uid-mismatched-owner", "root-a")):
            stray = copy.deepcopy(pod)
            stray["metadata"] = {"name": label, "namespace": namespace, "finalizers": [finalizer],
                                 "ownerReferences": [reference(a, name=owner_name, uid=str(uuid.uuid4()))]}
            create(stray)
            held_pods.append(label)
            stray_uid = kube.get("pod", label, "-n", namespace)["metadata"]["uid"]

            def unresolved(rows):
                require(not mappings(rows, stray_uid), "unresolved Pod gained attribution")
                for uid in (a_uid, b_uid):
                    current_health(rows, uid, False, "unresolved_owner")
            record(label + "-fails-namespace-observation", unresolved)
            kube.run("patch", "pod", label, "-n", namespace, "--type=merge", "-p", '{"metadata":{"finalizers":[]}}')
            kube.run("delete", "pod", label, "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=60s")
            held_pods.remove(label)
            record(label + "-removed-fresh-recovery", healthy_roots)
        wrapper_ref = {"apiVersion": group + "/v1", "kind": "Wrapper", "name": "unread-wrapper", "uid": str(uuid.uuid4()), "controller": True}
        kube.run("patch", "acceptanceroots." + group, "root-b", "-n", namespace, "--type=merge", "-p", json.dumps({"metadata": {"ownerReferences": [wrapper_ref], "finalizers": [finalizer]}}))
        held_roots.append("root-b")
        record("ignored-wrapper-not-read", lambda rows: attributed_to(rows, b_uid, "ignored_owner"))
        kube.run("patch", "acceptanceroots." + group, "root-b", "-n", namespace, "--type=merge", "-p", '{"metadata":{"finalizers":[]}}')
        held_roots.remove("root-b")
        kube.run("delete", "acceptanceroots." + group, "root-b", "-n", namespace, "--ignore-not-found", "--wait=true", "--timeout=60s")

        def deleted(rows):
            require(not any(r["labels"].get("workload_uid") == b_uid for r in rows), "deleted root still exposes current rows")
            require(not mappings(rows, pod_uid), "deleted root retains Pod attribution")
        record("root-deletion-removes-all-current-rows", deleted)
        kube.run("delete", "karta", name, "--wait=true", "--timeout=60s")
        for plural in ("acceptanceroots", "bridges", "lateapis"):
            kube.run("delete", "crd", plural + "." + group, "--ignore-not-found", "--wait=true", "--timeout=60s")
        record_main("definition-deletion-removes-main-replica-rows", removed=True)
        return results
    finally:
        if not args.keep:
            for root_name in held_roots:
                kube.run("patch", "acceptanceroots." + group, root_name, "-n", namespace, "--type=merge", "-p", '{"metadata":{"finalizers":[]}}', allow_failure=True)
            for pod_name in held_pods:
                kube.run("patch", "pod", pod_name, "-n", namespace, "--type=merge", "-p", '{"metadata":{"finalizers":[]}}', allow_failure=True)
            for obj in reversed(created):
                metadata = obj["metadata"]
                group_name = obj["apiVersion"].split("/")[0] if "/" in obj["apiVersion"] else ""
                kind = obj["kind"] + ("." + group_name if group_name else "")
                command = ["delete", kind, metadata["name"], "--ignore-not-found", "--wait=false"]
                if metadata.get("namespace"):
                    command += ["-n", metadata["namespace"]]
                kube.run(*command, allow_failure=True)
            kube.run("delete", "namespace", namespace, "--ignore-not-found", "--wait=false", allow_failure=True)
