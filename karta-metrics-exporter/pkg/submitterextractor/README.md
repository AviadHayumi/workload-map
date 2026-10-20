<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Submitter extractor

This package defines the submitter extraction contract. Implementation follows.
`Evaluate` receives one object's metadata and explicit group, version and kind.
It reads no Kubernetes resources and receives no workload spec or status.

CEL exposes `labels`, `annotations` and `resource`.
The `resource` map contains `namespace`, `name`, `group`, `version` and `kind`.
A future implementation compiles the expression once and requires a string result.
An empty result means unknown identity. On error, callers ignore the result.
Context cancellation remains `context.Canceled` or `context.DeadlineExceeded`.

The extractor does not authenticate metadata or combine identities across Pods.
Constructors and expression configuration remain outside this scaffold.
