<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Recreate the complete Karta metrics playground

This guide builds the workload demo, native NVIDIA DCGM collection, Prometheus, Grafana, activity observers and scheduled workloads on a Kubernetes test cluster. It starts with a reachable Kubernetes cluster; provisioning its machines, CNI and storage driver is a prerequisite.

The adjacent files contain the deployment inputs. No checkout from the original session is required. Copy this whole `recreate` directory to another machine, not just this Markdown file. Container images must remain reachable; this is not an air-gapped bundle.

The reference environment ran Kubernetes v1.35.5 on Linux amd64, with one worker advertising 32 fake GPUs. The full stack and its browser flows were verified there. The portable bundle has rendering and API dry-run checks; it has not been deployed again onto a second empty cluster. Keep that distinction when reporting validation.

## 1. Understand what will run

| Part | Purpose |
| --- | --- |
| Fake GPU operator 0.2.1 | Registers 32 GPU devices with kubelet and allocates them to Pods |
| NVIDIA mock NVML | Implements the GPU driver API with controllable device values |
| NVIDIA DCGM 4.6.1 / DCGM Exporter 4.8.4 | Reads mock NVML and exports native DCGM metric families with kubelet Pod attribution |
| Karta operator 0.2.6 | Reconciles Karta definitions; reuse it if already installed |
| Karta exporter | Exports workload identity, component structure, status and observation health |
| Prometheus and kube-state-metrics | Scrape and join measurements with workload identity |
| Separate GPU observers | Measure utilization and sustained low activity without changing Karta's public metrics |
| Grafana | Workload overview, component structure, activity history and raw metric samples |
| Two CronJobs | Rotate activity scenarios and submit finite workloads |

The DCGM software is unmodified NVIDIA software. GPU values are emulated. The Kubernetes controllers, Pods, device allocations, Pod UIDs and metric collection are real. The fixture containers do not execute real GPU training or inference.

The native metric path is:

```text
Pod controls -> mock NVML -> native DCGM Exporter -> Prometheus -> Grafana
                              |                         ^
                              +-> GPU observers --------+
Kubelet PodResources -> native Pod UID labels            |
Karta exporter -> workload/component/Pod UID identity ---+
```

Four DCGM processes expose eight devices each. This accommodates the pinned mock backend's eight-device limit. The setup expects exactly one 32-device worker. Other topologies require changes to the shard configuration, not just a different Helm value.

## 2. Prerequisites and target selection

Required locally: Bash, Python 3, kubectl, Helm 3 and `htpasswd` with bcrypt support. The packaged helpers use Python's standard library. Docker is not needed on the client when using the pinned images.

Required in the cluster:

- A Linux amd64 worker for the mock NVML/DCGM containers and GPU fixtures.
- A working default StorageClass with dynamic provisioning. Use that same class below; some workload operators create PVCs using the default class. Prometheus requests 10 GiB, Grafana 2 GiB, and several workload fixtures request additional PVCs.
- Capacity for the workload operators and fixtures. The reference used five nodes; that is a tested topology, not a measured minimum.
- Permission to install CRDs, operators and RBAC, and to mount kubelet's PodResources socket and device-plugin checkpoint read-only.
- Registry access to the pinned images in `IMAGES.txt`, plus images referenced by the bundled operator charts/manifests.

Start in this directory and select the target explicitly:

```bash
export DEMO_CONTEXT='your-test-context'
export DEMO_GPU_NODE='your-linux-amd64-worker'
export DEMO_STORAGE_CLASS='your-default-storage-class'

k() { kubectl --context "$DEMO_CONTEXT" "$@"; }
h() { helm --kube-context "$DEMO_CONTEXT" "$@"; }

k cluster-info
k get nodes -o wide
k get storageclass
h list -A
k get crd kartas.run.ai prometheuses.monitoring.coreos.com --ignore-not-found
```

Use a dedicated test cluster. The demo creates three namespaces and several cluster-scoped roles and definitions. Installing workload operators also introduces cluster-wide CRDs and admission webhooks. Do not run the complete installation against the presentation cluster.

Run the bundle integrity check before rendering:

```bash
python3 verify_bundle.py
```

