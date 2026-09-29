<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Recorded operator versions

Recorded flow fixtures per supported operator, exactly the set declared in
hack/e2e/supported-versions.env: for each operator, the newest patch of
every release line its maintainers still support. When upstream moves, bump
the env file, run hack/e2e/record-matrix.sh <operator> all, and drop the
folders that fell out of the window.

Each `<operator>/<version>/` folder holds the same recordings the e2e suite
keeps under `test/e2e/recorded_data/`: every distinct CR the operator
produced during each flow, labelled from the operator's own fields,
replayable through the catalog definitions.

How a folder is produced: `hack/e2e/record-matrix.sh <operator> <version>`
installs the operator at that version on the current cluster, verifies it,
records its flows, and files the fixtures here. Old releases that only run on
their era Kubernetes fail fast with the era-cluster hint (see
`require_k8s_max`). A version that cannot install or record is listed in
`SKIPPED.md` with the reason.

Coverage: jobset, lws, kuberay (RayCluster and RayJob), kubeflow (PyTorchJob
and MPIJob), knative, kserve, milvus, grove, dynamo, nim.
