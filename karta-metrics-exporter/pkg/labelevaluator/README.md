<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Label evaluator

One compiled expression evaluates one unstructured object and returns a string.
For example, the resolver passes the workload object to read a team annotation,
or the Pod object to read its scheduler name.

`CompileFunc` accepts `Config` and returns an `Evaluator`. A constructor will
compile the expression once. `Evaluate` will run it on each supplied object.
Compilation and evaluation errors are returned to the caller.

This package contains the contract only. It does not change or replace the
existing submitter extractor.