## 3. Render the configuration

The namespaces are intentionally fixed:

- `karta-metrics-poc`: exporters, observers, monitoring and CronJobs.
- `karta-metrics-workloads`: long-lived workload fixtures.
- `karta-finite-workloads`: workloads that finish.

Choose one access mode.

Local access requires no DNS, ingress controller or certificate:

```bash
python3 render.py --node "$DEMO_GPU_NODE" \
  --storage-class "$DEMO_STORAGE_CLASS" --local
```

For HTTPS access through an existing nginx ingress or gateway, use the hostname that routes to that ingress. A valid certificate must be provided either by the existing gateway or by a TLS Secret in `karta-metrics-poc`:

```bash
export DEMO_HOST='karta-demo.example.com'
python3 render.py --node "$DEMO_GPU_NODE" \
  --storage-class "$DEMO_STORAGE_CLASS" --host "$DEMO_HOST"
```

If using a dedicated TLS Secret, also pass `--tls-secret karta-demo-tls`. Create that Secret after the namespace step using the cluster's normal certificate process. The guide does not generate or copy private keys. If no ingress controller is installed, use local access first.

For an existing kube-state-metrics installation with different names, add `--monitoring-namespace NAME --monitoring-release RELEASE`. These select the Service that the demo scrapes. Defaults match the bootstrap command below.

Rendering writes `rendered/` and performs no Kubernetes calls. Inspect `rendered/settings.json` and the manifests before applying them. Changing access mode requires rendering and applying the configuration again; Grafana's root URL is part of its Deployment.

```bash
k apply -f rendered/namespaces.json
```

## 4. Supply Prometheus Operator and kube-state-metrics

Skip this installation if a suitable Prometheus Operator and kube-state-metrics are already installed. Do not install a competing operator for the same Prometheus CRDs. Confirm that the existing operator watches `karta-metrics-poc`.

For a fresh cluster:

```bash
h upgrade --install kube-prometheus-stack \
  charts/kube-prometheus-stack-68.3.0.tgz \
  --namespace monitoring --create-namespace \
  --values monitoring-operator-values.yaml --wait --timeout 10m

k wait --for=condition=Established crd/prometheuses.monitoring.coreos.com --timeout=2m
k wait --for=condition=Established crd/servicemonitors.monitoring.coreos.com --timeout=2m
k -n monitoring get deployments,services
k -n kube-system get services -l app.kubernetes.io/name=kubelet
```

These values install the shared operator and kube-state-metrics, while the demo's own Prometheus and Grafana are installed later. Kubelet Service discovery supplies cAdvisor CPU and memory measurements. The copied lab cAdvisor scrape uses `insecureSkipVerify` for kubelet serving certificates, matching the reference cluster. A cluster with a validated kubelet serving CA should use verified TLS instead; this does not change chart webhook TLS defaults.

## 5. Supply 32 fake GPUs

Skip the Helm install and relabeling if the target worker already has the required fake pool. Verify `nvidia.com/gpu: 32` in its allocatable resources. Reusing an existing FGO allocation backend is how the reference cluster was configured.

For a fresh CPU-only test worker:

```bash
k label node "$DEMO_GPU_NODE" \
  run.ai/simulated-gpu-node-pool=karta-demo \
  nvidia.com/gpu.deploy.device-plugin=true

h upgrade --install fake-gpu-operator \
  charts/fake-gpu-operator-0.2.1.tgz \
  --namespace gpu-operator --create-namespace \
  --values fake-gpu-values.yaml --wait --timeout 10m

k -n gpu-operator get pods -o wide
k get node "$DEMO_GPU_NODE" \
  -o jsonpath='{.status.allocatable.nvidia\.com/gpu}{"\n"}'
```

Wait until the last command reports `32`. The worker must be a real kubelet node, not a KWOK-only Node object. Its kubelet must expose `/var/lib/kubelet/pod-resources` and `/var/lib/kubelet/device-plugins/kubelet_internal_checkpoint`.

This recipe keeps FGO's device plugin and installs native DCGM separately. Do not additionally enable the full NVIDIA GPU Operator, a second device plugin, or the alternative FGO mock backend. They are different installation paths.

