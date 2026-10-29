<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Scale checks and evidence

The current exporter is tested through a modeled Kubernetes HTTP API and an
independent metric oracle. Start with the [executable harness guide](runtime-scale.md)
for commands, topology, diagnostic overhead and acceptance gates.

## Reporting results

Record the source revision, binary and harness hashes, topology, API budgets,
worker counts and run duration. Keep lifecycle correctness separate from CPU,
allocation, latency and memory measurements. Compare repeated runs with the same
settings and retain failed runs. Diagnostic profiles explain costs; repeat the
unprofiled comparison after a change.

The [component integration note](../../../docs/design/metrics-exporter/component-alignment.md#validation-status)
separates earlier evidence from validation of the rebased implementation.
The [ownership comparison guide](ownership/README.md) describes the narrower
cached owner-walk benchmark. It does not measure full exporter throughput.

## Source

- [runtime_scale.py](runtime_scale.py) serves the modeled API, runs the binary
  and independently checks metric identities, values and lifecycle behavior.
- [cmd/](cmd/) wraps the production application with loopback test diagnostics.
  Its CPU, allocation and HTTP overhead is included in measurements.
- [runtime_scale_test.py](runtime_scale_test.py) tests fixture and oracle failures.
- [owner_bench.py](owner_bench.py) runs the bounded ownership microbenchmarks.

These fixtures do not measure production API-server or etcd capacity. A passing
small scenario does not prove a larger topology or replace real-cluster tests.
