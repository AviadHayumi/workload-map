<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# verify

Validates a Karta definition, and optionally proves it against a real workload
object. Offline, no cluster.

## Validate

Every definition should pass this:

```bash
go run ./hack/karta-verify --karta ./mydef.yaml
```

It runs `KartaValidator` and exits 0 when the definition is well-formed, or 1
with the validator's message otherwise.

## Prove it against a real CR

Validation says nothing about whether a jq path resolves against a real object,
so a definition can pass it and still extract nothing. Adding `--workload` runs
the definition against a real custom resource and reports what came out.

```bash
go run ./hack/karta-verify --karta ./mydef.yaml --workload ./real-cr.yaml
```

## Predict first

Run it with `--predict` and it checks the extraction against values written
before the run. Reading the output after the fact invites rationalizing whatever
appears; committing to the numbers first does not.

```bash
go run ./hack/karta-verify --karta ./mydef.yaml --workload ./real-cr.yaml --predict ./predict.yaml
```

Predictions are partial. Only the fields present are compared, so a prediction
may cover one component or one field.

```yaml
status: [Running]
components:
- key: group          # "name", or "name[instanceId]" for a multi-instance component,
  replicas: 3         # prefixed by the owner path when nested, e.g. "group/leader"
- key: group/leader
  replicas: 3
  containers: [nginx2]
  podSpec: true
- key: group/worker
  replicas: 9
```

## Round-trip the writes

Reading proves half of a definition. The other half is that a consumer can
write through it without touching anything else, and that cannot be seen by
reading the paths. `--write` exercises every write path in memory, on copies of
the object, and prints what each write changed:

```bash
go run ./hack/karta-verify --karta ./mydef.yaml --workload ./real-cr.yaml --write
```

Per component it runs, each on its own copy:

- Identity write: extract the pod spec through the definition and write it back
  unchanged. It must change nothing. A changed path here means the write path
  drops fields or materializes defaults the object never had, or the read path
  and the write path do not address the same location.
- Change probe: set one field (`nodeSelector["karta-verify/probe"]` for a pod
  spec or pod template, otherwise the scheduler name, a label or the image of a
  fragmented spec) to `karta-verify-probe` and write it back. Exactly one leaf
  per instance must change, with that value. A `//` fallback or a filter in the
  path shows up here as a write that landed somewhere else or nowhere.
- Suspend, then resume: apply the suspend actions, then the resume actions.
  Only the action paths may change. After resume the object should be back to
  the original, except where the actions set a field the object did not carry.

Every unexpected change is a warning, so `--strict` fails on it. A write path
cannot be probed when the component extracts no instances from this object;
the run says so and skips it.

## Flags

| Flag | Purpose |
|---|---|
| `--karta` | Path to the Karta definition. Required. |
| `--workload` | Path to a real workload manifest. Without it, validation only. |
| `--predict` | Predictions file to check the extraction against. Requires `--workload`. |
| `--dump` | Write the observed extraction, in predictions format. Requires `--workload`. |
| `--write` | Round-trip every write path in memory and report what changed. Requires `--workload`. |
| `--strict` | Exit non-zero when the run reports warnings. Requires `--workload`. |

The extraction flags cannot do anything without a workload, so passing one
without it is an error rather than a pass. A dropped `--workload` must not look
like success.

Exit codes: 0 success, 1 load or validation failure, 2 prediction mismatch,
3 warnings under `--strict`. Note that `go run` reports a non-zero program exit
as `exit status N` on stderr and itself exits 1. To branch on the exact code,
build first: `go build -o verify . && ./verify ...`.

## Warnings

Every warning is a valid-but-empty extraction, which is the failure class the
validator cannot see:

- The status is unresolved, so no rule in `statusDefinition` matched the object.
- A component declares a `specDefinition` but extracted no pod spec, so the spec
  path missed.
- A component extracted a pod spec with no containers.
- A component produced no instances, so `instanceIdPath` matched nothing.

With `--write`, each write that did not do exactly what it should is a warning
too: an identity write that changed something, a change probe that changed more
than its field or landed in fewer places than there are instances, a suspend or
resume that changed a path outside its actions, or a write the engine refused.

## Example

Run against a definition and a manifest already in the repository:

```bash
go run ./hack/karta-verify \
  --karta docs/catalog/leaderworkerset-x-k8s-io-leaderworkerset-v1.yaml \
  --workload docs/examples/quickstart/lws.yaml
```

The manifest declares `replicas: 3` and `size: 4`. The `group` component reports
4 while its sibling `leader` reports 3, which is the sibling-consistency failure
described in the scale section of
`skills/add-workload-type/reference/technical-guide.md`.
