<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter executable scale checks

The harness launches the supplied exporter binary against a loopback HTTP fixture.
The fixture serves Kubernetes discovery, paginated LISTs, GETs and streaming
WATCH events. The binary runs its own cache, discovery, SDK evaluation, label
resolution, publication and OTel collection. No Store records are injected.
This models API responses. It does not measure etcd or production API capacity.

Each root is a Job. Each Job owns a ReplicaSet that owns its Pods. Intermediate
ReplicaSets are available through metadata GET and paginated metadata LIST;
only root and Pod watches are expected.
The bundled Job definition supplies the single `job` component. The independent
oracle checks every root and Pod UID, component coordinates, creation timestamp,
all nine phase values, custom `team` labels and observation health. Root phases
change from Running to Failed. Pod label updates change a visible public value.
Healthy WATCH delivery does not require a subsequent LIST to pass.

Stages are cold start, steady scrapes, a single Pod label update, root/Pod churn,
partial deletion, same-name recreation with new UIDs, delete-all and optional
settling. The oracle rejects old UIDs after recreation and any workload rows
after deletion. A steady or quiet scrape failure remains a failed run even
after recovery. `--quiet-seconds` and `--continuity-seconds` check every periodic
quiet scrape. Sustained churn first primes stable public phase values, then
changes root resource versions while an independent periodic oracle checks all
rows. Only a root whose emitted-to-verified-acknowledgment interval overlaps the
scrape may temporarily report coherent invalid rows. Its UID, coordinates,
labels and phase values must still match. Invalid status requires matching
workload health with reason `api_unavailable`; status and Pod validity must agree.
Creation may be omitted while that root is invalid. Quiet roots remain fully
valid, and the final scrape after churn requires all exact rows and valid proofs.
A changed root that fails to recover within 5 seconds (10 seconds for dense
cases) fails the run. `--refresh-seconds` sets an explicit refresh interval for bounded TTL
regression runs. `--watch-faults` injects Pod WATCH EOF and Job WATCH 410 before
checking reconnection and a fresh visible Pod update.

```sh
(cd karta-metrics-exporter && go build -o /tmp/karta-exporter ./cmd)
python3 karta-metrics-exporter/test/scale/runtime_scale.py \
  --binary /tmp/karta-exporter --roots 10 --pods 1 \
  --settle-seconds 0 --poll-seconds 0.2 \
  --output /tmp/karta-scale-smoke-10-1
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s karta-metrics-exporter/test/scale -p runtime_scale_test.py -v
```

Runs use fresh child processes and `GOMAXPROCS=4`. Start with 10 and 1,000 roots.
Stop other benchmark processes and local kind clusters before larger runs.
Run 10k, then 100k roots with 0, 1 and 10 Pods each. Separately run one root
with 100k Pods. The default RSS cap is 8 GiB. The 100k/10 scenario needs an
explicit `--max-rss-mib 16384`; its predeclared cold-start target is 300 seconds.
The monitor samples once per second and kills the child on RSS or wall limits.
This is a sampled limit, not an operating-system memory limit.

## Measurements and gates

`result.json` includes binary/harness hashes, configuration, request counts by
resource/operation/representation, WATCH delivery, response sizes, sampled RSS
and CPU time, and per-stage convergence. Pure HTTP request/transfer time appears
as `http_seconds`. The offline streaming oracle appears as `oracle_seconds`.
`scrape_seconds` includes both. Temporary response files avoid retaining a full
large exposition in Python memory.

Process CPU comes from `ps`, outside the exporter process. Go heap and allocation
samples are recorded only when the executable exposes the corresponding runtime
metrics. Their absence is not zero. Sampled RSS is not live heap. Polling-stage
convergence includes transfer/oracle cost and is not per-event publication latency.
Queue backlog and drain time require the test diagnostics wrapper below.

The following targets are predeclared engineering gates on the 48 GiB, 14-CPU
host. A missing measurement leaves its gate unverified.

| Measurement | 100k roots, 0/1 Pod | 100k roots, 10 Pods |
| --- | --- | --- |
| Sampled RSS | <=8 GiB | <=16 GiB |
| Cold start | <=180 s | <=300 s |
| Pure HTTP scrape | <=25 s | <=25 s |
| Oracle deadline | 60 s | 120 s |
| Publication p99 at 100 events/s for five minutes | <=5 s | <=10 s |
| Queue drain | <=30 s | <=60 s |
| Final three-minute live heap growth | <=5% | <=5% |
| Delete-all live heap | baseline + max(256 MiB, 10% peak) | same |

