<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# DRA validation and reproduction

## Observed starting state

Kubernetes v1.35.9, host NVIDIA driver 580.173.02, toolkit 1.20.1, and native
DCGM exporter 4.8.4 are installed. The existing DRA 0.5.0 controller is managed
by Argo CD. DeviceClasses for GPU and MIG exist, but no DRA GPU kubelet plugin
or ResourceSlices were present. No ResourceClaims existed. At the initial checkpoint, GPU
allocation used the NVIDIA device plugin.

The GPU worker exposes one full A100 and two static MIG 3g.20gb instances.
The prepared test preserves that geometry. Dynamic MIG, MPS, time slicing,
ComputeDomains, and GPUCluster-managed installation are outside this test.

The installed DCGM release's source calls the common Pod identity helper in
its DRA path and emits claim, driver, pool, device, and MIG identity labels.
Live full-GPU and MIG CUDA probes passed. The separate workload GPU observer
was extended to cross-check claims, Pod UID reservations, ResourceSlices, and
native labels. Existing Karta metrics and Helm values are unchanged.
The final cycle results and screenshots are in the parent A100 report.

## Prepared artifacts

- `chart-provenance.json`: pinned chart 0.5.0 OCI and archive digests.
- `values.yaml`, `rendered.yaml`: source values and original Helm rendering.
- `plugin.json`: isolated GPU-only plugin and RBAC, selected to the GPU worker.
- `device-plugin-disable.json`, `device-plugin-restore.json`: exact policy patches.
- `claim-templates.json`: one full A100 or one static MIG 3g.20gb allocation.
- `probes.json`: finite full-GPU and MIG CUDA smoke jobs, each with one claim.
- `full-cycle.json`, `mig-cycle.json`: finite activity cycles used for validation.
- Capture pre-change state locally before running the procedure. Private
  cluster-wide snapshots are excluded from this published bundle.

The rendered chart uses several fixed cluster-resource names even with
fullnameOverride. The isolated manifest renames those resources and references,
selects only GPU plugin RBAC, and reuses the existing DeviceClasses unchanged.
Its ResourceSlice admission-policy match names the actual test ServiceAccount.
The existing Argo CD application and controller are not part of the test edits.

## Execution gates

1. Arrange a GPU maintenance window for temporary device-plugin disablement.
   The change affects shared GPU scheduling.
2. Suspend the recurring GPU CronJob and wait for any current finite run to
   complete. Require no other active GPU consumers. Preserve the prior state.
3. Disable the device plugin with the saved policy patch. Verify its Pods are
   gone before starting DRA. Never allow both allocators to govern these GPUs.
4. Apply the isolated plugin. Require its Pod healthy and ResourceSlices showing
   one full GPU and both existing MIG instances. Do not change MIG geometry.
5. Enable native DCGM DRA mapping with the required ResourceSlice read
   permissions and preserved Pod UID/collection settings. Require readiness.
6. Apply claim templates and smoke Jobs. Require allocated claims, correct Pod
   reservations, exactly one visible CUDA device per container, successful
   computation, and normal completion. Verify native claim/device labels and
   Pod UID attribution. Save claims, native samples, and CUDA logs.
7. Run complete activity cycles only after the earlier gates pass. Validate
   claim-aware observer inventory before accepting low-activity classification.
   Unsupported claims must remain Unknown, not No GPUs or Low activity.
8. Save evidence, screenshots, failures and repairs into the parent A100 report.
9. Clean up only test-owned workload Pods/claims after preserving evidence.
   Verify claims released and remove the isolated DRA plugin before restoring
   the device plugin. Require original extended-resource capacity, healthy DCGM,
   and Ready nodes, then restore the prior CronJob state.

## Installation scope

GPU Operator 26.7 documents GPUCluster for operator-managed DRA allocation and
ClusterPolicy for device-plugin allocation. This temporary, standalone plugin
probe does not validate a migration to GPUCluster. A production migration needs
its own deployment plan; the existing operator resource is preserved here.

Sources:

