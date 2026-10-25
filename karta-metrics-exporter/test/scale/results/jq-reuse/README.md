<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Bounded JQ reuse microbenchmark

Candidate: 535c5e9305a874d32a93cc672308ad16a518e40f.
Baseline evaluator: runner.go from 4153bed48df1b0a719e971399f2b1dcdcefc0fa9.
Measured on Apple M4 Pro, darwin/arm64, using the repository Go toolchain.

Each operation creates a fresh SDK runner, converts the same representative Job-shaped JSON input, and evaluates four scalar status expressions. The benchmark includes JSON conversion and evaluation. It does not reuse a runner's JSON cache across operations. The two-worker case sets GOMAXPROCS to 2 and uses RunParallel with the default parallelism of 1. Both cases run five repeats.

The baseline uses the actual committed runner.go through a Go overlay. The benchmark source is identical between baseline and candidate. The unused cache file remains compiled into the baseline test binary, but baseline Evaluate does not call it.

| Median | Baseline | Candidate | Reduction |
| --- | ---: | ---: | ---: |
| Serial time/op | 15,854 ns | 7,252 ns | 54.3% |
| Serial bytes/op | 32,144 | 10,987 | 65.8% |
| Serial allocations/op | 487 | 166 | 65.9% |
| Two-worker time/op | 13,553 ns | 5,953 ns | 56.1% |
| Two-worker bytes/op | 31,932 | 11,608 | 63.6% |
| Two-worker allocations/op | 487 | 176 | 63.9% |

Raw outputs:
- [Baseline raw output](before.txt)
- [Candidate raw output](after.txt)

Baseline overlay: /tmp/jq-baseline-overlay.json, mapping execution/runner.go to /tmp/jq-runner-baseline.go.

Commands ran from karta/sdk with GOCACHE=/tmp/karta-layout-go-cache:

```sh
go test -overlay=/tmp/jq-baseline-overlay.json ./pkg/jq/execution -run '^$' -bench '^BenchmarkFreshRunnerStatus' -benchmem -count=5
go test ./pkg/jq/execution -run '^$' -bench '^BenchmarkFreshRunnerStatus' -benchmem -count=5
```

Validation: 390 affected SDK Ginkgo specs (jq 51, execution 68, resource 223, instructions 48), 151 exporter controller specs and 17 real API integration specs passed with the race detector. Targeted lint reported zero issues. Targeted vet and build passed. No public API, dependencies, flags, metrics or runtime scheduler changed.

This measures repeated scalar compilation cost only. It does not prove the full-scale live-heap gate, resource usage with arbitrary definitions, or giant-root/intermediate-owner capacity. The unchanged full-scale protocol remains required. Mutable outputs and assignment operations intentionally do not share compiled programs.
