<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Scale checks and evidence

The current exporter is tested through a modeled Kubernetes HTTP API and an
independent metric oracle. Start with the [executable harness guide](runtime-scale.md)
for commands, topology, diagnostic overhead and acceptance gates.

## Current evidence

- [100k-root zero-Pod candidates](results/full-scale/README.md): the latest
  protocol-2 repeat passed correctness, all 30,000 verified events and all other
  measured gates, but failed heap growth at 10.12% against the unchanged 5% gate.
  Earlier scheduling, health-reason and sampling failures remain available.
  The one-Pod case was not started.

- [Verified 1k-root smokes](results/smoke/README.md#corrected-oracle-and-verified-acknowledgment):
  direct and intermediate ownership passed bounded lifecycle and 20-second churn
  checks. These do not establish five-minute or 100k-root acceptance.
- [Matched 10k-root cold comparison](results/cold-start/README.md): priming
  converged in 13.46 seconds at unchanged API budgets. The pre-priming run
  failed its 180-second deadline with 3,256 of 10,000 Pod mappings. Both raw
  reports and their exact sources are preserved.
- [Warm owner-walk measurements](results/owner-walk/README.md): repeated small
  comparisons with the old PoC and k8s-workload-discovery. Read the
  [comparison scope](ownership/README.md); these are not full-runtime results.

The [smoke evidence index](results/smoke/README.md) also retains earlier failed
runs and explains the corrected churn oracle. The
[historical PoC continuity results](../../../docs/metrics-exporter-lab/historical-poc/scale/runtime-20261020-continuity/README.md)
retain the failed 100k-roots/10-Pods run above its 8 GiB cap. Historical semantics,
source versions and timing methods differ from the current harness.

## Source

- [runtime_scale.py](runtime_scale.py) serves the modeled API, runs the binary
  and independently checks metric identities, values and lifecycle behavior.
- [cmd/](cmd/) wraps the production application with loopback test diagnostics.
  Its CPU, allocation and HTTP overhead is included in measurements.
- [runtime_scale_test.py](runtime_scale_test.py) tests fixture and oracle failures.
- [owner_bench.py](owner_bench.py) runs the bounded ownership microbenchmarks.

These fixtures do not measure production API-server or etcd capacity. A passing
small scenario does not prove a larger topology or replace real-cluster tests.