`complete` reports correctness of the staged oracle. `measured_gates` separately
reports observed performance limits. A short smoke run cannot satisfy the
five-minute churn duration gate.

## Dense default and tuned API profiles

The 100k-root/10-Pod fixture contains 100,000 Jobs, 100,000 intermediate
ReplicaSets and 1,000,000 Pods in one Namespace. Each 500-item verification
round needs 2,000 Pod pages, 200 Job pages, 200 retained ReplicaSet metadata
pages and one Namespace page: 2,401 LIST requests. ReplicaSets have no WATCH.
At a 45-second refresh interval, the minimum average is 53.36 requests/s.
The default 20 QPS supplies only 900 requests per interval. Its burst of 40
cannot cover the sustained deficit. These counts exclude discovery, WATCH
connections, retries and cold GET races. They establish an API request floor,
not a prediction of processing time or production API-server capacity.

Run the default dense profile first at `--api-qps 20 --api-burst 40` and retain
its measured result, including any failure under the existing bounds. Then run
a separately named tuned profile at `--api-qps 100 --api-burst 200`.
The tuned profile changes API configuration, not the algorithm. It does not
turn a failed default profile into a pass. At 100 QPS, one round consumes at
least 24.01 seconds of sustained request capacity before other work.

Both dense profiles retain two workers, 45-second refresh, explicit
`maxAge: 120s`, RSS <=16 GiB and GOMEMLIMIT=12 GiB. Cold start stays <=300
seconds, HTTP <=25 seconds and oracle <=120 seconds. Each still requires
30,000 verified events over 300 seconds at 100 events/s, publication p99 <=10
seconds, drain <=60 seconds, the one-second scheduler lateness bound and the
existing heap/deletion gates. Record separate source hashes, configuration and
results. The 100k-root/0-Pod, 100k-root/1-Pod and giant-root profiles remain at
20 QPS with burst 40. The harness writes `maxAge: 120s` explicitly for every
profile so the source freshness bound appears in each result's configuration.

## Giant-root workload

The giant-root case is one Job with 100,000 Pods. It emits 0.05 root updates/s,
one every 20 seconds, for 300 seconds and exactly 15 verified acknowledgments.
This measures whole-root invalidation and rebuilding of 100,000 Pod mappings.
It does not establish throughput equivalent to 100 independently changing roots/s.

Use `--roots 1 --pods 100000 --churn-seconds 300 --churn-rate 0.05`
with `--publication-latency-seconds 15 --queue-drain-seconds 30`.
The predeclared bounds are verified publication p99 <=15 seconds, drain <=30
seconds, RSS <=8 GiB, cold start <=180 seconds, HTTP <=25 seconds and oracle
<=60 seconds. Set `--max-rss-mib 8192 --stage-seconds 180 --oracle-seconds 60`;
the wrapper receives GOMEMLIMIT=6144MiB (6 GiB). Diagnostic polling stays at
250 ms and scheduler lateness at one second. Existing quiet correctness,
final three-minute heap growth and deletion/heap recovery gates remain in force.
Each root still has at most one pending update. Fractional rates are preserved;
exact decimal duration/rate multiplication determines the target event count.
The explicit latency/drain options are recorded in every result. Their defaults
remain 5/30 seconds for 0/1 Pod and 10/60 seconds for dense independent roots.

## Test diagnostics wrapper

`test/scale/cmd` calls production `app.New` and `Application.Serve`. It adds a
separate loopback-only diagnostics listener. The normal command has no diagnostic
endpoints. Run a small case through the normal command as a separate composition
check before using the wrapper for measurement.

```sh
(cd karta-metrics-exporter && go build -o /tmp/karta-exporter-scale ./test/scale/cmd)
python3 karta-metrics-exporter/test/scale/runtime_scale.py \
  --binary /tmp/karta-exporter-scale --diagnostics --roots 1000 --pods 1 \
  --churn-seconds 300 --churn-rate 100 --max-seconds 900 \
  --output /tmp/karta-scale-instrumented-1000-1
```

