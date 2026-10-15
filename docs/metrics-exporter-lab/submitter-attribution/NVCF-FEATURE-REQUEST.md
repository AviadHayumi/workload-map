<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# NVCF submitter identity in workload metadata

Issue draft for review. No GitHub issue has been created.

## Problem

The tested NVCF Pods carry account, function/version or task IDs, but no original
submitting principal. A creation audit identifies a client or API-key owner,
which may be a service. Workload observers need a documented identity mapping
across submission platforms, including service-key paths.

## Requested behavior

1. Define the actor for each operation. Function creator, deployer and invoker
   can be different; task submission is another operation.
2. Capture the authenticated principal and its type at the API boundary. Include
   a delegated human only when that delegation is verifiable.
3. Carry approved fields through the launch spec. If an environment variable
   transports them, NVCA must also project them into Kubernetes metadata.
4. Stamp full principal, type, authority/tenant namespace and platform workload ID on
   workload resources and Pods. Add a short-user label only when known.
5. Preserve existing IDs and client behavior. Represent missing identity
   explicitly and never expose credentials as metadata.

See the [proposed operator contract](OPERATOR-CONTRACT.md) for candidate names.
An opt-in rollout can precede broader adoption; final key names are negotiable.

## Questions for maintainers

- Which authenticated subject is available for personal and service keys?
- Within which authority/tenant scope is that principal ID unique?
- Can an upstream platform provide a verified on-behalf-of identity?
- Which launch-spec field should carry these values, and which component will
  stamp metadata on both function and task resources?
- Which actor should be attributed to a long-lived function?
- How should Helm-created resources and nested Pod templates receive the metadata?
- Can a corrected identity be updated, or is it immutable after submission?

## Acceptance criteria

Test personal and service keys, functions and finite tasks, Pod and parent
metadata, absent values, unsupported modes and cleanup. A shared service key
remains a service identity without verified delegation.

## Interim option

Correlate account plus function/version or task ID with a documented audit/API
source. Retain principal type and operation. This needs an adapter and retention
policy; audit access alone does not identify the human behind a service.
