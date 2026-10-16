<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# CEL implementation and execution evidence

16 October 2026. Companion to the earlier investigation and PDF.

The exporter now evaluates optional CEL rules against cached Kubernetes metadata.
The operator can separately stamp the original Kubernetes request principal.
The Karta CRD, SDK, and the names and labels of the three existing KEP metrics
are unchanged. This is an implementation PoC, not proof of universal attribution.

The requested eight-workload matrix is not complete. Direct Kubernetes and
NVCF execution were tested. Authenticated Run:AI submission and the two new OSMO
execution cases remain blocked as described below. Earlier OSMO execution
results remain valid historical evidence; they are not tests of this new binary.

## Execution matrix

| Platform | Requested workload | Submission and execution | CEL result |
| --- | --- | --- | --- |
| Direct Kubernetes | Deployment | Created through admission as the test principal `alice@example.test`; Pod ran | Root and workload metric contain the complete principal; Pod metadata has no copied identity |
| Direct Kubernetes | Finite Job | Created through admission as the same test principal; Job Complete at 00:05:29 UTC | Root and workload metric contain the complete principal |
| NVCF | Container function | Created and deployed through the local NVCF API; Running; GET invocation HTTP 200, 896 bytes | Pod metadata has account/function/version IDs but no submitting principal; `unknown` |
| NVCT | Finite task | Submitted through the local task API; Running then COMPLETED; result uploads disabled | Pod and ICMSRequest parent both report `unknown` |
| OSMO | Single-task workflow | New request rejected before execution: no whole CPU available; fractional CPU request also rejected by the schema | No new Pod, so no new CEL execution result |
| OSMO | Two-task group | Prepared with a leader and worker; execution pending capacity approval | Not yet verified with the new exporter |
| Run:AI | Training | Authenticated submission pending playground credential approval | Not yet verified live |
| Run:AI | Inference | Authenticated submission pending playground credential approval | Not yet verified live |

The direct test uses Kubernetes impersonation of a namespace-scoped fixture
principal. It proves which `request.userInfo.username` the admission handler
records. It does not prove production OIDC authentication or that a principal is
human. Accordingly, its metric uses `submitter_type="unknown"`.

The NVCF tests use a self-hosted development installation and local service
credentials. NGC is used only for pulling images. No images or results were
uploaded to NGC. CPU/sleep fixtures and fake GPU allocation do not test GPU work.

## Use the implementation

The executable configuration is
[`exporter/examples/submitter-attribution/values.yaml`](../../../exporter/examples/submitter-attribution/values.yaml).
From the repository root:

```sh
helm upgrade --install karta ./charts/karta \
  --namespace karta-system --create-namespace \
  --values exporter/examples/submitter-attribution/values.yaml
```

Build and supply operator/exporter images containing this branch. Published
release images predating this change do not support these flags. The example
namespaces and identity authorities are placeholders to configure for the
installation. It grants no additional platform CRD permissions automatically.

For an existing operator installation, use a separate release with distinct
`fullnameOverride` and `exporter.fullnameOverride` names. Set
`operator.webhookOnly=true` to serve submitter admission without reconciling
Karta definitions. Keep the operator namespace outside the admission selector.
The exporter aggregation label is cluster-wide by default; separate names alone
do not isolate aggregated RBAC. The lab uses a separate, explicit read role.

CEL and admission are independent options:

| Setting | Behavior |
| --- | --- |
| `exporter.attribution.enabled` | Enables two new companion metrics; defaults to false |
| `exporter.attribution.rules` | Named, typed CEL extraction rules |
| `exporter.attribution.resourceKinds` | Optional exact `group/version/Kind` metadata watches, including resources with no Karta definition |
| `submitterAdmission.enabled` | Installs scoped TLS mutation and validation webhooks; defaults to false |
| `submitterAdmission.identitySource` | Identity authority for the Kubernetes API principal |
| `submitterAdmission.namespaceSelector` | Explicit namespace scope; an empty selector is rejected |
| `submitterAdmission.resourceRules` | Explicit namespaced resource types; defaults to Deployments, Jobs and Pods |

The tested Kubernetes 1.35.5 cluster does not expose the MutatingAdmissionPolicy API. The implementation
uses the operator's existing certificate provisioning and rotation. It does not
weaken TLS, bypass verification, or change other admission webhooks.

### A generic rule

