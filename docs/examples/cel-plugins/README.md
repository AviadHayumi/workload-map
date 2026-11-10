<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Give workloads storage, placement and a Service with CEL

Add three annotations to a Deployment or CronJob. The example controller creates
a PVC, selects a node pool and exposes the workload through a Service. Edit an
annotation and it reconciles again. Karta finds the Pod template in either kind.

```yaml
metadata:
  annotations:
    plugins.karta.dev/storage: 1Gi
    plugins.karta.dev/node-pool: blue
    plugins.karta.dev/service-port: "80"
```

| Plugin | Result | On removal |
| --- | --- | --- |
| Storage | A PVC named `karta-<workload-UID>-data` | Retains the PVC and its data |
| Placement | Pod template node selector `plugins.karta.dev/pool=blue` | Removes its selector |
| Service | `karta-<workload-UID>-service`, port 80 to the first container's first declared port | Deletes its Service and removes its Pod label |

This is a small controller example inside workload-map. It needs Kubernetes and
the Karta Go SDK. It installs no CRDs or additional policy engine. CEL computes
values; Go validates them and writes to Kubernetes.

## The three policies

Each program receives `object`, the complete workload, and `template`, its root
Pod template. For a Deployment with `containerPort: 8080`, these expressions
return `{"size":"1Gi"}`, `{"pool":"blue"}` and `{"port":80,"targetPort":8080}`.
An absent annotation returns `{}`, which disables that plugin.

```cel
// Storage
'plugins.karta.dev/storage' in object.metadata.annotations
? {'size': object.metadata.annotations['plugins.karta.dev/storage']}
: {}

// Placement
'plugins.karta.dev/node-pool' in object.metadata.annotations
? {'pool': object.metadata.annotations['plugins.karta.dev/node-pool']}
: {}

// Service
'plugins.karta.dev/service-port' in object.metadata.annotations
? {'port': int(object.metadata.annotations['plugins.karta.dev/service-port']),
   'targetPort': template.spec.containers[0].ports[0].containerPort}
: {}
```

The [programs](programs) are embedded and compiled once. Change them and rebuild
to change the policies. Result schemas are deliberately fixed: a positive size
up to 10Gi, a nonempty Kubernetes label value, and ports from 1 to 65535.

## Run the demo

Prerequisites: the Go version in `go.mod`, Docker, kind, kubectl and make.
Run commands from the repository root. No GPU is needed for these three plugins.

Create a dedicated cluster, build the controller and run the live tests:

```sh
KEEP_CLUSTER=1 make test-cel-plugins-e2e
```

The cluster has blue and green worker nodes. The test uses a fresh namespace and
static local volumes. It cleans up its successful fixtures and leaves the cluster
and images ready for this walkthrough. Logs are under `.artifacts/cel-plugins/`.
The command refuses an existing cluster unless `REUSE_CLUSTER=1` is explicit.

Install the controller in the demo namespace. Select kind's `standard` storage
class for this walkthrough; the controller default is explicitly no class.

```sh
export KUBECONFIG="$(mktemp)"
kind get kubeconfig --name karta-cel-e2e > "$KUBECONFIG"
kubectl apply -f docs/examples/cel-plugins/manifests/
kubectl -n karta-cel-demo patch deployment cel-plugins --type=json -p='[
  {"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--storage-class=standard"}
]'
kubectl -n karta-cel-demo rollout status deployment/cel-plugins --timeout=120s
kubectl apply -f docs/examples/cel-plugins/samples/deployment.yaml
kubectl -n karta-cel-demo wait deployment/web \
  --for=jsonpath='{.metadata.annotations.plugins\.karta\.dev/result}' --timeout=60s
kubectl -n karta-cel-demo get deployment web \
  -o go-template='{{index .metadata.annotations "plugins.karta.dev/result"}}{{"\n"}}'
```

Expect `"state":"Applied"`. That means configuration converged; it does not
mean Pods are ready or the PVC is bound. Start with zero replicas because the
controller edits the persisted workload after submission.

Storage provisioning creates a claim. Attach it with ordinary workload fields,
then start the application:

```sh
WORKLOAD_UID=$(kubectl -n karta-cel-demo get deployment web -o jsonpath='{.metadata.uid}')
CLAIM="karta-${WORKLOAD_UID}-data"
SERVICE="karta-${WORKLOAD_UID}-service"
kubectl -n karta-cel-demo patch deployment web --type=json -p="[
  {\"op\":\"add\",\"path\":\"/spec/template/spec/volumes\",\"value\":[{\"name\":\"data\",\"persistentVolumeClaim\":{\"claimName\":\"${CLAIM}\"}}]},
  {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/volumeMounts\",\"value\":[{\"name\":\"data\",\"mountPath\":\"/data\"}]},
  {\"op\":\"replace\",\"path\":\"/spec/replicas\",\"value\":1}
]"
kubectl -n karta-cel-demo rollout status deployment/web --timeout=120s
kubectl -n karta-cel-demo get pods -l app=web -o wide
read_marker() {
  kubectl -n karta-cel-demo exec deployment/web -- sh -c '
    for attempt in 1 2 3 4 5; do
      wget -T 3 -qO- "$1" && exit 0
      sleep 1
    done
    exit 1
  ' sh "$1"
}
read_marker "http://${SERVICE}/marker"
```

