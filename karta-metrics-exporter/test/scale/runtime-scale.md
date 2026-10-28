<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Exporter executable scale checks

The harness launches the supplied exporter binary against a loopback HTTP fixture.
The fixture serves Kubernetes discovery, paginated LISTs, GETs and streaming
WATCH events. The binary runs its own cache, discovery, SDK evaluation, label
resolution, publication and OTel collection. No Store records are injected.
This models API responses. It does not measure etcd or production API capacity.

Each root is a Job. Each Job owns a ReplicaSet that owns its Pods. Intermediate
ReplicaSets are available through metadata GET, paginated metadata LIST and
streaming metadata WATCH. Traversal-only ReplicaSets must not receive a full
watch. The report records active and peak watches per resource/representation.
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
scrape may temporarily report invalid rows. Its UID, coordinates, labels and
phase values must still match. Pod mapping and workload status validity are
independent. Invalid status requires matching workload health with reason
`api_unavailable`. Creation may be omitted while status is invalid. Quiet roots
remain fully valid. The final scrape requires all exact rows and healthy results.
A changed root that fails to recover within 5 seconds (10 seconds for dense
cases) fails the run. Candidate runs send no proof-expiry settings.
`--legacy-proof-config` enables `maxAge: 120s` and optional `--refresh-seconds`
only when comparing an older baseline that implements those settings. `--watch-faults` injects Pod WATCH EOF, Job WATCH 410 and, when present,
ReplicaSet metadata WATCH 410. It checks reconnection and a fresh visible Pod
update. A second Pod WATCH 410 forces a recovery LIST. The fixture mutates a Pod
during that paginated LIST and checks convergence to the later watch event.

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

## Bounded change scenarios

The default sustained load changes root resource versions after establishing
stable status values. `--churn-target pod` instead changes a visible Pod label
on one Pod per hot root. Success still requires the exact current label in the
independent diagnostic ACK. A continuity scrape may contain the old or new label
only when that exact Pod update overlaps its observation interval. Quiet rows and
the final scrape require current values. Root version checks remain enabled.

`--pod-lifecycle-burst N` deletes N Pods while leaving their roots present, checks
that their mappings disappear, then recreates them with new UIDs. The oracle
requires every root to remain and rejects each obsolete Pod UID.

`--ancestor-burst N` changes N intermediate ReplicaSet owner-reference UIDs to
mismatched values. The oracle requires retained Pod mappings to become invalid
with `unresolved_owner`, preserving exact identities and values. It then restores
the references and requires healthy output. This exercises intermediate event
routing and recovery. It does not measure successful reparenting between roots.
Use the same scenario for baseline and candidate. An older polling implementation
may require its normal refresh interval; record that latency without shortening
its refresh setting. The burst limits are 10,000 Pods and 1,000 ancestors.

These options add bounded cases, not a new claim about full-scale throughput.
Keep measured pairs serial and preserve failed runs. Background operators and
other host activity must be recorded beside results.

## Measurements and gates

`result.json` includes binary/harness hashes, configuration, request counts by
resource/operation/representation, WATCH delivery, response sizes, sampled RSS
and CPU time, and per-stage convergence. Fixture `request_seconds` records count,
sum and maximum server-side duration per non-watch API request category. These
durations include fixture encoding and socket writes, not client round-trip time
or production API latency. Long-lived watch durations are excluded. Pure HTTP request/transfer time appears
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

## Overall runner guard

Full acceptance runs prospectively use `--max-seconds 1800`. This is an
aggregate anti-hang guard for the complete lifecycle, separate from each
stage and performance deadline above. Short smoke runs retain their explicit
shorter guard. The Python command default is unchanged.

| Full profile | Overall runner guard |
| --- | --- |
| 100k roots, 0 or 1 Pod | 1,800 s |
| 100k roots, 10 Pods, default or tuned API budget | 1,800 s |
| One root, 100k Pods | 1,800 s |

The previous 1,200-second run remains a failure in the
[full-scale evidence](results/full-scale/README.md). Its completed sequential
stages consumed 802.173 seconds. Churn, acknowledgment, drain and sampler join
consumed another 321.991 seconds; polling and synchronization brought final
deletion to process time 1,184.546 seconds. Allowing that last stage its existing
180-second deadline and the final five-second GC request requires at least
1,369.546 seconds for that observed sequence, before shutdown/report overhead.
The prospective 1,800-second guard provides margin without changing cold-start,
per-stage, scrape, oracle, rate, latency, RSS, queue-drain or heap limits.
A fresh full run must still prove cleanup after the same long churn workload.
The earlier failure is not reclassified, and the arithmetic is not a worst-case
runtime guarantee.

## Dense default and tuned API profiles

The 100k-root/10-Pod fixture contains 100,000 Jobs, 100,000 intermediate
ReplicaSets and 1,000,000 Pods in one namespace. The candidate uses full Job/Pod
sources and a metadata ReplicaSet source. There is no periodic proof-verification
LIST floor. Count actual LIST pages, watch reconnects, GETs and metadata/full
representations from the report.

Run the default dense profile at `--api-qps 20 --api-burst 40` first. Retain its
outcome, including failures. A separately named tuned profile may use
`--api-qps 100 --api-burst 200`; it does not reclassify the default profile.
Keep the same API budget, worker count, toolchain and harness in each baseline /
candidate pair. Only the baseline receives `--legacy-proof-config`.

Both dense profiles retain RSS <=16 GiB and GOMEMLIMIT=12 GiB. Cold start stays
<=300 seconds, HTTP <=25 seconds and oracle <=120 seconds. Each requires 30,000
verified events over 300 seconds at 100 events/s, publication p99 <=10 seconds,
drain <=60 seconds, the one-second scheduler lateness bound and existing heap
and deletion gates. Other profiles retain their declared budgets and limits.
Historical reports retain their original proof-expiry configuration and semantics.

## Giant-root workload

The giant-root case is one Job with 100,000 Pods. It emits 0.05 root updates/s,
one every 20 seconds, for 300 seconds and exactly 15 verified acknowledgments.
This measures root status evaluation and fanout to 100,000 independent Pod mappings.
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
roots. They read independent workload and Pod results plus source health from
one Capture. A Pod result may exist before its workload result. An ACK requires
the current workload and every expected Pod mapping to be valid at that capture;
it does not require one atomic workload/Pod revision. The response includes the
selected root and Pod records. The independent harness verifies the exact root version, UID,
phase and current Pod identities, coordinates and labels before acknowledging.
These polls do not format metrics, but Capture scans the result maps and
selected Pod records add diagnostic CPU and response size, especially for the
giant-root case. That overhead is included in reported latency.

Protocol 4 retains protocol 3's independent paced emitter and sizes its hot-root set as
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

## Churn oracle versions

Earlier runtimes invalidated whole-workload proofs after accepted root updates.
Their oracle allowed that interval only for roots with unacknowledged events.
Protocol 4 retains the bounded interval for matched baseline runs, but permits
independent Pod and workload validity. All nine phase rows from one workload
result must agree on validity. An invalid Pod mapping need not invalidate status.
Health rows must account for invalid results. Quiet roots must remain valid.

Root-version-only churn leaves public phase and label values unchanged. Metric
scrapes alone cannot identify the accepted version. Diagnostic ACKs require the
exact workload version, current identities, values and healthy results. Independent
Pod rows may come from another reconciliation; they need not share that version.

Historical reports retain their original oracle and configuration, including
failed runs. Tests reject quiet-root invalidity, inconsistent workload phase
validity, stale UIDs, stale workload versions, invalid results and missing recovery.
Small matched comparisons do not reclassify earlier full-scale outcomes.