```yaml
exporter:
  attribution:
    enabled: true
    rules:
      - name: platform-submitters
        matchExpression: "resource['namespace'] == 'trusted-team'"
        submittedByExpression: |
          ('run.ai/submitted-by' in annotations)
            ? annotations['run.ai/submitted-by']
            : ('osmo.submitted_by' in labels)
              ? labels['osmo.submitted_by']
              : ''
        identitySourceExpression: "'configured-tenant'"
        submitterTypeExpression: "'unknown'"
        partial: true
```

This compact example is intentionally conservative: either branch is partial.
Use separate named rules, as in the supplied values file, when the two platforms
have different authorities or completeness. Do not use a generic `user`
annotation outside a deliberately configured trusted namespace.

The variables are `labels`, `annotations`, and `resource`, each a
`map<string,string>`. The resource map contains `namespace`, `name`, `group`,
`version`, and `kind`. Missing labels/annotations become empty maps. The bare
identifier `namespace` is reserved by CEL; use `resource['namespace']`.

`matchExpression` returns bool. The three identity expressions return strings.
An empty submitter means no candidate. Every matching nonempty rule participates
in conflict detection. A ternary chain inside one expression chooses its own
precedence and can hide conflicting keys; use separate rules when those
conflicts matter. Ten-key conditional lookup is covered by an executable test.

No platform names or metadata keys are hardcoded in the evaluator. The example
configuration owns those choices. The host does not call Run:AI, OSMO, NVCF,
a directory, or an audit API from CEL or during a Prometheus scrape.

### Read a platform parent without a Karta definition

```yaml
exporter:
  attribution:
    resourceKinds:
      - nvca.nvcf.nvidia.io/v2beta1/ICMSRequest
      - scheduling.run.ai/v2alpha2/PodGroup
```

Also grant get/list/watch on the corresponding resources through explicit
exporter RBAC. This emits resource identity observations. It does not establish
a workload hierarchy or promote the resource into a Karta workload.

## What was submitted

The direct Deployment and Job use one `busybox:1.37.0` container. It prints a
marker and sleeps for 120 seconds. The Job has `restartPolicy: Never`, zero
retries, and a one-hour TTL. The Deployment restarts its container normally.
Both creation requests deliberately supplied a forged submitter annotation.
Admission overwrote it with the authenticated/impersonated request principal.

The OSMO fixtures use `ubuntu:24.04`, a 180-second finite shell command, 512 MiB
memory and 1 GiB storage per task. The group has an explicit leader and worker.
The first single-task request failed CPU availability validation. A retry with
`cpu: 0.1` failed because this workflow field accepts integers. The final prepared
requests use one CPU. They have not passed the capacity gate in this run.

The NVCF function uses `nvcr.io/nvidia/omniverse/simple-nginx:1.0.2`, an HTTP
health check and one fake GPU instance. Its function ID is
`abb682e4-172b-4129-b5ac-90659cf3b6df`; version ID is
`5cc50881-723c-470e-b1dc-184cb4f64f73`. It was undeployed after its successful
invocation to free capacity for the task.

The NVCT task uses `nvcr.io/nvidia/cuda:12.6.3-base-ubuntu22.04`. It waits for the
helper-owned results directory, writes valid progress with its task ID, sleeps
for 180 seconds, then reports 100 percent. `resultHandlingStrategy: NONE`
prevents result uploads. Task ID:
`0d8660f6-e558-48eb-a4f1-ead2779067af`.

Selected requests, metadata and metric samples are in
[`evidence/cel/`](evidence/cel/). They omit credentials, internal endpoints,
node names and unrelated workload data.

## Metric results and queries

`karta_resource_submitter_info` describes the identity on each observed resource.
`karta_workload_submitter_info` combines a confirmed Karta root with its currently
attributed Pods. A nonempty root identity can supply the workload identity when
its Pods have no submitter. Conflicting nonempty candidates clear the principal.
Intermediate owners are not implicitly included in this aggregation.

Representative samples, with structural labels omitted for readability:

```prometheus
karta_workload_submitter_info{workload="direct-deployment",submitter="alice@example.test",identity_source="kubernetes:attribution-lab",submitter_type="unknown",status="complete",rule="canonical"} 1
karta_workload_submitter_info{workload="direct-job",submitter="alice@example.test",identity_source="kubernetes:attribution-lab",submitter_type="unknown",status="complete",rule="canonical"} 1
karta_resource_submitter_info{kind="ICMSRequest",submitter="",identity_source="",submitter_type="unknown",status="unknown",rule=""} 1
```

