<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Sampled workload GPU activity demo

This standalone example combines UID-bearing DCGM samples, Karta Pod mappings,
and a fresh Pod inventory. It supports a synthetic source and native NVIDIA
DCGM (`--native-dcgm`). It exposes direct workload metrics. It does
not change Karta's metric contract, Helm API, or recording rules.

Every assigned supported GPU or MIG allocation must measure at most 5% utilization throughout a complete
180-second sampled window before a workload enters `low_activity`. One busy GPU
makes the workload active. Quiet samples after activity start a new low-activity
window. No-GPU workloads are `no_gpu`, never idle. Missing evidence is `unknown`.
This measures GPU kernel activity, not application productivity, queued work,
useful work, waste, or safe reclamation. Sampling cannot detect every brief spike
between samples.

## Direct metrics

Every workload family uses `namespace`, `workload`, `workload_group`,
`workload_version`, `workload_kind`, `workload_uid`, and `source="synthetic"` or `source="dcgm"`, selected by the input mode.

| Family | Meaning |
| --- | --- |
| `workload_gpu_observer_state{state,reason}` | Current state, value one. States are `active`, `warming`, `low_activity`, `unknown`, or `no_gpu`. |
| `workload_gpu_observer_low_activity` | One for sustained low activity; zero otherwise. Omitted until a complete valid window exists. |
| `workload_gpu_observer_utilization_max_ratio` | Maximum utilization across every verified assigned GPU. |
| `workload_gpu_observer_assigned_devices` | Device count matching complete per-container GPU requests. |
| `workload_gpu_observer_observation_seconds` | Uninterrupted valid inventory and source observation duration. |
| `workload_gpu_observer_low_activity_observation_seconds` | Uninterrupted low-activity observation duration. |
| `workload_gpu_observer_observation_healthy{reason}` | Current complete identity, inventory, and sample evidence. |

Global families use the same source label: `workload_gpu_observer_source_healthy`,
`workload_gpu_observer_window_seconds`, and `workload_gpu_observer_threshold_ratio`.

```promql
workload_gpu_observer_state{state="low_activity"} == 1
```

```promql
workload_gpu_observer_low_activity
```

No rule is needed to produce these metrics. A consumer must also verify that the
observer scrape target is up and recent. A stopped process cannot publish an
unknown state after it stops.

## Identity and completeness

The observer supports one explicitly selected namespace and one verified GPU
node. It lists all current Pods in that namespace. Every live GPU Pod must have a
resolved Karta mapping with its exact Pod UID and a healthy matching workload UID.
Every requested GPU in each regular container must match exactly one source device.
A missing mapping makes namespace coverage unknown. Missing devices, stale values,
conflicting UUIDs, invalid utilization, and mismatched source timestamp identities
cannot become low activity. Pending or terminating GPU Pods are unknown.

Pod inventory, Karta mappings, and source samples are separate observations, not an
atomic cluster snapshot. Workload ancestry follows Karta watch convergence; an
in-place ancestor change resets history when the updated root mapping arrives.
This evidence is for observation, not instantaneous policy enforcement.

The membership fingerprint contains root identity, Pod UIDs, container GPU counts,
node, and GPU UUIDs. Reassignment, replacement, owner changes, restart, failed polls,
repeated timestamps, and gaps reset the observation window. The poll interval is
10 seconds; maximum source age or observation gap is 25 seconds. State is bounded
by 10,000 Pods/roots and 1,024 source devices. Missing inputs retain known roots as
unknown for up to ten minutes. This example is not a fleet-scale discovery service.

Native mode supports exclusive full GPUs, static MIG with one compute instance
per GPU instance, and NVIDIA DRA claims with validated allocation identity.
GPU init containers, fractional allocations, and shared assignments are
unsupported and produce unknown evidence. Multiple ordinary
containers are supported only when all their requested devices have complete
source assignments. Completed Pods do not contribute current GPU requests; a
source still reporting their allocation is an allocation mismatch.

## Native DCGM, MIG, and DRA

Native mode requires Pod UID enrichment. Full GPUs use
`DCGM_FI_DEV_GPU_UTIL` divided by 100. Tested MIG instances use
`DCGM_FI_PROF_GR_ENGINE_ACTIVE`, already a 0..1 ratio. These measure different
aspects of GPU activity. MIG identity includes the physical UUID, GPU instance,
compute instance, and requested profile. Repeated physical power/temperature
values must not be summed once per partition.

DRA inventory checks ResourceClaim UID, the exact Pod UID reservation,
ResourceSlice driver/pool/device identity, device UUID/profile, and native DCGM
claim labels. Changed allocations restart the evidence window. Pending, missing,
ambiguous, shared, administrative, or unsupported claims remain Unknown.
Additional read-only RBAC is provided in the DRA lab manifests.