## 6. Install workload operators

On a fresh test cluster:

```bash
bash install_workload_operators.sh
```

The script uses the bundled charts and manifests and waits for key controllers before continuing. On a cluster with existing operators, inspect the script and run only the missing installations. Preserve their existing ownership instead of forcing Helm adoption or SSA conflicts.

| Operator | Pin |
| --- | --- |
| cert-manager | 1.14.4 |
| JobSet | 0.10.1 |
| LeaderWorkerSet | 0.7.0 |
| KubeRay | 1.6.2 |
| Kubeflow training operator | 1.9.0, PyTorch scheme only |
| MPI operator | 0.8.2, v2beta1 API |
| Grove | 0.1.0-alpha.10-rc1 |
| Dynamo platform | 1.2.1 |
| NIM operator | 3.0.2, digest-pinned controller image |
| Knative Serving / Kourier | 1.15.2 / 1.15.0 |
| KServe | 0.13.1, with kube-rbac-proxy 0.18.0 |
| Milvus operator | 1.3.7 |

Grove uses the default scheduler; no KAI scheduler installation is required by this bundle. Dynamo reuses the installed Grove operator. KServe uses Kourier and the packaged setting that disables Istio VirtualService integration.

```bash
k get deployments -A
k get crd | grep -E 'jobsets|leaderworkersets|rayclusters|pytorchjobs|mpijobs|podcliquesets|dynamographdeployments|nimservices|inferenceservices|milvus'
```

## 7. Install or reuse Karta, then install the exporter

If the cluster already has a healthy Karta operator and CRD, reuse them. In particular, do not overwrite a Run:AI-managed CRD using a second Helm release.

For a fresh cluster without Karta:

```bash
h upgrade --install karta-operator charts/karta-operator \
  --namespace karta-system --create-namespace \
  --set fullnameOverride=karta-operator \
  --set image.tag=0.2.6 --wait --timeout 5m

k wait --for=condition=Established crd/kartas.run.ai --timeout=2m
```

Apply the relevant definitions and exporter:

```bash
k apply --server-side -f rendered/definitions.json
k apply -f rendered/exporter.json
k -n karta-metrics-poc rollout status deployment/karta-exporter --timeout=5m
```

The exporter file is the exact rendered manifest from the validated Helm deployment, with runtime ownership metadata removed and the image digest retained. It includes its ServiceAccount, aggregated read-only RBAC, Service and two-replica Deployment. It does not install or change the Karta CRD. This avoids depending on a newer chart with different values while reproducing this POC.

The definitions include the deployed MPI, Dynamo, RayJob, RayService and KServe compatibility fixes. Other supported fixture types use the exporter's built-in catalog. Inspect differences before applying definitions over another installation's custom definitions. Do not use `--force-conflicts` on the live cluster as a shortcut.

## 8. Create credentials and workload prerequisites

```bash
python3 bootstrap_secrets.py --context "$DEMO_CONTEXT"
k apply -f rendered/fixture-pvc.json
```

The helper creates a fresh random Grafana administrator password and a bcrypt Secret for the Prometheus ingress. Passwords are passed through stdin, never shell arguments or saved files. Existing Secrets are kept.

It also creates the three NIM fixture placeholder Secrets. They contain no working NGC or Hugging Face credentials. The bundled fixture images do not need those services; replacing the fixtures with real NIM/model containers requires real credentials through Kubernetes Secrets.

If a StorageClass uses WaitForFirstConsumer, a PVC may remain Pending until its workload Pod is scheduled. Do not block installation waiting for those claims before submitting the workloads.

## 9. Deploy native DCGM, monitoring and observers

```bash
k apply -f rendered/native-dcgm.json

# Create mutable state only once; AlreadyExists means keep the existing state.
k create -f rendered/bootstrap-state.json

k apply --server-side -f rendered/dashboard.json
k apply -f rendered/monitoring.json
k apply -f rendered/cronjobs-paused.json

k -n karta-metrics-poc rollout status deployment/karta-real-dcgm --timeout=5m
k -n karta-metrics-poc rollout status deployment/karta-grafana --timeout=5m
k -n karta-metrics-poc rollout status statefulset/prometheus-karta-poc --timeout=5m
k -n karta-metrics-poc get pods,pvc
```

