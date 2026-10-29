<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Deployed monitoring acceptance

This suite uses a separate, single-node local kind cluster named
`exporter-monitoring`. It does not use the operator matrix cluster or a remote
cluster. Run it with a final exporter image already built locally. A debug-image
run is diagnostic evidence only.

The suite starts before ServiceMonitor CRDs exist. It verifies the missing-CRD
Helm error, installs Prometheus Operator, and installs the exporter with the Karta
operator disabled. Prometheus must discover the ServiceMonitor and collect all
four metric families. The test then checks permission denial and recovery, a real
Helm upgrade, and resource removal when the exporter is disabled.

Before the permission-denial restart, the test waits until the exporter's
ServiceAccount cannot list or watch Deployments in the observed namespace.
After the upgrade restores both permissions, recovery requires a newly created
root UID and a healthy Deployment observation scraped after permission recovery.

A supplied NetworkPolicy selects only exporter Pods. It permits ingress from the
suite's Prometheus Pods and egress to the local Kubernetes API endpoints on TCP
443/6443. Endpoint addresses are discovered locally and never written to evidence.
An unauthorized Pod must connect before policy installation and fail afterward.
The exporter restarts under the policy before a new root is created. That root
must appear in Prometheus through new API connections. This checks
both fresh API reads and allowed metrics ingress. It does not add a chart policy
API or claim coverage of other CNIs.

## Run

Requirements: Docker, kind, kubectl, Helm, curl, shasum and the module's Go version.
The fixture pins kind's Kubernetes image to v1.34.0 and its digest, Calico to
v3.31.0, Prometheus Operator to v0.86.2, Prometheus to v3.7.1, and the connectivity
probe to BusyBox 1.36.1. `setup.sh` verifies SHA-256 checksums of the two downloaded
manifests before applying them. The checksums lock the reviewed release assets;
they are not upstream cryptographic signatures.

From the repository root:

```sh
export KUBECONFIG=/tmp/exporter-monitoring.kubeconfig
export MONITORING_EXPORTER_IMAGE=karta-metrics-exporter:FINAL_REVISION
export MONITORING_OUTPUT=/tmp/exporter-monitoring-results
karta-metrics-exporter/test/monitoring/setup.sh cluster
kind load docker-image "$MONITORING_EXPORTER_IMAGE" --name exporter-monitoring
mkdir -p "$MONITORING_OUTPUT"
cd karta-metrics-exporter
GOMAXPROCS=4 go test -v -tags=monitoring ./test/monitoring -timeout 25m \
  -ginkgo.v -ginkgo.no-color \
  -ginkgo.json-report="$MONITORING_OUTPUT/report.json" \
  -ginkgo.junit-report="$MONITORING_OUTPUT/junit.xml"
```

All kubectl and Helm operations specify the dedicated kubeconfig and
`kind-exporter-monitoring` context. Before mutations, the suite checks the node
name. Run against a fresh cluster: the missing-CRD check deliberately refuses an
existing Prometheus Operator installation.

The test leaves the monitoring infrastructure and workloads for inspection. It
writes the requested image reference before installation and the actual exporter
image ID immediately after deployment, before the monitoring checks. It verifies
that the image ID remains unchanged after upgrade. Acceptance checkpoints and
the Ginkgo reports record the checks that completed. Review reports for local
addresses before publishing. Do not treat compilation or dry-run output as
deployed acceptance.

Stop the cluster before running exporter scale benchmarks:

```sh
KUBECONFIG=/tmp/exporter-monitoring.kubeconfig \
  karta-metrics-exporter/test/monitoring/setup.sh stop
```

To resume the prepared cluster after a benchmark pause, run
`docker start exporter-monitoring-control-plane` and wait for that node to become
Ready using the explicit context above. Resume only when no benchmark is running.

To repeat from a clean state, remove only this fixture's cluster with
`kind delete cluster --name exporter-monitoring`, then run setup again.

## Upstream manifests

- [Calico v3.31.0 manifest](https://raw.githubusercontent.com/projectcalico/calico/v3.31.0/manifests/calico.yaml)
- [Prometheus Operator v0.86.2 bundle](https://github.com/prometheus-operator/prometheus-operator/releases/download/v0.86.2/bundle.yaml)
- [Prometheus Operator installation guide](https://prometheus-operator.dev/docs/getting-started/installation/)
