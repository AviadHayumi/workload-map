<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Metrics exporter acceptance

The acceptance runner checks the [metrics exporter KEP](keps/0001-metrics-exporter/README.md)
against real Kubernetes resources and operator-created ownership chains. It uses Python 3's
standard library and kubectl. The existing [e2e provisioner](../hack/e2e/README.md) installs
the workload operators. The runner does not install or upgrade those operators.

## Checks without a cluster

Run from the repository root:

```bash
python3 -m unittest discover -s exporter/test/e2e -v
make exporter-rules-test
make helm-lint helm-validate
make check
```

The Python tests exercise the inspector itself: extra child status, duplicate attribution,
stale health reasons, UID replacement, missing phase rows, exact labels and legacy metrics.
They do not establish that a running exporter passes. The PromQL fixtures check the exact
KEP expressions, including all failure-reason windows and conflicting exporter replicas.

## Prepare an isolated operator cluster

Requirements: Docker, kind, kubectl, Helm, Python 3 and sufficient memory/disk for the
selected operators. Use a dedicated kubeconfig on every command. Never store it in Git.
The provisioner pins versions in hack/e2e/global.env. The fixtures were derived from the
21-case operator audit; its GPU-heavy cases ran CPU fixtures on arm64 Kubernetes 1.34.3.

```bash
export ACCEPTANCE_DIR="$(mktemp -d)"
export KUBECONFIG="$ACCEPTANCE_DIR/kubeconfig"
export CLUSTER_NAME=karta-exporter-acceptance
export EXPORTER_IMAGE=karta-exporter:acceptance

make e2e-up CLUSTER_NAME="$CLUSTER_NAME" WORKLOADS="jobset"

# Reserve room for the complete operator/CRD stack used by this matrix.
helm upgrade karta charts/karta --kubeconfig "$KUBECONFIG" \
  --kube-context "kind-$CLUSTER_NAME" -n karta-system --reuse-values \
  --set resources.requests.cpu=100m --set resources.limits.cpu=1 \
  --set resources.limits.memory=1Gi --wait --timeout 5m

# Install the other operators in dependency order. The acceptance matrix supplies
# the workload checks, including the pinned MinIO fixture below.
for operator in lws kuberay kubeflow knative kserve milvus grove dynamo nim; do
  KUBECONFIG="$KUBECONFIG" CLUSTER_NAME="$CLUSTER_NAME" \
    bash "hack/e2e/operators/$operator/install.sh"
done

docker build -f exporter/Dockerfile -t "$EXPORTER_IMAGE" .
kind load docker-image "$EXPORTER_IMAGE" --name "$CLUSTER_NAME"
docker build -t karta-nimcache-cpu:matrix-v3 exporter/test/e2e/nimcache-image
kind load docker-image karta-nimcache-cpu:matrix-v3 --name "$CLUSTER_NAME"
docker pull milvusdb/minio@sha256:1dce27c494a16bae114774f1cec295493f3613142713130c2d22dd5696be6ad3
docker tag milvusdb/minio@sha256:1dce27c494a16bae114774f1cec295493f3613142713130c2d22dd5696be6ad3 karta-milvus-minio:matrix
MINIO_PLATFORM="$(docker image inspect karta-milvus-minio:matrix --format '{{.Os}}/{{.Architecture}}')"
docker image save --platform "$MINIO_PLATFORM" -o "$ACCEPTANCE_DIR/minio.tar" karta-milvus-minio:matrix
kind load image-archive "$ACCEPTANCE_DIR/minio.tar" --name "$CLUSTER_NAME"

# The pinned Dynamo operator uses v1alpha1. Select the matching current definition.
kubectl --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER_NAME" \
  apply -f docs/catalog/nvidia-com-dynamographdeployment-v1alpha1.yaml

helm upgrade karta charts/karta --kubeconfig "$KUBECONFIG" \
  --kube-context "kind-$CLUSTER_NAME" -n karta-system --reuse-values \
  --set exporter.enabled=true --set exporter.replicaCount=2 --set exporter.useCatalog=true \
  --set exporter.namespace=karta-exporter-acceptance \
  --set exporter.image.repository="${EXPORTER_IMAGE%:*}" \
  --set exporter.image.tag="${EXPORTER_IMAGE##*:}" \
  --set exporter.image.pullPolicy=IfNotPresent
```

