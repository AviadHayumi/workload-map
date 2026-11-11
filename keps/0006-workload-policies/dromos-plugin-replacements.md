<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Write Dromos behavior with the Karta CEL SDK

Keep workload differences in Karta's catalog. Write the plugin's decisions
in CEL: select workers, inspect their containers, choose resources and return
changes. The same building blocks work with PyTorch components and named Ray
groups. Users submit native workloads with annotations.

This follows the [Karta CEL proposal in Kyverno](https://github.com/kyverno/kyverno/issues/18547).
For a PyTorchJob named `trainer` with two workers, the entire scheduler policy is:

```cel
karta.setScheduler(object, 'worker', '', 'batch-scheduler')
```

`object` is the complete PyTorchJob. The result changes only its worker template:

```json
[{"op":"add","path":"/spec/pytorchReplicaSpecs/Worker/template/spec/schedulerName","value":"batch-scheduler"}]
```

For a RayCluster, select `('worker', 'gpu-workers')` instead. Karta resolves
that group's native path. CEL still describes the operation.

| Dromos behavior | What the policy expresses |
| --- | --- |
| Storage | Declare PVCs; use common workload states to decide cleanup |
| Node affinity | Read each Pod template and combine placement restrictions |
| Networking | Build a Service from a selected component's labels |
| Personal credentials | Copy the credentials referenced by selected Pod templates |
| MNNVL GPU fabric | Find GPU containers, build a ComputeDomain and attach claims |
| Restart rules | Validate a request, check workload state and prepare replacement edits |

## The language used below

The issue proposes `karta.statuses`, `karta.podTemplate`, `karta.setScheduler`,
`karta.suspend` and `karta.resume`. Their signatures stay unchanged.
These examples add three reusable SDK operations:

| Proposed addition | Result |
| --- | --- |
| `karta.podInstances(object)` | List of `{component, instance}` selectors |
| `karta.scale(object, component, instance)` | Map with optional integer `replicas`, `minReplicas`, `maxReplicas` |
| `karta.patchPodTemplate(object, component, instance, edits)` | Native workload JSONPatch list from template-relative JSONPatch edits |

For example, `/spec/resourceClaims` refers to the selected Pod template.
Karta translates it to the PyTorch or Ray path. The binding edits a private
copy of the raw template, preserves unrelated fields and returns `[]` when
unchanged. Missing parents and invalid edits fail before any patch is returned.
The examples cover complete Pod templates. Unsupported or fragmented shapes
must fail explicitly; silently skipping them would leave a workload partly changed.

The [Go SDK](https://github.com/AviadHayumi/workload-map/blob/435dcb58615a35a5136e9bc4f4864ff0909e8a6e/karta/sdk/pkg/resource/component.go)
already exposes component instances, templates and replica reads. These CEL
bindings are proposed. The host enables CEL-Go's `cel.bind`, `lists.range`
and `.flatten()` extensions. One additional pure utility,
`affinity.and(existing, requested)`, combines Kubernetes NodeAffinity values.
It ANDs required selectors, preserves their OR terms and matchFields, and
deduplicates preferred terms. It rejects excessive term expansion.

`request` contains validated values decoded from the workload's annotations.
`observed` contains host-resolved facts and references, never secret values.
The host enables each policy only when its annotation is present. Expressions
return values, manifests or patches. The host performs Kubernetes operations;
none of the functions below sends a request during CEL evaluation.

## Six plugin examples

<details>
<summary>1. Storage: create claims and clean up using Karta's common states</summary>

Input: the storage annotation supplies complete PVC manifests as
`request.claims`, plus `cleanupOnCompletion`. For example, one manifest names
`data` in namespace `demo`, with access mode `ReadWriteOnce` and request `10Gi`.
The host supplies its aggregate phase as `observed.aggregatePhase`.

```cel
{
  'resources': request.claims,
  'deleteClaims': request.cleanupOnCompletion &&
    observed.aggregatePhase in ['Completed', 'Failed'] &&
    karta.statuses(object).exists(s, s == observed.aggregatePhase)
}
```

If both the aggregate phase and mapped workload state are `Completed`, the
cleanup flag is true. If the workload still maps to `Running`, it is false.
PVC generation is ordinary CEL data; Karta supplies the workload-state mapping.

The host validates namespace and ownership, tracks PVC readiness and persists
terminal cleanup before deletion. It suppresses provisioning after that decision.
Claims survive replacement of the native workload during restart. Removing a
claim request alone does not delete stored data. Volumes and mounts remain
explicit fields in the submitted workload.

Source: [storage plugin](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/storage/plugin.go#L132).

</details>

<details>
<summary>2. Node affinity: apply one restriction to every Pod component</summary>

Input: `request.affinity` excludes `worker-a`:

```json
{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"NotIn","values":["worker-a"]}]}]}}
```

The policy reads every template, combines its existing affinity with the
request, then returns a patch for that template:

```cel
karta.podInstances(object).map(p,
  cel.bind(t, karta.podTemplate(object, p.component, p.instance),
    cel.bind(a, affinity.and(
      has(t.spec.affinity) && has(t.spec.affinity.nodeAffinity)
        ? t.spec.affinity.nodeAffinity : {},
      request.affinity),
      karta.patchPodTemplate(object, p.component, p.instance, [{
        'op': 'add',
        'path': has(t.spec.affinity)
          ? '/spec/affinity/nodeAffinity' : '/spec/affinity',
        'value': has(t.spec.affinity) ? a : {'nodeAffinity': a}
      }])
    )
  )
).flatten()
```

A PyTorch master and its workers both exclude `worker-a`. Existing zone
requirements, preferred terms and Pod affinity remain. To target only workers,
insert `.filter(p, p.component == 'worker')` before `.map`.
The host retains the original fields so removing the request can undo only
this policy's contribution. Applying affinity does not prove schedulability.

Source: [affinity composition](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/pkg/affinity/node_affinity.go#L34).

</details>

<details>
<summary>3. Networking: expose the selected worker group</summary>

Input: `request.component='worker'`, `request.instance='gpu-workers'` and
`request.port=8080`. The selected Ray template has labels
`app=trainer` and `role=gpu-workers`.

```cel
cel.bind(t, karta.podTemplate(object, request.component, request.instance),
  [{
    'apiVersion': 'v1',
    'kind': 'Service',
    'metadata': {
      'name': object.metadata.name + '-workers',
      'namespace': object.metadata.namespace
    },
    'spec': {
      'selector': t.metadata.labels,
      'ports': [{'port': request.port, 'targetPort': request.port}]
    }
  }]
)
```

Result: a Service selecting `app=trainer,role=gpu-workers` on port 8080.
Karta finds the template; the manifest and port choice are visible in CEL.
The host rejects missing or empty labels and verifies live workload ownership
before exposing endpoints. Labels alone are not ownership proof.

The full plugin also manages authenticated proxy Deployments, proxy Services
and Ingress, Route or HTTPRoute objects. Those need explicit resource templates
and dependency checks too. The Service example covers internal exposure;
it does not implement the authenticated endpoint flow.

Source: [networking plugin](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/networking/additional_resources_endpoints.go#L157).

</details>

<details>
<summary>4. Personal credentials: copy what the selected template references</summary>

Input: the PyTorch worker template has
`imagePullSecrets: [{name: registry-reader}]`. The host resolves that name to
an authorized source reference in `observed.credentials`.

```cel
cel.bind(t, karta.podTemplate(object, 'worker', ''),
  (has(t.spec.imagePullSecrets) ? t.spec.imagePullSecrets : []).map(ref, {
    'source': observed.credentials[ref.name],
    'target': {
      'namespace': object.metadata.namespace,
      'name': ref.name
    }
  })
)
```

Result: a request to materialize `registry-reader` in the workload namespace.
Karta reads the selected template; CEL chooses the copies. The host authorizes
and performs them, rejecting foreign destinations. Secret data and decryption
keys stay outside the evaluator.

This example derives demand from image-pull references. The existing plugin
accepts explicit personal-credential instructions and also supports other
credential uses. Retaining that request shape needs no Karta call: CEL can map
the annotation's source references directly. Copying is a host operation.

Source: [credential copy](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/personalcreds/copy.go#L27).

</details>

<details>
<summary>5. MNNVL: find GPU containers and attach a ComputeDomain claim</summary>

Input: a PyTorch worker template with two replicas and a container requesting
`nvidia.com/gpu: "1"` in its limits. This example accepts integer GPU quantities.
`request.domain` and `request.claimTemplate` are stable names such as
`trainer-domain` and `trainer-channel`. The policy owns the claim name
`compute-domain`; other claims and container fields must remain intact.

```cel
cel.bind(t, karta.podTemplate(object, 'worker', ''),
cel.bind(gpu, lists.range(t.spec.containers.size()).filter(i,
  has(t.spec.containers[i].resources) &&
  has(t.spec.containers[i].resources.limits) &&
  'nvidia.com/gpu' in t.spec.containers[i].resources.limits &&
  int(t.spec.containers[i].resources.limits['nvidia.com/gpu']) > 0),
size(gpu) == 0 ? {'resources': [], 'patches': []} :
cel.bind(c, t.spec.containers[gpu[0]], {
  'resources': [{
    'apiVersion': 'resource.nvidia.com/v1beta1',
    'kind': 'ComputeDomain',
    'metadata': {
      'name': request.domain,
      'namespace': object.metadata.namespace
    },
    'spec': {
      'numNodes': karta.scale(object, 'worker', '').replicas,
      'channel': {'resourceClaimTemplate': {'name': request.claimTemplate}}
    }
  }],
  'patches': karta.patchPodTemplate(object, 'worker', '', [
    {
      'op': 'add', 'path': '/spec/resourceClaims',
      'value': (has(t.spec.resourceClaims)
        ? t.spec.resourceClaims.filter(r, r.name != 'compute-domain') : []) +
        [{'name': 'compute-domain', 'resourceClaimTemplateName': request.claimTemplate}]
    },
    {
      'op': 'add',
      'path': '/spec/containers/' + string(gpu[0]) + '/resources/claims',
      'value': (has(c.resources.claims)
        ? c.resources.claims.filter(r, r.name != 'compute-domain') : []) +
        [{'name': 'compute-domain'}]
    }
  ])
})))
```

Result: a ComputeDomain with `numNodes: 2`, a Pod claim reference and a claim
on the first GPU container. Its image, arguments, environment, mounts and other
claims remain. A CPU-only worker produces no resources or patches.

This shows one component's fixed-size domain. Full parity also needs selection
across GPU-bearing components, minimum-replica fallback, elastic mode, topology
affinity and stable grouping. Those are policy choices over instance/template
reads. NVIDIA DRA controllers, drivers and compatible GPU hardware still
perform allocation. Creating the domain alone does not establish readiness.

Sources: [ComputeDomain fields](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/mnnvl/compute_domain_builder.go#L99),
[template changes](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/mnnvl/mutate_resource.go#L145).

</details>

<details>
<summary>6. Restart rules: authorize the request and use mapped workload state</summary>

A proposed failure-restart policy receives decoded takeover markers and a
restart reference in `request`. `previous.restartRef` is the prior reference
or an empty string. `observed.requester` is authenticated identity retained
with the request. `settings.restartUsers` is a trusted exact allowlist.

```cel
request.takeover == 'restart-controller' &&
request.restartRef != '' &&
(previous.restartRef == '' || request.restartRef == previous.restartRef) &&
observed.requester in settings.restartUsers &&
karta.statuses(object).exists(s, s in ['Failed', 'Degraded'])
```

For an authorized request and a failed PyTorchJob, the result is true.
A changed request reference or a running workload returns false. Ordinary CEL
checks the request; Karta supplies the common status names.

This is a proposed trigger, not a copy of the existing admission gate.
The current restart-rules plugin validates takeover/reference changes; a
separate executor performs the restart. Its admitted overrides are node affinity.
The placement expression above can prepare those edits for a replacement.

Before deletion, the host persists the desired workload, old UID, edits and
child identities. It then dry-runs, deletes, waits and recreates. PVCs must
survive that sequence. Cancellation must stop recreation. Suspend/resume
changes lifecycle fields; it does not substitute for this restart sequence.

Source: [restart admission](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/restartrules/plugin.go#L63).

</details>

## How it runs

The host compiles each policy with the Karta CEL library. On submission or
update, it supplies the native `object`, decoded annotations and fresh
observations. Karta resolves catalog paths while CEL evaluates the decisions.
The host validates the result and applies the patches or reconciles children.
Child changes trigger another evaluation. No-op results converge without writes.

A controller can host this without Kyverno or a user-facing WorkloadRunner.
It still needs ownership, watches, retries, field history and durable restart
state. Prepare immutable Pod fields before creation. Do not wait for every PVC
to bind before allowing a consumer to run.

## Scope and verification

The [six registered plugin families](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/cmd/main.go#L56)
were audited at `7add034fd`. These examples show their policy decisions using
the proposed Karta language. They are not complete production replacements.
All seven expressions parse and type-check against the declared signatures.
Those checks do not execute the proposed bindings or prove live-cluster behavior.
The [KEP's implementation history](README.md#implementation-history)
separates the existing controller demo from this language proposal.
