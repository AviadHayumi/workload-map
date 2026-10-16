<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Resumed execution evidence

Captured 16 October 2026. IDs are retained to correlate the selected resources.
Credentials, private endpoints, hostnames, node names and unrelated data are omitted.

- `osmo-verified-pods.json`: three Running task Pods with the shortened `alice` label.
- `osmo-verified-metrics.prom`: one resource and one workload observation per Pod, all `partial`.
- `osmo-capture-status.json`: both finite workflows COMPLETED. In the group replay, the worker completed under the lead-task termination rule; its exit code is null. The first group run reported exit zero for both tasks.
- `osmo-*-capture-request.json`: actual finite workflow request bodies. The named audit platform was selected explicitly; the existing default platform was unchanged.
- `runai-*-request-template.json`: authenticated request shapes; environment-specific IDs are placeholders. GPU quota is zero. This cluster disables CPU/memory project quota fields; each workload declares bounded container requests and limits.
- `runai-verified-resources.json`: selected Job, Pod, WorkloadRunner and Knative Service metadata. Training completed and inference became Ready.
- `runai-scoped-metrics.prom`: 14 resource/workload samples from an exporter restricted to the audit namespace. Both workload identities are complete; principal type remains unknown.
- `runai-inference-http.json`: HTTP 200 through the Knative revision queue-proxy, with the expected response body. This is not an external ingress or authentication acceptance test.

The OSMO and initial Run:AI samples precede the owner-scope fix in this continuation.
Post-fix health and Run:AI captures are recorded separately. Earlier NVCF and direct
Kubernetes evidence remains in the parent directory, with its original build scope.

Additional audit artifacts:

- `scope-health-comparison.json`: same-UID recovery counts for the scope fix; all observed health reasons remain visible.
- `runai-post-fix-metrics.prom`: both complete workload identities after the scope correction.
- `metadata-representation-diagnostic.json`: full versus metadata-only DaemonSet mismatch at equal resource versions.
- `runai-cleanup.json`: accepted API deletions for only the two audit fixtures.
- `metadata-health-comparison.json`: all 21 previously stale Pod observations recover with the same UID; final snapshot has 210 healthy workload observations.
- `final-lab-state.json`: both isolated exporters Ready and no remaining Run:AI test Pods/WorkloadRunners.
- `validation.json`: exact source commits, binary hash and final presubmit results.
