<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# NVCF Helm registry retest

Checked 16 October 2026. The isolated Helm function reached ACTIVE, its Service returned HTTP 200, and both Karta metric families carried the supplied submitter annotation. No Karta, OSMO or NVCF source/API changes were made.

## NGC access

The supplied key was used only for read-only metadata requests and a chart pull. The old Triton chart and NVCF's organization-specific sample returned HTTP 403. NIM chart `1.14.0-pb5.0` returned HTTP 200 for metadata and download. NVCF's existing SYSTEM registry credential also registered that chart successfully. No registry credential was replaced and nothing was pushed to NGC.

This shows chart-specific access, not a general failure of the supplied key. A 403 alone does not identify the underlying organization entitlement or artifact policy. The stored NVCF credential was not compared with the supplied credential, so they are not claimed to be the same key. [Read results](nvcf-helm-chart-read-checks.json).

The chart digest was `sha256:421d65ba7528c834a8b8a27c41186d1531bf3e927223c3922e1718b3e671e716`. The approved lab issuer supplied a temporary administration credential held in process memory. No OpenBao root credential was read. No credential is retained in this evidence.

## Submission and observed result

- [Function request](nvcf-helm-live-function-request.json).
- [Complete chart values](nvcf-helm-live-values.json). Passed as `deploymentSpecifications[0].configuration`, with one instance and an available lab GPU/instance type.
- [Selected Deployment, ReplicaSet and Pod fields](nvcf-helm-live-objects.json).
- [Karta metric samples](nvcf-helm-live-metrics.prom), including all workload phase values.
- [Result summary](nvcf-helm-live-result.json).
- [Cleanup and restoration](nvcf-helm-live-cleanup.json).

The chart ran a pinned public Python HTTP server on port 8080. It used fake GPU allocation but performed no GPU work or model inference. Chart-generated Secrets contained an explicit noncredential placeholder because the HTTP fixture does not use NIM's model credentials. Those placeholders are separate from NVCF's registered chart-pull credential.

The Deployment root had no submitter annotation. `podAnnotations` put `workload.dgxc.nvidia.com/submitter-id: transport-test-only` on its Pod template and Pod. Karta followed the Pod/ReplicaSet/Deployment controller chain. Its Pod identity consensus supplied `submitted_by="transport-test-only"` on both `karta_pod_workload_info` and `karta_workload_status`; Running was 1 and workload observation health was 1.

HTTP 200 was observed through an owned port-forward to the function Service. The separate NVCF invocation attempt returned 403 using a management JWT. That attempt did not establish gateway invocation for this Helm function. The earlier container-function gateway result used a separate invocation credential and remains a different test.

Namespace and object UIDs are substituted consistently in retained resources and metric rows. The workload name, metadata values, relationships and metric values are preserved. No cluster endpoint, node name, account identifier or credential is included.

## Failures resolved during the test

| Failure | Resolution and scope |
| --- | --- |
| Two NGC chart references returned 403 | Used the accessible pinned NIM chart. Existing NVCF registry access was sufficient. |
| ReVal evaluated the chart against Kubernetes v1.20.0 | Supplied the actual v1.35.5 through the operator's `k8sVersionOverride`. ReVal then returned valid with no validation errors. |
| Default Helm shared storage required missing `smb.csi.k8s.io` | Appended `-HelmSharedStorage` to this isolated backend for the storage-free HTTP function. Preserved resource-enforcement flags. |
| First nginx fixture could not write its temporary directory under the enforced user | Used the pinned Python HTTP fixture with its working directory under `/tmp`. No permission or resource-enforcement relaxation was needed. |
| Failed attempts left two ICMSRequests | Removed the requests after confirming they belonged to this test. No test Pods, MiniServices or ICMSRequests remained. |

The Kubernetes-version issue affects the tested NVCA 3.10.0 path. The public [discovery fix](https://github.com/NVIDIA/nvcf/commit/d9cf89008a399d0ee43b95c2e77ba7d6961eb963) postdates that tag. A release containing the fix was not tested. Do not report it as an unresolved current-main bug.

For reproduction on this older runtime, set the NVCA operator chart's `k8sVersionOverride` to the actual server version. The live test applied its equivalent `--k8s-version-override` argument and verified `agent.kubernetesVersionOverride` in generated configuration. Restore the previous setting after the test.

The live storage change appended `-HelmSharedStorage` to `NVCFBackend.spec.featureGate.values`. The NVCA operator Helm chart exposes this through `worker.featureFlags`. This is backend-wide, not per function. It is suitable for this fixture only because no shared model cache, resources or task progress/results storage is needed. Existing flags must be preserved. Normal shared-storage functions and Helm tasks need their supported storage setup. The original flags were restored after capture.

The existing ReVal setting was `reval.serviceConfig.skipSanitizeObjectMetadata: true`; it was not changed for this retest. With sanitization enabled, exact `reval.serviceConfig.preserveAnnotations` keys preserve root annotations. Current source retains Deployment template annotations separately while replacing template labels. Stricter-mode success was not live-tested. [ReVal metadata handling](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/control-plane-services/helm-reval/reval/run.go#L642-L667).

The failed-request cleanup behavior is separate from submitter attribution. It was observed on the tested older stack; reproduce it on the target release before opening a separate cleanup bug.

## Remaining identity requirement

Helm values transport a supplied principal; they do not authenticate it. The live result used `transport-test-only` deliberately. No authenticated human identity was inferred from that string or from the administration credential.

An existing trusted submission service could derive the principal from authenticated identity and supply the Helm annotation without changing NVCF source. It must prevent callers from replacing that authoritative value or bypassing the trusted path. This additional service was not implemented or tested here.

The preferred native alternative is the [feature-request draft](../../NVCF-FEATURE-REQUEST.md): retain the authenticated creator, deployer and task submitter, then propagate the appropriate principal to workload and Pod metadata. Shared service credentials must remain service identities unless verified human delegation is available.

## Cleanup

Function deployment deletion returned HTTP 200; version deletion returned HTTP 204. Both temporary NVCA settings were restored. Operator and agent were Ready after rollout. All owned port-forwards stopped. The process discarded its temporary JWT; this is not a claim of stateless-token revocation. The pre-existing function was left unchanged.