On a rerun, `create` can report AlreadyExists; continue only for that expected condition. Do not replace it with `apply`: applying empty scenario state would reset the current round.

Native DCGM uses four ports, 9400-9403. A helper on 9600 forwards native values to the existing observer and writes the mock NVML controls. Prometheus scrapes the native ports directly. It does not use FGO's legacy DCGM-compatible status exporter as the source for this dashboard.

The native DCGM Pod should show `6/6` Ready. Its startup helper reads the actual device-plugin IDs. Do not replace those IDs with FGO telemetry topology UUIDs: they can differ and break native Pod attribution.

The dashboard ConfigMap needs server-side apply because its content exceeds the annotation size limit of client-side apply. Prometheus owns its rule ConfigMaps and generated PVCs; the bundle does not copy those generated objects.

For HTTPS mode, create or confirm the TLS configuration described in step 3, then:

```bash
k apply -f rendered/ingress.json
```

The paths are `/karta/grafana/` and `/karta/prometheus/`. The ingress expects class `nginx`; adapt that file explicitly if the cluster uses another gateway. Grafana allows anonymous Viewer access. Prometheus's ingress requires basic authentication.

## 10. Submit the fixtures and start scheduled activity

```bash
k apply -f rendered/workloads.json
k -n karta-metrics-workloads get pods,pvc
k -n karta-metrics-poc logs deployment/karta-exporter --tail=30
```

Some completed Jobs are expected. Resolve missing images, unschedulable Pods or operator errors before starting the activity randomizer. Its first assignment requires healthy Karta attribution.

```bash
k apply -f rendered/cronjobs.json
k -n karta-metrics-poc get cronjobs
```

The GPU scenario CronJob runs every minute and changes assignments in ten-minute rounds. The finite workload CronJob runs every five minutes, alternating PyTorchJob and JobSet. Its fixtures run for about 90 seconds, exit successfully, and remain visible for about 45 minutes.

The main namespace contains Deployment, StatefulSet, Pod, Job, CronJob, PyTorchJob, MPIJob, JobSet, LeaderWorkerSet, RayCluster, RayJob, RayService, Grove, Dynamo, Knative, KServe, NIMService, NIMCache and Milvus fixtures. Operator-created subresources account for some additional workload roots.

The eight GPU workload roots are Dynamo, MPIJob, RayCluster, Grove, LeaderWorkerSet, JobSet, NIMService and PyTorchJob. Together they allocate 12 devices. The remaining 20 devices are unallocated.

## 11. Open Grafana and inspect the actual metrics

For local mode, run each command in a separate terminal with the same context variable:

```bash
kubectl --context "$DEMO_CONTEXT" -n karta-metrics-poc \
  port-forward service/karta-grafana 13000:3000
```

```bash
kubectl --context "$DEMO_CONTEXT" -n karta-metrics-poc \
  port-forward service/karta-prometheus 19090:9090
```

Open <http://127.0.0.1:13000/karta/grafana/>. Prometheus is at <http://127.0.0.1:19090/karta/prometheus/>. Port forwarding binds locally; the Prometheus ingress's basic authentication is not in this direct path.

For HTTPS mode, open `https://YOUR_HOST/karta/grafana/`. Grafana viewing needs no username or password. Administrator credentials are in `karta-metrics-poc/karta-monitoring-admin`, keys `username` and `password`. Do not put them into the guide or share them with dashboard viewers.

Use the dashboard in this order:

1. GPU activity by workload: complete-workload utilization and activity decisions.
2. Click a workload: Workload, Component, Component Instance, Replica Group and Pod structure, resolved by current UID.
3. Open component or device sections for narrower measurements.
4. Open Metric samples and choose `DCGM_FI_DEV_GPU_UTIL`. Inspect its actual `UUID`, `pod`, `pod_uid`, `namespace`, `container` and value.
5. Use the panel menu's Inspect > Query to see the executed PromQL; Inspect > Data exports the samples as CSV.

