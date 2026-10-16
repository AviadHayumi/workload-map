<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Follow-up checks: shared labels and environment transport

Live checks on 16 October 2026. Same exporter binary as implementation commit
`49ff26cccd65a64fd69f74ebc2547d7b2de51459`. No code or metric/Helm API changes.
The original exporter configuration and existing platforms were preserved.
A temporary exporter added one CEL fallback, `workload.dgxc.nvidia.com/user`.

## Results

| Check | Observation | Remaining limit |
| --- | --- | --- |
| Shared user label on a Job | Original exporter: empty. New CEL: `alice` on both existing metric families, inherited by the unlabeled Pod. Job completed. | Metadata must come from a trusted platform. This test does not authenticate a label. |
| OSMO user `alice` | Pod template stamped shared user `alice`; workflow completed. | Agreement with the NVIDIA username was not checked. |
| OSMO user `alice@example.test` | Same template stamped `alice`; workflow API kept the full email. Workflow completed. | The template token loses the email domain. |
| OSMO user `a`, original platform | Workflow completed, but `osmo.submitted_by` was absent and metrics had an empty value. | Missing-label bug candidate in OSMO 6.3.1. |
| OSMO user `a`, unguarded template | Undefined-variable error, no Pod, workflow stayed PENDING while CreateGroup failed without retry. | Error propagation also needs maintainer review. |
| OSMO guarded template | User `a` ran with an empty shared label; `alice` retained `alice`. | The guard prevents launch failure; it does not recover missing identity. |
| Organization labels | Fixed `demo_research_training` appeared on OSMO Pods. | No organization registry lookup or organization metric was implemented. |
| NVCF function environment | Function ACTIVE; HTTP 200; test value present in Pod spec. | Function image has no shell; process environment not inspected. No submitter metadata. |
| NVCT task environment | `printenv` returned the test value; task COMPLETED. | No submitter metadata; both metric families stayed empty. |

The NVCF/NVCT value was caller-supplied, not captured from an authenticated human.
These checks prove environment transport. They do not prove identity provenance.
SkyPilot, hosted NVCF, production OSMO authentication and organization-directory
mapping were not tested. No SkyPilot installation was available in this lab.

## Repeat the shared-label check

Use a test namespace outside direct-admission capture. Submit
[native-job-request.json](native-job-request.json), then scrape the original
exporter and a separate exporter configured with
[shared-user-rules.json](shared-user-rules.json). Only the Job has the user label.

```sh
kubectl create namespace karta-suggestions
kubectl create -f native-job-request.json
curl -fsS "$BASELINE_EXPORTER/metrics" > baseline.prom
curl -fsS "$TEST_EXPORTER/metrics" > shared-label.prom
kubectl -n karta-suggestions wait --for=condition=complete   job/suggestion-shared-user --timeout=5m
```

Compare `karta_workload_status` and `karta_pod_workload_info` for
`workload="suggestion-shared-user"`. The captured values are in
[running-baseline.prom](running-baseline.prom) and
[running-shared-label.prom](running-shared-label.prom).
The organization label stayed object metadata; the metric schema was unchanged.

## Repeat OSMO's Pod-template check

Tested with OSMO 6.3.1. Create a separate administrator-managed Pod template:

```yaml
metadata:
  labels:
    workload.dgxc.nvidia.com/user: "{{WF_SUBMITTED_BY | default('')}}"
    workload.dgxc.nvidia.com/org_heirarchy: demo_research_training
```

Append its name to a separate platform's existing `override_pod_template` list.
Keep its existing scheduling templates and leave the default platform unchanged.
Use that platform in the workflow from the PDF. The original trial used
`{{WF_SUBMITTED_BY}}`; the guard was added after the `a` failure.
The tested files are [unguarded](osmo-template.json) and
[guarded](osmo-template-guarded.json). They also contain the corrected
`org_hierarchy` spelling to test acceptance. This is not a recommendation to
standardize both keys. Agree one key and a real organization source.

The lab submitted each saved request through:

```sh
curl -fsS "$OSMO_API/api/pool/$POOL/workflow"   -H 'Content-Type: application/json'   -H 'x-osmo-user: alice@example.test'   -H 'x-osmo-roles: osmo-user'   --data-binary @osmo-shared-email-request.json
```

These headers are only for the tested development-auth installation. Use the
installation's normal authenticated CLI/API in production. Replace the saved
platform name with the separate platform configured above. Repeat with `alice`
and `a`. See [outcomes](osmo-outcomes.json), [running fields](running-resources.json),
[guarded fields](guarded-resources.json), [guarded metrics](guarded-shared-label.prom)
and the [template error](osmo-template-error.json).

The new platform initially returned "There are no resources". Capacity appeared
about ten minutes later; no shared platform, node labels or taints were changed.
The root cause of that registration delay was not established.

Suggested OSMO issue: the single-character user loses its label/token, and the
resulting template failure leaves workflow status PENDING instead of surfacing
failure. A full-principal annotation is a separate feature request if required.
Workflow-root grouping is a separate Karta/OSMO integration gap.

## Repeat NVCF / NVCT environment transport

Use the function/task API commands in the PDF with
[nvcf-env-request.json](nvcf-env-request.json) and
[nvct-env-request.json](nvct-env-request.json). Both include:

```json
{"key": "KARTA_TEST_SUBMITTED_BY", "value": "alice@example.test"}
```

Append that entry to `containerEnvironment`; do not replace the task's existing
script variable. The task sleeps 180 seconds, reports progress and exits.
Match the deployment/task GPU specification to the available compute pool.

```sh
kubectl -n "$BACKEND_NS" get pod "$POD" -o json |
  jq '[.spec.containers[].env[]? |
       select(.name == "KARTA_TEST_SUBMITTED_BY")]'
kubectl -n "$BACKEND_NS" exec "$TASK_POD" -c task --   printenv KARTA_TEST_SUBMITTED_BY
```

[Function result](nvcf-env-result.json), [task runtime](nvct-env-runtime.json),
[task completion](nvct-env-result.json), [function metrics](function-baseline.prom)
and [task metrics](nvct-running-shared-label.prom) record the result.
The captures retain selected resource fields and all labels on selected metric
samples, not complete Kubernetes objects.

This used the existing self-managed installation and its previous helper-image
overrides. Each test's primary container was resized from 1 CPU to 100m after
Insufficient CPU; GPU allocation came from the lab's fake GPU operator.
No real model inference was performed. No external identity mapper was used.

Suggested NVCF feature request: capture a trusted principal at the API, transport
it, and project it into workload/Pod metadata. The environment transport works;
metadata projection and authenticated-user capture remain missing.
Function creator, deployer and invoker must be distinguished.

## Evidence and cleanup

The test function was undeployed and deleted; the completed task was deleted.
OSMO completed workflows automatically removed their Pods. The failed edge test
required forced cancellation (`POST /api/workflow/NAME/cancel?force=true`).
Ordinary cancellation kept retrying while the group remained PROCESSING. Temporary API keys were revoked. The temporary exporter, its
configuration and Service, the native test namespace, and the extra OSMO platform
and template were removed. See [cleanup](cleanup.json) and
[verification](verification.json) for the checked final state.

Initial snapshots also contain unhealthy observations for newly created resources.
The verification uses each case's later converged capture, where observation health
was 1; the original rows remain in the evidence.

Raw authentication responses and cluster logs are excluded. Existing Run:AI,
OSMO and NVCF tests in the PDF were not rerun beyond the checks listed here.
No issue has been opened.
