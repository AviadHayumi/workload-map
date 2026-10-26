<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Controller cleanup verification

The controller cleanup at `cf0b9c8e6c6b75df9b9e83760cd19393fda0f06e`
passed five deployed exporter cases on 2026-10-26. The run used the new
`make test-exporter-e2e` entry point and a rebuilt image.

- Image: `karta-metrics-exporter:controller-cleanup-cf0b9c8e`.
- Observed container image ID: `sha256:ec0abc47ac2017eb17ccbd5ad017f2878eb21b19d9159e9b1c62f64afde37311`.
- Local context: `kind-exporter-e2e`.
- Result: 5 passed, 0 failed, 18 deliberately filtered out; 133.294 seconds.

| Case | Result | Seconds |
| --- | --- | ---: |
| deployment | passed | 36.265 |
| job-zero-completed | passed | 26.198 |
| job-zero-failed | passed | 12.287 |
| job-zero-suspended | passed | 3.147 |
| jobset | passed | 41.277 |

Each case checked root identity, phases, component coordinates, label replacement
and series removal after deletion. The suite removed its Helm release and all
labeled test namespaces. The installed operators remained available.

The controller race suite and all 17 API integration cases also passed with the
race detector. Independent source comparison confirmed that the file moves
preserved declarations. The two helper substitutions preserve owner validation
and namespace membership behavior.

This was a targeted follow-up to the [26-case acceptance run](2026-10-26.md).
The complete operator matrix, monitoring lifecycle and scale measurements were
not repeated for this cleanup.

Run from the repository root after building and loading the image:

```sh
KUBECONFIG=/tmp/exporter-e2e.kubeconfig \
EXPORTER_E2E_IMAGE=karta-metrics-exporter:controller-cleanup-cf0b9c8e \
make test-exporter-e2e \
  EXPORTER_E2E_FOCUS='operator workloads (jobset|deployment|job-zero)' \
  EXPORTER_E2E_OUTPUT=/tmp/exporter-e2e-controller-cleanup \
  EXPORTER_E2E_TIMEOUT=30m
```

Raw Ginkgo JSON, JUnit and object/metric captures are retained locally in
`/tmp/exporter-e2e-controller-cleanup`.
