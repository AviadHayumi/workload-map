<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# OSMO workflow references

An OSMO workflow can contain a leader and a worker in one group. OSMO submits
two Pods and, with KAI scheduling, a PodGroup. The workflow itself stays in the
OSMO database. Both Pods carry the workflow and group UUIDs, but neither Pod
has a Kubernetes owner reference to the workflow or PodGroup.

For example, a retry of the worker keeps its task UUID and Pod name. OSMO deletes
the old Pod and creates another Pod with an incremented `osmo.retry_id`. The
Kubernetes UID changes. A mapping that keys attempts only by Pod name merges
these two executions.

## Live results

The service reports `6.3.1.3326e42b`. Backend and pool `karta-reference-test`
route all test workload resources to `test-ref`. The default backend and pool
remain unchanged, as recorded by the [configuration comparison](evidence/default-config-comparison.json).

`karta-ref-single-4` completed. Its [Pod capture](evidence/live-single-association-capture.json)
contains real UID `c9ca03ed-f9f0-4685-8d24-ae69a74620cd`, empty owner references,
and the workflow/group/task UUID labels. A live KSM join preserved that Pod root
while adding the external workflow UUID. `karta-ref-group-2` also completed with leader and worker tasks. Its two Pods are
preserved in the [group capture](evidence/live-group-association-capture.json).
The optional group template also created a real ConfigMap; see the
[projection capture](evidence/live-group-projections-running.json).

After both successful runs completed, full successful reads found no remaining
task Pods, PodGroups or projected ConfigMaps for any of the four accepted
attempts. The early failed run's ConfigMap was initially present, then disappeared.
This proves eventual cleanup in these cases, not atomic cleanup with workflow
status. See the [evidence index](evidence/README.md) and [audit](../audit.md). The shared retry limit is
zero, so retry identity below is source-verified rather than tested live.
The older [historical capture](evidence/association-capture.json) remains separate.

## Source facts

The main source inspection used the clean local OSMO checkout at
`19b5170268bc2e221f2db9367f5893687b060864`. The previous lab used 6.3.1. The
prepared backend chart is therefore pinned to its recorded source revision,
`ed59b69715e4ee6fff70bcd9d715106358a7205f`, chart 1.3.1, application 6.3.1.
The live version was checked before deployment. Successful individual configuration
API writes confirm the running lab accepts the database configuration path.