Inspect current workload attribution:

```promql
karta_workload_submitter_info{namespace="karta-direct-test"}
```

Inspect Kubernetes metadata observations for NVCF:

```promql
karta_resource_submitter_info{namespace="nvcf-backend"}
```

Count identities by result without hiding failures:

```promql
count by (status) (karta_workload_submitter_info)
```

Join submitter onto existing workload-to-Pod mappings in a single cluster:

```promql
max by (namespace, workload, workload_uid, pod, pod_uid)
  (karta_pod_workload_info)
* on (namespace, workload, workload_uid)
  group_left(submitter, identity_source, submitter_type)
max by (namespace, workload, workload_uid, submitter, identity_source, submitter_type)
  (karta_workload_submitter_info{status="complete", submitter!=""})
```

Add a consistent cluster label to every key for multi-cluster data. Keep unknown,
partial, conflict and stale counts visible in separate panels. Filtering them
out of this successful-attribution join does not make them disappear from the
coverage denominator. The captured [Prometheus API response](evidence/cel/prometheus-query-result.json)
contains both direct workloads with `status="complete"` and gauge value 1.
Prometheus `honorLabels: true` preserves the resource
namespace instead of replacing it with the exporter's namespace.

## Failures found and corrections

| Finding | Correction or remaining limit |
| --- | --- |
| Bare `namespace` fails CEL parsing | Expose the typed `resource` map |
| Root metadata can change without generation or owner changes | Fingerprint all CEL inputs; reevaluate metadata changes; reuse results for status-only updates |
| A deleted/recreated Pod can reuse its name | Key results by UID; delete old results and reject stale events |
| Root and Pod disagree | Emit `conflict`, clear identity, recover after the conflicting value is removed |
| Old cached identity could appear current | Gate output on observation health; verification also compares identity input fingerprints |
| NVCA Pod owner reference lacks `controller: true` | Add explicit metadata-only kind watches; do not invent a function-level owner mapping |
| Exporter names collide across chart releases | Add an optional exporter name override; preserve existing default names |
| Operator reconciliation is unnecessary for an admission-only test | Add webhook-only mode; omit Karta reconciliation permissions in that mode |
| An explicit empty namespace selector could select everything | Reject empty selectors when admission is enabled |
| User-supplied owner metadata could bypass direct stamping | Stamp non-system callers even when they supplied owner references; protect captured fields on updates |
| Cluster-scoped metadata used an incorrect namespace/name cache key | Use the Kubernetes cache key helper; test both namespace scopes |
| Missing read permissions prevented readiness | Add exact owner resource reads to the isolated lab role; no wildcard or Secret access |
| Test ServiceMonitor was outside Prometheus discovery scope | Place it in the monitored namespace and target only the new service |
| Prometheus can overwrite exported namespace labels | Enable `honorLabels`, as the chart's existing ServiceMonitor already does |
| NVCF fixture initially lacked CPU capacity | Temporarily reduce the lab OpenBao request and new test Pod requests; restore OpenBao after task completion |
| New OSMO workloads need whole CPU capacity | Pending approval for temporary lab service CPU adjustments; not claimed fixed |
| Run:AI login was unavailable for this cluster | Pending explicit approval to use its bootstrap credential; no authenticated submission claim |

## Limits that remain

1. CEL cannot recover information that a platform never puts on Kubernetes.
   NVCF account/function/task IDs do not identify the individual caller. OSMO's
   short label cannot distinguish two emails with the same local part.
2. `complete` means a configured rule supplied an unshortened identifier. It is
   not proof of authentication, a human, coverage of every Pod, or directory
   normalization. `partial` preserves lossy or authority-less values.
3. Full identity is keyed by authority and identifier. Identical usernames from
   different tenants are not equivalent. No directory or cross-source mapping
   is implemented. Service principals are not guessed to be people.
4. Metadata remains untrusted unless the platform controls it. Canonical
   annotations on resources outside the admission scope can be forged. Rules
   must be reviewed; broad extraction can publish sensitive annotation values.
5. Admission sees the Kubernetes principal, including impersonation. Service
   accounts submitting directly are services. System/controller-created children
   are skipped; delegated human identity is not recovered. A service account can
   create owned objects carrying arbitrary metadata, so this is not an identity
   attestation or authorization mechanism.
