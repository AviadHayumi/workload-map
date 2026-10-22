<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Isolated baseline exporter

This setup observes namespace `test-ref` on context `kmetric-playground-dev`.
It uses the unmodified exporter from source `01352db5`. It does not implement
the separate reference-association prototype.

[values.yaml](values.yaml) selects only `core-pod-v1`, `batch-job-v1`, and
`apps-deployment-v1` from the built-in catalog. The catalog exclusion list is
explicitly empty, so bare Pods are included. Cluster Karta definitions are
disabled. ReplicaSet reads support the Deployment ownership path. No owner kind
is ignored, and no intermediate-owner fallback is enabled.

The operator, CRD upgrader, webhook, admission capture, and ServiceMonitor are
disabled. The chart can still render update-protection admission policies when
VAP APIs are present. For that reason, [render.py](render.py) selects only the
six exporter template files. Use its output instead of a full Helm install.
No Karta objects, CRDs, admission policies, or hooks appear in the output.

The chart normally binds a ClusterRole that aggregates roles from other
exporter installations. The renderer copies the selected base read rules into
the unique `karta-ref-exporter` ClusterRole, removes `aggregationRule`, and omits
the base role. The resulting role has no shared aggregation label. This is a
local manifest adjustment for the lab; the chart and Helm API remain unchanged.
The permitted resources are namespaces, Pods, CRDs, Jobs, Deployments, and
ReplicaSets, using only get/list/watch. These are ClusterRole permissions;
namespace selection limits exporter watches, not the ServiceAccount's
Kubernetes authorization scope.

## Render and apply

The saved [placeholder render](rendered-placeholder.yaml) is not deployable.
`EXPORTER_IMAGE` must identify the tested image by an explicit tag or digest.
The live experiment built the unmodified public source at
`01352db5c4be54b0b3eb6d53a0a04fb5faa34520` and deployed:

```text
ttl.sh/aviad-karta-ref-665389c52e6b@sha256:0d1fb296a86138dab7234b101cb088a44e6d96b0bbf978c4d3a2d1492e6240c5
```

The registry image is temporary. A later reproduction must rebuild and publish
an available image. The existing `exporter/Dockerfile` is the source-build path.
A prebuilt binary image must retain the non-root runtime and `/karta-exporter`
entrypoint.

Run from the repository root:

```sh
export EXPORTER_IMAGE='REGISTRY/karta-reference-exporter:TAG'
python3 docs/research/workload-references/exporter/render.py \
  > /private/tmp/karta-reference-exporter.yaml
kubectl --context kmetric-playground-dev -n test-ref \
  apply -f /private/tmp/karta-reference-exporter.yaml
kubectl --context kmetric-playground-dev -n test-ref \
  rollout status deployment/karta-ref-exporter --timeout=120s
```

Before using the ServiceAccount, verify its effective authorization. Pod and
Job list access in `test-ref` must be allowed. Cluster Karta, PodGroup, and
RayCluster reads and Pod creation must be denied. A read-only check of the
created ClusterRole must show no aggregation rule. Other pre-existing
ClusterRoleBindings could grant access independently, so inspect the live
result rather than relying only on the local render.

```sh
kubectl --context kmetric-playground-dev auth can-i list pods -n test-ref \
  --as=system:serviceaccount:test-ref:karta-ref-exporter
kubectl --context kmetric-playground-dev auth can-i list kartas.run.ai \
  --as=system:serviceaccount:test-ref:karta-ref-exporter
kubectl --context kmetric-playground-dev auth can-i list podgroups.scheduling.run.ai -n test-ref \
  --as=system:serviceaccount:test-ref:karta-ref-exporter
kubectl --context kmetric-playground-dev auth can-i list rayclusters.ray.io -n test-ref \
  --as=system:serviceaccount:test-ref:karta-ref-exporter
kubectl --context kmetric-playground-dev auth can-i create pods -n test-ref \
  --as=system:serviceaccount:test-ref:karta-ref-exporter
```

## Capture baseline metrics

Keep the port-forward in the foreground of a dedicated terminal:

```sh
kubectl --context kmetric-playground-dev -n test-ref \
  port-forward service/karta-ref-exporter 18092:8080
```

While the finite test workflows run, capture `/readyz`, then `/metrics`, from
`http://127.0.0.1:18092`. Keep raw metric names and label keys from the running
exporter. Filter samples by captured Pod names and verify their Pod UIDs. The
[live evidence](../spike/live/README.md) records the paired captures.
Reference fields from the separate spike are not baseline metric samples.

For OSMO bare Pods, the source-derived expectation is one Pod root per task Pod,
not one workflow root per OSMO workflow UUID. A live capture must establish the
actual output. Keep reference associations from the separate spike alongside
this baseline; they are not emitted by the deployed exporter.

## Verification state

Strict Helm lint passed. Rendering with Kubernetes 1.35.5 and VAP capabilities
produced six objects: ServiceAccount, ConfigMap, standalone ClusterRole,
ClusterRoleBinding, Service, and Deployment. All namespaced objects are in
`test-ref`; all resource names start with `karta-ref-`. The rendered config and
read rules passed local assertions. See [validation.json](validation.json).

All six resources are now applied and the exporter is Running and Ready.
The ServiceAccount apply initially returned an etcd timeout, but a later read
confirmed that it existed. Effective authorization was checked with
`kubectl auth can-i --list` for this ServiceAccount. It has the intended
get/list/watch resource rules plus normal self-review and API discovery rights.
It has no object-write, Karta, PodGroup or RayCluster permissions. The Karta lane
has paired actual exporter and KSM samples for two SkyPilot Pods. OSMO single and grouped
task captures also have actual exporter/KSM comparisons in the spike evidence.
