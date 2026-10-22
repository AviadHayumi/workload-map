<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# SkyPilot workload references

SkyPilot Pods represent compute nodes. Several SkyPilot jobs can run inside one
Pod at the same time. A Kubernetes owner chain therefore cannot identify one
exclusive original job for every SkyPilot Pod.

## Validation status

The pinned API server is installed in `test-ref`. Its Kubernetes credential check
passed. Actual SkyPilot Pods and metadata were captured. Successful runtime jobs,
concurrent jobs and managed-job execution remain unvalidated.

The first ordinary launch failed during control-plane connection errors and a
transient Pod-list 403. Current namespace permissions passed without an RBAC
change. A multinode attempt created separate bare head and worker Pods. Ray then
failed its metrics-port discovery timeout. That failure is preserved separately.

Supported per-task Ray ports were rendered onto a replacement Pod. The default
scheduler repeatedly lost its leader lease and left it Pending. After confirmed
cancellation and cleanup, a per-task KAI scheduler override used the dedicated
test queue and produced a Running Pod. The existing launch was recovered with a
terminal `FAILED` status. Kubernetes refused its in-cluster Ray startup request.
The actual head raylet also timed out on `metrics_agent_port`, a remaining
port-discovery wait. The Pod later reached `DeadlineExceeded`. A source review
found no supported task setting to remove that remaining wait. No additional live
attempt was submitted after that result.

The captures also show the same Pod name and cluster label reused with different
Pod UIDs. This directly validates why name-based grouping needs separate Pod
incarnation identity. The scheduler workaround is documented below; it does not
change the reference model.

`validation-status.json` records versions and completed checks. The isolated
client, chart render, four task files and probe syntax passed local checks.
`evidence/` contains actual sanitized snapshots and diagnostic summaries. A
Running Pod or a successful local check does not establish runtime job success.

## Source findings

The initial investigation used SkyPilot commit
`358c245e42eebe4ef8bc8ca7412ae2822c63ce02`. The published nightly selected for
execution contains commit `2b12f65313bfc045c4b673dfd383be8c317885f9`. The relevant
provisioning and job-sharing behavior is present in both. The later source adds
Kubernetes cluster UID persistence and preserves the type of resource choices
when adding managed-job annotations.

| Operation | Kubernetes objects | Logical relationship |
| --- | --- | --- |
| Ordinary `sky launch` | Bare head Pod and optional worker Pods | The cluster label groups compute nodes. |
| `sky exec` | Uses existing cluster Pods | Jobs are processes scheduled inside those Pods. |
| Dedicated managed job | Bare workload Pods | Job ID and task name are creation-time annotations. |
| Managed job on a pool | Existing replica cluster Pods | The jobs database records current cluster and cluster-local job ID. |
| SkyServe replica | A normal SkyPilot cluster | SkyServe state maps service and replica ID to cluster name. |
| High availability controller | Deployment, ReplicaSet, Pod | This chain identifies controller infrastructure. It does not own workload Pods. |

