<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Fixture contention diagnostic

These are bounded load-generator measurements, without an exporter process.
They do not replace or reclassify the failed full protocol-3 run.
Production source remains f868589a. The baseline fixture is from d66cb9a3;
`context.json` records both fixture source hashes.

The fixture has 100,000 roots, one Pod per root, a prefilled 20,000-event
history, three WATCH clients (Jobs, Pods, Namespaces), continuous 500-item
Job metadata LIST pages, and 3,000 root mutations scheduled at 100/s.
A separate thread repeatedly parses a generated 100,000-root/one-Pod
exposition through the existing independent oracle. The failed run's original
HTTP bodies were temporary and unavailable. This generated file is a quiet
Running snapshot while the mutation fixture changes 750 roots to Failed;
its oracle verdict is deliberately not an acceptance check. The experiment
measures parsing and fixture contention, not publication correctness.

The reported pair ran serially in fresh Python processes without concurrent
tests. Preliminary trials are not used in this table. Desktop background
work was not controlled. These are individual scenario measurements, without
statistical confidence estimates.

| Measurement | Before | After |
| --- | ---: | ---: |
| Mutations completed | 3,000 | 3,000 |
| Emission elapsed seconds | 29.999 | 29.990 |
| Maximum emission lateness | 44.23 ms | 26.94 ms |
| Maximum fixture.change duration | 31.07 ms | 0.097 ms |
| Metadata pages served | 735 | 843 |
| Completed oracle durations | 10.24, 10.32 s | 9.69, 9.70, 9.79 s |
| Process CPU seconds | 30.43 | 30.49 |
| Job WATCH events delivered at cutoff | 2,999 | 2,998 |
| Response bytes at cutoff | 92,690,751 | 105,902,565 |

The experiment stops at the emission cutoff without draining in-flight HTTP
requests or WATCH delivery. Page counts describe completed client reads; raw
server request counts can include the next in-flight page.

Both versions sustain the requested small workload. Neither reproduces the
original one-second scheduler failure. The result supports removing measured
contention but does not prove how much lock waiting, Python GIL contention,
or another host effect caused the original failure.

The correction releases the fixture lock before LIST object construction,
JSON serialization, and response writes. GET takes a detached object under
the lock and writes it after release. LIST completion is still recorded after
the response write. Each WATCH traverses only the new suffix of the global
history, restores ascending order, and advances over other resource events.
The global retained-history expiration check and fault behavior are unchanged.
No acceptance threshold, event rate, hot-root set, or exporter API changed.

Blocked response-writer regressions fail on the baseline for LIST, GET, and
404 responses. A paused page-construction test also fails there: mutation
cannot proceed until construction resumes. Both tests pass after correction.
The complete 37-test Python suite passes, including ordered interleaved WATCH
events, 410 expiration, pagination snapshots, and explicit EOF.

`before.json.gz` and `after.json.gz` retain all emission timing samples,
served API counters, and completed oracle durations. `reproduce.py` is the
exact diagnostic script (with the repository license header added). Run it
with a directory containing `runtime_scale.py` and `runtime_scale_test.py`,
then an output JSON path. It creates `/tmp/fixture-capacity.prom` when missing.
For the before case, export the fixture from d66cb9a3 to a separate directory.
Use `PYTHONDONTWRITEBYTECODE=1`. Do not run the two processes concurrently.