Native DCGM has no original per-sample timestamp in the tested output. The
window uses successful HTTP observations and cannot prove that a responsive
collector is not serving frozen data.

[The real A100 report](../../../docs/metrics-exporter-lab/real-gpu/A100-REPORT.md)
contains successful full-GPU, static-MIG, DRA, contention, and restoration tests.
[The setup guide](../../../docs/metrics-exporter-lab/real-gpu/README.md) includes
renderable manifests and Grafana dashboards. This remains a bounded single-node
example; GPU sharing, dynamic MIG, and production fleet scale are not validated.

## UID-bearing fake source

The existing fake GPU exporter omits Pod UID. A current Pod lookup cannot safely
attach a UID to an older name-only sample. The isolated source overlay builds on
[fake GPU operator v0.2.1](https://github.com/run-ai/fake-gpu-operator/tree/0901c8fc9c235e5b2ef8a2876fb89db1f7af23f4).
Its topology stores GPU usage in a map keyed by the allocating Pod UID. The overlay
uses that key only when the allocation has exactly one entry. Empty or multiple
entries produce no trusted UID. The observer independently verifies the exact
current namespace, Pod name, Pod UID, container, node, and requested device count.
It never invents a UID from a name lookup.

The metrics-only entrypoint reads one named topology ConfigMap every five seconds.
It uses upstream topology decoding and utilization generation, then publishes the
value, Pod UID, allocation labels, and source timestamp in one immutable snapshot.
It emits `DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_FB_FREE`,
`fake_gpu_source_sample_timestamp_seconds`, and `fake_gpu_source_healthy`.
The source label is always `synthetic`. The source follows the operator's existing
utilization annotation ranges. It generates a separate random sample from the
shared exporter, so values need not match the shared endpoint byte for byte.
The shared Run:AI exporter and its consumers remain unchanged.

Unsupported Knative-driven source calculations are omitted rather than converted
to zero. Stale or failed topology reads omit GPU samples. A successful read proves
current observed simulator state, not a real device's utilization. Range changes
can appear after the fake operator reconciles them; scenario metadata is not used
as a measured value.

The overlay adds only a read-only entrypoint. It does not launch upstream node
label, filesystem, or device-plugin writers. It imports the pinned upstream MIT
module; the source image includes that module's license. No third-party Go module
is added to Karta's exporter module. Source templates use `.go.txt` because they
compile inside the upstream module, where its `internal` package is accessible.
The source Dockerfile runs their tests during the build.

## Desired scenario metadata

Optional `--demo-configmap=karta-gpu-demo-round` reads only that named ConfigMap.
These families describe desired simulation settings independently of measurements:

- `workload_gpu_demo_intent_info{namespace,workload_uid,scenario,round_id}`.
- `workload_gpu_demo_assignment_status{namespace,workload_uid,round_id,status,reason}`.
- `workload_gpu_demo_round_started_timestamp_seconds{namespace,round_id}`.
- `workload_gpu_demo_round_ends_timestamp_seconds{namespace,round_id}`.
- `workload_gpu_demo_round_status{namespace,round_id,status}`.
- `workload_gpu_demo_metadata_healthy{namespace}`.

Invalid, expired, or unavailable metadata is omitted with metadata health zero.
It does not change measured activity or reset its history. Partial annotation
application is visible as metadata; a workload can disagree with its desired role.

## Build and deploy

Run from the repository root:

```sh
docker build -f exporter/examples/workload-gpu-observer/source/Dockerfile \
  --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t karta-demo-dcgm:local .
docker build -f exporter/examples/workload-gpu-observer/Dockerfile \
  --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t karta-workload-gpu-observer:local .
```

Edit `deploy.yaml` to select a verified node and its exact topology ConfigMap,
replace example image references with reviewed immutable image digests, and use
explicit Kubernetes context on apply. Namespaces must already exist. The source
Role grants only `get` on one topology ConfigMap. The observer Role grants only
Pod `list` and optional `get` on one scenario ConfigMap in the selected workload
namespace. Both Pods are nonroot, read-only, and have no write API permissions.
ServiceMonitors set `honorLabels: true` to preserve workload namespace and Pod
identity rather than substitute monitoring target labels.

```sh
cd exporter
go test -race ./examples/workload-gpu-observer
```

Tests cover full windows, mixed GPU activity, per-container completeness, stale
or ambiguous source data, UID replacement, missing mappings, restart and gap
resets, zero GPUs, metadata isolation, and concurrent collection.

The example observes evidence. It does not install Kyverno or enforce admission,
scale-down, deletion, or reclamation policies.
