<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter operator matrix

This Ginkgo suite installs the exporter chart with the Karta operator disabled.
It creates workload objects in separate labeled namespaces and scrapes the real
exporter Service through a loopback port-forward. It requires the operators and
images installed by the repository's pinned `hack/e2e/up.sh` workflow.

## Run

Use an isolated kind cluster with the pinned operators installed.
Rebuild and load a fresh image after changing the exporter:

```sh
# From the repository root, with the prepared exporter-e2e kind cluster running:
image_tag="e2e-$(git rev-parse --short HEAD)-$(date +%s)"
make exporter-image EXPORTER_IMAGE_REPOSITORY=karta-metrics-exporter IMAGE_TAG="$image_tag"
kind load docker-image "karta-metrics-exporter:$image_tag" --name exporter-e2e

KUBECONFIG=/tmp/exporter-e2e.kubeconfig \
EXPORTER_E2E_IMAGE="karta-metrics-exporter:$image_tag" \
EXPORTER_E2E_INCLUDE_BARE_PODS=true \
make test-exporter-e2e
```

The command requires an explicit kubeconfig and image. It checks the expected
context before creating resources and uses `-count=1` to run every time.
Relative paths start at the repository root.

| Setting | Default or requirement |
| --- | --- |
| `KUBECONFIG` | Required; one isolated kind kubeconfig file |
| `EXPORTER_E2E_IMAGE` | Required; image already loaded into kind |
| `EXPORTER_E2E_CONTEXT` | `kind-exporter-e2e` |
| `EXPORTER_E2E_API_PROFILE` | `default`; or `alternate` |
| `EXPORTER_E2E_FOCUS` | Empty; optional Ginkgo focus expression |
| `EXPORTER_E2E_OUTPUT` | `/tmp/exporter-e2e-results`; choose a separate path to retain each run |
| `EXPORTER_E2E_TIMEOUT` | `90m` |
| `EXPORTER_E2E_INCLUDE_BARE_PODS` | Unset; `true` opts bare Pods into the catalog |

For a targeted JobSet and native Job run:

```sh
KUBECONFIG=/tmp/exporter-e2e.kubeconfig \
EXPORTER_E2E_IMAGE="karta-metrics-exporter:$image_tag" \
make test-exporter-e2e \
  EXPORTER_E2E_FOCUS='operator workloads (jobset|job-zero-suspended|job-zero-completed|job-zero-failed)$' \
  EXPORTER_E2E_OUTPUT=/tmp/exporter-e2e-targeted \
  EXPORTER_E2E_TIMEOUT=30m
```

Run the `pod` case again without `EXPORTER_E2E_INCLUDE_BARE_PODS` to verify the
default catalog exclusion. It checks that a Ready bare Pod produces no workload
or Pod-info series. The opt-in run clears that exclusion and checks its metrics.

Cleanup removes the exporter release and test namespaces. The cluster and its
operators remain available. `make check` compiles both deployed suites through
`build-exporter-e2e`. The [monitoring suite](../monitoring/README.md) has separate
setup and run commands.

## API profiles

`EXPORTER_E2E_API_PROFILE` selects the scenarios when Ginkgo registers the suite.
The default is `default`, which keeps the existing 23 cases. `alternate` runs
exactly two cases: DynamoGraphDeployment v1beta1 and LLMInferenceService v1alpha1.
Each profile selects only one catalog Karta per GroupKind. Loading both versions
of either family together would test definition selection instead of both mappings.
The suite rejects other profile names and records the selected profile in its
Ginkgo report.

Run the alternate profile against the same pinned operators after the default run:

```sh
KUBECONFIG=/tmp/exporter-e2e.kubeconfig \
EXPORTER_E2E_IMAGE="karta-metrics-exporter:$image_tag" \
make test-exporter-e2e \
  EXPORTER_E2E_API_PROFILE=alternate \
  EXPORTER_E2E_OUTPUT=/tmp/exporter-e2e-alternate-results \
  EXPORTER_E2E_TIMEOUT=30m
```

Dynamo v1beta1 uses `spec.components` and `podTemplate.spec.containers[name=main]`
with the same CPU mocker image and resources as the v1alpha1 fixture. It expects
component `component`, instances `Frontend` and `decode`, and empty replica groups.
LLMInferenceService v1alpha1 uses the existing PVC and tiny HTTP server fixture.
It expects component `main` with empty instance and replica group. Both profiles
assert the exact observed API version and catalog definition. Alternate fixtures
supply no status; the operator must establish readiness.

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
`EXPORTER_E2E_OUTPUT`. The suite records the profile, image and evidence directory in its
Ginkgo report. A failed assertion remains a failure even if another case passes.

Dynamo uses the repository's CPU mocker backend. NIM uses the CPU test server, and
LLMInferenceService uses its existing tiny HTTP server. These prove operator
reconciliation and exporter attribution, not GPU inference or model quality.

## Coverage boundaries

The default profile contains 23 cases across the catalog workload families.
The alternate profile adds two separate API-version cases.
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
| LLMInferenceService | v1alpha2 default; v1alpha1 alternate | Main component |
| Milvus | v1beta1 | Standalone |
| Grove PodCliqueSet | v1alpha1 | Two Pods in one replica group |
| DynamoGraphDeployment | v1alpha1 default; v1beta1 alternate | CPU mocker |
| NIMService | v1alpha1 | CPU test server |
| NIMCache | v1alpha1 | Actual NotReady reconciliation failure, zero Pods |
| Sandbox | v1beta1 | Ready agent |

NIMCache references an absent PVC with automatic creation disabled. The pinned
operator reports `NotReady` and `NIM_CACHE_RECONCILE_FAILED` before any model
retrieval. The catalog maps that state to Initializing. This verifies failure
observation and deletion, not a successful cache download. No controller status
is patched. The installed pinned CRDs must serve the versions selected by each
profile. The profile definitions describe intended coverage; passing reports
provide evidence for the specific operator and exporter images used.