| Relationship or lifetime | Source |
| --- | --- |
| Workflow is a Python model persisted in PostgreSQL. Its run UUID is the database primary key. The human ID is the workflow name plus a counter. | [workflow.py, 874-1006](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/workflow.py#L874-L1006), [database keys](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/connectors/postgres.py#L935-L965) |
| Every task in one workflow receives the same selected backend. The backend worker assigns its namespace to every group resource before creation. | [workflow.py, 365-374](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/workflow.py#L365-L374), [backend_jobs.py, 129-147](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/backend_jobs.py#L129-L147) |
| Generated Pod metadata includes labels and a cleanup finalizer. The built-in path does not add owner references. An administrator Pod template can add other metadata. | [task.py, 2897-2917](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L2897-L2917) |
| KAI creates a namespaced PodGroup named after the group UUID. Each Pod has `pod-group-name` set to that UUID. This is an annotation reference, not an owner reference. The basic scheduler factory returns only the Pods. | [kb_objects.py, 72-109](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/kb_objects.py#L72-L109), [KAI creation](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/kb_objects.py#L397-L444) |
| Group UUID and task UUID are generated once. A retry preserves task UUID, workflow UUID, group and name, creates a new database attempt key, and increments retry ID. | [initialization](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L864-L881), [retry identity](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L1412-L1423) |
| Pod name is the first 16 hexadecimal characters of the workflow UUID, a dash, and the first 16 characters of the task UUID. Retrying therefore reuses the Pod name. | [name construction](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/kb_objects.py#L39-L40), [retry creation](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/jobs.py#L1160-L1231) |
| Task and group name labels are shortened to 63 characters. Full UUID labels avoid collisions from these shortened names. Human workflow IDs are shortened too. | [shortening](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L126-L133), [system labels](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L2420-L2475) |
| A task can read output from `workflow_id:task_name`. This is an external data dependency, not a Kubernetes ownership relation or permission to follow another namespace. | [task input format](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L268-L312), [input URL construction](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L2622-L2635) |

The workflow and task models do not define a project object or a homogeneous
replica role. A group is a scheduling and execution unit. Its tasks can have
different images, resources, and commands. The leader flag identifies one task;
it does not identify a common replica role. Do not infer replicas by stripping
numeric suffixes from task names.

## Identity available on each Pod

| Field | Meaning | Lifetime |
| --- | --- | --- |
| `osmo.workflow_uuid` | External workflow run ID | Stable for one submission and its retries |
| `osmo.workflow_id` | Human workflow run name | Display value; label can be truncated |
| `osmo.group_uuid` | Group ID within the run | Stable across task retries |
| `osmo.group_name` | Human group name | Display value; label can be truncated |
| `osmo.task_uuid` | Logical task ID | Stable across task retries |
| `osmo.task_name` | Human task name | Display value; label can be truncated |
| `osmo.retry_id` | Attempt number | Increments when OSMO reschedules the task |
| `metadata.uid` | Kubernetes Pod incarnation | Changes when a retry recreates the Pod |
| `pod-group-name` annotation | KAI PodGroup name | Group UUID; absent with the basic scheduler |

For the logical run, use a configured platform-instance scope plus the full
workflow UUID. Keep the cluster and namespace on the Kubernetes observation.
Workflow names, user labels, group names, and the shortened Pod name are not
adequate global identity keys. An external workflow UUID is not a Kubernetes UID.

A useful explicit mapping is workflow run, group, task, and attempt. Mapping
these onto component, instance, or replica-group fields needs a declared
application convention. The source does not establish that every group is a
homogeneous replica group. A conservative representation leaves that role
unspecified or treats each distinct task as a separate one-Pod component.

## Isolated backend and pool

The prepared [backend values](backend-values-6.3.1.yaml) set both the operator
and workload namespaces to `test-ref`. They use backend `karta-reference-test`
and release `osmo-ref`. The matching 6.3.1 listener registers its own namespace
under the new backend name. Reusing `default` would update the existing backend
namespace, so it is intentionally excluded from these inputs. See the
[6.3.1 registration code](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/service/agent/helpers.py#L64-L170).

The [pool request](pool-request.json) creates platform `reference-cpu`. It reuses
the existing `default_ctrl`, `default_user`, and `default_compute` runtime
templates with their resource defaults and validation rules. These named
runtime templates are prerequisites of this 6.3.1 installation. The new
[metadata template](pod-template-request.json) adds only test labels, annotations
and a downward-API retry value.

The new [scheduling template](scheduling-template-request.json) selects
`node-role.kubernetes.io/control-plane: ""` and tolerates that key with
`operator: Exists` and `effect: NoSchedule`. This lab uses the real control-plane
node for execution. The example no longer depends on the previous lab's
`cel-audit-control-plane` template or a private node name. Other clusters should
select their real worker nodes through the same dedicated template.

Backend values disable node-label updates, backend tests and shared priority
class creation. The chart's cluster read and KAI Queue permissions remain.
Queue names and cleanup selectors include the backend namespace, so the new
backend uses `osmo-default-test-ref` and its own pool queue. CPU and memory
requests are reduced only for the two new backend services.

A private values file supplies the existing service address. The documented
`POST /api/auth/user` and `POST /api/auth/user/{id}/access_token/{name}` routes
created synthetic principal `karta-reference-backend` with only `osmo-backend`
and a token expiring `2026-10-23`. The token response went directly into Secret
`test-ref-osmo-backend-token`. A fresh run needs its own short expiry. No token
value is included in files, arguments or captured output.

For the pinned chart, the local render is reproducible with a placeholder URL:

```sh
git -C "$OSMO_SOURCE" archive ed59b69715e4ee6fff70bcd9d715106358a7205f \
  deployments/charts/backend-operator | tar -x -C "$CHART_EXPORT"
helm template osmo-ref "$CHART_EXPORT/deployments/charts/backend-operator" \
  --namespace test-ref --values backend-values-6.3.1.yaml \
  --set-string global.serviceUrl=https://osmo.example.invalid
```

In the 6.3.1 database configuration mode, the new objects use these individual
routes. `OSMO_CURL_CONFIG` is a private curl config containing the already
verified authentication configuration. `OSMO_API` is a private local endpoint.
No credential or endpoint should be included in captured output.

```sh
curl --fail --silent --config "$OSMO_CURL_CONFIG" \
  -H 'Content-Type: application/json' -X PUT \
  "$OSMO_API/api/configs/pod_template/karta-reference-test" \
  --data-binary @pod-template-request.json
curl --fail --silent --config "$OSMO_CURL_CONFIG" \
  -H 'Content-Type: application/json' -X PUT \
  "$OSMO_API/api/configs/pod_template/karta-reference-scheduling" \
  --data-binary @scheduling-template-request.json
curl --fail --silent --config "$OSMO_CURL_CONFIG" \
  -H 'Content-Type: application/json' -X PUT \
  "$OSMO_API/api/configs/group_template/karta-reference-test" \
  --data-binary @group-template-request.json
curl --fail --silent --config "$OSMO_CURL_CONFIG" \
  -H 'Content-Type: application/json' -X POST \
  "$OSMO_API/api/configs/backend/karta-reference-test" \
  --data-binary @backend-request.json
curl --fail --silent --config "$OSMO_CURL_CONFIG" \
  -H 'Content-Type: application/json' -X PUT \
  "$OSMO_API/api/configs/pool/karta-reference-test" \
  --data-binary @pool-request.json
curl --fail --silent --config "$OSMO_CURL_CONFIG" \
  -H 'Content-Type: application/json' \
  "$OSMO_API/api/pool/karta-reference-test/workflow" \
  --data-binary @single-request.json
```

Before submission, verify the registered backend reports `test-ref` and the
new pool selects that backend. Verify the API's current configuration mode.
ConfigMap mode and 6.4 require the deployment's supported configuration update
path; the database-mode write commands above are not interchangeable with it.
Compare the existing pool and backend configuration before and after setup.

## Finite cases

- [Single](single.yaml): one task runs for 90 seconds, then exits 0.
- [Group](group.yaml): leader and worker run together. The worker exits after
  230 seconds and the leader after 240 seconds. Capture both before cleanup.
- [Retry](retry.yaml): the first attempt exits 75 after 45 seconds. A later
  attempt exits 0. The Pod template reads `osmo.retry_id` through the downward
  API. This case requires the existing workflow retry limit to permit at least
  one retry. Its default is zero. Do not change the shared retry limit just for
  this experiment.

The executed requests used `ubuntu:24.04`. OSMO resolved it to a digest in each
Pod specification. Both running group containers reported the same image ID.
The reproduction YAML and JSON now pin that [observed digest](evidence/task-image-digests.json).
No successful task was rerun solely to change the image spelling.

The files have exact JSON request equivalents. Record the returned workflow ID,
then capture Pod and PodGroup metadata while the tasks are running:

```sh
set -o pipefail
kubectl --context kmetric-playground-dev -n test-ref \
  get pods,podgroups.scheduling.run.ai -o json \
  | python3 capture_metadata.py --complete-podgroups \
  > evidence/live-association-capture.json
```

The helper keeps only identity fields, rejects objects outside `test-ref`, and
omits user identity and Pod contents. The command includes infrastructure Pods;
filter the sanitized output by `osmo.pool=karta-reference-test` for this test.
Only use `--complete-podgroups` after a successful full-namespace PodGroup list.
Separate reads need separate timestamps and do not form an atomic snapshot. Capture retry attempts separately.
Compare the full task UUID, retry ID, Pod name, and Kubernetes UID. Verify final
workflow/task status through OSMO rather than assuming success from Pod removal.

## Optional group projection

[group-template-request.json](group-template-request.json) creates a ConfigMap
named `osmo-reference-{{WF_GROUP_UUID}}`. The Pod template independently stamps
that name in `reference.example.test/group-name`. These are configuration-only
inputs for a typed, same-namespace group reference experiment. Enable the group
template only on the new pool, using [the isolated patch](projected-pool-patch.json).
The worker values already include ConfigMap create/list/delete/patch permission.
The running two-task group used ConfigMap UID
`a78129d4-c691-4eb4-9cd7-f10470700360` and PodGroup UID
`2aa3ccfe-5937-447e-9f0e-307c2cf5104d`. Both had no owner references.

This object represents one group. It is not the workflow root and is not a Pod
owner. OSMO creates it before the group's Pods and deletes it during group
cleanup. The generated manifest contains workflow/group identity, not task
inventory or workflow lifecycle status.

Do not use the workflow UUID as a shared group-template resource name. Each
group would try to create the same object. The create path skips `AlreadyExists`
and retains the first group's labels. That group's cleanup can then delete the
object while another group is still running. This follows from
[group template creation](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/task.py#L2396-L2415),
[AlreadyExists handling](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/backend_jobs.py#L138-L159),
and [group cleanup selectors](https://github.com/NVIDIA/OSMO/blob/19b5170268bc2e221f2db9367f5893687b060864/src/utils/job/jobs.py#L815-L852).

Pod templates can insert arbitrary metadata, but their variables do not include
the Kubernetes UID returned when another group resource is created. The backend
create loop does not feed resource results back into the already rendered Pod
specification. A valid owner reference to a newly created projection therefore
needs a UID-resolving component or an OSMO change. Guessing or substituting the
OSMO UUID as a Kubernetes owner UID would not establish ownership.

## Implication for a generic exporter

The practical two-week option is a configurable Pod metadata association. Keep
the actual Kubernetes top owner as the workload selected by the existing
ownership path. Report external workflow/group/task identity as separate
associations with explicit scope and provenance. All required OSMO identity
values already exist on the Pod. No OSMO-specific exporter branch or database
access is needed for that projection. Missing fields remain unresolved.

A typed lookup can also resolve the same-namespace PodGroup annotation when KAI
is present. Require a current object match and retain its Kubernetes UID. This
is a group association, not a replacement for the top owner. Finding a Karta
definition for that peer object does not authorize changing ownership.

For a later workflow object projection, a bridge or OSMO submission hook must
own one namespaced record per workflow run, update its state and complete group
inventory, and remove it according to the workflow lifecycle. That gives the
exporter an actual object to fetch. Whether it should also own Pods requires a
separate garbage-collection and lifecycle decision. Per-group templates alone
do not supply that workflow contract.
