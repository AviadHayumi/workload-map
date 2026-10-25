<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Sampled heap diagnostic run

Source `a52b121618acb378d0f67a8195491157d62960af` retains production `4153bed4`.
Binary SHA256:
`e2c6f7dba8d2a56e5edb54fbda4303ae87b948cff68a2e150c9462f7d87377fe`.
All four kind nodes were stopped. Small concurrent profile analysis was allowed;
no other benchmark or tests ran. This is a diagnostic run, not full acceptance.

The scenario retained 100k roots, zero Pods, 20 QPS, burst 40, two workers,
45-second refresh, 120-second maxAge, 8 GiB RSS and existing timeouts. It used
zero quiet seconds, 180-second churn and 15-second GC samples with profiles.
No additional GC was performed for profiling. GC, snapshot and profile times,
profile sizes and hashes are in `result.json.gz`. Profiles use the exact binary
for symbolization. They are sampled allocation evidence, not an exact census.

All lifecycle/oracle checks passed in 445.5388 seconds. All 18,000 events were
verified; publication p99 upper bound was 0.89455 seconds and drain 1.00967
seconds. Peak RSS was 6,359,842,816 bytes. The five-minute duration and full
sampling-window gates correctly remain false: actual GC coverage was 179.94975
seconds. The first/last heap ratio was +5.237%; no earlier result is reclassified.

| Profile | Churn completion seconds | HeapAlloc bytes |
| --- | ---: | ---: |
| heap-001.pprof | 0.2508 | 786540560 |
| heap-002.pprof | 16.7345 | 859260736 |
| heap-003.pprof | 30.2565 | 836059864 |
| heap-004.pprof | 45.2302 | 784653968 |
| heap-005.pprof | 66.3409 | 864916144 |
| heap-006.pprof | 75.2016 | 824098816 |
| heap-007.pprof | 91.2958 | 789381064 |
| heap-008.pprof | 116.6086 | 870058592 |
| heap-009.pprof | 120.3152 | 853553208 |
| heap-010.pprof | 142.4123 | 876727088 |
| heap-011.pprof | 150.2676 | 841250456 |
| heap-012.pprof | 168.5185 | 788330144 |
| heap-013.pprof | 180.2006 | 827733056 |

Heap allocation oscillation is visible across repeated low/high samples. Profile
serialization took approximately 5.2-5.7 ms per churn sample and produced 55-67 KB
compressed profiles. Full diagnostic HTTP traffic totaled 11,768,635 response
bytes and 19.751 seconds across 1,131 requests, including ACKs/stats. This does
not isolate profiling CPU from other runtime work. Profile 014 follows delete-all:
HeapAlloc was 92,665,184 bytes and recovery passed. Raw results and every profile
are preserved for allocation attribution; no threshold or baseline was changed.
