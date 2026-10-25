<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# GPU lab helpers

These helpers belong to the earlier GPU demonstrations. They run separately
from the [Karta metrics exporter](../../../karta-metrics-exporter/README.md).
The observer, activity randomizer and finite workload submitter remain here with
their original tests. The submitter-attribution examples are historical; that
configuration is not part of the current exporter.

Run the helper tests from this directory:

```sh
go test ./...
```

Build the helper Dockerfiles from the repository root. Deployment files need the
endpoints and image names for your own lab. Historical cluster evidence lives
in the parent directory.