Stop if any installer fails. The Ray installer loads the architecture-specific image
as ray-e2e:local. The NIM installer builds nim-cpu:e2e. The NIMCache image above simulates
a cache download. It never downloads model data or uses real credentials. Placeholder
Secrets are created only in the test namespace. The pinned MinIO image was used in the
recorded arm64 matrix; validate availability for the selected node architecture.

The runner checks the selected API version, not only group/kind. A preexisting cluster
Karta overrides the catalog. Use this isolated cluster or inspect existing definitions
before treating a different selection as an exporter failure.

## Run every case

```bash
python3 exporter/test/e2e/acceptance.py \
  --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER_NAME" \
  --namespace karta-exporter-acceptance \
  --exporter-namespace karta-system --exporter-service karta-exporter \
  --image "$EXPORTER_IMAGE" --suite all \
  --output "$ACCEPTANCE_DIR/results"
```

The namespace must not already exist. Each successful workload root is deleted after
capture. The runner waits for two consecutive passing observations, with a default
600-second timeout per case. Increase --timeout for cold image pulls. Milvus runs last
because its operator can retain independent dependency releases after root deletion.

Each exporter Pod selected by the Service is scraped separately through the Kubernetes
API. A load-balanced Service scrape cannot conceal a disagreeing replica. The deployment
example uses two exporter replicas to exercise this path. The runner requires exactly
--exporter-replicas endpoints, default two; a missing replica cannot pass the comparison.

For constrained local hosts, run --cases in operator batches and retain the same image
for every batch. Scale only idle controllers in the dedicated test cluster, save their
original replica counts, and restore each required operator before its cases. Keep
controllers that serve generic Pod or ConfigMap admission webhooks running. Removing
webhook policies or changing unrelated clusters is unnecessary. Use a fresh namespace
for each matrix invocation and retain each batch's output directory. Memory or I/O
pressure that causes control-plane lease loss invalidates a test attempt.

The inspector examines every Karta sample in the namespace. It verifies:

- Exactly the three KEP families, gauge types, exact labels and bounded health reasons.
- Pod UID and workload UID agree with Kubernetes controller ownership, with one mapping per Pod.
- The highest described owner receives attribution and status. Extra child status fails the test.
- Root GVK, component, named instance and repeated-group identity match the fixture.
- Nine dense phase rows exist for each described root, including roots with no Pods.
- Current workload health is fresh and healthy; an old reason row cannot coexist with recovery.
- Ready or terminal owned Pods are checked independently of observation health.

The following cases are available through --cases, separated by commas:

| Case | Owned Pods | Main identity assertion |
| --- | --- | --- |
| dynamo | 3 | Frontend, prefill, decode; Deployment-backed groups empty |
| grove | 4 | worker instance, groups 0 and 1 |
| jobset | 3 | chief group 0; workers groups 0 and 1 |
| lws | 6 | leader and two workers in each of groups 0 and 1 |
| pytorch | 3 | master and workers |
| mpi | 3 | launcher and workers |
| raycluster | 2 | head and named worker group |
| rayjob | 3 | head, worker and submitter |
| rayservice | 2 | head and named worker group |
| knative | 1 | revision through Configuration and Revision owners |
| kserve | 1 | serverless predictor ownership |
| kserve-raw | 1 | Deployment-backed predictor status |
| milvus | 1 | owned standalone Pod; independent dependencies remain separate |
| nim | 1 | CPU fixture attributed to nimservice |
| nimcache | 1 | completed HF downloader attributed to nimcache |
| deployment | 1 | Deployment above ReplicaSet |
| deployment-zero | 0 | root status and health without Pods |
| statefulset | 1 | StatefulSet ownership |
| job | 1 | completed Pod remains attributable while present |
| cronjob | 1 | CronJob above Job |
| pod | 1 | no default attribution or root status |

