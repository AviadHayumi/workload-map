<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Replace workload plugin code with Karta SDK operations

A policy chooses what to do to a workload. Karta handles each operator's
fields, component lookup and patch construction. The policy stays short when
a PyTorchJob becomes a RayCluster or another catalog-supported workload.

The [Kyverno issue](https://github.com/kyverno/kyverno/issues/18547) expresses
that through direct SDK calls. To change a PyTorchJob's worker scheduler:

```cel
karta.setScheduler(object, 'worker', '', 'batch-scheduler')
```

The same API style can cover placement:

```cel
karta.setNodeAffinity(object, 'worker', '', request.affinity)
```

`object` is the complete native PyTorchJob. The first call changes only its
worker scheduler. The proposed second call sets only its worker NodeAffinity.
Both return native workload patches. Karta constructs those patches internally.
For a named Ray worker group, use `('worker', 'gpu-workers')` instead.

| Plugin behavior | Operations available to the policy |
| --- | --- |
| Storage | Apply PVCs; use `karta.statuses` to decide when to remove them |
| Placement | Add or replace affinity on selected components |
| Networking | Read component labels; apply Services and exposure resources |
| Personal credentials | Request authorized Secret copies |
| MNNVL GPU fabric | Select GPU instances; count replicas; attach resource claims |
| Restart rules | Check workload state; prepare edits; request a controlled restart |

## The SDK boundary

`karta.statuses`, `karta.podTemplate`, `karta.setScheduler`, `karta.suspend`
and `karta.resume` keep the signatures proposed in the issue. The following
additions extend that same operation-based API:

| Proposed operation | Meaning |
| --- | --- |
| `karta.podInstances(object)` | Return unique `{component, instance}` selectors for the workload's mapped Pod instances |
| `karta.setNodeAffinity(object, component, instance, affinity)` | Replace that instance's NodeAffinity |
| `karta.addNodeAffinity(object, component, instance, affinity)` | Combine required restrictions and preserve existing placement rules |
| `karta.addPodAffinity(object, component, instance, affinity)` | Add Pod affinity terms without duplicating existing terms |
| `karta.gpuContainers(object, component, instance)` | Return names of containers with positive GPU demand, in template order |
| `karta.replicaCount(object, instances)` | Total effective replicas for the selected instances |
| `karta.addResourceClaim(object, component, instance, container, claim)` | Add the Pod claim and its selected container reference |

Mutation operations return `list<JSONPatch>`. They preserve unrelated fields,
create required parent maps, avoid duplicates and return `[]` when unchanged.
Unsupported types, unmapped fields and invalid inputs fail before returning
patches. Karta owns quantity parsing, container-name lookup and affinity merging.
No operation performs Kubernetes I/O during evaluation.

The examples also assume generic host operations: `resources.applyOwned`,
`resources.deleteOwned`, `resources.copySecret` and `resources.restart`.
They return requests for the host to execute after successful evaluation.
They are separate from Karta's workload reads and mutations.

`request` contains validated values decoded from workload annotations.
`settings` contains trusted policy configuration. `observed` contains fresh
host-resolved facts and authorized references, never secret values.
Named `variables`, where useful, are evaluated in declaration order.
These are proposed policy snippets; the bindings and host operations are not
implemented APIs. The existing [Go SDK](https://github.com/AviadHayumi/workload-map/blob/435dcb58615a35a5136e9bc4f4864ff0909e8a6e/karta/sdk/pkg/resource/component.go)
provides the component, template and replica access underneath this design.

## Six plugin examples

<details>
<summary>1. Storage: create PVCs and remove them after completion</summary>

Input: the storage annotation supplies complete PVC manifests in
`request.claims` and a `cleanupOnCompletion` flag. For example, one claim is
`demo/data`, with access mode `ReadWriteOnce` and a storage request of `10Gi`.

```cel
request.cleanupOnCompletion &&
observed.aggregatePhase in ['Completed', 'Failed'] &&
karta.statuses(object).exists(s, s == observed.aggregatePhase)
  ? request.claims.map(pvc, resources.deleteOwned(object, pvc))
  : request.claims.map(pvc, resources.applyOwned(object, pvc))
```

While the workload runs, the result requests its PVCs. Once the aggregate
phase and mapped workload state agree on completion or failure, the policy
requests deletion if cleanup is enabled.

The host checks namespace and ownership, persists terminal cleanup and prevents
recreation afterward. PVCs survive workload replacement during restart.
Removing a claim declaration alone does not delete its data. Volumes and
mounts remain explicit fields in the submitted workload.

Source: [storage plugin](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/storage/plugin.go#L132).

</details>

<details>
<summary>2. Placement: add one restriction across the workload</summary>

Input: `request.affinity` is a Kubernetes NodeAffinity value excluding
`worker-a`. Apply it to every mapped Pod instance:

```cel
karta.podInstances(object).map(p,
  karta.addNodeAffinity(object, p.component, p.instance, request.affinity)
).flatten()
```

PyTorch masters and workers both receive the restriction. Existing zone
requirements, preferred terms and Pod affinity remain. Karta combines required
selectors with AND semantics and bounds expansion of their OR terms.

To change only one Ray worker group, the complete policy is:

```cel
karta.addNodeAffinity(object, 'worker', 'gpu-workers', request.affinity)
```

Use `setNodeAffinity` when the policy intends to replace the complete value.
Use `addNodeAffinity` when it intends to preserve and extend restrictions.
The host retains field history so removing a policy can undo its contribution.

Source: [affinity composition](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/pkg/affinity/node_affinity.go#L34).

</details>

<details>
<summary>3. Networking: expose a selected worker group</summary>

Input: `request.component='worker'`, `request.instance='gpu-workers'` and
`request.port=8080`. That template has labels `app=trainer,role=gpu-workers`.

```cel
resources.applyOwned(object, {
  'apiVersion': 'v1',
  'kind': 'Service',
  'metadata': {
    'name': object.metadata.name + '-workers',
    'namespace': object.metadata.namespace
  },
  'spec': {
    'selector': karta.podTemplate(object, request.component, request.instance).metadata.labels,
    'ports': [{'port': request.port, 'targetPort': request.port}]
  }
})
```

Result: a Service for that group's labels and port. Karta finds the template;
the host checks ownership and reconciles the Service. Missing or empty labels
are rejected. Labels alone do not establish ownership of the selected Pods.

For authenticated external access, the policy configuration must also provide
the proxy Deployment, proxy Service and Ingress, Route or HTTPRoute manifests.
The same host operation handles that resource set:

```cel
request.external
  ? request.exposureResources.map(r, resources.applyOwned(object, r))
  : []
```

`request.exposureResources` contains complete manifests for this workload,
with Secret references for authentication. The host tracks dependencies and
readiness. The policy still needs those manifests; Karta does not invent an
application-specific authentication setup.

Source: [networking plugin](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/networking/additional_resources_endpoints.go#L157).

</details>

<details>
<summary>4. Personal credentials: request authorized copies</summary>

Input: the annotation supplies source names in `request.credentials`.
The host resolves each to an authorized reference in `observed.credentials`.

```cel
request.credentials.map(name,
  resources.copySecret(
    object,
    observed.credentials[name],
    'pc-' + object.metadata.name + '-' + name)
)
```

For workload `trainer` and source `registry-reader`, the host creates
`pc-trainer-registry-reader` in the workload namespace. It checks ownership,
reads and decrypts the source, then writes the destination. Secret values
never enter CEL. This operation copies credentials; it does not issue them.
Pod references to the copied Secret remain explicit workload fields.

This rule needs no workload-shape lookup. Credential copying belongs to the
host library, so it does not need a new Karta workload operation.

Source: [credential copy](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/personalcreds/copy.go#L27).

</details>

<details>
<summary>5. MNNVL: select GPU instances and attach a domain claim</summary>

Input: `request.domain='trainer-domain'` and
`request.claimTemplate='trainer-channel'`. The example has two GPU workers
and a CPU-only master. `settings.elastic` selects elastic or fixed-size mode.

```yaml
variables:
  - name: gpuInstances
    expression: >-
      karta.podInstances(object).filter(p,
        karta.gpuContainers(object, p.component, p.instance).size() > 0)
expression: >-
  variables.gpuInstances.size() == 0 ? {'resources': [], 'patches': []} : {
    'resources': [resources.applyOwned(object, {
      'apiVersion': 'resource.nvidia.com/v1beta1',
      'kind': 'ComputeDomain',
      'metadata': {
        'name': request.domain,
        'namespace': object.metadata.namespace
      },
      'spec': {
        'numNodes': settings.elastic ? 0 : karta.replicaCount(object, variables.gpuInstances),
        'channel': {'resourceClaimTemplate': {'name': request.claimTemplate}}
      }
    })],
    'patches': variables.gpuInstances.map(p,
      karta.addResourceClaim(
        object, p.component, p.instance,
        karta.gpuContainers(object, p.component, p.instance)[0],
        {'name': 'compute-domain', 'resourceClaimTemplateName': request.claimTemplate})
    ).flatten()
  }
```

For fixed-size mode, the result requests a two-node ComputeDomain and attaches
its claim to the first GPU container in each GPU instance. CPU-only instances
stay unchanged. Karta creates both claim references, resolves container names
and preserves existing claims and container fields.

`replicaCount` uses nonzero replicas, otherwise nonzero minimum replicas,
otherwise one for each selected instance. It counts replicas, not GPUs divided
by node capacity. The policy owns the `compute-domain` claim name; conflicting
existing use fails rather than being overwritten.

Topology restrictions use the placement policy above. Grouping uses a separate
`karta.addPodAffinity` policy with trusted grouping terms and a stable identity
across restarts. NVIDIA DRA controllers, drivers and compatible GPU hardware
still perform allocation. Domain existence alone does not prove readiness.

Sources: [ComputeDomain fields](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/mnnvl/compute_domain_builder.go#L99),
[template changes](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/dromos/plugins/mnnvl/mutate_resource.go#L145).

</details>

<details>
<summary>6. Restart: prepare placement changes and request a controlled replacement</summary>

Input: an authorized restart request contains `request.restartRef` and a new
NodeAffinity value. This example requests restart only for failed workloads:

```cel
karta.statuses(object).exists(s, s == 'Failed')
  ? [resources.restart(object, request.restartRef,
      karta.podInstances(object).map(p,
        karta.addNodeAffinity(object, p.component, p.instance, request.affinity)
      ).flatten())]
  : []
```

Karta prepares the placement edits. The host validates requester identity,
takeover and reference immutability, then executes the restart request.
This status condition is a proposed policy choice. The current restart-rules
plugin supplies admission checks; its separate executor performs the restart.
Its admitted overrides are node affinity.

Before deleting the old workload, the host persists the desired replacement,
old UID, edits and child identities. It dry-runs, deletes, waits and recreates.
Claims survive this sequence. Cancellation stops recreation. Suspend/resume
changes lifecycle fields and does not perform this restart sequence.

Source: [restart admission](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/plugins/restartrules/plugin.go#L63).

</details>

## How it runs

The host watches submitted native workloads and their dependencies. It supplies
the current `object`, requests and observations to each policy. Karta resolves
workload differences through its catalog and SDK. The host validates the
returned patches and operation requests, then reconciles their effects.

Mutation policies run in order against a working copy, so later policies see
earlier edits. Evaluation errors prevent writes. Child changes trigger reevaluation.
The host owns permissions, ownership, retries, field history and durable
restart state. It prepares immutable fields before workload creation and
retains children across an authorized restart. It does not require users to
submit a WorkloadRunner or install Kyverno.

## Scope and verification

The [six plugin families](https://github.com/dsx-ai-factory/run-ai/blob/7add034fd350afbebdfafc4213afd4e29071e6a6/runai-cluster/cluster/anyworkload-controller/cmd/main.go#L56)
were audited at `7add034fd`. The examples propose an operation-based API for
replacing their policy logic. Full replacement also needs the resource templates,
validation, lifecycle handling and runtime dependencies described above.

The expressions parse and type-check against declared signatures. These checks
do not implement or execute the proposed operations. The
[implementation history](README.md#implementation-history) records what the
existing controller prototype actually tests.