- [DRA 0.5.0 prerequisites](https://dra-driver-nvidia-gpu.sigs.k8s.io/docs/prerequisites/)
- [DRA chart allocation and coexistence controls](https://dra-driver-nvidia-gpu.sigs.k8s.io/docs/reference/helm-values/)
- [Static MIG claim examples](https://dra-driver-nvidia-gpu.sigs.k8s.io/docs/guides/gpu-allocation/mig/)
- [GPU Operator 26.7 allocation modes](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/26.7/dra-intro-install.html)
- [DCGM exporter 4.8.4 DRA mapping source](https://github.com/NVIDIA/dcgm-exporter/blob/4.8.4/internal/pkg/transformation/kubernetes.go)

## Verified corrections

- The first new namespace failed the cluster privileged-container policy.
  The corrected plugin uses `gpu-operator`, the already permitted GPU
  infrastructure namespace. No policy exception or global change was added.
- The original chart uses fixed resource names. The deployed `plugin.json`
  uses unique names, correct ServiceAccount references, and a corrected
  ResourceSlice admission-policy identity. It does not replace Argo CD objects.
- Native DCGM needs `KUBERNETES_ENABLE_DRA=true` and ResourceSlice read access.
  Preserve `enablePodUID=true` and `DCGM_EXPORTER_INTERVAL=5000`.
- CUDA container images can default to exposing all GPUs. Test manifests set
  `NVIDIA_VISIBLE_DEVICES=void`; CDI supplies the allocated device. The probes
  assert one visible CUDA device, perform matrix multiplication, and exit.
- A 45-second DCGM rollout wait expired before the normal readiness delay.
  Confirm actual readiness; do not treat this timeout alone as a driver failure.

## Repeat the test

These are sanitized reference artifacts. Adapt namespace, node selector,
context, existing versions, and immutable image references before reuse.
Do not apply the saved old ClusterPolicy wholesale. Its before snapshot is
for comparison, not replacement of unrelated operator settings.

From this `dra/` directory, set the kubeconfig and context explicitly:

```sh
export KARTA_KUBECONFIG=/path/to/authorized/kubeconfig
export KARTA_CONTEXT=your-context
export KARTA_GPU_NODE=your-gpu-worker
K=(kubectl --kubeconfig "$KARTA_KUBECONFIG" --context "$KARTA_CONTEXT")
```

Save current state, pause the finite CronJob, and require no active GPU
consumers. This change needs an agreed GPU maintenance window. The actual
run received explicit authorization for the temporary allocator switch.

```sh
"${K[@]}" -n karta-metrics-workloads patch cronjob real-gpu-cycle \
  --type=merge -p '{"spec":{"suspend":true}}'
"${K[@]}" patch clusterpolicies.nvidia.com cluster-policy \
  --type=merge --patch-file device-plugin-disable.json
```

Require the regular device-plugin Pods gone before installing DRA:

```sh
"${K[@]}" apply -f plugin.json
"${K[@]}" -n gpu-operator rollout status \
  daemonset/karta-dra-validation-kubelet-plugin --timeout=120s
"${K[@]}" get resourceslices
"${K[@]}" apply -f dcgm-rbac.json
"${K[@]}" patch clusterpolicies.nvidia.com cluster-policy \
  --type=merge --patch-file dcgm-dra-patch.json
"${K[@]}" -n gpu-operator rollout status \
  daemonset/nvidia-dcgm-exporter --timeout=120s
```

Require one full GPU and both static MIG devices in the ResourceSlice. Save
its current names for cleanup. Run the probes before the longer tests:

```sh
"${K[@]}" apply -f claim-templates.json
"${K[@]}" apply -f probes.json
"${K[@]}" -n karta-metrics-workloads get resourceclaims,pods,jobs
"${K[@]}" -n karta-metrics-workloads logs job/real-dra-full-probe
"${K[@]}" -n karta-metrics-workloads logs job/real-dra-mig-probe
```

Require both Jobs Complete, exactly one visible CUDA device per probe, and
correct native Pod UID and claim/device labels. Save evidence and remove the
completed probe Jobs. Confirm their generated claims disappear.

```sh
"${K[@]}" apply -f observer-rbac.json
"${K[@]}" apply -f observer.json
"${K[@]}" -n karta-metrics-poc rollout status \
  deployment/karta-workload-gpu-observer --timeout=120s
"${K[@]}" apply -f full-cycle.json
"${K[@]}" apply -f mig-cycle.json
```

After all three allocations are running and observer identity is healthy,
apply `contention-probe.json`. It must remain Pending and unallocated while
the full GPU is occupied. Unknown is the expected observer result for that
pending workload. It must subsequently run after the first claim releases.

The full-GPU Job and MIG Master run 120 seconds active, 240 seconds quiet,
90 seconds active, and 30 seconds quiet. The MIG Worker stays quiet for the
480-second test. These are finite CUDA activity tests, not distributed
training benchmarks. Run `acceptance.py` and `timing.py` after completion;
their local Prometheus forward must use the parent guide's port and path.
`browser.cjs` captures the verified historical low-activity timestamp.
The acceptance script also reads `cycle-claims-active.json`: save the current
claims into that file while the cycles are allocated, before they complete:

```sh
"${K[@]}" -n karta-metrics-workloads get resourceclaims -o json > cycle-claims-active.json
```

## Restore the regular demo

Save evidence first. Require test workloads complete and no active claims.
Remove only the test workload objects and claim templates. Remove the isolated
plugin and wait until its Pods have exited before restoring the normal device
plugin. If its ResourceSlices remain, remove only the names saved for this
run. Preserve any pre-existing ResourceSlices.

```sh
"${K[@]}" delete -f full-cycle.json -f mig-cycle.json \
  -f contention-probe.json -f claim-templates.json --ignore-not-found
"${K[@]}" delete -f plugin.json --ignore-not-found
"${K[@]}" patch clusterpolicies.nvidia.com cluster-policy \
  --type=merge --patch-file dcgm-restore-patch.json
"${K[@]}" patch clusterpolicies.nvidia.com cluster-policy \
  --type=merge --patch-file device-plugin-restore.json
```

Restore the original DCGM environment using the saved narrow patch. Remove
only the temporary DCGM DRA RBAC once mapping is disabled. The separate
observer's read-only DRA support may remain; it reads claims only for active
Pods with claims and continues to validate regular device-plugin allocations.

Require Ready nodes, healthy DCGM/Prometheus, and one full GPU plus two
3g.20gb resources. Resume the original CronJob only after those checks pass.
The parent report records the actual restoration outcome and evidence.


## Final outcome

Real full-GPU and static-MIG DRA probes, both activity cycles, all 14 live
acceptance checks, and all seven contention/lifecycle checks passed. All 57
dashboard queries and active/low-state browser checks passed. The waiting
full-GPU Job started three seconds after the previous consumer finished.

The regular device plugin and native DCGM environment were restored. New
full-GPU and MIG CUDA probes passed afterward. All six nodes, scrapes,
and Knative routing were healthy; the original finite CronJob was resumed.
See `final-state.json`, `restored-probe-results.json`, and the parent report.

GPUCluster migration, dynamic MIG, MPS, time slicing, shared claims,
ComputeDomains, and multi-node DRA are outside this run's verified scope.