Phases can overlap. MPI/PyTorch can be Initializing and Running; NIMCache can be
Initializing and Completed. The expectations retain these catalog semantics.

NIMCache uses the v3.0.2 HF source path, which skips the manifest helper and invokes
huggingface-cli in one download Job. Its CPU image validates that command and writes
a placeholder into the cache PVC. See the pinned upstream
[helper gate](https://github.com/NVIDIA/k8s-nim-operator/blob/v3.0.2/internal/controller/nimcache_controller.go#L578-L584)
and [download command](https://github.com/NVIDIA/k8s-nim-operator/blob/v3.0.2/internal/nimsource/hf.go#L16-L30).
The operator image must match its CRDs; a moving release-3.0 image is not a v3.0.2 pin.
The installer rejects an existing release with a different image. Align its chart,
CRDs and image together before retrying; it does not silently upgrade that release.

## Run faults separately

```bash
python3 exporter/test/e2e/acceptance.py \
  --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER_NAME" \
  --namespace karta-exporter-acceptance \
  --exporter-namespace karta-system --image "$EXPORTER_IMAGE" \
  --suite faults --output "$ACCEPTANCE_DIR/fault-results"
```

The fault runner creates unique test CRDs and a Karta definition, a separate exporter
and service account, and a Role confined to its namespace. Its cluster role reads only
Karta definitions. It changes that test Role, not the normal exporter's permissions.

It verifies startup denial, grant, revocation and fresh recovery; required API absence
and late discovery, disappearance and reappearance without restart; a valid definition
whose expression fails evaluation and then recovers; full-Pod selectors; already-attributed Pod reparenting
after a middle-owner update; zero-Pod child status suppression; missing and UID-mismatched
owners with namespace-scoped failure and recovery; an ignored unread wrapper; and deletion
of all current rows for a removed root. Both main exporter replicas must first observe
the unique fault definition. After its definition and CRDs are deleted, both must remove
all rows for that API group without restarting.

The missing-owner Pods and the root under an ignored wrapper have a test finalizer
so Kubernetes garbage collection cannot erase the fault before it is observed.
Normal cleanup releases it. On interruption, inspect the retained namespace and
remove only the acceptance.karta.test/hold finalizer from those test resources
before deleting the namespace.

Use --keep to retain namespaces and failed fixtures for diagnosis. Otherwise the runner
deletes the resources it created. The unique fault exporter, definitions, CRDs and RBAC
are tracked for cleanup. Deleting a whole disposable kind cluster also removes any
operator-managed dependency resources that outlive their root workload.

## Evidence and limits

Keep summary.json, per-case result.json, ownership.json and one metric snapshot per
exporter Pod. Failed cases retain their last captured metrics and the failure text.
Ownership snapshots contain GVK/name/UID/owner references, not Secrets, Pod environment
values, node identities or API endpoint addresses. Record the tested source commit,
working-tree changes and exporter image digest alongside the results.

The [2026-10-14 evidence summary](../exporter/test/e2e/results/acceptance-2026-10-14.json)
records a passing run of all 21 workload cases, 21 isolated fault checks and four
transport checks. The exporter was built from commit
02a613462995f11ac3825c6b3db7a7f51c485e0c and ran with two replicas on a three-node
arm64 kind cluster with Kubernetes 1.34.3. Every workload case passed two consecutive
observations with exact per-replica parity. The cases observed 40 fixture-owned Pods
across separate runs, with 39 mappings; the bare Pod intentionally had none. Independent
Milvus dependencies were checked as separate roots. The definition-deletion fault
removed every row for its unique API group on both main replicas without a restart.

The summary separates the immutable source manifest, Docker OCI index, arm64 image
manifest, runtime image configuration and later harness hashes. Production source
remained unchanged across all batches. The run corrected RayService actor placement
and a NIM operator/CRD mismatch, then reran the affected cases. It also repaired a
Knative image-cache alias without changing image content. Interrupted attempts are
recorded as such; the summary does not count them as passing batches. Detailed raw
snapshots remain local, while the checked-in summary omits endpoints, node identities,
credentials and raw object manifests.

These checks establish exporter attribution and observation behavior. The CPU/mock
fixtures do not establish real training, GPU utilization, GPU sharing or application
idleness. PromQL tests use synthetic measurement inputs. Native OTLP delivery remains
deferred by the KEP. Collector forwarding is covered by the separate transport
runner below; it is not native OTLP delivery from the exporter.

The CLI caches full Pods by default so custom definition selectors can read arbitrary
Pod fields. The advanced --full-pod-cache=false CLI option reduces retained fields to
metadata, spec.nodeName and status.phase; use it only when all definitions use that
shape. It is not an extra public Helm value. Capacity testing must account for full
Pod size and all nine status rows per root, not merely the workload count.

At 100,000 roots, status and workload-health rows alone approach one million current
samples. An in-memory benchmark does not validate API-server list/watch throughput or
prove that a laptop kind cluster can sustain that workload count. Report actual tested
cardinality, memory, scrape latency, API cost and hardware separately.

## Prometheus and Collector transport

Use two exporter replicas and retain the observed namespace with the matrix
runner's --keep option. The transport runner creates its own infrastructure
namespace and one uniquely named Deployment in the observed namespace. It
removes only those resources. It restarts the selected test exporter Deployment
to verify both replicas after a rolling update.

```bash
helm upgrade karta charts/karta --kubeconfig "$KUBECONFIG" \
  --kube-context "kind-$CLUSTER_NAME" -n karta-system --reuse-values \
  --set exporter.replicaCount=2

python3 exporter/test/e2e/transport.py \
  --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER_NAME" \
  --namespace karta-exporter-acceptance \
  --exporter-namespace karta-system --exporter-service karta-exporter \
  --exporter-deployment karta-exporter \
  --output "$ACCEPTANCE_DIR/transport-results"
```

The runner pins Prometheus v3.14.0 and Collector contrib 0.148.0. Load these
images into kind first for an offline run. If an image contains an incomplete
multi-platform manifest, use docker image save --platform with the node's
architecture, then kind load image-archive. Record the image digests with the run.

One Prometheus instance discovers and scrapes each exporter endpoint directly.
A Collector independently discovers the same endpoints and forwards OTLP/HTTP
to a second Prometheus instance. A temporary namespace-scoped read-only Role
permits discovery of Pods, Services and Endpoints in the exporter namespace.
The runner removes that Role and its binding after the test. The backend
uses NoTranslation so gauge names remain unchanged. The test compares all Karta
labels, values and exact sample counts with both source replicas, retaining
exporter_pod as the discovered target identity. Duplicate Karta identities
within one exporter target fail; both target copies must be present and agree.
Empty labels can be omitted from Prometheus query JSON and are normalized to
empty strings before comparison.

The test repeats the comparison after exporter rollout, deletes the workload,
and recreates its name with a new UID. The bridge query applies a ten-second
timestamp freshness guard to its two-second scrape interval. This explicitly
tests a consumer freshness policy: an OTLP backend may retain its last gauge
after the source stops publishing it. The test does not claim backend retention
or deletion semantics are identical to a direct Prometheus scrape.

Prometheus documents the [OTLP receiver](https://prometheus.io/docs/guides/opentelemetry/).
The pinned Collector's [distribution manifest](https://github.com/open-telemetry/opentelemetry-collector-releases/blob/v0.148.0/distributions/otelcol-contrib/manifest.yaml)
identifies the Prometheus receiver and OTLP HTTP exporter used here. Run evidence
is written to transport.json; a failed or absent report is not a passing result.
