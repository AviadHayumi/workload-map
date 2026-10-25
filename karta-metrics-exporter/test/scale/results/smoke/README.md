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

## Batched-refresh smoke checkpoint

`direct-1000-v3.json.gz` and `intermediate-1000-v3.json.gz` use the same
production runtime at `c40f5852`, with explicit QPS 20, burst 40, two workers,
5-second refresh, 20 seconds quiet and 20 seconds churn at 100 events/s.
Go binary build metadata confirms the actual source revision. The direct report's
supplied revision field says `9afe1c5f`; the intervening `c40f5852` changed only
the Dockerfile. Image builds and other checks were active on the host.

The direct case passed in 49.50 seconds, with 2.84 seconds cold convergence.
The intermediate case converged cold in 53.61 seconds. It made exactly 1,000
intermediate metadata GETs during cold start, plus 20 metadata LIST pages.
The full run made 1,001 GETs (including recreation) and 40 LIST pages, with no
intermediate WATCH. Cold GET demand still dominates startup; batched refresh
addresses maintenance rather than cold population. A direct-owner pass cannot
establish intermediate-owner capacity.

Both observed 2,000 of 2,000 churn events. Their p99 publication observation
upper bounds were 26 ms and 32 ms; full queue drain was 38 ms and 48 ms.
All heap and deletion checks passed. Neither is five-minute acceptance.
The intermediate run failed the current strict continuity oracle because one
churn scrape had one invalid root, then recovered within the next 0.57 seconds.
Quiet scrapes stayed valid. All reconnect, update, recreation and cleanup stages
subsequently converged. Raw evidence preserves the failure.

`intermediate-1000-v3-reasons.json.gz` is one bounded repeat on the unchanged
binary with 10 seconds quiet, 20 seconds churn and one-second oracle cadence.
The harness adds bounded failed-row examples, scrape time bounds and root-event
emission versions/times. It does not change acceptance rules. The repeat passed
and did not reproduce the churn gap. It observed all 2,000 events with a 51 ms
p99 upper bound and 49 ms drain; cold convergence was 53.00 seconds.

The repeat captured `api_unavailable` invalidity during the explicit initial
root change and Pod update. For example, the churn-baseline scrape at
63.359794-63.535600 seconds reported that reason for `job-0-g0`; its Pod row
retained resolved coordinates with `observation_valid="false"`. At this source
revision, `acceptObject` calls `failPublicationLocked(..., discovery.ErrStale)`
on an accepted root resourceVersion change, and `failureReason` maps that to
`api_unavailable`. This supports distinguishing deliberate update invalidation
from quiet-refresh continuity. It does not establish the reason for the earlier
unsampled gap. No oracle rule was relaxed; contract interpretation remains open
for that earlier failure. No 10k or 100k run followed these smokes.

## Corrected oracle and verified acknowledgment

`direct-1000-verified.json.gz` and `intermediate-1000-verified.json.gz` use
exact source `f605ac23`, including runtime correctness checkpoint `63dc9ed1`
and the corrected oracle described in [the harness guide](../../runtime-scale.md).
Both use QPS 20, burst 40, two workers, 5-second refresh, 20 seconds quiet,
20 seconds churn, one-second full-oracle cadence and 10 ms diagnostic polling.
The diagnostic acknowledgment now verifies one captured publication's creation,
status and current Pod mappings, plus exact UID, version, phase and labels.
These latency observations are stronger than the historical version-only ACK.

Both runs passed every correctness stage and observed 2,000 of 2,000 events.
The direct run took 51.11 seconds, including 3.94 seconds cold convergence;
its verified p99 observation upper bound was 42.9 ms and queue drain 41.4 ms.
The intermediate run took 102.28 seconds, including 52.74 seconds cold
convergence; its corresponding bounds were 45.0 ms and 41.9 ms. All quiet and
final scrapes were valid. Churn allowed only coherent invalidity for roots
whose emitted-to-verified-ACK intervals overlapped that scrape. Heap gates
passed. Twenty seconds still does not meet five-minute acceptance.

The intermediate run made 1,000 cold owner GETs and one recreation GET, plus
40 metadata LIST pages over the full run. It started no intermediate WATCH.
Cold per-owner API demand remains the dominant difference from direct ownership.
Diagnostic traffic was 1,545 requests / 2.39 MB / 2.83 seconds cumulative HTTP
time for direct ownership, and 1,606 / 2.44 MB / 3.06 seconds for intermediate
ownership. CPU and allocation samples include that work.

The local kind nodes were confirmed stopped at 19:15:26 UTC. Exact stop
completion was not captured, so the first direct run may overlap shutdown.
The node absence was also checked during the intermediate run. These remain
bounded development smokes, not isolated-host capacity measurements. Further
review found runtime issues outside these scenarios after `63dc9ed1`; passing
these reports does not override those findings. No 10k run followed.

## Primed 1k intermediate smoke

`primed-1000-33d292fb.json.gz` uses the same immutable primed binary as the
[10k cold comparison](../cold-start/README.md#matched-primed-runtime), with
QPS 20, burst 40, two workers and 5-second refresh. All lifecycle, quiet and
churn correctness checks passed in 48.40 seconds. Cold convergence took 2.052
seconds and used two intermediate LIST pages plus 36 GETs. Peak sampled RSS
was 171,556,864 bytes. Verified acknowledgment p99 was bounded by 50.2 ms and
queue drain by 39.5 ms; heap gates passed.

The exact event-count gate failed: 1,999 events were emitted and observed against
a 2,000-event schedule over 20 seconds. The old scheduler could drop a final
due tick when diagnostic polling resumed after the wall-clock boundary. The
failed gate remains in the unchanged report. Twenty seconds also remains below
the five-minute acceptance duration.