The Pod provisioner sets `skypilot-cluster-name` and `ray-cluster-name` labels to
the transformed cloud cluster name. It sets the `skypilot-cluster-name` annotation
to the original cluster display name. The head and workers are siblings. The
stock template does not make workers depend on the head through owner references.
[Pod creation](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/provision/kubernetes/instance.py#L2307),
[head and worker naming](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/provision/kubernetes/instance.py#L2515).

The Deployment path is enabled for recognized jobs or serve controller clusters.
Ordinary workload clusters do not use this path. There is no SkyPilot workload CR
in these provisioning paths. External admission controllers can add their own
objects or metadata; that resulting owner graph has not been inspected live.
[Controller availability mode](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/utils/controller_utils.py#L283).

`skypilot-managed-job-id` and `skypilot-managed-job-name` are annotations, not
labels. The name is the task name. Several tasks can share a managed job ID.
Pooled jobs call `sdk.exec` on existing clusters, so their job identity cannot be
inferred from the Pod's provisioning metadata. Both ordinary cluster jobs and
resource-aware pool jobs can share a Pod concurrently.
[Managed annotations](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/jobs/controller.py#L251),
[pool execution](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/jobs/recovery_strategy.py#L1234),
[pool resource sharing](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/serve/serve_utils.py#L1239),
[concurrent cluster jobs](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/docs/source/reference/job-queue.rst#L111).

Cloud cluster names include a user hash but can be reused after deletion. The
SkyPilot database has a separate cluster UUID; the inspected Pod creation path
does not publish it. Pod UID identifies one Pod incarnation. Managed job IDs are
database-local integers, so an external association needs control-plane identity.
The user label is sanitized. The same-key annotation preserves the original user.
The workspace annotation is conditional on Kueue or GPU-direct configuration.
It is not a universal tenant key. Workspaces can choose different namespaces or
share a context. Use actual cluster and namespace identity to scope joins.
[Cluster name transformation](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/utils/common_utils.py#L279),
[cluster UUID](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/global_user_state.py#L1141),
[Pod metadata](https://github.com/skypilot-org/skypilot/blob/358c245e42eebe4ef8bc8ca7412ae2822c63ce02/sky/templates/kubernetes-ray.yml.j2#L335).

For the exporter, retain the true top Kubernetes owner. Add cluster grouping as
a separate association keyed by Kubernetes cluster, namespace and cluster label.
Treat that key as a reusable name. A SkyPilot state join can add immutable cluster
identity, service identity and active jobs. Several matching jobs mean ambiguous
exclusive attribution. Pod-level resource measurements cannot divide usage among
concurrent jobs without runtime allocation or process measurements.

## Reproduce the test

The setup follows the
[official API server Helm deployment](https://docs.skypilot.ai/en/stable/reference/api-server/api-server-admin-deploy.html).
The chart uses its supported custom service account option. It disables runtime
RBAC management, system components, ingress, Prometheus and Grafana. Namespaced
writes are confined to `test-ref`. Its ClusterRole permits only discovery reads
of nodes, Pods, runtime classes, storage classes and the `kube-system` namespace.

The final task files also depend on the dedicated test KAI queue
`osmo-pool-test-ref-karta-reference-test`, created by the OSMO test backend under
`osmo-default-test-ref`. They set `schedulerName: runai-scheduler` and both the
current and compatibility queue labels. The queue is scoped by its test purpose
and the `osmo.namespace: test-ref` label. No existing queue is modified.
Keep that test queue until the SkyPilot tasks finish. For another cluster, select
its authorized scheduler and queue in the three task files before launch.
[Supported KAI Pod settings](https://github.com/kai-scheduler/KAI-Scheduler/blob/04e8472c359b7f62cd412b92011076565b820efa/docs/quickstart/README.md#L29).

This is an execution workaround. The default scheduler repeatedly lost its leader
lease after API timeouts, leaving a Pod Pending without scheduling conditions.
`evidence/scheduler-diagnostics.json` records that failure. The scheduler selection
does not change the meaning of SkyPilot's cluster and job metadata.

The test sets explicit Ray metrics and agent ports inside each isolated Pod.
The first multinode head hit Ray's 15-second port-file discovery timeout just
before the dashboard agent became ready. These variables are consumed by the
pinned SkyPilot startup command. They avoid port discovery for those settings.
No host networking or shared platform changes are required. This was an attempted
recovery, not a validated complete fix: the remaining metrics-agent port wait also
timed out in the corrected runtime.
[SkyPilot startup flags](https://github.com/skypilot-org/skypilot/blob/2b12f65313bfc045c4b673dfd383be8c317885f9/sky/provision/instance_setup.py#L611),
[Ray port discovery timeout](https://github.com/ray-project/ray/blob/06729817574615e25afe8f20902b37660c2e6962/src/ray/util/port_persistence.h#L70).

Ray accepts `--dashboard-agent-grpc-port` for the remaining port. The pinned
SkyPilot Kubernetes startup path does not expose that flag through task settings.
Disabling metrics leaves the port-file wait in place. New clusters use the pinned
Ray version; no public task override was found. The final Pod had no CPU limit,
so CPU throttling was not established as a cause. The source assessment and final
Pod state are in `evidence/runtime-recovery-assessment.json`.
[Ray flag mapping](https://github.com/ray-project/ray/blob/06729817574615e25afe8f20902b37660c2e6962/python/ray/scripts/scripts.py#L949),
[unconditional port discovery](https://github.com/ray-project/ray/blob/06729817574615e25afe8f20902b37660c2e6962/src/ray/raylet/node_manager.cc#L3445),
[SkyPilot Kubernetes options](https://github.com/skypilot-org/skypilot/blob/2b12f65313bfc045c4b673dfd383be8c317885f9/sky/provision/kubernetes/instance.py#L3346).

The API server uses ephemeral state. Do not restart or uninstall it before
capturing results. A 30-minute Pod deadline bounds workload Pod lifetime. The
API server itself remains until cleanup.

Run from this directory. Set `KUBE_CONTEXT` to the authorized context. The
namespace must already exist.

```sh
export KUBE_CONTEXT='authorized-context'
uv venv --python 3.11 /private/tmp/sky-ref-venv
uv pip install --python /private/tmp/sky-ref-venv/bin/python \
  'skypilot-nightly[kubernetes]==1.0.0.dev20261021'
helm pull skypilot-nightly --repo https://helm.skypilot.co \
  --version 1.0.0-dev.20261021 --destination /private/tmp
shasum -a 256 /private/tmp/skypilot-nightly-1.0.0-dev.20261021.tgz
# Expected: 77f0ce07a5f5de61410e214f54e27a3ed3bf4ff6f2efb15c344c389eb04f385b
helm upgrade --install sky-ref \
  /private/tmp/skypilot-nightly-1.0.0-dev.20261021.tgz \
  --kube-context "$KUBE_CONTEXT" --namespace test-ref \
  -f helm-values.yaml --wait --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n test-ref \
  port-forward service/sky-ref-api-service 46581:80
```

Keep port-forward running. In a second terminal, use the same `KUBE_CONTEXT`:

```sh
export SKY_RUNTIME_DIR=/private/tmp/sky-ref-runtime
export SKYPILOT_API_SERVER_ENDPOINT=http://127.0.0.1:46581
export SKYPILOT_USER=sky-ref-user
export SKYPILOT_USER_ID=a1b2c3d4
export SKYPILOT_DISABLE_USAGE_COLLECTION=true
/private/tmp/sky-ref-venv/bin/sky check kubernetes
/private/tmp/sky-ref-venv/bin/python run.py shared
/private/tmp/sky-ref-venv/bin/python run.py multinode
/private/tmp/sky-ref-venv/bin/python run.py managed
```

Each submitted mutation is recorded in `evidence/requests.json` before waiting.
If a client connection drops, recover that same request with `sky.get(requestID)`
or inspect `sky.api_status`. A disconnected client does not imply cancellation.
Do not submit another launch until the original request has a known outcome.

The multinode and managed probes report `runtime.succeeded` separately from
`mapping.passed`. Their final success message requires both. Multinode metadata
must contain two distinct bare Pod UIDs, one head and one worker, with consistent
cluster identity. Managed metadata must cover every submitted job ID and match
the task-name annotation. Nine fake-SDK tests cover missing and inconsistent
metadata, partial runtime results, and valid captures. These are local proof-gate
checks, not live execution evidence.

The shared probe requires two jobs to be observed `RUNNING` concurrently and then
both to finish successfully. It records the same Pod UID before and during the
jobs. The multi-node probe captures two sibling Pods. The managed-job probe
captures the job annotations before automatic job-cluster cleanup.

Captures contain whitelisted Pod metadata. They omit endpoints, node names,
service-account data, container environment and unrelated annotations. User
metadata is retained only for the synthetic test identity. Queue evidence omits
user identity and log paths. The `association-capture.json` job targets are logical
SkyPilot jobs, not Kubernetes objects.

## Cleanup

Keep the local API environment variables above set while running cleanup:

```sh
/private/tmp/sky-ref-venv/bin/sky jobs cancel --all -y
/private/tmp/sky-ref-venv/bin/sky down sky-ref-shared sky-ref-multinode -y
helm uninstall sky-ref --kube-context "$KUBE_CONTEXT" --namespace test-ref
kubectl --context "$KUBE_CONTEXT" -n test-ref get pods,services
```

If the API server becomes unavailable, delete only workload Pods with
`research.karta.dev/suite=skypilot` and Services whose names begin with the test
cluster names. Do not delete the namespace or resources belonging to other tests.
Helm uninstall removes the API server's ClusterRole and ClusterRoleBinding.