6. The admission selector and resource rules define coverage. Existing objects
   are not backfilled. Default rules do not include CronJobs, StatefulSets or
   arbitrary CRDs. Add explicit rules for the original resource when needed.
   Fields captured on creation are immutable within the selected scope; audited
   corrections need a separate administrative process. Admission fails closed:
   webhook unavailability blocks matching CREATE/UPDATE operations. Namespace
   label changes or removing admission can remove that protection. Do not enable
   it broadly without agreeing the scope and availability requirements.
7. Generic extraction applies only to watched resources. Pods are always watched;
   other kinds need a Karta/observed controller owner or `resourceKinds` and RBAC.
   A value on an unobserved parent, namespace, API database row or container
   environment is not found by scanning Pod annotations.
8. The tested NVCA owner link is a non-controller owner reference. Existing Karta
   resolution consequently treats the described Pod as the workload. The new
   parent resource metric does not establish one function-level workload across
   replicas. OSMO workflow API records are also not Kubernetes root CRDs.
9. Workload identity combines only its selected root and currently attributed
   Pods. A submitter present only on an intermediate object is visible through
   its resource metric, but is not automatically inherited. Missing owner links,
   ambiguous structure, missing definitions or the existing walk-depth limit
   still limit workload joins.
10. Metadata corrections are reflected, including later edits outside the
    protected admission scope. This is current attribution, not a durable
    historical submitter ledger. Deleted resources disappear from current output.
    Prometheus retains history according to its retention settings. Do not join
    old GPU samples to today's mapping.
11. Freshness is bounded by the existing verification interval and maximum age.
    It is not instantaneous proof of API reachability. Unhealthy/stale output
    clears the identity. A rapidly changing collection can temporarily report
    stale during verification.
12. Rules compile once at startup. Helm config changes roll the Deployment.
    Editing a mounted ConfigMap alone does not hot-reload it. Invalid configuration
    rejects the new process; there is no in-process last-valid hot-reload manager.
13. Limits are 32 rules, 8,192 bytes per expression, 256 KiB configuration,
    10,000 CEL cost units per expression and 512 bytes per output field. Invalid
    types, missing unguarded keys, control characters, invalid UTF-8 or oversized
    values produce `evaluation_error`, not a guessed identity. Identity output is
    never truncated. The bound is per expression, not per scrape or whole ruleset.
14. New companion metrics add roughly one series per observed resource and one
    per confirmed workload, plus churn when identity/status changes. The local
    single-rule microbenchmark is not a 100,000-workload cluster test. Full-scale
    scrape, memory, update fan-out and recovery still need measurement.
15. This run does not prove all Run:AI composers, OSMO submission modes, hosted
    NVCF, Helm-based functions, external delegation, real GPU execution, or
    production authentication. The requested eight-workload execution matrix
    remains incomplete until its blocked entries are run.
16. Identity records are keyed by resource UID. Avoid configuring several served
    API versions of the same group/kind: version-dependent rules can replace one
    another and verification can remain stale. Select the version used by the
    chosen Karta definition. Multi-version identity observation has not been
    validated as a supported mode.

## Validation and reproducibility

Platform-shaped unit fixtures also exercise the shipped chart example for
Run:AI, OSMO and NVCF. These are not authenticated platform submission tests.

The focused race suite covers extraction, ten-key lookup, bad configuration,
missing keys, evaluation budgets, partial/conflicting tenants, metadata updates,
UID replacement/deletion, stale output, and admission mutation/immutability.
The existing exact three-family KEP contract test still passes with attribution
disabled. Exporter `make check` passed, including all race tests and lint.
Operator `make check` passed, including envtest integration tests on Kubernetes
1.31.0. Seven chart configuration cases passed: defaults, enabled extraction,
admission-only mode, and rejection of empty scope, disabled TLS, empty rules and
admission-only mode without capture. The normal Helm/rule checks also run.

A local single-rule benchmark evaluated 100,000 inputs at approximately
1.35 microseconds per evaluation, 1,979 bytes and 43 allocations per operation
on an Apple M4 Pro. This excludes API watching, fingerprinting, aggregation and
Prometheus serialization, and must not be presented as fleet-scale validation.

The lab deploys compiled Linux binaries through a temporary volume and a public
base image. No unpublished binary was published to a registry. Chart rendering
supplies the feature flags, ConfigMap, TLS and selectors; a local rendering step
adds the binary volume and isolates RBAC. A production image/release pipeline
and immutable image provenance still need their normal release verification.
