<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Reproduce the setup

Run commands from the repository root. Use a disposable/demo namespace on a
cluster where Karta and monitoring already exist. The supplied files retain the
namespaces `monitoring` and `karta-metrics-workloads`.

The commands below are portable adaptations of the completed deployment. Rendering
and validation are checked separately; a fresh cluster installation using this
packaged sequence has not been executed end to end.

## 1. Check prerequisites

The cluster needs an existing Karta operator/CRDs, Prometheus Operator and
Prometheus, kube-state-metrics, cAdvisor scraping, an ingress controller and a
StorageClass. The fixture set also requires Kubeflow Training Operator and
LeaderWorkerSet. Fake GPU operator 0.2.1 and its device plugin must already provide
schedulable simulated devices. This bundle does not install or replace those
shared components.

Prometheus must discover ServiceMonitors and PrometheusRules in `monitoring`.
The raw DCGM overview query expects `exported_namespace` and `exported_pod` labels
from the existing scrape target. Inspect target labels when using another setup.

```sh
export KUBE_CONTEXT=your-demo-context
export DEMO_DIR="$PWD/docs/karta-metrics-exporter/grafana/demo"
export DEMO_OUTPUT="$(mktemp -d)"
kubectl --context "$KUBE_CONTEXT" get nodes
kubectl --context "$KUBE_CONTEXT" api-resources
```

The saved exporter exclusions reflect the APIs installed during the recorded run.
Review `manifests/exporter-values.yaml` before installing. Enable a definition only
when its expected API version is available. The chart retains its read-only RBAC
and resource-exclusion defaults.

## 2. Install the pinned exporter

Create `monitoring` if it does not already exist. Download the public prerelease:

```sh
gh release download exporter-runtime-9c14b074d03d \
  --repo AviadHayumi/workload-map --dir "$DEMO_OUTPUT" \
  --pattern 'karta-*.tgz' --pattern 'karta-metrics-exporter-linux-amd64' \
  --pattern SHA256SUMS --pattern image-digest.txt
(cd "$DEMO_OUTPUT" && shasum -a 256 -c SHA256SUMS)
helm --kube-context "$KUBE_CONTEXT" upgrade --install karta-metrics-exporter \
  "$DEMO_OUTPUT/karta-0.0.0-runtime.9c14b074.tgz" \
  --namespace monitoring --create-namespace --skip-crds \
  -f "$DEMO_DIR/manifests/exporter-values.yaml"
kubectl --context "$KUBE_CONTEXT" apply --server-side \
  -f "$DEMO_DIR/manifests/exporter-servicemonitor.yaml" \
  -f "$DEMO_DIR/manifests/dashboard-rules.yaml"
```

The chart values disable installation of the operator, webhook and CRD upgrader.
They pin the exporter image built from `exporter-controller-runtime`. The helper
image used later has a different digest and runs different binaries.

## 3. Render and install Grafana

Supply your own hostname, reachable Prometheus URL, StorageClass, ingress class
and existing TLS Secret in `monitoring`. The example URL is a placeholder.

```sh
python3 "$DEMO_DIR/scripts/render_setup.py" \
  --grafana-host grafana.example.com \
  --prometheus-url http://prometheus.monitoring.svc:9090 \
  --storage-class your-storage-class --ingress-class nginx \
  --tls-secret your-grafana-tls-secret --output-dir "$DEMO_OUTPUT"
```

Grafana permits anonymous Viewer access for this demo and disables the login form.
This makes the dashboards readable to anyone who can reach that route. The
administrator credential is separate and never published. Create it once:

```sh
python3 "$DEMO_DIR/scripts/create_grafana_admin.py" --context "$KUBE_CONTEXT"
```

On a reinstall, retain the existing `karta-monitoring-admin` Secret instead of
running the credential command again. Inspect rendered files, then apply:

```sh
kubectl --context "$KUBE_CONTEXT" apply --server-side --field-manager=karta-demo-setup \
  -f "$DEMO_OUTPUT/dashboard.json" -f "$DEMO_OUTPUT/grafana.json"
kubectl --context "$KUBE_CONTEXT" -n monitoring rollout status deployment/karta-grafana
```

Grafana's root URL is `https://YOUR_HOST/karta/grafana/`. Dashboard UIDs are
`karta-workload-overview`, `karta-structure-activity` and `karta-metric-samples`.
The renderer always reads the latest three JSON files beside this demo directory.

## 4. Create bounded demo workloads

The namespace must exist before validating namespaced fixtures. Create
`karta-metrics-workloads` if it is absent, then inspect and apply the fixture set:

```sh
kubectl --context "$KUBE_CONTEXT" apply --server-side --dry-run=server \
  -f "$DEMO_DIR/manifests/workloads.json"
kubectl --context "$KUBE_CONTEXT" apply --server-side --field-manager=karta-demo-setup \
  -f "$DEMO_DIR/manifests/workloads.json"
kubectl --context "$KUBE_CONTEXT" create -f "$DEMO_DIR/manifests/initial-job.json"
```

The initial Job is optional on repeat runs. The recurring generator creates new
unique Jobs every two minutes and PyTorchJobs every six minutes. Expect temporary
extra GPU requests while finite PyTorchJobs run. GPU usage is simulated; containers
perform CPU loops instead of actual GPU training.

## 5. Render GPU helpers for the existing nodes

