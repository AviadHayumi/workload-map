<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Platform source audit and configuration tests

Checked 16 October 2026. This audit made no OSMO, NVCF or Karta source/API changes. This supplements the earlier [submission captures](../submitted-by/README.md) and [review-suggestion tests](../review-suggestions/README.md). The report separates live results from source-reviewed options.

## OSMO

The deployed service was 6.3.1. Public 6.4.0 and current main were also reviewed. Exact source revisions, six independent source-review invocations and the final document review are recorded in [provenance](source-and-review-provenance.json): two Opus 5.5 source reviews at max effort, four Astra reviews requested at ultra effort, and an additional Opus 5.5 document review using updated Claude Code 2.1.301. Findings were reconciled against source and live results; model agreement alone was not treated as proof.

Three isolated leader/worker workflows tested an administrator template and platform-level mapping. Existing platforms and the default platform were preserved.

| Submitted principal | Native username | Mapped annotation | Karta submitted_by | Workflow result |
| --- | --- | --- | --- | --- |
| alice@example.test | alice | alice@example.test | alice@example.test | COMPLETED |
| alice | alice | alice@example.test | alice@example.test | COMPLETED |
| a | missing | empty | empty | COMPLETED |

The six tasks all reported COMPLETED. Leader exit codes were 0; worker exit codes were null, not assumed to be 0. These runs used development-authentication headers. They verify metadata rendering and exporter behavior, not production IdP authentication.

- [Template](osmo-admin-template.json), [mapping](osmo-platform-map.json), and [submitted cases](osmo-submissions.json).
- [Email request](osmo-mapped-email-request.json), [short-username request](osmo-mapped-short-request.json), [missing-username request](osmo-unknown-one-char-request.json).
- [Completed task results](osmo-completed-results.json) and [Karta metrics](karta-metrics.prom). All six captured workload observations were healthy. Metric phase samples were captured while workloads ran.
- [Retained Pod metadata](osmo-objects.json) contains the last two Pods from the short-username case; it is not a complete six-Pod inventory. Other Pods were cleaned up by OSMO before the final object capture.
- [Configuration isolation](osmo-config-isolation.json) and [restoration](osmo-cleanup.json) confirm the pool/template configuration was restored exactly, the temporary discovery-refresh label was removed, and no test Pods or PodGroups remained.

A separate 90-second single task retained the complete [full-user metric sample](osmo-full-user-metric.prom), [request](osmo-full-user-metric-request.json), [native Pod/PodGroup fields](osmo-full-user-metric-objects.json), and [completion result](osmo-full-user-metric-result.json). It exited 0. No configuration change was needed. Resource discovery required a temporary non-scheduling node-label event after restoring platform configuration; that label was removed immediately.

Existing no-source-change options:

