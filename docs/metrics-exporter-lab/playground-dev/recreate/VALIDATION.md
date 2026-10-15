<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Recreation bundle validation

Validated on 2026-10-14. Creating this guide and bundle did not modify the running Kubernetes environment.

## Passed

- Parsed the Python helpers and embedded DCGM bridge/init scripts.
- Checked Bash syntax for the ordered workload-operator installer.
- Parsed bundled operator YAML and manifest JSON.
- Rendered local and HTTPS configurations with a different node name and storage class.
- Checked node/host substitutions, storage class substitutions, local ingress omission, HTTPS TLS Secret configuration, and paused versus enabled CronJobs.
- Checked that packaged resource metadata has no old UIDs, resource versions, owner references, finalizers or managed fields.
- Checked that deployment assets contain no original cluster hostname, node names, local session paths or saved workload UIDs.
- Checked that the assets contain no Secret objects. The Secret helper generates new credentials directly into the target cluster and keeps existing Secrets.
- Rendered all eight bundled charts with Helm: Karta, fake GPU operator, kube-prometheus-stack, Grove, Dynamo, NIM, KubeRay and Milvus.
- Kubernetes accepted 65 resources in a server-side dry run: exporter, native DCGM, monitoring, dashboards, relevant Karta definitions and paused CronJobs. Conflict override was used only in this non-persisting validation, not as a deployment instruction.
- Created a SHA-256 inventory for the bundle's static files. These hashes detect local changes; they are not a claim of an upstream publisher signature. Upstream release URLs and available OCI digests are recorded separately.

## Runtime evidence inherited from the reference deployment

The source configuration was tested with 32 healthy Karta workload roots, 32 native DCGM device series, 12 native Pod UID attributions, and eight GPU workloads. After the observation window, four were Active and four were Low activity. Both finite workload types completed with container exit code 0. Anonymous Grafana viewing, workload UID drilldowns and filtered native metric samples passed browser checks.

That evidence is documented in the parent deployment notes. It is evidence for the reference deployment, not a second clean-room installation of this portable bundle.

## Not performed for this documentation task

- No second empty cluster was created or redeployed.
- No existing operator, workload, Secret, dashboard or PVC was changed.
- No source code commit, Git push, or Go presubmit was needed for these standalone deployment documents and assets.
- Bootstrap variants for a fresh FGO pool, a standalone Karta operator and a fresh monitoring operator were chart-rendered, but not installed again. Existing equivalents were reused in the reference environment.

Before presenting a new installation, run the readiness, PromQL, finite workload and browser checks in the guide. Cluster-specific admission policies, storage, registry access and gateway configuration can still affect deployment.
