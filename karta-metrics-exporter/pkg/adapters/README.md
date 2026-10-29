<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Output adapters

The [OpenTelemetry adapter](otel/) exposes saved workload observations through
Prometheus. It reads one Store capture per collection. It does not discover
workloads, evaluate Karta definitions or run label expressions.
