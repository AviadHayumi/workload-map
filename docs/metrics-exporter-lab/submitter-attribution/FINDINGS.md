<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Submitter attribution experiment log

Target: isolated KIND test cluster; context name redacted.
Started: 2026-10-15.

## Current verified status

- OSMO 6.3.1: installed; all 13 Deployments ready. Four finite identity
  workflows completed. API identities remained full, while Pod/PodGroup labels
  collapsed two distinct emails to alice and omitted a one-character username.
  Development identity headers were used; production OIDC was not tested.
- NVCF self-managed: function became ACTIVE and a GET invocation returned 200.
  The function was undeployed afterward. Pod/workload metadata and creation
  audit were correlated; the observed principal was a client, not a human.
- NVCT: corrected finite task reached COMPLETED at 2026-10-15T18:48:11Z.
  Creation audit names the local API-key owner; Pod and root contain IDs only.
  No NGC results were uploaded. Earlier failed attempts remain documented below.
- NGC: authenticated chart, image, and resource reads succeeded; pull-only use.
- Run:AI: source path inspected, existing Pods inspected. No authenticated
  Run:AI workload submission was performed, so live coverage is not claimed.
- Final health: all 32 lab Deployment/StatefulSet controllers are ready; no
  Pods remain in nvcf-backend. The temporary CPU adjustment was restored.
- Limitations: production auth, hosted NVCF, Helm/shared-cache modes, and real
  GPU computation were not tested. Malformed NVCA cancellation needed manual
  lab cleanup; no upstream fix was made. Some saved Helm overrides are not yet
  reconciled into installed release values.

Concise report: [RESULTS.md](RESULTS.md).

The progress entries below are chronological and include superseded failures.
Paths mentioned in those entries refer to the private original lab directory.
This publication includes only the sanitized subset indexed in EVIDENCE.md;
installer logs, credentials and rendered installation files are not copied.

## Scope and isolation

Install and test NVCF first, then OSMO, in the user-selected existing KIND cluster.
Do not change the shared kubectl current context. Use explicit context flags and
an isolated kubeconfig for installers that do not expose a context flag.
Keep Helm and application CLI state in the private lab directory. Inventory shared operators
before installation. Do not replace existing cluster-wide dependencies blindly.
Record non-secret findings only. Credentials and kubeconfig contents are never
included in this report or terminal output. No changes to source repositories.

## Test plan

1. Verify cluster connectivity, version, capacity, existing Helm releases, storage,
   ingress, GPU resources, and operators.
2. Install an isolated NVCF control and compute plane using the remote installation
   path. Submit a minimal function and, when supported, a finite task. Record API
   identity, workload IDs, workload/Pod metadata, and correlated audit events.
3. Test identity differences between named users and service identities where the
   installed authentication mode supports them. Do not equate an account ID with
   an individual human.
4. Install OSMO with authentication and submit a minimal finite CPU workflow.
   Compare API submitted_by with task Pod labels. Check accepted username, email,
   and unsupported-label cases where the authentication setup permits them.
5. Record results separately as source-verified, component-tested, or live-tested.
   Record bugs, fixes, and untested paths without claiming universal coverage.

## Source versions inspected

- NVCF: `81d815fedda70477655a4ecc1b1a4ee6b4187a32`.
- OSMO: `19b5170268bc2e221f2db9367f5893687b060864`.

## Progress

- Initial sandboxed Kubernetes inventory failed at DNS lookup. No cluster
  resources were changed. A network-enabled retry has been requested.
- Context is present in the local kubeconfig. Shared current context has not
  been modified.
- kubectl, Helm, Docker, KIND, jq, and yq are installed. helmfile, nvcf-cli,
  OSMO CLI, and Bazel are not on PATH.
- NGC_API_KEY and NGC organization/team environment variables are not set.
  This does not yet establish whether usable registry credentials already exist.
- NVCF one-click quickstart requires a local k3d context and is not suitable for
  this existing remote KIND cluster. Use the explicit Helmfile/CLI primitives.

## Live results

Initial checkpoint: cluster connection verified; workload submission had not yet been tested. See Current verified status for the latest results.

