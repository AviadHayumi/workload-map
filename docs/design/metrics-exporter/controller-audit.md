<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Controller ownership audit

A Deployment can adopt an existing ReplicaSet while its Pod keeps the same UID,
resourceVersion and immediate owner. The input chain changes from
`Pod -> ReplicaSet` to `Pod -> ReplicaSet -> Deployment`. Resolving ownership
only when the Pod changes would retain the wrong workload root.

The controller separates Pod attribution from root publication while retaining
ancestor repair. Direct client-go Reflectors feed one accepted-object store.
Ownership-discovery workers resolve Pods and admit non-Pod candidates. Root
workers publish complete workloads. Maintenance workers plan sources and refresh ownership
metadata. The [full sequence](full-flow/design.md) shows startup through shutdown;
[consistency](consistency.md) defines the publication guards.

This audit covers the controller change from `cdcc8779`. The preceding
[component simplification and measurements](simplification.md) and
[review record](verification.md) remain separate evidence.

## Problems, changes and evidence

| Problem | Change and reason | Evidence or remaining limit |
| --- | --- | --- |
| SharedIndexInformer and Reader both retained watched full objects. | A direct Reflector store accepts observations into Reader. Remove informer indexing, handler registration and handler-sync lifecycle. | `sources.go`: `reflectorStore`. This removes an owner of retained objects; it does not establish the cause of the earlier heap failure. |
| Generic operation dispatch mixed discovery, maintenance and publication. | Typed input keys identify object address, API representation and UID. Pod resolution and non-Pod candidate admission share ownership-discovery workers. Root keys contain only UID and only publish. | `runtime.go`, `leaf.go`, `root.go`. Different API representations of one UID share publication serialization. |
| Slow candidate owner reads occupied every root worker and blocked a healthy root update. | Move candidate admission to the existing discovery input queue. Keep publication in the root UID queue. | [Capacity regression](../../../karta-metrics-exporter/pkg/controller/root_capacity_test.go) failed normally and under the race detector before this repair. It passes after moving admission, with unchanged assertions and default worker counts. The final controller and full-module race suites pass. |
| A canceled or retired LIST could record a replacement stamp. | Check context and source incarnation before recording the read stamp. | Source lifecycle regressions passed 30 focused race runs; the final controller and full-module race suites pass. |
| Recovery could begin with resourceVersion `0`, which permits an older cached snapshot. | Start authoritative recovery with an empty resourceVersion to request current state. | Full-snapshot provenance and replacement checks remain. Recovery regressions passed 30 focused race runs; the final controller and full-module race suites pass. |
| Removing intermediate work could lose roots with no Pods. | Retain selected non-Pod candidates and walk their ancestors before admission. Register candidate path dependencies alongside Pod paths. | A selected child can reveal an unselected generic ancestor even with zero Pods. Missing-owner failure retains work for retry. |
| Ancestor ownership can change without a Pod event. | Keep targeted path invalidation and scheduled authoritative metadata refresh. | Pinned operator findings below. The [final-image kind regression](#final-image-kind-acceptance) also verifies exporter reattribution with unchanged Pod metadata and no Pod watch events. |
| Returning an error from a rejected Reflector callback does not ensure replay. | Invalidate affected evidence and coalesce a full-source restart when current state is not already proven equivalent. | client-go v0.37.1 logs callback errors and advances its cursor. Metadata verification cannot reconstruct a missed full object. |
| A partial or stale replacement could remove live objects or renew old proof. | Stage the complete replacement. Check source incarnation, failure epoch, read-start age, object revisions and alias fences before acceptance. | Empty snapshots retain their read stamp. Absence is UID-safe and requires a complete accepted snapshot. |
| Publication renewal repeatedly scanned saved Pod rows. | Carry a conservative earliest evidence deadline while building publication. Sparse updates may shorten it but cannot extend unchanged rows. | Removing the earliest row may cause an early refresh until the next full pass. Urgent work still wins. |
| One worker setting could not tune discovery and evaluation separately. | Add `leafWorkers` for Pod/candidate discovery and `rootWorkers` for publication; zero inherits `workers`. Resolve precedence before inheritance. | `workers` remains positive and defaults to two. Negative overrides fail validation. |
| Obsolete owner-health failures can remain after workload rows disappear. | No production change. Existing owner maintenance prunes failures after recomputing metadata demand. | The [inherited cleanup delay](open-issues.md#obsolete-metadata-health-after-workload-deletion) is separate from workload deletion. A coalesced maintenance pass is an optional proposal, not an implemented fix. |
| Six files alone would only relocate complexity. | Remove duplicate storage and generic operation dispatch, assign sole publication ownership, and remove renewal-only scans. Consolidate the remaining cohesive operations. | Six files contain 3,818 physical lines versus nine files and 3,605 lines before (+213). There is no net line reduction. A file-count target is not a performance result. |

The implementation has six production controller files:
[runtime.go](../../../karta-metrics-exporter/pkg/controller/runtime.go),
[sources.go](../../../karta-metrics-exporter/pkg/controller/sources.go),
[reader.go](../../../karta-metrics-exporter/pkg/controller/reader.go),
[owners.go](../../../karta-metrics-exporter/pkg/controller/owners.go),
[leaf.go](../../../karta-metrics-exporter/pkg/controller/leaf.go) and
[root.go](../../../karta-metrics-exporter/pkg/controller/root.go).
Their responsibilities are listed in the [controller README](../../../karta-metrics-exporter/pkg/controller/README.md).

## Alternatives and boundaries

Keeping SharedIndexInformer would retain a second persistent full-object store
and handler lifecycle although this controller does not need its indexes.
A direct Reflector fits the required LIST/WATCH transport, provided acceptance
and recovery remain explicit. The
[installed Reflector callback handling](https://github.com/kubernetes/client-go/blob/v0.37.1/tools/cache/reflector.go#L1036-L1075)
explains why returning a stale-event error alone is insufficient.

A Pod-only queue with no ancestor repair is smaller, but misses the opening
example. Removing selected non-Pod admission also loses an unselected ancestor
when no Pod exists. Watching every intermediate kind would add full-object
streams and permissions for data used only as ownership metadata. The chosen
path retains grouped metadata acquisition, shared GETs, permission fallback and
a small ancestor-to-consumer index.

Putting candidate admission in the root queue initially kept all work for a
root together. A capacity test blocked every worker in candidate owner GETs,
then updated an unrelated already healthy root. Publication could not advance
until those GETs completed. Candidate discovery therefore shares the existing
input queue with Pods. Accepted healthy-root changes can still queue publication
without waiting for candidate discovery. The external `leafWorkers` name remains,
but now counts both kinds of ownership-discovery work.

A fourth queue or asynchronous request framework would also separate slow reads,
but would add workers and another request lifecycle. The existing input queue
provides the needed isolation while retaining three queues. This does not prevent
slow owner reads from delaying other discovery work; retries and evidence expiry
still bound what can remain valid.

A general ownership graph or a new scheduler could combine these operations,
but would add a new lifecycle for demand, retries and eviction. Concrete state
and three explicit work lanes keep the existing contracts. Root status caching
and merging Reader/Runtime locks are outside this change. API requests and
expression evaluation remain outside those locks.

The final root commit still checks UID, generation, selected definition origin,
root representation/resourceVersion, root boundary and active proof keys.
Failed calculations restore drained dirty work. One root worker owns publication
for its UID; discovery workers commit attribution or admission and queue that UID. Store
keeps immutable copy-on-write Pod pages and independent synchronization.

## Operator source findings

All arrows below follow `controller=true` references from child to parent.
A catalog definition target is not necessarily the terminal workload root.
Discovery continues through an object's controller owner even when that object
has a Karta, stopping at the terminal or configured ignored-owner boundary.

These are bounded source audits of 13 repositories. They establish writers and
possible transitions, not production frequency. No operator-specific adoption
tests were executed by the source audits. Prepared HEAD revisions do not prove
parity with installed operator images. Training Operator is the exception: its
v1.9.0 tag was read directly. Native controller and dependency boundaries remain
visible in each finding.

| Repository and audited revision | Ownership and change finding | Source-version limit |
| --- | --- | --- |
| Kubernetes `6737adf59793` | Pod -> ReplicaSet -> Deployment; Pod -> Job -> CronJob; direct StatefulSet/Job ownership. Deployment adopts matching orphan ReplicaSets and releases its nonmatching ones by patching the ReplicaSet. Direct Pod adoption by ReplicaSet, StatefulSet and Job changes the Pod. CronJob does not claim orphan Jobs. | Prepared HEAD; separate probe ran Kubernetes v1.34.0. |
| JobSet `01901ec9def3` | Pod -> Job -> JobSet. Creates owned Jobs; an unowned name collision is a creation error. Suspend, scale and restart preserve ownership or replace Jobs. No existing-Job adoption writer found. | Prepared HEAD, not installed-release parity. |
| LeaderWorkerSet `53466a1dddaa` | Ordinal leader: Pod -> StatefulSet -> LWS. Workers add worker StatefulSet -> leader Pod before that chain. Hash leaders use Deployment/ReplicaSet. Leader apply can repair an existing intermediate owner without patching Pods; existing worker StatefulSets are reused without ownership repair. | Prepared HEAD. Hash mode and imported native behavior must not be assumed present in the installed release. |
| KubeRay `fa76e8c19501` | Pod -> RayCluster -> RayJob/RayService; submitter Pod -> Job -> RayJob. Existing cluster reuse and service promotion preserve references. PVC retention changes are outside the Pod ancestor chain. | Prepared HEAD. Redis cleanup template ownership needs native-controller or live confirmation. |
| KServe `2797996f1b50` | Standard InferenceService owns Deployments. A spec-triggered whole-object patch can also replace the Deployment owner; a replicas-only change need not modify surviving Pods. Knative Service updates preserve ownership. LLM child updates reject a different controller UID. | Prepared HEAD; installed-tag parity unverified. ModelMesh and downstream LWS behavior are separate boundaries. |
| Knative Serving `f2153bddfbb2` | Pod -> ReplicaSet -> Deployment -> Revision -> Configuration -> Service. Existing Configuration and Deployment updates require expected ownership and retain it. No Knative reparent writer found. Native ReplicaSet adoption remains possible below its Deployment. | Installed v1.15.2 source blobs unavailable; prepared HEAD only. |
| Dynamo `3763cee6080c` | Deployment/LWS -> DynamoComponentDeployment -> DynamoGraphDeployment; Grove PodCliqueSet -> DGD. Common sync rejects missing or foreign ownership. Legacy provider/LWS migration reuses already-owned children. SnapshotJob ownership changes cross a separate controller boundary. | Installed v1.2.1 controller blob unavailable; prepared HEAD only. |
| Grove `90d6ff4fba76` | Pod -> PodClique -> PodCliqueSet, optionally through PodCliqueScalingGroup. Create-or-patch rewrites expected intermediate metadata. Imported controller-runtime permits orphan adoption and same-name parent UID refresh, but rejects a different controlling owner. | Installed alpha release blobs unavailable. Adoption conclusion depends on the inspected v0.23.3 helper contract. |
| NIM Operator `0aa8bb720406` | Deployment/LWS/InferenceService -> NIMService. Cache Pods are direct children or Job descendants of NIMCache. Desired objects receive references, but existing workload update helpers preserve live references; no repair writer found. | Installed v3.0.2 blobs unavailable. A StatefulSet renderer alone does not prove an active standalone path. |
| agent-sandbox `4e6cefb00467` | Pod -> Sandbox -> SandboxWarmPool can become Pod -> same Sandbox -> SandboxClaim. Claim adoption patches Sandbox ownership before later Pod metadata propagation. The exporter must consider those extension ancestors. | Installed v1.0.5 blobs unavailable. A standalone Sandbox fixture does not test pool-to-claim transfer. |
| Milvus successor `d2eedcf9f668` | Pod -> ReplicaSet -> Deployment -> Milvus. Ordinary update requests owner-reference repair. One-to-two Deployment migration orphan-deletes/recreates intermediates and relies on native adoption. Legacy MilvusCluster references may be preserved. | `zilliztech/milvus-operator`; archived predecessor rejected. Installed v1.3.7 blobs unavailable. HEAD StatefulSet support must not be attributed to v1.3.7. |
| MPI Operator `b3b5b84250dd` | Direct worker Pod -> MPIJob; launcher Pod -> Job -> MPIJob. Existing named Jobs/worker Pods require expected ownership. No adoption writer found. Launcher template ownership alone does not prove final live Pod ownership. | Shallow HEAD checkout had no v0.8.2 tag. Native Job behavior is a separate boundary. |
| Training Operator `6f74c7f6b2ed` | Pod -> PyTorchJob directly. PyTorchJob overrides shared claiming with label selection plus controller-UID filtering. Its active path does not adopt/release orphan Pods, although a shared unused claiming method exists. | v1.9.0 tag source verified. Current Trainer v2 HEAD was not substituted for it. |

### Pinned public evidence

- Kubernetes: [ReplicaSet adoption/release](https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/controller_ref_manager.go#L319-L370), [shared claim rules](https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/controller_ref_manager.go#L69-L127), [orphaning](https://github.com/kubernetes/kubernetes/blob/6737adf59793fd79cb4a0c65ef36340b66f4643e/pkg/controller/garbagecollector/garbagecollector.go#L680-L701).
- JobSet: [owned Job creation and collisions](https://github.com/kubernetes-sigs/jobset/blob/01901ec9def3631ef358a8ca2fb85d54fc354d7f/pkg/controllers/jobset_controller.go#L969-L997), [owner-UID child selection](https://github.com/kubernetes-sigs/jobset/blob/01901ec9def3631ef358a8ca2fb85d54fc354d7f/pkg/controllers/jobset_controller.go#L482-L506).
- LWS: [leader apply](https://github.com/kubernetes-sigs/lws/blob/53466a1dddaae2528dd7357546fa5adf081fef70/pkg/controllers/leaderworkerset_controller.go#L397-L431), [worker reuse](https://github.com/kubernetes-sigs/lws/blob/53466a1dddaae2528dd7357546fa5adf081fef70/pkg/controllers/pod_controller.go#L237-L265), [hash leaders](https://github.com/kubernetes-sigs/lws/blob/53466a1dddaae2528dd7357546fa5adf081fef70/pkg/controllers/leaderworkerset_hash.go#L116-L195).
- KubeRay: [existing cluster reuse](https://github.com/ray-project/kuberay/blob/fa76e8c19501f13a9c810b85891e81c290353812/ray-operator/controllers/ray/rayjob_controller.go#L952-L994), [RayService cluster updates](https://github.com/ray-project/kuberay/blob/fa76e8c19501f13a9c810b85891e81c290353812/ray-operator/controllers/ray/rayservice_controller.go#L1204-L1248), [PVC ownership](https://github.com/ray-project/kuberay/blob/fa76e8c19501f13a9c810b85891e81c290353812/ray-operator/controllers/ray/raycluster_controller.go#L794-L813).
- KServe: [spec comparison](https://github.com/kserve/kserve/blob/2797996f1b50bd0687ee25995ae52d9fa1343b79/pkg/controller/v1beta1/inferenceservice/reconcilers/deployment/deployment_reconciler.go#L226-L276), [whole-object patch](https://github.com/kserve/kserve/blob/2797996f1b50bd0687ee25995ae52d9fa1343b79/pkg/controller/v1beta1/inferenceservice/reconcilers/deployment/deployment_reconciler.go#L483-L531), [LLM ownership guards](https://github.com/kserve/kserve/blob/2797996f1b50bd0687ee25995ae52d9fa1343b79/pkg/controller/v1alpha2/llmisvc/lifecycle_crud.go#L59-L180).
- Knative: [Configuration ownership check](https://github.com/knative/serving/blob/f2153bddfbb28a9c8719d42ce47bac482b4e8c74/pkg/reconciler/service/service.go#L222-L287), [Deployment ownership check](https://github.com/knative/serving/blob/f2153bddfbb28a9c8719d42ce47bac482b4e8c74/pkg/reconciler/revision/reconcile_resources.go#L58-L85).
- Dynamo: [common ownership validation](https://github.com/ai-dynamo/dynamo/blob/3763cee6080ce6a8ab4156f055d8ce72d984458c/deploy/operator/internal/controller_common/resource.go#L111-L145), [existing-child sync](https://github.com/ai-dynamo/dynamo/blob/3763cee6080ce6a8ab4156f055d8ce72d984458c/deploy/operator/internal/controller_common/resource.go#L254-L338).
- Grove: [GET/mutate/patch](https://github.com/ai-dynamo/grove/blob/90d6ff4fba7669682da0807d82763b220053e6f5/operator/internal/utils/kubernetes/createorpatch.go#L48-L79), [scaling-group reference](https://github.com/ai-dynamo/grove/blob/90d6ff4fba7669682da0807d82763b220053e6f5/operator/internal/controller/podcliqueset/components/podcliquescalinggroup/podcliquescalinggroup.go#L205-L213). The audited dependency is [controller-runtime v0.23.3](https://github.com/ai-dynamo/grove/blob/90d6ff4fba7669682da0807d82763b220053e6f5/operator/go.mod#L30); its helper semantics remain a dependency-qualified finding.
- NIM: [resource synchronization](https://github.com/NVIDIA/k8s-nim-operator/blob/0aa8bb72040607bf0439793e1df98e141856cace/internal/k8sutil/k8sutil.go#L211-L255), [existing workload update fields](https://github.com/NVIDIA/k8s-nim-operator/blob/0aa8bb72040607bf0439793e1df98e141856cace/internal/utils/utils.go#L395-L414), [cache Job handling](https://github.com/NVIDIA/k8s-nim-operator/blob/0aa8bb72040607bf0439793e1df98e141856cace/internal/controller/nimcache_controller.go#L758-L794).
- Sandbox: [claim owner replacement](https://github.com/kubernetes-sigs/agent-sandbox/blob/4e6cefb00467e2126b188a358668b516ab928f46/extensions/controllers/sandboxclaim_controller.go#L1307-L1344), [deferred Pod metadata propagation](https://github.com/kubernetes-sigs/agent-sandbox/blob/4e6cefb00467e2126b188a358668b516ab928f46/controllers/sandbox_controller.go#L1400-L1439).
- Milvus: [ordinary update](https://github.com/zilliztech/milvus-operator/blob/d2eedcf9f6685d08110eefcf8c8601e6ce02091d/pkg/controllers/deployments.go#L221-L264), [owner wrapper](https://github.com/zilliztech/milvus-operator/blob/d2eedcf9f6685d08110eefcf8c8601e6ce02091d/pkg/controllers/utils.go#L473-L486), [migration ordering](https://github.com/zilliztech/milvus-operator/blob/d2eedcf9f6685d08110eefcf8c8601e6ce02091d/pkg/controllers/deploy_mode_changer.go#L26-L38), [ancestor recreation](https://github.com/zilliztech/milvus-operator/blob/d2eedcf9f6685d08110eefcf8c8601e6ce02091d/pkg/controllers/deploy_mode_changer.go#L119-L191).
- MPI: [existing launcher guard](https://github.com/kubeflow/mpi-operator/blob/b3b5b84250ddf86c7de58bd166fc506945364743/pkg/controller/mpi_job_controller.go#L757-L778), [worker guard](https://github.com/kubeflow/mpi-operator/blob/b3b5b84250ddf86c7de58bd166fc506945364743/pkg/controller/mpi_job_controller.go#L1016-L1039), [launcher template](https://github.com/kubeflow/mpi-operator/blob/b3b5b84250ddf86c7de58bd166fc506945364743/pkg/controller/mpi_job_controller.go#L1668-L1677).
- Training: [active PyTorchJob selector override](https://github.com/kubeflow/training-operator/blob/6f74c7f6b2ed6c27aabd82d0a316b23f0646059c/pkg/controller.v1/pytorch/pytorchjob_controller.go#L281-L295), [dispatch through the override](https://github.com/kubeflow/training-operator/blob/6f74c7f6b2ed6c27aabd82d0a316b23f0646059c/pkg/controller.v1/common/job.go#L109-L118), [shared claiming implementation](https://github.com/kubeflow/training-operator/blob/6f74c7f6b2ed6c27aabd82d0a316b23f0646059c/pkg/controller.v1/common/pod.go#L219-L253).

## Verification status

The saved native kind probe orphan-deleted and recreated a Deployment. The
ReplicaSet UID and Pod name, UID, resourceVersion and owner references stayed
unchanged while the Deployment UID changed. This demonstrates Kubernetes
behavior only. It is not an exporter acceptance pass or a frequency measurement.

The final-image kind matrix and recovery probes passed as recorded below.
Passing these fixtures cannot prove every optional operator mode in the source
table or establish adoption frequency.

The final module and envtest race suites passed after the routing, source and
fixture fixes. Deployed and performance checks remain separate.

| Check | Observed result and scope |
| --- | --- |
| Initial controller/discovery checks | Passed complete/empty replacement, UID-safe deletion, malformed/expired/failed-epoch rejection, alias fences, retired callbacks and full-body recovery without another mutation. |
| Publication capacity regression | Failed before the repair in normal and race runs. Passed after moving candidate reads, retaining the original assertions and default worker counts. |
| Source lifecycle, recovery and status-proof renewal regressions | Passed 30 focused race runs. |
| Application HTTP fixture | Passed 30 focused runs after using an owned transport and draining responses. The subsequent fake-clock timer fixture repair also passes in the final full-module race suite. |
| Final envtest race suite | Passed after the routing and source fixes for Discovery, definition inventory and controller integration. |
| Final controller/discovery race suite | Passed with `-count=1`: all 214 controller specs plus Discovery. Includes the publication capacity regression with unchanged assertions and default workers. |
| Final full-module race suite | Passed after the routing, source and fixture fixes across command, application, configuration, packages, chart tests and scale command. |
| Clean-snapshot repository `make check` | Passed with the unmodified Makefile on snapshot `e6bd6c4f`. Its 1,484-file manifest matched the captured worktree. All six components passed; all 13 module resolutions stayed inside the snapshot. This is snapshot evidence, not the actual branch's post-commit check. |
| Helm lint and validation | Passed `helm-lint` and `helm-validate`. |
| Actual-branch publication requirement | Unmodified `make check` must pass on the clean branch after the final commit and before publication. The snapshot result above does not replace it. |
| Final-image kind matrix and recovery probes | Passed; scope and image identity below. |
| Matched performance suites | All 17 planned runs passed every applicable absolute gate. Four additional queue-idle heap diagnostics completed. Results and limits below. |
| Independent architecture review | Final invariant review found no remaining blockers. The live invalid-definition and dedicated monitoring limits remain explicit below. |

### Final-image kind acceptance

On 2026-10-27, the matrix and isolated recovery probe used
`karta-metrics-exporter:controller-simple-20261027-r1`. Every run recorded the same
running image ID:
`sha256:94c12854562e752d15d85ccddc36b04e44e7111cc86b1b4b6343536b38c81b5f`.

| Run | Result and scope |
| --- | --- |
| Default profile | All 24 cases passed: 23 workload cases and the natural Deployment adoption regression. The same ReplicaSet and unchanged Pod UID, resourceVersion and owner references moved to the replacement Deployment in exporter output. No non-bookmark Pod watch event occurred. |
| Alternate profile | Both cases passed: DynamoGraphDeployment v1beta1 and LLMInferenceService v1alpha1. |
| Default bare-Pod exclusion | The focused case passed; the other 23 cases were intentionally skipped. The Ready bare Pod produced no workload or Pod-info series. |
| Zero-Pod generic ancestor | A selected Deployment revealed its generic ConfigMap ancestor. Reparenting the ConfigMap to a Namespace replaced the exported root while the Deployment UID/resourceVersion stayed unchanged. Assertions held for 14 seconds, beyond the 12-second maximum evidence age. |
| Workload source denial and recovery | Actual GET authorization failed after isolated RBAC revocation. Verification produced permission-denied health, invalid retained status and absent creation. Restoring permissions recovered verified creation and all nine valid status phases. |
| Karta source denial and recovery | Retained status stayed invalid with the selected cluster Karta through expiry. Creation stayed independently valid. No catalog fallback occurred. Restoring permissions recovered the same cluster interpretation. |
| Recovery continuity and cleanup | The exporter Pod UID stayed the same and restart count remained zero. Probe resources were removed and their absence checked. |

The source-denial probes exercised authoritative verification. Revoking RBAC
does not by itself prove that an already-open watch closed. The tests do not
claim that behavior.

The live Karta admission webhook rejected the attempted malformed definition.
That is admission behavior, not a pass for exporter handling of an accepted
invalid definition. Invalid-winner behavior remains covered by controller tests
and envtest. The dedicated monitoring suite's upgrade, network-policy and full
lifecycle checks remain outside this kind result.

The [E2E suite](../../../karta-metrics-exporter/test/e2e/README.md) describes the
raw API, watch and scrape artifacts retained with its reports. Source audits,
the initial Kubernetes-only probe and these exporter runs remain separate
evidence.

### Matched performance

The [final comparison report](../../../karta-metrics-exporter/test/scale/results/controller-leaf-root.md)
records all 17 acceptance runs, four supplemental heap diagnostics, exact
acceptance commands and source fingerprints. Every applicable absolute gate passed. The
runs cover cold 1,000/10,000 roots, 10,000 zero-Pod roots, three alternating
five-minute small-root pairs, a five-minute 100,000-Pod root, source faults and
normal-command composition. They use a modeled API and do not establish
production API-server capacity.

Across the three small-root pairs, changes between per-variant medians were
+0.3% CPU, -0.9% publication p99, -7.4% allocated bytes and +3.9% peak RSS. These
are descriptive median comparisons, not medians of paired deltas or confidence
intervals. Each run delivered and verified 30,000 updates over 300 seconds.

Retained heap increased despite passing the absolute gates. The zero-Pod case
increased 2.36 MiB (+19.0%); the giant case increased 13.77 MiB (+20.2%). Four
additional diagnostics confirmed a retained-memory cost after queue-idle GC.
Profiles support retained map and queue backing capacity as the main source of
those samples. Instrumented queue metrics contribute to this cost. Neither
exact attribution of every byte nor bounded memory under indefinite turnover
is established. The report also preserves worse HTTP maxima and a slower drain
sample rather than treating gate passes as universal improvement.

The earlier 649/656 MB heap failures were not reproduced by either fresh
variant, including the baseline. Their cause remains unproven because source
versions and measurement conditions differ. The earlier single-pair 10.4% CPU
increase did not repeat in the three-pair comparison. Historical failures and
remaining [memory limits](open-issues.md#memory-after-deleting-a-giant-workload)
remain visible. The inherited [health cleanup delay](open-issues.md#obsolete-metadata-health-after-workload-deletion)
is separate from workload-row deletion.
