<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# CEL definition guide

The API is in `karta/sdk/pkg/api/runai/v1alpha2/`. Use the generated catalog as
complete examples. Run commands below from `karta/sdk/`.

```yaml
apiVersion: run.ai/v1alpha2
kind: Karta
metadata:
  name: apps-deployment
spec:
  structureDefinition:
    rootComponent:
      name: deployment
      kind: {group: apps, version: v1, kind: Deployment}
      specDefinition:
        podTemplateSpec:
          read:
            expression: object.spec.template
          write:
            path: /spec/template
      scaleDefinition:
        replicas:
          read:
            expression: object.?spec.?replicas.orValue(1)
          write:
            path: /spec/replicas
      statusDefinition:
        statusMappings:
          running:
          - byExpression:
              expression: >-
                object.?status.?observedGeneration.orValue(0) >= object.metadata.generation
                && object.?status.?readyReplicas.orValue(0) == object.?spec.?replicas.orValue(1)
```

This small example illustrates syntax. The catalog Deployment has the full
controller-specific status rules and recorded flow coverage.

## Values and writes

`specDefinition` selects one of `podTemplateSpec`, `podSpec` with optional
`metadata`, or `fragmentedPodSpecDefinition`. Fragment fields include `image`,
`container`, `containers`, `resources`, `labels`, `annotations`, `schedulerName`,
`priorityClassName`, `resourceClaims`, `nodeAffinity` and `podAffinity`.
`scaleDefinition` contains `replicas`, `minReplicas` and `maxReplicas`.

Every field uses a `ValueAccessor` with optional `read` and `write` blocks.
A read needs `expression`; its optional `matchExpression` must return a boolean.
A write has exactly one of `path` and `pathExpression`, plus an optional boolean
`matchExpression`. The empty fixed path addresses the whole object; omitting
both paths is invalid. Write expressions have no new-value variable.

The editor accepts raw JSON Merge Patch. An object merges recursively, null
members delete keys, and arrays/scalars replace. The input value cannot itself
be null. Untouched fields survive. The batch fails atomically on a false match,
invalid path, overlapping target, identity change or failed re-extraction.
Suspend/resume actions contain a write rule and a JSON value string.

`instanceIds.read.expression` returns stable string IDs. It has no write block.
Each multi-instance read returns an object keyed by those IDs. A write receives
one `instance` and its sorted `index`. See the Dynamo, Grove and LWS catalogs.

## Expression inputs

| Rule | Available inputs |
| --- | --- |
| Value read, status, variable | Original workload `object`, `variables`, `references` |
| Write location or match | Workload inputs plus `instance` and `index` |
| Reference eligibility, lookup name, selector value | Original workload `object` |
| Pod selector and grouping | One Pod as `object` |
| Reference membership match | Original workload `object`, resolved `target` |

Use CEL optional selection for missing fields. `orValue` supplies a default for
absence, without replacing false, zero or an empty string. Variables are lazy.
A dependent read resolves active references once per factory observation.
Missing lookups are unbound; successful empty lists bind `[]`; read failures
are errors. Callers supply a reader or copied reference objects. Explicitly
disabling references exposes an empty map and makes no reference calls.

A Pod component selector has `read.expression` and optional string `value`.
An absent/null result does not match. Grouping expressions return ordered keys.
Keep hierarchy, role, instance and replica selectors separate. An `ownerRef`
inside a Karta names a logical component, not a Kubernetes owner reference.

## Validation and evidence

```bash
go run ./hack/karta-verify --karta ../../docs/catalog/apps-deployment-v1.yaml \
  --workload /absolute/path/deployment.yaml

go test ./pkg/catalog/... ./pkg/resource/... ./pkg/tree/...
```

Check the command's `--help` for Pod and strict-validation options. A definition
must pass the same validator used by admission. Record the operator's real
lifecycle and replay it before adding a builtin catalog entry. Unit fixtures
alone do not establish live operator support. Preserve source pins and explain
unmapped controller states in the catalog builder.

For the full contract see `docs/keps/0002-cel-expressions/README.md` and
`docs/design/cel-expressions/README.md`. The local PoC coverage and limitations
are recorded in `docs/design/cel-expressions/POC.md`.