The wrapper records `runtime.ReadMemStats` and named client-go queue metrics.
Queue metrics include depth, additions, retries, work/queue duration count, sum
and maximum. In client-go v0.37.1, every `AddAfter` call increments the retry
counter, including healthy delayed renewals. `AddRateLimited` also uses that
path. The raw retry counter is not a failed-reconcile count; queue depth,
completed work and separate error evidence are needed to interpret a backlog. Drain requires two diagnostic samples with zero queued and active
work. Protocol 2 polls immediately after emission and verified ACK completion
and the requested churn window, before joining the oracle or GC sampler. It
records each poll start/completion, emitter completion and last verified ACK.
Drain duration excludes later oracle/sampler joins. Earlier reports included
those joins in a conservative upper bound; their raw values remain unchanged. Active work is observed queue starts minus completed durations. A saturated
maintenance queue fails the drain gate even if current metric rows are correct.
The harness records that failure and continues the lifecycle correctness stages.
Publication polls take one `Store.Capture` and select at most 100 requested
roots. They check creation, status and every Pod mapping against that same
captured failure state and clock. The response includes the selected root and
Pod records. The independent harness verifies the exact root version, UID,
phase and current Pod identities, coordinates and labels before acknowledging.
These polls do not format metrics, but Capture scans the publication set and
selected Pod records add diagnostic CPU and response size, especially for the
giant-root case. That overhead is included in reported latency.

Protocol 3 uses an independent paced emitter and sizes its hot-root set as
`min(roots, max(100, ceil(rate * (publication_limit + 2 * lateness_limit + 2 * poll_seconds))))`.
At 100 events/s, 250 ms polling and one-second scheduling lateness, this is 750
roots for the five-second publication limit and 1,250 for the ten-second limit.
The giant-root case stays at one root and 0.05 events/s, a 20-second rotation.
Reports record the formula, count, rotation time and population cap. A smaller
available population can intrinsically constrain rate; the harness records that
cap rather than inventing roots. Event `i` is scheduled at `i/rate`, for an exact target of 30,000 events
over 300 seconds at 100 events/s. Diagnostic HTTP calls and forced exporter GC
do not block that emitter. Each root has at most one pending event, so a later
generation cannot falsely acknowledge an older one. The emitter waits for that
root's verified acknowledgment before changing it again.

A final due tick is not discarded merely because a poll resumes just beyond
the requested duration. The harness still observes the full requested window.
It records each scheduled and actual offset, lateness, actual emission span
and throughput. There is no zero-tolerance emission-duration gate. Exact event
count and verified publication recovery remain independent requirements.

Before the large runs, the scheduling lateness limit is declared as one second
(`--max-scheduling-lateness-seconds`). This is one complete rotation of the
original 100-root pool at 100 events/s. This limit stays unchanged in protocol 3.
Exceeding it fails the run before a large catch-up
burst; it does not waive exporter backpressure or publication latency limits.
The limit and actual maximum lateness are recorded. The earlier 1,999-event
report is retained unchanged. Deterministic tests cover the final boundary,
pending-root backpressure and long stalls.

Each ACK round covers the entire current pending snapshot in sequential batches
of at most 100 UIDs. Each batch is acknowledged immediately after its response.
HTTP runs outside the emitter condition lock. The polling interval follows the
full round, not one batch per interval. Batch/round start and completion times,
request counts and rejection categories are recorded. Each batch calls
Store.Capture, so larger pending sets increase observer CPU, allocation and
traffic. These costs remain included in latency/RSS. A diagnostic timeout is an
observer failure, not proof of an exact exporter publication timestamp.

Protocol 2 used 100 hot roots at 100 events/s with one pending event per root.
Its one-second rotation plus one-second scheduler allowance could abort around
two seconds, before the declared five-second publication bound. Protocol 3
changes this load shape, not rates, counts, ACK validity or acceptance limits.
Earlier failures remain recorded under their actual protocol, without
reclassification. Their aggregate timing supports the rotation constraint,
but missing per-root ACK history prevents proving each root's exact blocking
sequence retrospectively. Tests cover three-second ACKs over multiple rotations,
over-budget publication and actual emitter stalls.

Reported publication latency
is an upper bound from just before event emission to the first verified diagnostic response.
It includes polling and diagnostic transport. It is not an exact publication
timestamp. Diagnostic traffic and CPU are part of the measured wrapper overhead.
The default poll interval is 250 ms at 10k or more roots, or 100k or more Pods;
smaller cases use 10 ms. Explicit overrides are recorded. Use the same cadence
for matched cases. A 250 ms poll adds that sampling resolution plus request
time to the observed latency bound. Reports include diagnostic request count,
response bytes and cumulative HTTP time. Wrapper CPU and TotalAlloc include
diagnostics; their exclusive cost is not separated from concurrent runtime work.

Measurement protocol 2 records post-GC heap outside client metrics scrape
requests. A separate sampler thread and the metrics HTTP reader share one gate.
HTTP holds it through the complete response read and close, then releases it
before the offline Python oracle. GC holds it through its complete request and
response. The emitter and publication ACK loop never acquire or wait on this
gate. Background runtime work, diagnostic captures and resulting GC pauses still
contribute to measured latency and RSS.