- [Native label generation](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/utils/job/task.py#L2551-L2585) preserves a suitable username, shortens an email, or omits the value. [Template variables](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/utils/job/task.py#L2627-L2648) derive from those labels. A template cannot recover a lost domain or distinguish colliding usernames.
- An administrator map can supply a known principal or organization. It requires unique keys and maintained data. Pool/platform classification is another option, but is not a user-directory lookup.
- The [unified 6.4 OIDC setting](https://github.com/NVIDIA/OSMO/blob/483ae4188d4f5a0900a1bf39d1b0daaa76cad83e/deployments/charts/osmo/values.yaml#L262-L272) selects an existing trusted username claim. Changing it changes account identity and does not migrate existing tokens/history.
- [Task metrics](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/service/core/workflow/workflow_metrics.py#L107-L145) already expose the full stored user and workflow UUID. API/metrics joins can support reporting without changing Pod metadata.
- [KAI PodGroups](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/utils/job/kb_objects.py#L405-L451), workflow/group UUIDs and pod-group-name already describe grouping. The current Karta exporter follows controller references, so these bare Pods remain individual workloads. That limitation belongs to the Karta integration.

Configuration is version-specific. 6.3.1 database mode supports configuration API writes; ConfigMap mode uses Helm/GitOps. [6.4 rejects configuration mutations](https://github.com/NVIDIA/OSMO/blob/483ae4188d4f5a0900a1bf39d1b0daaa76cad83e/src/service/core/config/configmap_guard.py#L32-L38); a non-null [configuration.snapshot](https://github.com/NVIDIA/OSMO/blob/483ae4188d4f5a0900a1bf39d1b0daaa76cad83e/deployments/charts/osmo/templates/configs.yaml#L28-L32) replaces the normal configuration sections. Preserve current template lists and authentication providers.

The 6.3.1 gateway's [provider user_claim mapping](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/deployments/charts/service/templates/_gateway-envoy-config.tpl#L498-L531) supplies `x-osmo-user`; OSMO-issued tokens contain [unique_name](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/src/utils/auth.py#L154-L158). The [task-metrics chart flag](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/deployments/charts/service/values.yaml#L1095) becomes the [OSMO_DISABLE_TASK_METRICS environment variable](https://github.com/NVIDIA/OSMO/blob/ed59b69715e4ee6fff70bcd9d715106358a7205f/deployments/charts/service/templates/api-service.yaml#L157-L159). The same template declares port 9464. [Live selected settings](osmo-live-metrics-settings.json) confirm the flag was false and that port was configured; the saved scrape confirms the endpoint returned metrics.

The [OSMO draft](../../OSMO-FEATURE-REQUEST.md) is limited to direct, lossless principal access in administrator templates or annotations. Generic workflow labels and prefixes already exist in merged [#1221](https://github.com/NVIDIA/OSMO/pull/1221) and [#1259](https://github.com/NVIDIA/OSMO/pull/1259).

## NVCF / NVCT

Prior live container tests remain valid: an HTTP function returned 200, a finite task completed, and explicit environment values reached Pod specs and the task process. They did not become submitter metadata. The new audit found existing reporting and metadata paths:

- Audit events retain actor/subject identity and correlation IDs. Collect INFO-level events from `ROOT.com.nvidia.boot.audit.AuditService` across replicas and join by account plus function/version/deployment or task ID and operation/time. A shared credential identifies its credential principal, not an additional human. This requires a log pipeline; it does not populate metadata-only CEL inputs.
- Static NVCA `agent.customAnnotations` reaches directly created worker Pods. Values are copied literally. It supports fixed backend classification, not per-submitter identity. The HelmCustomAnnotations flag separately affects the Helm utility Pod, not all chart Pods.
- Function deployment `configuration` and task `gpuSpecification.configuration` supply Helm values. A chart can stamp root and Pod-template annotations. This transports supplied data; authenticated identity needs a trusted submission path.

A pinned public nginx 18.2.5 chart rendered successfully locally. [Values](nvcf-helm-configuration.json) and [rendered objects](nvcf-helm-rendered.yaml) show annotations on the Deployment and its Pod template.

Verified chart digest: `sha256:69b7c8d0ffb80a37254426714b878eaa063838d4078ef26e2dca72a22d34be36`. The old Bitnami image tag was unavailable. The local probe used the public legacy mirror pinned to `sha256:2d08c41aebabe86837ed1c0ca6857135558d8d2bd90b08ccfbe639b996bc1097`. It was not deployed and is not a production-image recommendation.

The [live ReVal setting](nvcf-reval-metadata-config.json) had `skip-sanitize-object-metadata: true`. When sanitization is enabled, use exact annotation keys in `reval.serviceConfig.preserveAnnotations`. [The code](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/src/control-plane-services/helm-reval/reval/run.go#L645) replaces Deployment Pod labels independently; preserveLabels alone is insufficient. The [chart setting](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/deploy/helm/helm-reval/values.yaml#L94) controls root annotation preservation.

Live Helm function creation stopped before creating resources:

- [registry-1.docker.io request](nvcf-create-request-registry-1.docker.io.json) and [response](nvcf-create-response-registry-1.docker.io.json): unsupported HELM hostname.
- [docker.io request](nvcf-create-request-docker.io.json) and [response](nvcf-create-response-docker.io.json): missing HELM registry credential.

The supported account setup is `GET /v2/nvcf/registry-credentials?artifactType=HELM`, then `POST /v2/nvcf/registry-credentials` if needed, using `manage_registry_credentials` or `apikey:manage_registry_credentials`. Required body: registryHostname `docker.io`, secret.name, secret.value containing base64 of a real username:password, and artifactTypes `["HELM"]`. No supported empty credential entry was found. See the [public registry setup](https://github.com/NVIDIA/nvcf/blob/81d815fedda70477655a4ecc1b1a4ee6b4187a32/docs/self-managed/third-party-registries.md). No registry settings or credentials were added. Other registered artifact alternatives were not inventoried. No full Helm-to-Karta result is claimed.

The [NVCF draft](../../NVCF-FEATURE-REQUEST.md) asks for authenticated creator/deployer/task-submitter capture and supported metadata propagation. Existing [metric identity labels](https://github.com/NVIDIA/nvcf/pull/1944), [account attribution](https://github.com/NVIDIA/nvcf/pull/2041), and [operator metadata work](https://github.com/NVIDIA/nvcf/issues/1014) are related, not missing generic capabilities.

The [NVCF cleanup check](nvcf-cleanup.json) confirms no test function was created and existing Karta rules were unchanged. The [final OSMO restoration](final-restoration.json) covers all four completed workflows and confirms no test Pods or PodGroups remained.

## Remaining agreements

Select a stable NVIDIA username and define service/shared credential semantics. Organization mapping needs an authoritative directory and agreed key spelling. The experiment used an organization annotation with a generic test value; it did not implement a directory lookup or add an exporter metric. Production IdP integration, human A creating/B deploying, hosted NVCF, and the full Helm path remain untested. No public issues or messages were posted.

## Final report verification

The [document verification record](report-verification.json) captures the final source/evidence review, chart rendering, compiled CEL examples, focused admission and attribution tests, and PDF inspection. This pass corrected OSMO versioned settings and attachment paths, NVCF read permissions, and Run:AI verification commands. It did not rerun platform submissions or change implementation or cluster settings.