List topology ConfigMap names without reading their contents. The node name is
the suffix after `topology-`. Review the list and retain the real fake GPU nodes
that demo Pods may use. Do not include the top-level `topology` ConfigMap.

```sh
kubectl --context "$KUBE_CONTEXT" -n gpu-operator get configmaps \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
```

Write a local JSON array to `$DEMO_OUTPUT/nodes.json`, for example
`["fake-node-a", "fake-node-b"]`. The renderer supports one to eight nodes, matching
the tested deployment envelope. These names are used in named read permissions.

```sh
python3 "$DEMO_DIR/scripts/render_gpu.py" \
  --nodes-file "$DEMO_OUTPUT/nodes.json" --output "$DEMO_OUTPUT/gpu-helpers.json"
kubectl --context "$KUBE_CONTEXT" apply --server-side --dry-run=server \
  -f "$DEMO_OUTPUT/gpu-helpers.json"
```

Create the round-state ConfigMap once. Do not overwrite its data on updates:

```sh
kubectl --context "$KUBE_CONTEXT" create -f "$DEMO_DIR/manifests/round-state.json"
kubectl --context "$KUBE_CONTEXT" apply --server-side --field-manager=karta-gpu-demo \
  -f "$DEMO_OUTPUT/gpu-helpers.json"
```

On repeat runs, skip `create` when the round-state ConfigMap already exists.
Reapplying `gpu-helpers.json` preserves that state. The randomizer will start on the
next minute boundary. Its selected workload groups last ten minutes.

The source only reads named topology ConfigMaps. The workload observer only reads
demo Pods and round metadata. The randomizer can list/patch Pods and get/update the
named round ConfigMap in the demo namespace. Per-device observers have no API token.
No helper receives cluster-wide write permissions or host filesystem mounts.

## 6. Verify samples and histories

```sh
kubectl --context "$KUBE_CONTEXT" -n monitoring get deployments \
  karta-metrics-exporter karta-grafana karta-demo-dcgm \
  karta-workload-gpu-observer karta-gpu-observer
kubectl --context "$KUBE_CONTEXT" -n monitoring get cronjob gpu-demo-randomizer
kubectl --context "$KUBE_CONTEXT" -n karta-metrics-workloads get cronjob demo-generator
python3 "$DEMO_DIR/scripts/verify.py" \
  --grafana-url https://grafana.example.com/karta/grafana \
  --namespace karta-metrics-workloads --workload demo-pytorchjob \
  --output "$DEMO_OUTPUT/verification.json"
```

Allow Prometheus monitor discovery to refresh. Then allow a full three-minute
low-activity window. Select the last 15 minutes in Grafana. Earlier observer
history cannot be reconstructed. Click a workload to see its graph, utilization
history and activity timeline. Empty failure queries are expected when no failures
exist. Unknown means evidence is missing, not zero GPU use.

## 7. Pause, restore and remove the demo

Pause new resource creation and changes to simulated GPU settings independently:

```sh
kubectl --context "$KUBE_CONTEXT" -n karta-metrics-workloads patch cronjob demo-generator \
  --type merge -p '{"spec":{"suspend":true}}'
kubectl --context "$KUBE_CONTEXT" -n monitoring patch cronjob gpu-demo-randomizer \
  --type merge -p '{"spec":{"suspend":true}}'
```

Suspending a CronJob does not stop an already running Job. Existing Pod annotations
remain effective. To return a demo Pod to the fake operator's default utilization,
first pause the randomizer and let its active Job finish, then remove
`run.ai/simulated-gpu-utilization` from that specific Pod. Removing the annotation
resets simulator behavior; it does not delete the workload.

Before a dashboard update, save its ConfigMap locally with `kubectl get configmap
karta-grafana-dashboard -n monitoring -o json` using the explicit context. Reapply
that saved file with the same field manager to roll back the dashboard.

To remove helpers, delete only the resources rendered in `gpu-helpers.json`, then
remove the round-state ConfigMap if no longer needed. Preserve shared GPU operator,
Karta, Prometheus and ingress resources. Do not delete the shared `monitoring`
namespace. Remove fixture resources only after checking for other namespace users.
Grafana's PVC is persistent data and should be retained unless deletion is intended.

## Optional: rebuild the helper image

The pinned image is sufficient for installation. To change or audit its code, use
Python 3.12+ and Go. Source files are stored as text snapshots so the demo modules
remain separate from the exporter build. The build script materializes normal Go
files in a new directory, verifies the pinned upstream archive, runs race tests,
and builds Linux amd64 binaries.

```sh
python3 "$DEMO_DIR/scripts/build_helpers.py" --output-dir /tmp/karta-gpu-demo-build
```

With `crane` installed and authenticated to your own registry, package and publish
those binaries on the pinned nonroot base. Choose your own image tag:

```sh
crane append --platform linux/amd64 \
  --base gcr.io/distroless/static-debian13@sha256:336e53e9941facdb69b755e6fd2c63f5400abd3b1ba47bb17ebafc2b1547cebd \
  --new_layer /tmp/karta-gpu-demo-build/helpers-layer.tar \
  --new_tag YOUR_REGISTRY/karta-gpu-demo:YOUR_TAG
```

Record the resulting digest and update the renderer before deploying a custom
build. The source and dependency versions are pinned; a new image digest is still
expected when rebuilding packaging or changing code. The historical release
workflow in `sources/` is for the exporter release, not this helper image.