For a 300-second run with 30-second samples, the baseline is scheduled at 90
seconds, one sampling interval before the final 180-second window. Further
samples are scheduled every 30 seconds, including the final 300-second sample.
Each records scheduled time, gate-wait start, request start and completion,
relative to churn start. Acceptance requires the baseline to complete by 120
seconds, the final sample at or after 300 seconds, and actual completion-to-
completion coverage of at least 180 seconds. Short or delayed coverage fails;
it is not substituted with nominal scheduled time. The growth threshold remains
5%. The final sampler join is bounded and occurs after queue-drain polling.

This measures post-GC HeapAlloc outside client scrape requests, not isolated
Store heap. Other exporter work can remain reachable. RSS includes all work.
Post-delete GC still tests baseline + max(256 MiB, 10% peak) recovery. Protocol 1
reports, including the 17be01f1 failure whose final GC overlapped collection,
remain unchanged and do not become passes under this protocol. The production
command does not force GC; this remains an instrumented production composition.

Optional `--heap-profiles` is for diagnosis, not a matched acceptance run.
It requests `/gc?heap=1` from the test wrapper. The wrapper performs the same
single GC, reads MemStats, and then serializes `pprof.Lookup("heap")` without
another GC. The harness saves `heap-NNN.pprof` files and records each file's
size, SHA256, GC start/end, snapshot time and profile start/end timestamps.
Profile bytes are removed from the JSON report after saving. Capture and response
serialization add work and temporary allocations; diagnostic traffic/timing and
inclusive RSS expose that cost. The ordinary path does not collect profiles.

A heap profile is sampled Go allocation evidence, not an exact census of live
objects. Concurrent runtime work and Go heap-profile accounting affect what it
contains. Compare profiles from identified low/high samples with the exact
binary, for example `go tool pprof -sample_index=inuse_space <binary> <profile>`.
Short diagnostic runs cannot establish the 180-second heap or five-minute churn
gates, and cannot retrospectively turn earlier failures into passes.

Large scenario gates remain unverified until their full runs complete.

Modeled API runs complement kind tests: they make large object counts and
controlled LIST/WATCH failures practical, while kind exercises real API and
operator behavior. Upstream [kube-state-metrics benchmarks](https://github.com/kubernetes/kube-state-metrics/blob/0c043c480319b17a7b1325429c8651e6e204b490/pkg/app/server_test.go#L64-L147)
and [client-go indexer benchmarks](https://github.com/kubernetes/kubernetes/blob/121abd026c869bdb9b3896ac64190efcdb8d70cb/staging/src/k8s.io/client-go/tools/cache/thread_safe_store_test.go#L288-L315)
also use synthetic inputs for scoped measurements. An individual full run is a
scenario outcome, not statistical confidence in a performance comparison.
Matched microbenchmark repeats retain raw variation; none of these measurements
establish production API-server capacity.

## Historical evidence

The historical runtime at `63d74fc` passed 100k roots with one Pod at 7.072 GiB
sampled RSS and 127 seconds cold start. Its 37-38 second scrape timing included
the Python oracle. The 100k roots/10 Pods run failed above its 8 GiB cap before
complete output. That failure remains part of the evidence.

The exact old baseline at `01352db5` is retained separately. Its old configuration
and submitter semantics differ from this harness. A small baseline smoke or an
isolated Store benchmark is not a matched full-runtime performance comparison.
The separate k8s-workload-discovery project is suitable for an ownership-walk
comparison only; its full command has different publication semantics.

## Churn oracle correction

The earlier oracle required every churn scrape to remain valid even after an
accepted root resourceVersion update. The runtime deliberately invalidates the
old proof until it publishes the new verified result. The documented consistency
contract permits that interval. The corrected oracle models those intervals
explicitly; it does not allow invalid quiet roots or waive UID, phase or mapping
checks. Root-version-only churn leaves public phase and label values unchanged,
so the metric scrape alone cannot distinguish old and new versions. The exact
version and evidence checks in the diagnostic acknowledgment provide that proof.

Historical reports remain unchanged, including the earlier intermediate smoke
failure and its unsampled reason. Those reports used the older oracle and
version-only acknowledgment. Their latency values are not measurements of the
new verified acknowledgment. Deterministic adversarial tests cover quiet-root
invalidity, mixed validity, wrong UID, stale version, invalid proof and missing
recovery. No historical failure was rewritten into a pass.
