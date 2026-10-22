<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# OSMO live evidence

All new task and group objects used context `kmetric-playground-dev`, namespace
`test-ref`, backend/pool `karta-reference-test`. The service/backend reported
`6.3.1.3326e42b`. UTC capture times are inside the JSON files.

| Accepted workflow | Result | Evidence |
| --- | --- | --- |
| `karta-ref-single-1` | Failed: test pool omitted resource-template defaults | [failure](first-submission-failure.json) |
| `karta-ref-single-4` | Completed, one task | [terminal status](single-completed.json), [Pod metadata](live-single-association-capture.json) |
| `karta-ref-group-1` | Failed: Kubernetes returned an etcd timeout during CreateGroup | [failure](first-group-failure.json) |
| `karta-ref-group-2` | Completed, leader and worker | [terminal status](group-live-status.json), [Pod and PodGroup metadata](live-group-association-capture.json) |

Validation and rejected requests consumed some human-name counters. Workflow
UUIDs and real Pod UIDs identify the runs and incarnations. No retry policy was
changed. Every captured task has `osmo.retry_id=0`.

The successful group's two Pods have no owner references. They share workflow
UUID `faa9dfbbaa0b4d009ab7ef312f3d6444` and group UUID
`c60ec160a9954b4a82c5475def93f5be`. The annotation `pod-group-name` names the real
PodGroup with UID `2aa3ccfe-5937-447e-9f0e-307c2cf5104d`. The optional template
created ConfigMap UID `a78129d4-c691-4eb4-9cd7-f10470700360`; its generated
workflow/group data is in [the projection capture](live-group-projections-running.json).
Neither group object is a Pod owner or an OSMO workflow root.

The [single capture with targets](live-association-capture.json) records separate
read times. The group capture combines sequential successful reads of dedicated
pool Pods and all namespace PodGroups. These are real observations, not atomic
cluster snapshots. Target inventory is marked complete only after its list
succeeded. User identity, node names, endpoints, spec contents and credentials
are omitted by the capture helper.

The first failed single's ConfigMap was observed before cleanup in
[live-group-projection.json](live-group-projection.json). The
[post-completion check](lifecycle-after-completion.json) successfully listed Pods,
PodGroups and ConfigMaps after both successful workflows were terminal. No
objects remained for any of the four accepted attempts. No manual deletion was
used. This shows eventual cleanup in this run; it does not establish a fixed
retention delay or atomic deletion with workflow status.

[Image evidence](task-image-digests.json) records the actual public Ubuntu digest
from both running group containers. Submitted files used `ubuntu:24.04`; OSMO
added its digest to the Pod spec. Reproduction files now pin that same digest.
[Default-config equality](default-config-comparison.json) verifies the existing
pool's routing/templates and backend's namespace/scheduler settings stayed equal.

The following commands describe the reads used for capture. API requests use a
private local endpoint and the established lab authentication configuration.
Only selected identity/status fields are saved from each response.

```sh
kubectl --context kmetric-playground-dev -n test-ref \
  get pods -l osmo.pool=karta-reference-test -o json
kubectl --context kmetric-playground-dev -n test-ref \
  get podgroups.scheduling.run.ai -o json
kubectl --context kmetric-playground-dev -n test-ref \
  get configmaps -l osmo.workflow_uuid -o json
```

The accepted requests used `POST /api/pool/karta-reference-test/workflow` and
the saved `single-request.json` or `group-request.json` payload. Validation used
the same route with `validation_only=true`. Terminal statuses came from
`GET /api/workflow/{name}`. The backend, pool and template setup inputs are one
directory above. The later cleanup check repeated all three Kubernetes lists.

The older [association-capture.json](association-capture.json) is explicitly
historical and remains separate from these new captures. Live exporter, KSM,
PromQL and generic-reference results are owned by the [spike](../../spike/README.md).