## Bugs and blockers

| ID | Category | Observation | Status |
| --- | --- | --- | --- |
| ENV-001 | Connectivity | Initial API hostname lookup failed in the sandbox | Resolved by approved network-enabled access |

## References

- [NVCF remote installation](https://docs.nvidia.com/nvcf/self-managed/helmfile-installation)
- [OSMO local deployment](https://nvidia.github.io/OSMO/main/deployment_guide/appendix/deploy_local.html)

### Inventory completed

- Kubernetes server: v1.35.5 on Linux amd64; client: v1.34.2.
- Five KIND nodes, each reporting the shared host capacity of 8 CPUs and about
  30 GiB RAM. These figures must not be summed as independent physical capacity.
- One worker advertises 32 fake GPUs.
- Run:AI, Karta demo/metrics, Prometheus, cert-manager, and multiple workload
  operators already exist. They must be preserved.
- Default StorageClass: standard, local-path provisioner, WaitForFirstConsumer.
- NVCF fixed service namespaces and an OSMO namespace are not present.
- First automatic network permission review timed out; one retry succeeded.
- Created mode-0600 isolated kubeconfig with only the selected context.
- No NGC login found in the normal Docker/Helm registry configuration; registry
  access must be tested before installation. Existing unrelated credentials were
  not printed or changed.

### Registry preflight

- NVCF public Helm catalog URL returned HTTP 403 Access forbidden.
- Anonymous registry token flow could not access
  nvcr.io/nvidia/nvcf/nvcf-service-oss:1.18.0 (HTTP 401).
- NVCF installation is blocked until authorized artifact access is available.
  Asked for a credential location, existing pull Secret, or registry mirror.
  No credentials were requested in chat.
- OSMO public Helm index returned HTTP 200. Anonymous access to
  nvcr.io/nvidia/osmo/service:6.3.1 succeeded, manifest digest
  sha256:33ee79c7d59bdcbabb87d9edb79d04bcfd20c7a47759eca002e1319f24814ab8.
- Copied OSMO chart to this lab directory; source checkout remains untouched.
- Existing Run:AI KAI scheduler owns cluster-wide scheduler CRDs. Reusing or
  compatible configuration must be established before any scheduler install.

### OSMO preparation

- Downloaded dependencies exactly as pinned by Chart.lock: Dex 0.24.1,
  Valkey 0.11.0, PostgreSQL cluster 0.8.0, RustFS 1.0.0.
- Downloaded CloudNativePG operator chart 0.29.0, configured to watch only
  its installation namespace.
- OSMO service tags latest and 6.3.1 resolved to the same manifest digest.
  Use explicit 6.3.1 image tags for this test.
- OSMO defaults would conflict with existing ingress NodePort 30080. Use a
  ClusterIP gateway plus a session-specific localhost port-forward instead.
- OSMO defaults expect kai-scheduler; existing Run:AI config uses runai-scheduler
  and runai/queue. OSMO source sets both queue labels; override scheduler name
  rather than installing or replacing the existing scheduler.
- Added four temporary bootstrap test identities: alice, alice@team-a.example,
  alice@team-b.example, and a. They use the standard osmo-user role and generated bootstrap tokens.
  The chart supports bootstrap token expiry only for admin identities. Test user
  credentials must be revoked or removed after verification.
- First chart render rejected identity settings: the dex object requires email
  even when enabled=false. Supplied unique test emails; rerender pending. This
  is a corrected lab configuration error, not yet evidence of a product bug.

- Second render rejected expiry on non-admin bootstrap identities. Removed the
  unsupported expiry/generation options while retaining the osmo-user role.

### First cluster mutation

- Created namespace osmo-submitter-lab, labeled as part of the attribution lab.
- Started a bounded, unprivileged image preflight Pod with no service account
  token. It checks /osmo/bootstrap-step --help in the immutable published
  service image before installing the full chart.
- The copied chart renders 100 resources with the proposed overrides and
  CloudNativePG API capability. No rendered resources have been applied yet.
- Source contains 6.4-era bootstrap configuration while the chart appVersion and
  public latest image are 6.3.1. Runtime compatibility is being checked first.

### Confirmed runtime compatibility failure

BUG-OSMO-001: main-branch unified chart is not compatible with the published
6.3.1/latest service image. Its bootstrap Job invokes /osmo/bootstrap-step.
The bounded live preflight Pod failed with StartError / exit 128 because that
path does not exist in the image. Both public tags resolved to the same digest.

The new unified chart was not installed. The published chart catalog has
service/backend-operator 1.3.1 for OSMO 6.3.1; it does not publish the new osmo
unified chart. Use a consistent released chart/runtime pair and distinguish
released-version live results from main-branch source findings.

### Released OSMO installation started

- Source release: 6.3.1, commit ed59b69715e4ee6fff70bcd9d715106358a7205f.
- Built quick-start chart 1.3.1 from that tag with matching local service and
  backend-operator subcharts. Public quick-start catalog currently only lists
  an older prerelease, so the tagged chart is used with 6.3.1 runtime images.
- Namespace: osmo-submitter-lab. Chart-owned test namespace:
  osmo-submitter-lab-test. Workload Pods stay in the lab namespace.
- Gateway is ClusterIP. Shared ingress and host files are unchanged.
- Removed quick-start-specific node_group selectors in local overrides and the
  copied setup template. No shared node labels were changed.
- Embedded PostgreSQL and Redis use standard PVCs. Local object storage uses
  an ephemeral volume, not the quick-start hostPath. A generated database
  password is stored only in the private local values file and Kubernetes.
- This released quick-start uses development-mode identity headers. Initial
  tests can verify metadata propagation and formatting, but cannot prove
  authenticated human identity or production OIDC behavior. Authentication
  coverage must be reported separately.
- No CloudNativePG operator was installed; the released chart uses its own
  embedded PostgreSQL deployment.
- Started Helm installation with a ten-minute timeout. Evidence log:
  evidence/osmo-install.log. Installation completion is not yet verified.

### Scheduling fix

Initial released-chart Pods remained Pending because nested Helm global values
retained the quick-start node_group=service selector (and one empty label
selector). Setting null in the overlay was not sufficient. Replaced the copied
chart's quick-start-only selectors with kubernetes.io/os=linux and aligned the
overrides. Patch only this lab's new Deployments to unblock the running install;
then reconcile the corrected values through Helm after installation completes.
Shared nodes and scheduler configuration remain unchanged.

- Local port 18081 was already owned by Docker. The attempted forward failed;
  no existing process was stopped. Switched to kubectl's automatically allocated
  loopback port, recorded in evidence/osmo-port-forward-isolated.log. Only two
  read-only probes hit the occupied port; no writes were made to it.

### Authenticated NGC checks and OSMO bootstrap recovery

- User authorized the supplied NGC credential for reads/pulls only. No NGC
  uploads, pushes, or configuration changes are permitted or performed.
- Authenticated pull-scope token and manifest GET both succeeded for
  nvidia/nvcf/nvcf-service-oss:1.18.0 and nvidia/osmo/service:6.3.1.
- OSMO backend bootstrap Job exhausted its retry limit; backend Pods remain in
  wait-for-token and the default backend has not registered.
- Prepared a lab-only bootstrap Job repair: route through the quick-start
  gateway, remove Bash-specific expansion from the POSIX script, remove
  token-response logging, and retain bounded Job evidence for one day.
  Root-cause confirmation and successful bootstrap are still pending.

LAB-002: The rendered retry Job omitted metadata.namespace and its first apply
was sent without -n. It was created in default and could not find the lab
service account. Remove only that newly created Job, add namespace explicitly
to the local manifest, and always use -n osmo-submitter-lab for subsequent
applications. No product workload was submitted by that Job.

### Live OSMO identity results

The repaired backend bootstrap Job completed, the backend registered online,
and workflows now use the existing runai-scheduler. LAB-002 was corrected by
deleting only the accidental default-namespace Job and recreating it with an
explicit lab namespace.

All submissions below use development identity headers, not authenticated OIDC.
Observed API and Kubernetes metadata during execution:

| Supplied identity | API submitted_by | Pod label | PodGroup label |
| --- | --- | --- | --- |
| alice | alice | alice | alice |
| alice@team-a.example | alice@team-a.example | alice | alice |
| alice@team-b.example | alice@team-b.example | alice | alice |
| a | a | absent | absent |

This confirms email-domain loss and a collision between two distinct identities.
It also confirms omission for a single-character username. Full identity remains
available through the workflow API. It does not prove coverage for every OSMO
submission mode or production authentication.

The first fixture requested 2 CPU total including its sidecar and remained
Pending. It was canceled through OSMO. The workflow CPU field accepts integers;
a fractional value was rejected before creating a workload. Lab Pod templates
now request 100m for the sleeping user container and 50m for its sidecar. The
user-container CPU limit remains 1. Shared workload templates are untouched:
these are templates in the newly installed isolated OSMO service.

### NVCF artifact and tooling checks

Authenticated pulls of the control-plane chart versions pinned by stack tag
self-managed/v1.0.1 succeeded and matched catalog SHA256 values. Container
manifest access works with a pull-only token; the anonymous comparison grants
no pull action. The NGC profile read returned HTTP 200. Resource bundle listing
returned HTTP 403, so matching already-cloned source release tags are used.
No NGC writes or pushes were made.

Helmfile 1.1.9 invokes a flag removed by Helm 4. Installed Helm 3.19.0 only in
this lab directory and verified its official archive checksum. Global Helm and
kubeconfig settings remain unchanged. Gateway names are required by the stack
values template even when gateway routes are disabled; supplied unused names
and disabled the admin issuer gateway in the lab chart overrides.

All four bounded OSMO fixtures reached COMPLETED. Their final API responses are
saved under evidence/*-workflow.json. Pod and PodGroup metadata were collected
while they were running, because OSMO removes those Kubernetes objects after
completion. The canceled oversized fixture is tracked separately.

NVCF preflight checked 20 required nvcr.io image manifests: 19 were accessible;
the alpine-k8s helper path was not. The stack already documents that alpine-k8s
is not republished in that catalog. Changed the lab-only Cassandra initialization
image to the pinned upstream docker.io/alpine/k8s:1.37.0 used by the account
bootstrap path. Disabled the NATS auth-callout chart's default external Ingress.
The rendered OpenBao admission webhook matches only namespaces labeled for this
NVCF lab. Existing cert-manager, ingress, scheduler, and monitoring are reused.

The 124-resource render was inspected before installation. Created only the
seven previously absent NVCF namespaces, labeled for this lab, and namespace
pull secrets. Started NATS, Cassandra, and OpenBao dependency installation.

### Installation reconciliation and resource correction

- OSMO Helm revision 2 is deployed. All 13 lab Deployments are ready.
- NVCF Cassandra initially OOMKilled because the lab override used obsolete
  heap keys. The downloaded chart requires cassandra.jvm.maxHeapSize. Set it
  to 1536M under the existing 4Gi memory limit. Restarted only the new lab Pod,
  retaining its PVC. It became ready with zero restarts, and initialization
  succeeded. This was a lab values error, not an attribution bug.
- Built the NVCF CLI from the matching release tag in this temporary directory.
  A local-only patch makes NVCF_LAB_STATE select a task-local state file; the
  shared home state and original source checkout are untouched.
- Read-only inspection of existing Run:AI Pods found neither the user annotation
  nor run.ai/submitted-by. No workload was submitted through Run:AI's authenticated
  API during this check. Source findings therefore remain unverified live for
  Run:AI; this absence does not establish that the supported submission paths
  omit metadata.

### NGC resource API correction

The earlier resource-listing 403 was caused by the wrong API path. Repeating the
read at /v2/org/nvidia/team/nvcf/resources/nvcf-self-managed-stack/versions with
the same credential succeeded (HTTP 200), including released version 1.0.1.
This is not an outstanding credential/permission blocker. The release-tag source
used for installation remains pinned. NVCA operator chart 1.28.0 and both
nvca/nvca-operator 3.10.0 image manifests were also accessible read-only.

### NVCF bootstrap ordering

- OpenBao's initialization script explicitly waits for replicas 0, 1, and 2.
  Restored its default three-replica HA shape in the lab values and live
  StatefulSet. Initialization and all migrations then succeeded.
- All dependency Helm releases completed successfully. Started the core API
  releases with install concurrency limited to two for the shared host.
- That reduced concurrency delayed NATS auth-callout behind services that need
  it. SIS initially failed NATS authorization. Installed only the auth-callout
  release ahead of the waiting API stage. It became ready; SIS subsequently
  completed application startup. API Keys and ESS also became ready.
- One SIS restart was a startup probe timeout during the initial recovery.
  Later application startup completed in about 59 seconds. No health probes
  were disabled.
- Session-specific loopback forwards and a task-local CLI config are used.
  Hosted NVCF endpoints and inherited NVCF credentials are excluded from the
  test CLI wrapper. Task tests must use resultHandlingStrategy NONE to avoid
  any NGC result upload.

### NVCF control and compute planes installed

- Control-plane Helmfile completed successfully. The rate limiter initially
  could not schedule because its application and injected secret agent each
  requested 250m CPU. Lab-only requests of 50m and 25m resolved scheduling; the
  invocation service then recovered from its dependency connection failures.
- Local cluster registration succeeded in the supported logical region
  us-west-1. An initial arbitrary region value was rejected without registration.
- Matching-release nvcf-cli was built in this lab. Its state path was isolated
  with a local source patch; normal CLI configuration and state are untouched.
- All CLI endpoints point to task-owned localhost forwards. Local admin JWT
  subject is ncp, a client identity, not a verified human username.
- Function/task test API keys were created only in the local API service with
  two-hour expiry. This does not create or alter NGC keys.
- NVCA operator and agent installed successfully; both operator containers and
  both agent containers report ready. Admission source restricts the workload
  webhooks to NVCF-labeled namespaces. Optional GXCache disabled for these
  resource-free container tests. Shared-cache/Helm resource mode is untested;
  the published Samba image paths checked returned HTTP 401 and 404.
- NVCA reports missing GPU feature labels on the fake-GPU node. Inspect
  discovery before deciding whether a documented manual instance definition
  is necessary; do not claim real GPU or driver validation.

### Bounded CPU-capacity workaround

The existing GPU node was almost fully reserved. Lab control services and NVCA
were moved to the KIND control-plane node. The lab function requests 1 CPU and
its helper 25m. For the short execution tests, only the lab OpenBao replica on
the GPU node temporarily has zero CPU requests; its CPU limits and all memory
settings remain unchanged. The two other replicas retain requests. Original
resource settings are saved for restoration after the function/task tests.
Existing demo Pods, shared node taints, and GPU operator settings are unchanged.

### Live NVCF function and audit evidence

- Function attribution-lab-function became ACTIVE. The runtime Pod is Running,
  inference and utils are Ready, both have zero restarts, and init exited 0.
- Function ID: 0779d9a2-cf6a-4806-9aae-1301a9a5bc8d.
- Version ID: cf1997c3-02bd-459d-969d-36046eab0672.
- Pod owner is an NVCA ICMSRequest. Pod metadata includes nca-id, function-id,
  function-version-id, deployment ID and GPU-specification ID. No human
  submitting identity appears in its labels or annotations.
- The CREATE_FUNCTION audit event contains the same function/version IDs and
  subjectId ncp. This proves local audit correlation, but the subject is the
  configured client identity, not the person who invoked the CLI.
- API-key introspection reports owner_type USER with owner_id
  svc@nvcf-api.local. Even USER classification does not prove a human identity.
- The deprecated cluster-list endpoint returns 403 for this CLI auth setup;
  the documented ICMS cluster-management endpoint works. This is separate
  from successful function create/deploy/get operations.

- GET invocation through the local NVCF invocation service returned HTTP 200
  and the test container's Nginx page. This exercised the NVCF request path.
- CLI direct invocation assumes function-prefixed DNS and failed with a bare
  localhost forward. Supplying the function Host header resolved routing,
  without DNS changes. Its default POST returned the expected Nginx 405; GET
  matched the static test application's supported method and succeeded.
- Function deployment was removed through the API after capture, to release
  capacity before the finite task test. The function definition is retained.

### NVCT task packaging failure and retry preparation

BUG-NVCF-004: released NVCT chart references nvcf_worker_init:2.107.0 and
nvcf_worker_task:2.107.0 under the configured public NVCF repository. The live
task hit ImagePullBackOff on the init image. The blocked task was canceled
through the API before retrying.

The registry tag list for nvcf-worker-task-oss returned 1.1.4. Task helper
references are corrected in the lab Helm values to nvcf-worker-init-oss:1.2.1
and nvcf-worker-task-oss:1.1.4. Successful execution after this correction is
still pending. No missing helper was replaced with a stub.

The first task's CREATE_TASK audit event recorded actorId and subjectId
svc@nvct-api.local, matching the local API-key owner. Its Pod carries task/account
IDs, not that owner identity. This is a second live authentication path, distinct
from the function's ncp management JWT. The service-style owner is not a verified
human username. Result handling is NONE; no task output upload was requested.

### NVCT application completion contract

The corrected helper images pulled successfully and the task container ran.
A bare sleep command does not satisfy NVCT's completion protocol: the worker
waits for NVCT_PROGRESS_FILE_PATH, then progress to reach 100. This was a lab
fixture error, not a submitter-attribution defect. That retry was canceled.
The next fixture writes progress 1, sleeps 90 seconds, then writes progress
100 using NVCT_TASK_ID and the provided shared-directory environment values.
Result handling remains NONE.

### NVCT argument parsing correction

The third task was canceled before Pod creation. Its multiline shell argument
failed the NVCA shlex parser. The next fixture passes a single-line command and
a base64-encoded non-secret script through a container environment variable.
Result handling remains NONE, so there is no NGC result upload.

### NVCT shared-directory startup race

The fourth task parsed and launched, but the fixture created its results
directory as root before the helper started. The helper runs as UID 1000 and
failed chmod with operation not permitted. The fixture now waits for the
helper-owned directory instead. Container privilege settings were preserved.
The failed fixture was canceled; the corrected task remains finite.

### NVCT progress schema validation

The next task reached the helper but its progress payload omitted lastUpdatedAt.
NVCT correctly rejected it as an invalid ISO8601 timestamp. The fixture now
includes an RFC3339 timestamp in each atomic progress update. The full progress
validator was inspected before retrying; percentage is monotonic from 1 to 100.

### Malformed request cleanup limitation

The API reported the malformed-arguments task CANCELED, but its ICMSRequest
kept retrying parsing and did not complete deletion. After checking that it
owned no Pods, Secrets, ConfigMaps, Services, Deployments, StatefulSets, Jobs,
or PVCs, removed only its finalizer using UID/resource-version preconditions.
This cleans the lab resource; the NVCA cancellation behavior itself was not
patched. Evidence: evidence/nvcf-malformed-request-cleanup.json.

### Credential artifact separation

A final scan found no NGC key in reports or evidence, including its encoded
forms. Rendered YAML containing Kubernetes Secrets was moved from evidence/
to private mode-0600 files in config/. The private config directory is excluded
from the shareable report and must not be published.

### Final successful NVCT execution and restoration

Task 5bc18dde-08c1-445c-beee-9ab218d121c7 transitioned from QUEUED through
LAUNCHED and RUNNING to COMPLETED. Completion audit timestamp was
2026-10-15T18:48:11.464761904Z. Both task and helper containers were Ready
with zero restarts during execution, and the Pod was removed afterward.
The task uses resultHandlingStrategy NONE.

The temporary CPU change was restored on the lab OpenBao Pod. Final readiness
checks found all 32 lab Deployment/StatefulSet controllers ready, including
13 OSMO Deployments. No Pods remain in nvcf-backend. The OSMO failed image
preflight Pod and malformed NVCA request were cleaned up.

Evidence is linked from RESULTS.md. No original repository was changed and no
NGC write or push was performed. Helm/CLI/kubeconfig state remains isolated.
The installed services are left available for further testing.
