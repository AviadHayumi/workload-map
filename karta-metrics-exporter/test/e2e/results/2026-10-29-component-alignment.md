<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Component integration acceptance, 2026-10-29

The rebased exporter passed full presubmit, nine actual-main kind checks,
27 operator cases and twelve matched performance runs. All 2,400 emitted
performance events were acknowledged. A first-use startup delay remains
unexplained, and the short runs do not establish production capacity.

## Tested source

| Identity | Value |
| --- | --- |
| Upstream base | `50719d65c52a5a0fb88502576175fff0e3f6691a` |
| Measurement snapshot | `df5a1b0d0dfa90227c6df78e9ce78e53287c2e4b` |
| Final presubmit snapshot | `a2e7597a09256ed6226004ced944c0f8d866ed06` |
| Exporter production source SHA256 | `cb0038d47df739301bd210b23113291b4009ec6b27403ada01397526c6c39668` |
| SDK source SHA256 | `c945e90dc0b7b9c5c964f6be35d5c9fcd21039302df85b3d219000ef03691f97` |
| Main binary SHA256 | `49087c2084b635fa5388ee44e39d8be89551a923d44fec5937b4d9ab05763177` |
| Scale binary SHA256 | `590fa97305f36582c00cb40300451150194b37a9895c6c179ff4d7461a14c77c` |
| Operator image digest | `sha256:e632cb19e125273d7d661fb12b9fee1d89dc6e65120016ed0dc7ec8ded7ba603` |
| Toolchain | Go 1.27.3, darwin/arm64 |
| Kind run | `20261029-143636-b5e02c` |

The binary was built from the upstream base plus integration changes. The base
commit alone does not identify the tested code. The production source digest
was unchanged before and after the kind run. The final presubmit includes the
test-harness change below and retains the measured production and module files.
The delivery commit will retain these executable sources, with documentation
updated after validation. The
[component integration note](../../../../docs/design/metrics-exporter/component-alignment.md)
describes the component boundaries and dependency choices.

## Actual-main kind checks

| Check | Result |
| --- | --- |
| Startup, Pod attribution and a zero-Pod root | PASS |
| Definition update with unchanged Pod resource version | PASS |
| Definition deletion selects the enabled catalog definition | PASS |
| Definition re-addition with a new UID | PASS |
| Pod label update | PASS |
| Native Job suspension | PASS |
| Root deletion | PASS |
| Same-name root replacement with new root and Pod UIDs | PASS |
| SIGTERM shutdown | PASS, exit code 0 |

This run did not inject an API outage. The Job status transition was suspension,
not completion. Replacement Pods had different names as well as different UIDs.
The nine checks do not substitute for the broader operator matrix or performance
comparison.

## Presubmit and operator matrix

Full `make check` passed with exit status 0 on the clean final presubmit snapshot.
The verification manifest reported no production or module drift from the
measurement snapshot. Unchanged tests could use the Go test cache.

| Operator profile | Result | Duration |
| --- | --- | ---: |
| Default | 24/24 PASS | 1,017.404 s |
| Alternate API versions | 2/2 PASS | 137.336 s |
| Bare-Pod exclusion | 1/1 PASS; 23 cases intentionally filtered | 34.570 s |

All three profiles used image
`karta-metrics-exporter:component-alignment-rebased-20261029` with the digest
recorded above. They exercised real operator-created workloads using the
[operator matrix](../README.md).

The initial default attempt failed in BeforeSuite before executing any case.
Helm attempted to install the already-provisioned Karta CRD and encountered field
ownership conflicts. The test harness now supplies `--skip-crds` during release
installation. It reuses the provisioned CRDs; these runs do not validate CRD
installation. This test-only repair did not change the exporter binary. The
initial failed attempt remains a failure, separate from the successful rerun.

## Matched performance

The modeled-API comparison used baseline `d9d1710a`, two repeats of three workload
shapes, and both baseline and candidate binaries: twelve runs total. Each ran
for ten seconds at 20 events/s. Settings were Go 1.27.3, `GOMAXPROCS=4`, two
workers and API QPS/burst 20/40. Runs were serial, with order reversed for the
second repeat and no concurrent heavy builds or operator workload changes.

All lifecycle checks passed. All 200 events in each run were independently
acknowledged, for 2,400 total. Dense-root cumulative allocation was 0.992x/0.995x
baseline; sampled CPU was 0.785x/0.948x. No measured cost exceeded 1.25x baseline
in both repeats. API request counts and response bytes matched within each
shape. These results cover the complete rebased composition, not an isolated
resolver or dependency change.

The 1,000-root candidate cold start took 1.409 s versus baseline 0.403 s in the
first repeat, then 0.389 s versus 0.392 s in the second. The original baseline
also showed a first-use delay of 1.421 s. These observations are consistent with
a first-use effect but do not establish its cause. Existing evidence places most
of the candidate delay before the metrics listener becomes available. It does
not identify which initialization or system behavior caused it. The outlier was
retained without a selective rerun.

CPU and allocation include diagnostics and metrics collection. End-to-end
latency includes polling, transport and oracle work. These ten-second runs do not
establish statistical
confidence, sustained capacity, production API-server capacity, or the longer
churn and heap-stability gates described in the
[scale guide](../../scale/runtime-scale.md).

Earlier pre-rebase results remain historical evidence. They are not counted as
passing checks for this source snapshot.
