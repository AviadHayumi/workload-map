<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Skipped operator versions

Versions inside the supported window that could not be recorded, with the
reason. Everything else under this tree recorded and replayed green.

- dynamo 1.4.x (the line upstream supports): the operator installs and runs,
  but no longer drives the deprecated v1alpha1 DynamoGraphDeployment to
  running; the workload sits Initializing until the timeout. The matrix keeps
  1.2.1, the newest version the flows can record, until the flows move to the
  v1beta1 API.
- kubeflow v2.x (the lines upstream supports): the v2 trainer is a new API
  generation (TrainJob) and no longer serves the PyTorchJob and MPIJob APIs
  these flows record; v1.9.4 is the newest line that does.
