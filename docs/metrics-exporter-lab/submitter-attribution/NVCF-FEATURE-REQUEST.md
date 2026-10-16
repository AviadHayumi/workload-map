<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Capture authenticated creators and deployers in workload metadata

Issue draft for maintainer review. No issue has been opened.

## Problem

NVCF/NVCT authenticates callers and records actor/subject information in audit events, but the traced function, deployment and task models do not retain that principal for supported propagation to Kubernetes workload metadata. Observers can identify the account and workload, but cannot distinguish two authenticated submitters sharing an account from metadata alone.

In the self-managed lab, the container function became ACTIVE and returned HTTP 200; the finite task reached COMPLETED at 100%. Pods carried account and function/version or task IDs. Both tested Karta exporter PoC metric families retained `submitted_by=""`. A caller-supplied environment value reached the Pod specs and the task process, but was not projected into metadata. These service-credential tests do not establish a human identity. [Requests and outcomes](https://github.com/AviadHayumi/workload-map/blob/karta-metrics-exporter-poc-impl/docs/metrics-exporter-lab/submitter-attribution/evidence/submitted-by/README.md), [environment follow-up](https://github.com/AviadHayumi/workload-map/blob/karta-metrics-exporter-poc-impl/docs/metrics-exporter-lab/submitter-attribution/evidence/review-suggestions/README.md).

## Existing options

- Audit events already record authenticated actor/subject information and correlation IDs. Durable collection and joins can support reporting; they do not populate metadata for observers restricted to labels and annotations. Function creation, deployment creation/update and task submission must remain distinct operations.
- Helm configuration already transports supplied values through `deploymentSpecifications[].configuration` or `gpuSpecification.configuration`. A chart can stamp annotations on workload roots and Pod templates. ReVal's `skipSanitizeObjectMetadata` / `preserveAnnotations` settings control preservation. This solves transport when a trusted submission service supplies the principal and controls submission; it does not authenticate arbitrary values.
- NVCA `agent.customAnnotations` can stamp fixed annotations on directly created worker Pods. It can classify a dedicated backend, but cannot derive per-request identity. The Helm utils-Pod feature flag has narrower scope.
- Account/NCA, function/version, deployment, task, ICMSRequest and MiniService identifiers already support correlation. Standard Deployment/Job owner chains provide grouping. This request does not propose a new grouping model.

The live Helm retest completed using NIM chart `1.14.0-pb5.0` and a public HTTP fixture. The function reached ACTIVE and its Service returned HTTP 200 through a port-forward. A supplied `transport-test-only` annotation reached the Pod; both Karta metric families carried it through the standard Pod/ReplicaSet/Deployment owner chain. The Deployment root had no submitter annotation. Existing NVCF registry access was sufficient. This proves metadata transport, not authenticated principal capture. The separate gateway attempt with the management token returned 403; gateway invocation for this Helm function was not established. [Retest and lab prerequisites](https://github.com/AviadHayumi/workload-map/blob/karta-metrics-exporter-poc-impl/docs/metrics-exporter-lab/submitter-attribution/evidence/platform-source-audit/nvcf-helm-registry-retest.md).

Sources at public commit `81d815fedda70477655a4ecc1b1a4ee6b4187a32`: [audit identity](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/libraries/java/nv-boot-parent/nv-boot-starter-audit/src/main/java/com/nvidia/boot/audit/AuditService.java#L151), [audit emission](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/libraries/java/nv-boot-parent/nv-boot-starter-audit/src/main/java/com/nvidia/boot/audit/listener/BootAuditEventListener.java#L35), [native metadata](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/compute-plane-services/nvca/pkg/types/common_labels.go#L104), [Helm rendering](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/compute-plane-services/nvca/internal/miniservice/reconcile.go#L1152), [ReVal preservation](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/control-plane-services/helm-reval/reval/run.go#L1016).

## Requested behavior

1. Capture and retain the authenticated principal for function-version creation, deployment creation/update and task submission. Define which actor remains attached to each object; do not collapse creator, deployer and invoker into one ambiguous user.
2. Retain a stable principal ID, principal type and authority/tenant scope. Preserve a service identity when a shared/service credential is used. Include a delegated human only when verified delegation is available.
3. Carry the approved identity through the launch path and publish it in documented, server-controlled annotations on workload roots and Pods for container and Helm paths. Cover replacement Pods and redeployments. An administrator template variable may complement this; arbitrary caller values must not replace the authoritative fields.
4. Use annotations for lossless identity. An optional short-user label should require an agreed unique mapping. Represent unavailable identity explicitly and never include tokens or credential material.
5. Preserve current account/workload IDs and client behavior. Provide an opt-in rollout if needed. The tested observer PoC can read an agreed annotation through its existing CEL without an API or metric-schema change; `workload.dgxc.nvidia.com/submitter-id` is one candidate, not a required standard.

## Acceptance criteria

- Human A creates a function, B deploys/updates it and C invokes it: metadata and audit records distinguish the agreed creator/deployer roles. Invocation does not rewrite workload identity.
- Two authenticated submitters in one account launch finite tasks: workload/Pod annotations identify the correct submitting principals.
- A shared service key remains a service principal unless verified delegation is supplied. Missing identity does not become a guessed username.
- Container and Helm paths retain authoritative metadata across Pod recreation. Caller chart values cannot override the authoritative identity fields; unrelated metadata remains intact.
- Document retention, update semantics, supported versions and how to join the metadata with existing audit/account/workload identifiers.

## Related public work and scope

[Issue #813](https://github.com/NVIDIA/nvcf/issues/813) covers Pod event enrichment for container, Helm and task paths. [Issue #1014](https://github.com/NVIDIA/nvcf/issues/1014) discusses metadata propagation to operator-created objects. [PR #1944](https://github.com/NVIDIA/nvcf/pull/1944) preserves existing account/function labels on worker metrics. These are related propagation and observability paths; their described scope is not authenticated creator/deployer capture.

The tested Karta exporter PoC's controller-owner-reference behavior, NVCT helper-image packaging, task cancellation cleanup, registry provisioning and organization-directory mapping remain separate integration or operational work.
