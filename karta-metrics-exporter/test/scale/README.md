<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Scale checks and evidence

The current exporter is tested through a modeled Kubernetes HTTP API and an
independent metric oracle. Start with the [executable harness guide](runtime-scale.md)
for commands, topology, diagnostic overhead and acceptance gates.

## Current evidence

- [Current 100k-root outcomes](results/full-scale/README.md): `f868589a` with
  harness `b234bfc8` passed every protocol-3 one-Pod gate in a fresh full run
  using the prospectively declared 1,800-second overall guard. `535c5e93`
  passed protocol-2 zero-Pod gates. Earlier failures remain preserved;
  the default dense profile failed cold startup at 20 QPS/burst 40; the separately
  tuned 100/200 profile exceeded 16 GiB RSS before cold convergence. Final-source
  zero-Pod and giant-root results remain pending.

- [Fixture contention diagnostic](results/fixture-capacity/README.md): bounded
  before/after tests remove proven API-lock and WATCH-scan overhead. Both sustain
  the small requested rate; the full-run scheduling failure remains unclassified.
- [Optional heap-profile diagnosis](results/heap-diagnostic/a52b1216/README.md):
  a separate short run reproduces heap oscillation with 13 sampled profiles.
  It is not full acceptance and does not reclassify earlier failures.
- [Verified 1k-root smokes](results/smoke/README.md#corrected-oracle-and-verified-acknowledgment):
  direct and intermediate ownership passed bounded lifecycle and 20-second churn
  checks. These do not establish five-minute or 100k-root acceptance.
- [Matched 10k-root cold comparison](results/cold-start/README.md): priming
  converged in 13.46 seconds at unchanged API budgets. The pre-priming run
  failed its 180-second deadline with 3,256 of 10,000 Pod mappings. Both raw
  reports and their exact sources are preserved.
- [Scalar JQ reuse microbenchmark](results/jq-reuse/README.md): five matched
  repeats measure compilation/evaluation cost, not full-runtime capacity.
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
