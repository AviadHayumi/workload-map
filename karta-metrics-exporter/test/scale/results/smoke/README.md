<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Development smoke evidence

These results were collected while kind setup and implementation work continued
on the host. They prove bounded correctness and diagnostic behavior, not fair
performance comparisons or large-scale acceptance.

- `recreation-failure.json`: the first production command run failed to restore
  one Pod after same-name root/intermediate recreation within 30 seconds.
- `production-10-1.json`: the rebuilt production command passed all stages after
  the runtime owner-cache UID fix. It used 10 Job roots and one Pod per root.
- `diagnostic-10-1-debug.json`: the instrumented composition passed all stages.
  Its two-second, 10-event/s debug churn observed 20 of 20 published root versions.
  This does not satisfy the five-minute, 100-event/s acceptance gate.

Each JSON records binary and harness hashes. The harness evolved between runs;
compare hashes and recorded settings before comparing timing. Some early results
predate the complete `measured_gates` map. Missing fields are not passes.

`store-before.txt` and `store-after.txt` are isolated Go helper microbenchmarks,
provided by the Store implementation lane. Both use the same benchmark harness,
Go 1.27.3, GOMAXPROCS=4 and a 300 ms benchmark duration on darwin/arm64 M4 Pro.
The before overlay uses Store source from `746b96f6`; after uses `76f54074`.
They do not include HTTP API, informers, ownership discovery or exporter startup.
For 100k Pods, lookup changed from 287 us to 1.81 us and single-Pod replacement
plus publication from 318 us to 15.8 us. Full refresh remained linear. Raw
allocation and timing measurements are retained in the files.

`cold-start-1000-1-failure.json` is a failed instrumented 1,000-root smoke with
one intermediate and one Pod per root. It used a 5-second metadata refresh to
exercise TTL behavior. No workload rows converged within the 180-second cold
stage. The process stayed below 65 MB sampled RSS. Quiet/churn/reconnect stages
were not reached. Queue and API observations are preserved for diagnosis.

The failed 1,000-root binary used client-go's source-verified defaults of QPS 5
and burst 10, with two runtime workers. Its configuration is preserved separately.
There were 906 intermediate ReplicaSet metadata GETs in 180.5 seconds, consistent
with client throttling. Only initial metadata LISTs completed; source verification
was queued behind object work. Final queue depth was 1,979 and retries 1,902.
Both client throttling and source-verification starvation matter. Increasing an
API budget alone would not establish an algorithmic improvement.

`direct-owner-1000-1.json` uses the same binary and limits as the failed
intermediate-owner case, with direct Job-to-Pod ownership. It passed cold start
in 1.20 seconds and all continuity/reconnection stages in 52.17 seconds. There
were no object GETs. In 20 seconds it emitted and observed 1,999 root changes.
This isolates the intermediate metadata GET and queue interaction; it does not
prove the deeper-owner scenario or the five-minute churn target.

`intermediate-1000-v2-drain-failure.json.gz` records a later runtime with separate
source/object/publication queues and explicit QPS 20, burst 40, two workers.
Cold start and all periodic quiet/churn oracle scrapes passed. The run then failed
the full-queue drain deadline: 5-second intermediate refresh demand exceeded the
20-request/s budget. Publication/source queues drained while object maintenance
remained queued. Reconnect/delete stages were not reached. This is a combined
code and API-budget change, not a controlled algorithm speed comparison.
The compressed JSON retains all sampled queue and API observations.

These earlier intermediate-owner fixtures also changed ReplicaSet resourceVersion
when only its Job changed. The harness now assigns versions independently per
object. Cold-start failures are unaffected. Treat earlier intermediate churn
results as diagnostic evidence, not a faithful immutable-owner comparison.