Native DCGM samples do not contain Karta's workload labels. The sample dashboard uses a Pod UID membership filter when a workload is selected, preserving native values and labels. Do not rename `pod_uid` to a workload UID or join solely on Pod names.

## 12. Verify the deployment

Run these expressions in Prometheus, or use the local API with `curl -G --data-urlencode 'query=EXPRESSION' http://127.0.0.1:19090/karta/prometheus/api/v1/query`.

| PromQL | Expected after the fixtures settle |
| --- | --- |
| `count(DCGM_FI_DEV_GPU_UTIL)` | 32 |
| `count(DCGM_FI_DEV_GPU_UTIL{pod_uid!=""})` | 12 |
| `count(up{job="karta-real-dcgm"} == 1)` | 4 |
| `count(max by(workload_uid)(karta_exporter_observation_healthy{scope="workload",namespace="karta-metrics-workloads",reason="none"}) == 1)` | 32 in the reference fixture set |
| `count by(state)(workload_gpu_observer_state == 1)` | Typically 4 Active, 4 Low activity, 24 No GPUs after a stable round |
| `workload_gpu_observer_window_seconds` | 180 |
| `workload_gpu_observer_threshold_ratio` | 0.05 |
| `max(DCGM_FI_DEV_GPU_UTIL{pod_uid=""})` | 0 |

Native measurement and attribution examples:

```promql
DCGM_FI_DEV_GPU_UTIL{namespace="karta-metrics-workloads"}

DCGM_FI_DEV_GPU_UTIL{namespace="karta-metrics-workloads",pod_uid!=""}
and on(namespace,pod_uid)
max by(namespace,pod_uid)(
  karta_pod_workload_info{namespace="karta-metrics-workloads",workload="demo-dynamo-gpu"}
)

workload_gpu_observer_state{state="low_activity"} == 1

workload_gpu_observer_low_activity_observation_seconds
```

Low activity requires all assigned GPUs at or below 5% for 180 seconds of valid observations. Warming is expected during a new quiet period. Unknown means missing or invalid evidence, not idle. The finite namespace has its own Karta exporter but is outside the GPU observer's namespace scope, so CPU-only finite workloads may display No GPU telemetry.

Verify completion separately:

```bash
k -n karta-finite-workloads get pytorchjobs,jobsets,pods
k -n karta-metrics-poc get jobs
k -n karta-metrics-poc logs deployment/karta-real-dcgm -c dcgm-bridge --tail=30
```

Within two five-minute schedule slots plus runtime, both a PyTorchJob and JobSet should finish. Completed Pods should have exit code 0. Read the finite controller Job's logs if nothing is submitted.

## 13. Control GPU activity manually

Pause the randomizer before manual control so its next run does not overwrite the annotations:

```bash
k -n karta-metrics-poc patch cronjob gpu-demo-randomizer \
  --type merge -p '{"spec":{"suspend":true}}'
k -n karta-metrics-workloads get pods

# Choose an actual GPU Pod from the output.
export DEMO_POD='your-gpu-pod'
k -n karta-metrics-workloads annotate pod "$DEMO_POD" \
  run.ai/simulated-gpu-utilization='0-0' --overwrite
```

For a whole-workload idle test, set every GPU Pod belonging to that workload to the quiet range. Setting only one worker to zero is insufficient if another assigned GPU remains active. Allow the mock-control, DCGM collection and Prometheus scrape intervals to pass, then wait the full three-minute observer window.

Use `70-90` for an active range. Resume automatic scenarios with:

```bash
k -n karta-metrics-poc patch cronjob gpu-demo-randomizer \
  --type merge -p '{"spec":{"suspend":false}}'
```

The bridge writes explicit static NVML values. Do not restore a dynamic mock range with min=0 and max=0: in the pinned mock library that means random 0-100, not zero. Temperature and power are fixed profile values; this scenario controller varies utilization and assigned framebuffer memory.

## 14. Troubleshooting and limits

