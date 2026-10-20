<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Submitter extraction

Given a Pod with `metadata.annotations.user: alice`, this call returns
`Submitter{Identity: "alice"}`:

```go
extractor, err := submitterextractor.New(
    "'user' in annotations ? annotations['user'] : ''",
)
if err != nil {
    return err
}
submitter, err := extractor.Evaluate(
    ctx, pod, schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
)
```

The evaluator receives only these values:

```text
labels:      pod.metadata.labels
annotations: pod.metadata.annotations
resource:    {namespace, name, group, version, kind}
```

Absent labels and annotations become empty maps. Resource namespace and name
come from the object. Group, version and kind come from the supplied GVK. CEL
cannot read spec, status or Kubernetes APIs.

`New` compiles a string-valued expression once. `Parse` accepts the JSON object
`{"submittedByExpression": "..."}` and rejects unknown fields. Empty or invalid
expressions reject construction. The controller disables extraction by leaving
its dependency unset.

Expressions are limited to 8 KiB and evaluation to 10,000 CEL cost units.
Evaluation checks caller cancellation, including cache hits. Identities may be
empty. Nonempty values must be valid UTF-8, at most 512 bytes, and contain no
control characters or surrounding whitespace. Failure returns an empty
`Submitter` and an error.

The extractor caches up to 4,096 successful results by every CEL input. Spec,
status, UID and resource-version changes do not invalidate this derived cache.
Changes to labels,
annotations or any resource field do. Failed or canceled evaluations are not
cached. Calls may run concurrently, but callers must not mutate their input
objects during evaluation.

The controller owns agreement across a root and all its Pods. Extraction alone
does not establish workload membership, freshness or authenticated identity.