The response is `retained-data`. The Pod runs on the blue worker. Change the
Service port and inspect the reconciled resource. The bounded retry allows time
for Kubernetes networking to apply the change:

```sh
kubectl -n karta-cel-demo annotate deployment web plugins.karta.dev/service-port=81 --overwrite
kubectl -n karta-cel-demo wait "service/${SERVICE}" --for=jsonpath='{.spec.ports[0].port}'=81 --timeout=60s
read_marker "http://${SERVICE}:81/marker"
```

The [CronJob sample](samples/cronjob.yaml) uses the same annotations. Its template
lives at `spec.jobTemplate.spec.template`. Keep it suspended during preparation.
Changes affect future Jobs; existing Jobs and Pods retain their original spec.
The live suite also moves a storage-free workload from blue to green. A local
volume stays attached to its original node.

## Reconciliation and data ownership

The controller watches workload, PVC and Service changes in one namespace. It
also checks every 30 seconds and lists existing workloads after restart. It
uses fresh API reads and conditional patches to avoid overwriting concurrent edits.

Remove annotations to disable plugins. The controller removes only template
fields it previously wrote and that still match its saved state. A conflicting
field or foreign child produces an error instead of being adopted. It preserves
other labels, selectors, Service allocations and ports.

PVCs survive opt-out and parent deletion. Their capacity and storage class cannot
be changed through this example. Volume mounts remain user-owned. A deleted or
replaced claim is an error; it is never silently recreated with empty storage.
Services follow their parent's lifetime through an owner reference.

<details>
<summary>Failure states and scope</summary>

Read `plugins.karta.dev/result` for `Applied` or `Error`, a reason and an input
hash. `plugins.karta.dev/state` records field ownership and the claim UID. These
annotations are controller bookkeeping, not a security boundary; do not edit them.

| Reason | Meaning |
| --- | --- |
| InvalidInput | An annotation or evaluated result is invalid |
| FieldConflict / OwnershipConflict | A reserved field or child belongs to another writer |
| StorageChangeUnsupported | Requested capacity or storage class changed |
| StorageMissing | The recorded claim disappeared |
| StorageCreationUncertain | A saved creation reservation exists but no claim can be found |

Storage creation records a reservation before creating the PVC. After an
interruption, a matching existing claim can complete that reservation. If the
claim is absent, the controller stops: it cannot distinguish a request that never
reached the API from a claim that was created and then deleted. Inspect and retain
any existing data. Use a new workload UID for a fresh storage request. There is
no state-reset or storage-migration API.

The example watches only Deployments and CronJobs. Run one controller replica.
It is eventual reconciliation, so an already active workload can create Pods
before preparation. It does not inject mounts, mutate existing Pods, resize
storage or dynamically load plugins. A CronJob Service has endpoints only while
matching Pods run.

</details>

<details>
<summary>Use the CEL SDK from Go</summary>

```go
import kartacel "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"

// workload is an *unstructured.Unstructured Deployment or another supported root.
input, err := kartacel.NewInput(ctx, workload)
if err != nil { return err }
program, err := kartacel.Compile(`{'image': template.spec.containers[0].image}`)
if err != nil { return err }
result, err := program.Evaluate(ctx, input)
if err != nil { return err }
// result["image"] is the original container image string.
```

`NewInput` uses the built-in Karta catalog and accepts a root with one complete
Pod template. It rejects array paths and fragmented or child templates. Programs
are reusable and pure; Kubernetes writes remain the caller's responsibility.
Detached raw maps preserve integer values and fields outside the selected edits.

Limits: 4096 source bytes, 1 MiB activation, 8192 result bytes, 10000 CEL cost
units and a 100 ms cooperative deadline per operation. Nesting and node counts
are bounded too. These limits are not a process sandbox.

</details>

## Verify and clean up

`make check-cel-plugins` runs offline and race tests. The kind suite exercises
real scheduling and HTTP, updates, drift, opt-out, restart, collisions, retained
data, garbage collection and API conflicts. Fault-injection tests cover storage
write interruptions that cannot reliably be timed against a live API.

```sh
kubectl delete namespace karta-cel-demo
kind delete cluster --name karta-cel-e2e
rm "$KUBECONFIG"
unset KUBECONFIG
```

Deleting the disposable kind cluster removes its local test data. On a persistent
cluster, retained PVCs require a separate, deliberate storage cleanup.
