<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Reproduce the mixed A100 and MIG validation

Run the full-GPU baseline in the parent README first. This follow-up changes one
A100 into two `3g.20gb` allocations and leaves the other A100 whole. It was tested
with NVIDIA GPU operator v26.7.1, driver 580.173.02, and DCGM exporter image
4.6.1-4.8.4-distroless. Device plugin and native DCGM must both recover before
submitting workloads. Commands always require an explicitly selected kubeconfig
and context. Do not use an unrelated current kubectl context.

## Preparation and hardware activation

1. Save the NVIDIA ClusterPolicy, node labels, GPU inventory, and running GPU
   consumers. Use the fully qualified `clusterpolicies.nvidia.com` resource;
   `clusterpolicy` can select Kyverno instead when both operators are installed.
2. Suspend the GPU CronJob. Wait for finite GPU jobs to finish normally, and
   verify there are no other active GPU Pods or CUDA processes.
3. Confirm the hardware supports the requested profile. Inspect
   `nvidia-smi mig -lgip` and current/pending MIG mode. The host toolkit container
   does not include nvidia-smi in PATH here; the host binary or SSH is required.
4. Apply `config.json`, then `policy-patch.json` to the NVIDIA ClusterPolicy.
   The custom ConfigMap keeps an `all-disabled` rollback profile and configures
   GPU index 0 with two 3g.20gb instances and GPU index 1 without MIG. Verify GPU
   indexes against UUIDs before reusing this configuration on another machine.
5. Set the selected GPU node's `nvidia.com/mig.config` label to
   `karta-one-mig-one-full`. Require `nvidia.com/mig.config.state=success`.
6. If A100 MIG stays pending and reset is refused, stop at that gate. On this
   passthrough VM a reboot was needed. Drain and reboot only the identified GPU
   worker after maintenance approval. Preserve standalone demo manifests and
   completed Pod logs first; drain can remove completed Pods too.
7. Require a new worker boot ID, Ready=True, successful MIG manager status,
   Ready device-plugin/DCGM Pods, and exactly one `nvidia.com/gpu` plus two
   `nvidia.com/mig-3g.20gb` resources. Restore scheduling after the drain.
8. Verify any temporarily changed host services and all GPU deployment selectors
   are restored. Recreate the saved standalone CPU fixture if it was removed.

The worker uses boot-image provisioning. Its cluster manager temporarily
reported INSTALLING with SYNC in progress; recovery took several minutes.
The other five Kubernetes nodes kept their boot IDs and Ready status.

The first reset failed while host DCGM restarted during MIG manager shutdown.
A temporary runtime service mask removed that visible holder but did not fix
reset. A direct reset without visible device-file users still failed. The mask
was removed and host DCGM restored. The saved manager logs preserve that diagnostic attempt. It is not a necessary
installation step or a proven reset fix.

## Native metrics and observer

The native service exposes distinct instance activity and memory samples with
`UUID`, `GPU_I_ID`, `GPU_CI_ID`, `GPU_I_PROFILE`, `pod_uid`, and container labels.
The parent UUID is shared between both partitions.
Use `DCGM_EXPORTER_INTERVAL=5000` for native collection and verify it in the
rolled exporter Pod. `DCGM_EXPORTER_COLLECT_INTERVAL` is ignored; the first
baseline and MIG runs therefore used the 30-second default. The installed
`dcgm-exporter --help` confirms the correct variable. The corrective patch is `dcgm-interval-patch.json`. Capture the prior policy
from the target cluster before changing it.
GPU_UTIL is absent for the
A100 MIG instances tested here. GR-engine activity is available as a 0..1 ratio.

The separate workload observer uses full-GPU GPU_UTIL/100 and MIG GR-engine
activity. It verifies the complete per-container allocation count and requested
MIG profile against Karta Pod UID attribution. Every allocation must remain at
or below 0.05 for 180 seconds. Missing data, mismatched profiles, duplicate
instances, unsupported sharing, or an incomplete allocation inventory must not
be classified as low activity. Allocation changes restart the evidence window.
Multiple compute instances within one GPU instance are rejected.

The [observer source](../../../../exporter/examples/workload-gpu-observer)
includes native full-GPU, MIG, and DRA paths and regression tests. The parent
`observer-provenance.json` records the immutable deployed image and source hash.
Use the observer README for image build instructions.
Karta public metrics and Helm values are unchanged.

