<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Karta with physical A100 GPUs

This runbook describes the isolated real-GPU deployment and its checks.
It reuses the installed NVIDIA GPU operator, native DCGM exporter, PyTorch,
LeaderWorkerSet, Knative, and kube-state-metrics. It does not install a fake GPU
operator or inject GPU measurements. NIM is installed but full NIM inference is
not part of this test: no model download or NGC credential was needed.

## Open the dashboards

With the current SSH tunnel and local port forwards running:

- [Grafana](http://127.0.0.1:13000/karta/grafana/)
- [Prometheus](http://127.0.0.1:19090/karta/prometheus/)
- [Raw metric samples](http://127.0.0.1:13000/karta/grafana/d/karta-metric-samples/karta-metric-samples)

Grafana allows anonymous Viewer access. Editing requires the generated admin
credential in the `karta-monitoring-admin` Kubernetes Secret. No password is
stored in this directory. These localhost links require the local connection;
they are not public URLs or routes through the cluster's existing gateway.

The overview starts with the entire workload. Click a workload name to see its
Karta Components and Pods. GPU component and device details are expandable.
The metric-samples page displays actual label sets and values. Each panel's
Inspect > Query action and the Metrics & PromQL section expose the query.

## Connection

Keep the supplied kubeconfig outside Git with mode 0600. Use it explicitly for
all commands so other clusters are unaffected. The original API is reachable
on the SSH target's loopback interface. Forward that port through the jump host:

```sh
ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
  -L 127.0.0.1:19443:127.0.0.1:10443 \
  -J "$JUMP_USER@$JUMP_HOST" "$TARGET_USER@$TARGET_HOST"
```

In an isolated copy of the supplied kubeconfig, set the server to
`https://127.0.0.1:19443`. Preserve its certificate authority and credentials;
do not disable TLS verification. The API certificate used in this test accepts
loopback. Set the path and context for the following examples:

```sh
export KARTA_KUBECONFIG=/path/to/isolated-config
export KUBECONFIG="$KARTA_KUBECONFIG"
export KARTA_CONTEXT=your-context
export KARTA_GPU_NODE=your-gpu-worker
kubectl --context "$KARTA_CONTEXT" get nodes
```

Open each port forward in its own terminal:

```sh
kubectl --context "$KARTA_CONTEXT" -n karta-metrics-poc \
  port-forward --address=127.0.0.1 svc/karta-grafana 13000:3000
```

```sh
kubectl --context "$KARTA_CONTEXT" -n karta-metrics-poc \
  port-forward --address=127.0.0.1 svc/karta-prometheus 19090:9090
```

## Components and identity

- `karta-system`: released Karta operator 0.2.6.
- `karta-metrics-poc`: two Karta exporter replicas, Prometheus, Grafana, a device
  observer, and the separate workload GPU observer.
- `karta-monitoring-system`: isolated Prometheus operator; the pre-existing
  kube-state-metrics instance is reused.
- `karta-metrics-workloads`: attribution fixtures and finite real CUDA workloads.
- `gpu-operator`: the cluster's existing NVIDIA exporter, reading the two A100s.

The exporter image and its digest are in `exporter.json`. The native observer
image digest and source hash are in `observer-provenance.json`. The observer
[source and regressions](../examples/workload-gpu-observer)
are included in this repository.
The existing observer tests and added native tests passed with Go's race detector.
No Karta public metric names, labels, or Helm value definitions were changed.

DCGM Pod UID attribution was enabled through the GPU operator's supported
`spec.dcgmExporter.enablePodUID` field. Set `DCGM_EXPORTER_INTERVAL=5000` for five-second collection.
The earlier `DCGM_EXPORTER_COLLECT_INTERVAL` setting was ignored by the exporter;
the initial baseline used its 30-second default. The MIG follow-up corrects this
and repeats the activity-window checks.
Native metrics contain `hostname`, `UUID`, `pod_uid`, `namespace`, `pod`, and
`container`. The ServiceMonitor adds `node` as an alias of `hostname` and sets
`source="dcgm"`; it leaves the GPU values and `pod_uid` untouched. The service
port is named `gpu-metrics`.

## What the finite GPU test does

`real-pytorch-cycle` and `real-pytorch-verified` are actual PyTorchJobs. Each
requests two physical GPUs: one for the Master and one for the Worker. Both
containers load CUDA-enabled PyTorch, allocate GPU buffers, and report the
A100 device model. The Master performs synchronized FP16 matrix multiplication.
The Worker holds allocated GPU memory and sleeps.

Each run lasts approximately eight minutes after CUDA initialization:

1. 120 seconds: Master computes; Worker remains quiet.
2. 240 seconds: both retain their allocations and remain quiet.
3. 90 seconds: Master resumes computing.
4. 30 seconds: both remain quiet, then exit successfully.

The Master verifies finite output before exiting. This is real CUDA compute
under the PyTorch operator, not a trained model, distributed training benchmark,
or inference service. CPU fixtures exercise Karta ownership for LWS, Knative,
Deployment, StatefulSet, Job, CronJob, and standalone Pod resources.

The separate observer checks the current Pod UID, container GPU requests,
physical GPU UUID, and Karta workload mapping. It uses the maximum utilization
across every assigned GPU. It classifies low activity only after every assigned
GPU is at or below 5% throughout 180 seconds of uninterrupted observations.
Missing or conflicting evidence yields Unknown. Completing all GPU Pods releases
the devices and changes current allocation to No GPUs; historical activity stays
in Prometheus.

Native DCGM does not expose a per-sample collection timestamp here. The window
therefore uses successful HTTP observation times. It cannot prove that an
otherwise responsive hardware collector is not returning frozen values.
Low kernel activity also does not prove that an application is unproductive or
safe to terminate. The observer is scoped to one GPU node and full-device
allocations; the baseline did not validate MIG, DRA claims, sharing, or multi-node
GPU attribution.

## Recurring real GPU activity

`gpu-cronjob.json` runs the same finite compute/quiet sequence on one GPU every
15 minutes. It uses `concurrencyPolicy: Forbid`, no retries, a 15-minute hard
execution deadline, a one-hour TTL, and bounded Job history. It generates real
hardware load; it does not edit metric values or fake-GPU annotations. The
schedule is UTC. Another workload can occupy the second GPU.

Pause future executions without interrupting a running Job:

```sh
kubectl --context "$KARTA_CONTEXT" -n karta-metrics-workloads \
  patch cronjob real-gpu-cycle --type=merge -p '{"spec":{"suspend":true}}'
```

Resume by setting `suspend` to false. Change the CronJob schedule or
`gpu_cycle.py` and its ConfigMap to change future runs. Existing containers read
the script at startup; changing the ConfigMap does not alter a running phase.
The older CPU `matrix-cronjob` fixture is intentionally suspended.

## Useful queries

Raw NVIDIA GPU utilization, in percent:

```promql
DCGM_FI_DEV_GPU_UTIL{namespace="karta-metrics-workloads"}
```

Raw framebuffer usage, in MiB:

```promql
DCGM_FI_DEV_FB_USED{namespace="karta-metrics-workloads"}
```

Direct workload activity metrics, without a recording rule:

```promql
workload_gpu_observer_state{namespace="karta-metrics-workloads"}
workload_gpu_observer_utilization_max_ratio{namespace="karta-metrics-workloads"}
workload_gpu_observer_low_activity_observation_seconds{namespace="karta-metrics-workloads"}
```

A Pod's Karta ownership and Component:

```promql
karta_pod_workload_info{namespace="karta-metrics-workloads"}
```

Raw utilization joined to Components using the existing deduplication rule:

```promql
max by (namespace, workload, workload_uid, component) (
  DCGM_FI_DEV_GPU_UTIL{job="karta-real-dcgm-native", pod_uid!=""}
  * on(namespace, pod_uid) group_left(workload, workload_uid, component)
  karta:pod_attribution:unique
)
```

The dashboard adds freshness and observer-health checks. For production decisions,
use those guarded expressions rather than treating a missing series as zero.
The native exporter also exposes temperature, power, energy, memory, and several
profiling counters. Do not assume a counter exists: inspect its raw samples.

## Recreate this variant

Create a separate working copy before applying files or running acceptance scripts:

```sh
python3 render.py --node "$KARTA_GPU_NODE" \
  --storage-class your-storage-class --output /tmp/karta-real-gpu
```

Run the following steps against that working copy. Set `KARTA_KUBECONFIG`,
`KARTA_CONTEXT`, and `KARTA_GPU_NODE` for Python verification scripts. They
require these variables and never fall back to an implicit kubectl context.
The `browser*.cjs` scripts use a locally installed Playwright package.
Saved evidence uses node aliases and documentation IP addresses. It is historical,
not a ready-to-use connection configuration.


The [playground bundle](../playground-dev/recreate/README.md) supplies the pinned charts.
Keep its original path available when using a rendered physical-GPU working copy.
Use this directory's adapted manifests for physical GPUs. Do not apply the
bundle's fake-GPU operator, mock-NVML exporter, or GPU annotation randomizer.

1. Verify physical GPU capacity, existing consumers, operators, service names,
   storage class, and DCGM `/metrics`. Adjust this directory's node and storage
   settings for a different cluster.
2. Install Karta from the bundled `charts/karta-operator`, with
   `fullnameOverride=karta-operator` and `image.tag=0.2.6`.
3. Install the bundled Prometheus operator chart using
   `monitoring-operator-values.yaml` with `kubeStateMetrics.enabled=false` when
   kube-state-metrics already exists. Its default Prometheus and Grafana stay
   disabled because this deployment provides isolated instances.
4. Create the namespaces in `rendered/namespaces.json`. Generate a strong random
   `karta-monitoring-admin` Secret with username/password keys directly through
   the Kubernetes API. Do not save that Secret in manifests or Git.
5. Apply `definitions.json`, `exporter.json`, `dashboard.json`, `monitoring.json`,
   `native-servicemonitor.json`, and `observer.json`. Enable Pod UID labels through
   the existing GPU operator; preserve any pre-existing exporter environment
   variables when adding `DCGM_EXPORTER_INTERVAL=5000`.
   Confirm the installed exporter help names that variable and wait for rollout.
   `DCGM_EXPORTER_COLLECT_INTERVAL` is invalid and silently ignored.
6. Wait for readiness, then apply `cpu-workloads.json` and `gpu-workload.json`.
   Verify both GPUs are free before the two-GPU test. `gpu-verified.json` is a
   second run with a different workload UID. Each PyTorchJob retains completed
   Pods for inspection and has a two-hour TTL.
7. Apply `gpu-cronjob.json` if continuing periodic real GPU load is desired.
8. Open the port forwards. Run `acceptance.py` after both finite runs complete,
   using the environment variables above. `verify_queries.py` checks
   every dashboard expression; `QUERY_TIME` selects historical evidence.

The published manifests use `__GPU_NODE__`. Run `render.py` as described below
and use the rendered working directory for every apply and verification step.
Capture rollback settings from the target cluster, not from historical evidence.

## Evidence and fixes

- `acceptance.json`: live workload completion, native provenance, activity
  thresholds, and attribution checks. Inspect check values, not just file presence.
- `real-pytorch-*-history.json`: retained samples used by the acceptance checks.
- `real-pytorch-cycle-*.log`: CUDA device identity, phase timings, compute
  iterations, and normal completion from the first run.
- `query-validation.json`: exact evaluated dashboard expressions and results.
- `browser-acceptance.json`: UI check result and workload UID drill-down.
- `real-gpu-overview.png`, `real-gpu-structure.png`, and
  `real-dcgm-metric-samples.png`: screenshots from the tested dashboard.

Issues found during setup and corrected:

- The Karta release image tag is `0.2.6`, not `v0.2.6`.
- Native DCGM's Pod UID label is `pod_uid`. It must be enabled through the GPU
  operator; a differently named relabel source would erase it.
- The installed DCGM Service exposes `gpu-metrics`, not `metrics`.
- The old workload observer expected simulator freshness metadata. A tested
  native path now consumes actual DCGM output and identifies its source as DCGM.
- Two copied component panels still selected the old bridge job. They now select
  `karta-real-dcgm-native`.
- Simulator-only panels and descriptions were removed from the physical-GPU copy.
- Definitions for absent operators were removed from this cluster's test setup.

## Knative routing repaired

The original HTTPRoutes pointed to nonexistent Istio gateways. Dedicated
kgateway Gateways and ClusterIP Services now serve Knative external/local routes.
The KnativeServing operator configuration references those actual gateways.
Service Ready and RoutesReady became True, and a real routed request returned
HTTP 200 with `Hello smoke!`. The shared TLS gateway and MetalLB pool were
preserved. The tested external route is accessed through a local port forward.

Recreate with `knative-fix/gateways.json` and the operator patch in
`knative-fix/serving-patch.json`, after verifying the installed kgateway class
controller and KnativeServing resource names. See `knative-fix/http-acceptance.json`.
NIM model serving was not exercised.


## MIG follow-up

[MIG recreation and verification guide](mig/README.md) documents the mixed
layout, reboot requirement, native query differences, observer changes, and
finite validation workloads. The original two-full-GPU baseline manifest does
not fit the mixed layout; use the new manifests or restore full-GPU geometry.

## DRA validation

The verified DRA full-GPU and static-MIG procedure is in `dra/README.md`.
The real A100 report includes native claim labels, exact queries, screenshots,
failures and fixes, activity windows, contention, and restoration results.
DRA testing temporarily replaces device-plugin allocation; both allocators
must not govern the same GPUs at the same time.
