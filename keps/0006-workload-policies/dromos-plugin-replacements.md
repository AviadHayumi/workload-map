<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Replace Dromos plugin rules with Karta CEL policies

Submit a native workload with annotations. CEL turns those requests into
workload patches, desired resources and controlled operations. Karta resolves
the workload's components and templates. One controller executes the plans,
tracks their resources and evaluates them again when inputs change.

This can replace plugin-specific validation, decisions and object-building
code. It still needs a reconciliation host. That host can be independent of
Dromos and WorkloadRunner, provided it preserves their lifecycle guarantees.

The current application registers five plugins and conditionally adds restart
rules. Storage, node affinity and MNNVL live under Dromos. Networking, personal
credentials and restart rules live under its anyworkload-controller consumer.
[Registration](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/cmd/main.go#L56).

| Plugin | What becomes a policy | What the host or another controller still does |
| --- | --- | --- |
| Storage | Desired PVCs, configuration precedence, readiness and terminal-cleanup decisions | Read/apply/watch/delete PVCs with ownership and lifecycle checks |
| Node affinity | Validate requested affinity and merge it into all supported Pod instances | Apply patches, preserve owned-field history and handle immutable workloads |
| Networking | Build Services, authenticated proxies and exposure resources from endpoint requests | Verify live ownership, apply/watch objects and observe endpoint conditions |
| Personal credentials | Validate source references and request destination copies | Authorize, read, decrypt and write Secrets; keep values outside CEL |
| MNNVL | Select GPU instances, plan claim references, affinity and ComputeDomain | Reconcile the domain; NVIDIA DRA controllers and drivers allocate resources |
| Restart rules | Validate takeover and request-reference changes | Persist restart state, dry-run overrides, delete/wait/recreate and recover |

## One input and one plan contract

`object` is the complete native workload, such as a PyTorchJob. Requests come
from its annotations. For example:

```yaml
metadata:
  annotations:
    plugins.example.com/node-affinity: >-
      {"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"NotIn","values":["worker-a"]}]}]}}
    plugins.example.com/credential-sources: '["registry-reader"]'
```

For each policy, the host decodes and validates the named annotation into
`config`. It supplies trusted `settings`, non-secret `observed` state and
host-verified dependency facts, plus admission identity where needed.
Secret observations contain references and metadata only.
Missing requests disable their policy.
The example annotation names and helper functions below are a proposed API.

A generation policy returns `resources`, a list of desired-object records.
A mutation policy returns `patches`. Privileged host work is described through
`operations`. An admission policy returns a boolean. Helpers construct values
or patches; none performs Kubernetes requests from inside CEL.

`observed.run.owner` is an owner-reference map for a durable controller record.
That record stores the original desired workload and resource identities.
It survives replacement of the native workload during restart. Users still
submit the native workload; this record is internal recovery state.
User cancellation must be recorded before deletion and prevent recreation.
Only an authorized restart deletion retains the record. On cancellation,
remove the durable owner so its dependents follow the declared collection
rules. Object absence alone cannot distinguish these two intentions.

## Six replacements

<details>
<summary>1. Storage: declare PVCs and retain the existing lifetime rules</summary>

The storage annotation contains `claims`, a list of complete PVC manifests,
and optional `cleanupOnCompletion`. The host fills the namespace, validates
unique names and normalizes the flag using the controller default. It rejects
a different explicit namespace and requires access modes and a storage request.
The policy is:

```cel
{
  'resources': config.claims.map(claim, {
    'manifest': claim,
    'owner': observed.run.owner,
    'deleteAfterCompletion': config.cleanupOnCompletion
  })
}
```

For one claim named `data` requesting `10Gi`, the host converges that PVC in
the workload namespace. It rejects a same-name claim with different ownership.
This reproduces provisioning; Pod volumes and mounts remain ordinary workload
fields. The existing storage plugin does not inject them.

Readiness uses observations: missing means Creating, unbound means
Initializing, Lost means Failed, and all Bound means Ready. Terminal cleanup
requires the aggregate and workload phases to agree on Completed or Failed.
The host persists that decision before deleting and must not recreate claims
while it holds. Removing a declaration currently does not prune its PVC.

Keep that lifetime distinct from the earlier demo, which retains PVCs even
when the workload is deleted. Current Dromos PVCs are runner-owned and can be
garbage-collected when the runner is deleted. Replacing only its child workload
during restart leaves those claims in place.

[Provisioning and ownership](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/storage/plugin.go#L132),
[status](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/storage/plugin.go#L212),
[terminal cleanup](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/storage/plugin.go#L374).

</details>

<details>
<summary>2. Node affinity: one policy across every supported Pod component</summary>

The node-affinity annotation contains a Kubernetes `NodeAffinity` object.
For the input above, the complete mutation policy is:

```cel
{
  'patches': karta.mergeNodeAffinity(object, config)
}
```

The proposed helper resolves all Pod instances through Karta and returns
native patches excluding `worker-a`. A PyTorchJob receives the restriction
on its master and workers; a RayCluster receives it on its head and groups.
Existing restrictions stay in force.

This helper needs the current merge semantics: AND existing and requested
required selectors, preserve OR terms, deduplicate preferred terms, and
produce the same result when evaluated twice. The current merge limits the
required-term product to 256. A shallow JSON merge is not equivalent.
Admission also limits requested required/preferred terms to 16 each and
In/NotIn values to 256 per requirement. Preferred weights must be 1 through 100.

The host validates supported catalog shapes and applies the patch. Applied
means the desired workload was applied, not that Pods found a schedulable
node. Removing an annotation must undo only the policy's contribution. Dromos can rebuild from
its stored template; a direct-native host needs equivalent field history.

[Mutation](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/nodeaffinity/mutate_resource.go#L35),
[merge semantics](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/pkg/affinity/node_affinity.go#L34),
[status](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/nodeaffinity/status_resource.go#L45).

</details>

<details>
<summary>3. Networking: generate an endpoint without a workload-specific adapter</summary>

This example's networking annotation names a component and instance, a port,
and an `external` flag. The full plugin also supports endpoint lists and
references to existing Services. For one endpoint:

```cel
{
  'resources': [
    networking.service(
      object,
      karta.podSelector(object, config.component, config.instance),
      config.port)
  ] + (config.external
    ? networking.authenticatedExposure(object, config, settings.networking)
    : [])
}
```

For component `worker`, instance `gpu-workers`, port 8080 and `external=false`,
the plan contains a Service selecting that Ray worker group. With external
access enabled, the second constructor adds the proxy Deployment, proxy
Service and selected Ingress, Route or HTTPRoute. These constructors return
desired-object records, including their dependency and ownership metadata.

The host must prove that selected Pods and EndpointSlices belong to the
workload. A selector returned by Karta is not that proof. Referenced Services
must not be adopted or modified. The proxy uses a configured Secret reference;
this policy neither issues credentials nor reads their values.

Current readiness combines resource presence and relevant conditions. It is
not a connectivity probe. Existing cleanup includes ownership-denial handling
and metrics-Service pruning, but not complete pruning of every removed endpoint.
A replacement must state any broader cleanup behavior as a separate change.

[Endpoint reconciliation](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/networking/additional_resources_endpoints.go#L157),
[live ownership checks](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/networking/service_ownership.go#L42),
[readiness](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/networking/status_probe.go#L49).

</details>

<details>
<summary>4. Personal credentials: request copies while keeping secret values out of CEL</summary>

The credential-sources annotation is a list of authorized source names.
The host resolves those names to metadata-only references in `observed.sources`:

```cel
{
  'operations': config.map(name, {
    'type': 'CopyCredential',
    'source': observed.sources[name].reference,
    'namespace': object.metadata.namespace,
    'name': 'pc-' + object.metadata.name + '-' + name,
    'owner': observed.run.owner
  })
}
```

For `registry-reader`, the plan requests a destination Secret named
`pc-trainer-registry-reader` in the native workload's namespace. The host
checks authorization and ownership, reads the encrypted source, decrypts it,
and applies the copy. Secret values and encryption keys never enter CEL.
Pod references to the destination Secret remain explicit workload fields.

The current plugin copies existing credentials; it does not mint them.
It watches destination Secrets, not source Secrets. Rotation is refreshed
when another reconcile happens. Removing a request does not prune its copy.
Source-rotation watches and explicit removal cleanup would be additional work.

[Validation](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/personalcreds/validate.go#L20),
[copy and ownership](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/personalcreds/copy.go#L27),
[watches and status](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/personalcreds/status.go#L31).

</details>

<details>
<summary>5. MNNVL: plan GPU fabric resources and mutate only GPU templates</summary>

The GPU-fabric annotation opts in. Trusted settings supply the topology keys,
grouping policy and elastic mode. `observed.names` contains stable child names.
The proposed helpers expose the current selection and replica-count rules:

```cel
{
  'resources': karta.gpuReplicaCount(object) == 0 ? [] : [
    gpu.computeDomain(
      object,
      observed.names.domain,
      settings.elastic ? 0 : karta.gpuReplicaCount(object),
      observed.names.claimTemplate,
      observed.run.owner)
  ],
  'patches': karta.bindComputeDomain(
    object, observed.names.claimTemplate,
    settings.topologyKeys, settings.grouping)
}
```

The count sums nonzero replicas, otherwise nonzero minimum replicas, otherwise
one per GPU-bearing instance. It does not divide GPU demand by node capacity.
The patch helper skips CPU instances,
adds the Pod resource-claim reference, attaches it to the first GPU container,
and merges the required node and configured Pod affinity. It must preserve
the shared affinity merge rules and avoid duplicate claim entries.

The host applies a ComputeDomain. NVIDIA DRA controllers and drivers provide
the claim-template materialization and allocation. Those components and GPU
hardware remain necessary. The current plugin reports Ready when the domain
object exists; it does not verify domain status or usable GPU networking.
Disabling the request does not currently delete the domain.

Actual application wiring replaces the plugin's default grouping behavior.
A replacement must carry that provider behavior into `settings.grouping`;
copying only the plugin's default affinity would change placement.
Its fallback grouping identity must remain stable across restart, using the
durable run identity rather than the replacement workload's new UID.

[Resource plan](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/mnnvl/additional_resources.go#L31),
[template mutation](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/mnnvl/mutate_resource.go#L145),
[status](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/mnnvl/status_resource.go#L41),
[application grouping](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/internal/mnnvlconv/provider.go#L19).

</details>

<details>
<summary>6. Restart rules: replace the admission policy, retain a durable restart executor</summary>

The existing plugin validates takeover and request-reference changes. It does
not execute a restart. The direct-native design can represent those markers
as protected annotations. This policy preserves the current requester gate:

```cel
restart.owner(object) in ['', 'restart-controller'] &&
(
  (restart.owner(object) == '' &&
   restart.owner(object) == restart.owner(oldObject)) ||
  admission.userInfo.username.startsWith('system:serviceaccount:') ||
  settings.extraAllowedPatterns.exists(p,
    admission.userInfo.username.contains(p))
) &&
(admission.operation == 'CREATE' ||
 restart.validReferenceChange(oldObject, object))
```

`restart.owner` is null-safe and reads the protected takeover annotation.
`restart.validReferenceChange` requires a request reference while takeover is
active, forbids one otherwise, and prevents changing an existing reference.
The CREATE/UPDATE distinction and broad service-account allowance reflect
current behavior. Narrowing the requester set is a separate policy change.

The executor still needs a persisted request and exclusive ownership. Its
sequence is: acquire takeover, dry-run overrides, delete the old workload,
persist overrides, wait for deletion, then release takeover for recreation.
Current production admission permits only node-affinity overrides. Helper
unit tests covering other fields do not expand that admitted contract.

When replacing a native workload directly, first persist its desired object,
old UID, override and child identities outside that object. Otherwise deleting
it also deletes the only input needed to recreate it. Claim retention and
child ownership must survive this step. A CEL expression cannot supply that
crash-recovery state by itself.

[Admission rules](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/restartrules/plugin.go#L63),
[allowed override](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/internal/restart/restartrequest_validator.go#L177),
[restart sequence](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/internal/restart/restart_reconciler.go#L232).

</details>

## What replaces the framework

The proposed runtime needs five responsibilities around the policies:

1. Validate requests and resolve trusted observations. Keep credential values
   outside the evaluator. Preserve per-plugin enablement and takeover rules.
2. Compose mutations on a private copy in deterministic order. Bound repeated
   evaluation and reject nonconvergent or conflicting policies before writes.
3. Reconcile resource plans using stable identities, ownership checks, watches
   and retries. Preserve data-dependent cleanup rules and record status first.
4. Track original fields and child identities so opt-out and restart can recover.
   A live object already containing policy edits is not a clean desired template.
5. Prepare immutable workload templates before creation, using admission or a
   controlled submission path. Do not impose a universal wait-for-Bound gate:
   some volumes need a scheduled consumer before binding.

A sensible migration starts with node-affinity planning and restart admission
validation. Then replace PVC and endpoint planning while retaining proven
executors. Credential materialization and GPU-driver integration stay behind
explicit host operations. Move restart execution only after durable recovery
and ownership have equivalent tests.

[Current additional-resource-before-workload ordering](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/pkg/controller/reconcile_spec.go#L148),
[bounded mutation rounds](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/pkg/plugin/framework/framework.go#L223),
[status-before-cleanup ordering](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/pkg/controller/reconcile_status.go#L38).

## Evidence

Source reviewed at public commit `7add034fd` on 2026-11-11.
All six plugin implementations and their relevant framework/controller paths
were inspected. Existing tests were read; they were not run in this audit.
All six CEL examples passed the real CEL parser. They describe a proposed
plan API; its helpers were not compiled or executed. No live-cluster or GPU
behavior is claimed by this comparison.