Apply the parent `observer.json`, wait for rollout, then apply the parent
`dashboard.json`. Its exact PromQL reference is generated from the actual panel
queries. Query freshness is checked on raw samples before percent conversion.

## Submitted verification workloads

- `probe.json`: ConfigMap and finite `real-pytorch-mig-probe` PyTorchJob. Master
  and Worker each request one MIG instance. Native CUDA and DCGM attribution
  were checked before accepting the observer integration.
- `verified.json`: `real-pytorch-mig-verified`, a repeat after deploying the
  corrected observer. This passed activity-window checks but exposed a collection timing setup bug.
- `full-with-mig.json`: finite `real-full-with-mig` Job on the full A100. It runs
  concurrently to verify full-GPU behavior alongside MIG.
- `interval-fixed/verified.json` and `interval-fixed/full-with-mig.json`: repeat
  MIG and full-GPU cycles after correcting the native collection interval.
  Final verification scripts and artifacts live in `interval-fixed/`.

Master phases are 120 seconds active, 240 quiet, 90 active, then 30 quiet.
The Worker stays quiet while retaining its allocation. Each process allocates
about 2.25 GB of CUDA tensors. Busy work is synchronized FP16 matrix
multiplication, not model training or inference. All processes exit normally.

After both corrected-interval workloads finish, run
`interval-fixed/acceptance.py`. It requires
successful completion, separate busy/quiet instance measurements, correct Karta
UID/Component joins, no premature low classification, resumed activity, allocation
release, healthy scrapes, and the expected mixed GPU capacity. Inspect the saved
JSON booleans. `interval-fixed/verify_queries.py` checks every dashboard expression;
`QUERY_TIME` can select the recorded evidence time in `verified-time.txt`.
`interval-fixed/browser.cjs` verifies the real UI and captures screenshots at that time.

## Queries and GPU interpretation

Instance compute activity as a ratio:

```promql
DCGM_FI_PROF_GR_ENGINE_ACTIVE{
  job="karta-real-dcgm-native", GPU_I_ID!="", GPU_CI_ID!=""
}
```

Maximum activity for the whole workload, retaining workload UID:

```promql
max by (namespace, workload, workload_uid) (
  DCGM_FI_PROF_GR_ENGINE_ACTIVE{
    job="karta-real-dcgm-native", GPU_I_ID!="", GPU_CI_ID!="", pod_uid!=""
  }
  * on (namespace, pod_uid) group_left (workload, workload_uid)
  karta:pod_attribution:unique
)
```

This raw query is useful for inspection. The live dashboard additionally gates
activity on complete, fresh observer evidence. It does not replace the observer's
three-minute decision with a range-query approximation.

Deduplicate repeated physical power values before totaling devices:

```promql
sum(max by (node, UUID) (
  DCGM_FI_DEV_POWER_USAGE{job="karta-real-dcgm-native"}
))
```

The tested exporter repeats physical power and temperature under both MIG
instance labels. Those are not independent per-instance power/temperature
measurements. Per-instance framebuffer memory and GR/tensor/DRAM profiling
serve different purposes. Quiet compute can retain substantial framebuffer
memory. Missing error counters do not prove an error-free GPU.

## Final-state and scope notes

Read `interval-fixed/acceptance.json` and the parent report for the final
observed result.
The final demo state is one full A100 plus two 3g.20gb MIG instances. The
original finite one-GPU CronJob was resumed after all verification passed. The original
baseline PyTorchJob requests two full GPUs and therefore does not fit this mixed
layout; use the MIG manifests or restore the baseline geometry before rerunning it.

Only this profile, one GPU worker, unshared allocations, and one compute instance
per GPU instance were exercised. Time slicing, MPS, DRA allocation, arbitrary MIG
profiles, multi-node collection, and large-scale load were not validated here.
Do not reset or reconfigure an allocated GPU to change geometry.

Sources:
- [GPU operator MIG manager](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-operator-mig.html)
- [A100 MIG mode, reset, and monitoring](https://docs.nvidia.com/datacenter/tesla/mig-user-guide/getting-started-with-mig.html)
- [DCGM MIG profiling and normalization](https://docs.nvidia.com/datacenter/dcgm/latest/learn/modules/profiling.html)

Final checks passed: `interval-fixed/acceptance.json`, `interval-fixed/timing.json`,
`interval-fixed/query-validation.json`, and `interval-fixed/browser-acceptance.json`.
