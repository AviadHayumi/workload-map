<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Finite workload submitter

This example submits small operator-managed CPU fixtures that really exit zero.
It alternates JobSet and PyTorchJob every five minutes. Each root has two Pods,
each requesting 50m CPU and 64Mi memory. Each process hashes a sample once per
second for 90 seconds, then exits. This is demonstration work, not distributed
training or a GPU benchmark. The installed operators report completion from the
actual Pod and Job status; no status fields are patched.

Requirements: JobSet v0.10.1 and Kubeflow Training Operator v1.9.0 with PyTorchJob
support, a dedicated application namespace, and a separate control namespace.
The fixture image is pinned to the busybox 1.36 digest used by the acceptance run.
The binary uses the existing exporter Go module dependencies.

## Deployment

Build from the repository root:

```sh
docker build -f exporter/examples/finite-workload-submitter/Dockerfile \
  -t example/finite-workload-submitter:local .
```

Replace the image placeholder in `deploy.yaml` with the published immutable image.
Replace `demo-control` with an existing control namespace. The application namespace
`finite-demo` is created by the example. Keep it separate from demonstrations that
require uninterrupted attribution health. Karta invalidates a namespace while
unknown Pod owners await fresh verification, including during creation and garbage
collection of completed jobs.

Run a Karta exporter scoped to `finite-demo`, with catalog definitions and all
workload phases enabled, and scrape it using the existing attribution rules.
An exporter scoped to another namespace does not observe these workloads.
The core Karta metrics and Helm API remain unchanged.

```sh
kubectl --context YOUR_CONTEXT apply -f deploy.yaml
kubectl --context YOUR_CONTEXT -n demo-control create job \
  --from=cronjob/finite-workload-submitter finite-manual-EXAMPLE
kubectl --context YOUR_CONTEXT -n finite-demo get jobsets,pytorchjobs,pods
kubectl --context YOUR_CONTEXT -n finite-demo get configmap finite-workload-submitter -o yaml
```

Karta phases are not exclusive. PyTorchJob can retain its Created condition after
succeeding, so both Initializing and Completed may equal one. PyTorch success is
based on the Master; inspect both retained Pods for zero exits. CPU and memory
telemetry remains actual container telemetry. These fixtures have no GPU requests.

## Bounds and retries

The CronJob runs every five minutes with concurrencyPolicy Forbid. A ConfigMap
resourceVersion CAS lease also covers manual Jobs and competing invocations.
The process deadline is 45 seconds, its Job deadline is 60 seconds, and the lease
is 120 seconds. A crashed invocation leaves a lease that expires naturally.
Deterministic slot names make retried creation idempotent. Even slots select
JobSet and odd slots select PyTorchJob; skipped slots are not replayed.

At most one nonterminal owned root is admitted at once. A root already deleting
at the start of a tick blocks admission. Cleanup can request terminal-root deletion
and submit a new root in the same tick. At most ten owned roots are retained,
including roots still undergoing garbage collection.
An active root blocks submission even if its deadline has elapsed; the operator
must report a terminal condition before another root starts. JobSet child Jobs
and PyTorch runPolicy have 300-second active deadlines. The root bound is based
on operator status, not a guarantee that every child Pod is already terminated.

Completed and failed roots are retained at least 45 minutes after their terminal
condition transition. PyTorch cleanPodPolicy is None and child Jobs have no TTL,
so completed Pods remain inspectable. Cleanup only targets roots with this
ConfigMap UID in both their label and owner reference, a valid scheduled name,
and a terminal timestamp. Each deletion rechecks ownership, terminal status, UID
and resourceVersion, then uses UID and version preconditions. A malformed terminal
timestamp is retained and counts toward the cap. No active root is deleted to make
room. Stopping the CronJob stops cleanup; the ten-root bound remains.

The service account can only read/update the named ConfigMap and list/get/create/
delete JobSet and PyTorchJob roots in the dedicated namespace. It cannot patch
Pods or status, read Secrets, change operators, or modify other namespaces.
This is a trusted demonstration namespace, not a security boundary against other
writers that already have create/delete permissions there.

## Controls and cleanup

```sh
kubectl --context YOUR_CONTEXT -n demo-control patch cronjob finite-workload-submitter \
  --type=merge -p '{"spec":{"suspend":true}}'
kubectl --context YOUR_CONTEXT -n demo-control patch cronjob finite-workload-submitter \
  --type=merge -p '{"spec":{"suspend":false}}'
```

Suspending preserves submitted workloads, which continue to completion. For removal,
suspend and wait for the active submitter Job to finish, then delete that exact
CronJob. Inspect the ConfigMap and owned roots before deleting them. Deleting the
dedicated ownership ConfigMap garbage-collects its roots and their children.
Do not reuse this ConfigMap for unrelated work.