| Symptom | Check or correction |
| --- | --- |
| Native DCGM starts with fewer devices | Exactly 32 registered IDs are required. Inspect the selected worker and FGO device plugin. |
| DCGM has no Pod labels | UUIDs must match kubelet allocation IDs, not topology UUIDs. Check the startup helper and PodResources mount. |
| Bridge reports changed inventory | On the dedicated test cluster, restart `deployment/karta-real-dcgm`; the startup helper rediscovers IDs. |
| All GPU activity is Unknown | Check bridge logs, native scrape targets, source health, Pod UID consistency and Karta health. |
| Quiet workload stays Warming | Check every assigned GPU, observation gaps and the last scenario change. Wait 180 valid seconds. |
| PVC remains Pending | Check default StorageClass, capacity, access mode and WaitForFirstConsumer. Do not copy old binding annotations or selected-node hints. |
| Dashboard ConfigMap apply fails on annotation size | Use server-side apply for `rendered/dashboard.json`. |
| Helm reports existing CRD ownership | Reuse the existing operator/CRD and skip its bootstrap. Do not force adoption. |
| MPI attribution is unhealthy | Check the served MPI API is v2beta1 and the relevant Karta definition matches it; inspect obsolete v1 definitions before any scoped removal. |
| NIM/NIMCache Pod cannot start | Check the three fixture Secrets and dynamically provisioned PVCs. These fixture Secrets are placeholders only. |
| CPU/memory panels are empty | Check kube-state-metrics Service labels and kubelet/cAdvisor targets. |
| A structure view shows no selected workload | Enter from an overview workload link or select a current workload UID. Old UIDs are never portable. |
| Finite fixtures are missing | Check `finite-workload-submitter`, its Role/RoleBinding and controller Job logs. |

The bridge's freshness timestamp is the time of a successful DCGM HTTP scrape, not the age of an internal driver sample. A native exporter returning frozen cached values with HTTP 200 cannot be detected by that timestamp. Missing endpoints, incomplete inventory and identity mismatches do fail closed.

Low GPU utilization does not prove application idleness or that resources can safely be reclaimed. This environment demonstrates attribution and observation behavior. It does not validate all DCGM metrics, CUDA execution, multi-node topology, MIG or production capacity.

## 15. Update or remove this test installation

For dashboard-only updates, edit the dashboard asset, render again and apply only `rendered/dashboard.json` with server-side apply. For script changes embedded in native DCGM ConfigMaps, apply the native manifest and restart that Deployment so its processes load the new script. Restarts reset the continuous evidence window.

To stop new work while retaining data:

```bash
k -n karta-metrics-poc patch cronjob gpu-demo-randomizer \
  --type merge -p '{"spec":{"suspend":true}}'
k -n karta-metrics-poc patch cronjob finite-workload-submitter \
  --type merge -p '{"spec":{"suspend":true}}'
```

For removal, first delete the fixture roots using `rendered/workloads.json` while their operators are still running, and wait for their finalizers to finish. Delete the finite workload namespace while PyTorch and JobSet controllers are still available. Then remove the demo monitoring resources, exporter, native DCGM resources, fixture PVC and remaining demo namespaces. Namespace/PVC deletion discards local monitoring data; export anything needed first.

Remove Karta definitions or uninstall workload operators only if this installation created and exclusively owns them. Do not delete shared CRDs or strip finalizers to hurry cleanup. The bundle intentionally does not provide an unconditional cluster-wide teardown command.

## Bundle contents

- `assets/`: clean manifests; no kubeconfig, administrator credentials, old object UIDs or bound PV identities.
- `charts/`: pinned Helm chart archives and the Karta 0.2.6 operator chart.
- `operators/`: pinned upstream manifests and the compatibility settings used by this setup.
- `render.py`: substitutes node, storage class and access URLs; does not contact Kubernetes.
- `bootstrap_secrets.py`: creates missing Secrets without replacing existing ones or printing passwords.
- `install_workload_operators.sh`: ordered operator installation with readiness gates.
- `PROVENANCE.json`, `operators/SOURCES.json`, `IMAGES.txt`: origin and image records.
- `SHA256SUMS.json`, `verify_bundle.py`: local file integrity check.
- `VALIDATION.md`: checks performed on this portable bundle and what has not been redeployed.

Keep the source image digests when recreating this version. Updating the Karta exporter or operator pins is a separate change that should include another workload and dashboard acceptance run.
