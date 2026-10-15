<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Proposed platform identity contract

Discussion draft. These keys are proposed integration metadata, not new Karta
CRD fields or an existing exporter API. Values below are illustrative.

## Minimum useful agreement

Each submission platform should capture the authenticated principal when it
accepts a workload. Preserve that principal through controller-created objects.
Do not substitute the controller's Kubernetes service account for the original
submitter. If the caller is a service and no delegated human identity is known,
record the service and its type.

| Proposed metadata | Location | Meaning and requirement |
| --- | --- | --- |
| `workload.dgxc.nvidia.com/user` | Label, when representable | Canonical short username agreed by the fleet; omit for unknown or unrepresentable values |
| `workload.dgxc.nvidia.com/submitter-id` | Annotation | Full authenticated principal; required when known; never truncate to fit a label |
| `workload.dgxc.nvidia.com/submitter-type` | Annotation | `human`, `service` or `unknown`; do not infer from an email-like string |
| `workload.dgxc.nvidia.com/identity-source` | Annotation | Agreed identity authority/tenant namespace in which the principal ID is unique; not a token or an endpoint |
| `workload.dgxc.nvidia.com/platform-workload-id` | Annotation | Platform's stable workflow/function/task ID; retain platform-specific IDs too |
| `workload.dgxc.nvidia.com/org_hierarchy` | Label, when representable | Optional agreed organization path, for example `portfolio_program_project` |
| `workload.dgxc.nvidia.com/org-id` | Annotation | Optional stable directory/project ID that remains useful if display names change |

`org_hierarchy` is the proposed spelling. Agree the final key before rollout.
If a deployment already uses the misspelled `org_heirarchy`, support it through
an explicit temporary mapping rather than silently creating two canonical keys.

The domain above is an example of a fleet-owned contract. The extractor should
accept configured keys rather than hardcoding a company domain. Other operators
can use the same semantics under their own agreed domain.

## Principal identity across platforms

The proposed principal join key is `(identity-source, submitter-id)`. The source
identifies an agreed authority/tenant namespace, not merely the submission platform.
The platform agreement must establish the principal ID's uniqueness in that scope,
including human and service principals. A full email or username alone is not a
fleet-wide identity guarantee.

Two platforms may emit the same source and ID only when they use the same verified
authority scope and identifier semantics. Otherwise keep their identities separate
until an explicit verified mapping establishes equivalence. For example,
`(example-idp:tenant-a, alice)` and `(example-idp:tenant-b, alice)` remain distinct.
Keep the platform/rule that supplied the observation as separate provenance.

A legacy extraction rule must supply an agreed source namespace through its
configuration or validated metadata. If that scope is unknown, retain partial
source-local attribution; do not merge it into a canonical fleet identity.

## Example

```yaml
metadata:
  labels:
    workload.dgxc.nvidia.com/user: alice
    workload.dgxc.nvidia.com/org_hierarchy: research_models_training
  annotations:
    workload.dgxc.nvidia.com/submitter-id: alice@example.com
    workload.dgxc.nvidia.com/submitter-type: human
    workload.dgxc.nvidia.com/identity-source: example-idp:tenant-a
    workload.dgxc.nvidia.com/platform-workload-id: workflow-123
    workload.dgxc.nvidia.com/org-id: project-456
```

A label value is limited to 63 characters and a restricted character set.
Keep long/full values in annotations. Do not split an arbitrary organization
path on underscores unless the naming agreement forbids underscores within
individual segments. Stable IDs are preferable for joins; display paths can change.

## Where to place it

- Put metadata on the submitted Kubernetes root, when one exists, and on Pods
  created for it. For Deployment-like resources, populate the Pod template so
  future Pods receive the same submission identity.
- For a platform workflow that exists only in an API/database, such as the tested
  OSMO path, stamp its task Pods and grouping resources and preserve its workflow ID.
- For NVCF, preserve function/version or task identity through the launch spec,
  then place approved identity fields on the created workload object and Pods.
- An arbitrary extra manifest submitted by a task does not automatically inherit
  this contract. The platform must document that path or mark it unsupported.

## Authority, updates and organization ownership

The platform owns the principal it asserts. Define which actor may write or
change the canonical keys. User-editable metadata is a claim, not authentication.
A consumer must retain the source and confidence/provenance of the claim.

Record the original submitter at creation. A later editor is a different concept.
If correction is supported, make it an explicit audited operation. Organization
mapping may evolve separately; decide whether reporting needs submission-time
organization, current organization, or both. Do not rewrite historical usage
with today's mapping without making that choice explicit.

The organization directory is outside Karta. The platform may populate stable
project IDs from its chosen source of truth. A cluster without that directory
still participates in workload and submitter mapping; missing organization is
reported as unknown. A UI is not required for this contract.

## Acceptance before claiming support

A platform integration should demonstrate a human submission, a service-key
submission, missing identity, long/full identity, root-to-Pod propagation,
controller-created children, metadata correction, and deletion/recreation.
Document which principal each API reports: creator, deployer and invoker can be
different. Preserve raw evidence and redact credentials before sharing.
