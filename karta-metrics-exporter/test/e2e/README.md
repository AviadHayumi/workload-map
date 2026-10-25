<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter operator matrix

This Ginkgo suite installs the exporter chart with the Karta operator disabled.
It creates workload objects in separate labeled namespaces and scrapes the real
exporter Service through a loopback port-forward. It requires the operators and
images installed by the repository's pinned `hack/e2e/up.sh` workflow.

Use an isolated kind cluster and load the exporter image before running:

```sh
cd karta-metrics-exporter
KUBECONFIG=/tmp/exporter-e2e.kubeconfig \
EXPORTER_E2E_CONTEXT=kind-exporter-e2e \
EXPORTER_E2E_IMAGE=karta-metrics-exporter:e2e \
EXPORTER_E2E_INCLUDE_BARE_PODS=true \
EXPORTER_E2E_OUTPUT=/tmp/exporter-e2e-results \
go test -v -tags=e2e ./test/e2e -timeout 90m -ginkgo.v \
  -ginkgo.json-report=/tmp/exporter-e2e-results/report.json \
  -ginkgo.junit-report=/tmp/exporter-e2e-results/junit.xml
```

Run the `pod` case again without `EXPORTER_E2E_INCLUDE_BARE_PODS` to verify the
default catalog exclusion. It checks that a Ready bare Pod produces no workload
or Pod-info series. The opt-in run clears that exclusion and checks its metrics.

For a small first run, append `-ginkgo.focus='job-zero|lws'`. The suite rejects an
unexpected kubeconfig context. Helm and kubectl receive the kubeconfig and
context explicitly. It uninstalls its exporter release and deletes its workload
namespaces during cleanup. It does not install or remove operators.

## Assertions

Cases reuse `test/e2e/flows/testdata` for Deployment, StatefulSet, CronJob,
bare Pod, LWS, JobSet, RayCluster, RayJob,
PyTorchJob, MPIJob, Knative Service, KServe InferenceService and
LLMInferenceService, Milvus, Grove, Dynamo, NIMService and Sandbox. RayService and
its small application ConfigMap retain the existing exporter matrix fixtures.
Completed and failed native Jobs run real Pods, then remove those terminal Pods
to verify root status without Pod membership. A suspended Job starts with no Pods.

Raw controller conditions or status fields establish readiness. The oracle
walks controller owner references directly through the Kubernetes API. It does
not call Karta SDK evaluation or exporter Discovery. It checks:

- The actual root and Pod UIDs, root GVK and creation timestamp.
- Every supported status phase, including zero-valued phases.
- Exact component, instance and replica-group counts from each fixture.
- Per-Pod role labels where the fixture contains multiple component roles.
- The configured `team` label on both Pod and status metrics.
- Label replacement after an update and complete series removal after deletion.
- Valid root and Pod resource collection, with no workload failure row.

Each passing stage saves the raw root, owned Pods and Prometheus exposition under
`EXPORTER_E2E_OUTPUT`. The suite records the image and evidence directory in its
Ginkgo report. A failed assertion remains a failure even if another case passes.

Dynamo uses the repository's CPU mocker backend. NIM uses the CPU test server, and
LLMInferenceService uses its existing tiny HTTP server. These prove operator
reconciliation and exporter attribution, not GPU inference or model quality.

## Coverage boundaries

The matrix contains 23 cases across the current catalog workload families.
The three native Job cases cover suspended, completed and failed roots with zero
Pods. Other cases cover the fixture states described above, not every lifecycle
state for every operator.

| API family | Version exercised | Boundary |
| --- | --- | --- |
| Native Deployment, StatefulSet | apps/v1 | Running |
| Native Job, CronJob | batch/v1 | Job zero-Pod states; scheduled CronJob |
| Native Pod | v1 | Ready; default exclusion and explicit opt-in |
| LeaderWorkerSet | v1 | Leader and worker |
| JobSet | v1alpha2 | Replicated Job |
| RayCluster, RayJob, RayService | v1 | Running |
| PyTorchJob, MPIJob | v1, v2beta1 | Initializing and Running predicates overlap |
| Knative Service | v1 | Ready revision |
| KServe InferenceService | v1beta1 | Ready predictor |
| LLMInferenceService | v1alpha2 | Main component; v1alpha1 mapping not exercised |
| Milvus | v1beta1 | Standalone |
| Grove PodCliqueSet | v1alpha1 | Two Pods in one replica group |
| DynamoGraphDeployment | v1alpha1 | CPU mocker; v1beta1 mapping not exercised |
| NIMService | v1alpha1 | CPU test server |
| NIMCache | v1alpha1 | Actual NotReady reconciliation failure, zero Pods |
| Sandbox | v1beta1 | Ready agent |

NIMCache references an absent PVC with automatic creation disabled. The pinned
operator reports `NotReady` and `NIM_CACHE_RECONCILE_FAILED` before any model
retrieval. The catalog maps that state to Initializing. This verifies failure
observation and deletion, not a successful cache download. No controller status
is patched. The installed pinned CRDs also serve Dynamo v1beta1 and
LLMInferenceService v1alpha1; those mapping versions remain separate coverage gaps.
