<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Prometheus ingestion constraints for large workload exporters

## TLDR

A fast exporter is only part of the capacity problem. Prometheus buffers the entire decompressed scrape response, then parses and appends samples. Exporter-side HTTP streaming does not make this scrape path use bounded streaming-parser memory. A sample limit rejects the whole scrape and deliberately continues parsing; it does not cap network or parsing cost. [E1, E2]

Illustrative arithmetic, not a benchmark: 100,000 workloads times 10 samples each equals 1 million samples per scrape. At a 30-second interval that is about 33,333 samples/second per complete scraped copy. Assuming an average of 150 bytes per sample gives a 150 MB uncompressed body, before parser caches, TSDB memory, metadata, and temporary allocations. Measure actual sample multiplicity and bytes instead of sizing from workload count alone.

Scope: read-only inspection of `prometheus/prometheus` at verified HEAD `fd4847802c65256cb16e34f5e5c0e52a4b584cb6`. No network, other repository inspection, source edits, or benchmarks were run for this note.

## How it works

### Body, parser, and cache costs

The scrape loop obtains a pooled byte buffer using the previous response size, reads/decompresses the response into a `bytes.Buffer`, then passes its bytes to the parser. The parser advances sample by sample over this already resident byte slice. Gzip reduces transfer bytes, but Prometheus still retains decompressed bytes. Concurrent targets can have concurrent body buffers because each target has a scrape-loop goroutine. [E1, E3]

Each target also maintains a scrape cache: raw metric identity to final labels/storage reference, dropped identities, metadata, and current/previous series maps for staleness. A warm cache avoids rebuilding/relabeling every unchanged series. Deleted entries are flushed after successful scrapes; failed-scrape churn has a forced-flush heuristic to prevent indefinite cache growth. Stable series identities help, but they do not remove the repeated full body and sample append cost. Dropping samples through metric relabeling still requires receiving and parsing their exposition and can retain dropped-series identities. [E4]

### Limits and their actual boundaries

| Control | What the inspected implementation establishes |
| --- | --- |
| `sample_limit` | Counts accepted samples after metric relabeling. Exceeding it rolls back the scrape. Parsing continues to obtain the total scraped count. Zero means unlimited. |
| `body_size_limit` | Applied after decompression with `io.LimitReader`. Fails when bytes read reach the configured limit in this implementation. It bounds this body-read allocation, not total process memory. Zero means unlimited. |
| `scrape_timeout` | Must not exceed the scrape interval. The request gets a timeout context, which is canceled after response reading and before parsing/appending. Do not treat this as a demonstrated CPU/commit deadline. |
| Label limits | Label count/name/value limits apply after metric relabeling. Violations fail the entire scrape. |

These are failure boundaries, not partial-export or pagination mechanisms. Set body/sample budgets above the measured expected output with deliberate headroom, and test the failure behavior. [E1, E2, E5]

### Staleness and replica behavior

Series present in the previous scrape but absent from the current scrape receive stale markers. A failed scrape is handled as an empty scrape for staleness; parse/sample-limit failures roll back and then append an empty scrape. Stopping a target delays final stale markers for just over two scrape intervals to allow recreation. Explicit sample timestamps change this behavior: their staleness tracking defaults off unless enabled. [E6]

Consequently, an exporter must not alternate arbitrary subsets behind one target identity and call each response complete: omitted series can become stale. A relist or reconfiguration that temporarily exposes a partial snapshot can likewise create apparent disappearance. Distinct shard targets should each expose a complete view of their assigned partition. These are implications of the staleness implementation, not repository-provided deployment prescriptions.

Prometheus defaults `instance` to the target address and attaches target labels to samples. Thus two full exporter replicas scraped as distinct targets normally create two copies distinguished by `instance`, multiplying scrape work and time-series cardinality. Merely dropping that label is not a deduplication protocol; it can make independent target streams write the same series. Multiple Prometheus servers independently scraping the same exporter multiply requests and ingestion work across those servers. Downstream HA deduplication was not evaluated. [E7]

### Benchmark evidence

`BenchmarkScrapeLoopAppend` covers storage on/off, appender versions, metadata settings, formats, and cold/warm caches. Its explicit gauge fixture has 2,000 samples; its other fixture has 237 metric families. The comments recommend storage-enabled runs for acceptance. `BenchmarkScrapeLoopScrapeAndReport` includes pooled/unpooled response handling. These are useful test patterns, not 1M-sample capacity evidence. [E8]

The gzip benchmark defines up to 100,000 metrics, but its timed loop only calls `scrape`, which returns an HTTP response; it does not call `readResponse`, parse, or append. Its name must not be used as evidence of complete large-response ingestion performance. [E9]

## Relevance to Karta

General recommendations only; no Karta code was inspected:

- Define a sample and uncompressed-byte budget per scrape endpoint, then derive a workload budget from measured expansion per workload.
- Prefer limiting generated fields and labels before exposition. Prometheus metric relabeling saves accepted storage but does not eliminate response buffering and parsing.
- If partitioning is needed, use stable partitions with complete responses and distinct target identities. Include overlap, removal, restart, and reshard behavior in correctness tests.
- Benchmark exporter and Prometheus together at 1M+ samples, including real TSDB appends, cold caches, label churn, concurrent targets, and slow responses. Measure peak RSS and total scrape/append time, not only HTTP response time.
- Explicitly count exporter replicas and Prometheus replicas in the load model. A second complete exporter is not automatically a shard.

## Evidence

All links are pinned to the inspected commit.

- E1: [Decompression and body-size check](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L844-L907); [whole-body buffering, request timeout, and append sequence](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L1562-L1635).
- E2: [Continue parsing after sample limit](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L2160-L2173); [report sample-limit failure after loop](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L2056-L2062).
- E3: [Parser consumes a byte slice](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/model/textparse/interface.go#L28-L35); [target loop goroutines](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L575-L583).
- E4: [Per-target cache, churn cleanup, and map reuse](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L1001-L1134); [warm cache and relabel path](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L1890-L1924).
- E5: [Timeout constraint](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/docs/configuration/configuration.md#L323-L328); [body, sample, and label limit semantics](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/docs/configuration/configuration.md#L545-L571).
- E6: [Missing-series tracking](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L1163-L1174); [stale-marker writes](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L1753-L1768); [target-removal delay](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L1663-L1707); [explicit-timestamp behavior](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/docs/configuration/configuration.md#L371-L387).
- E7: [Default instance label](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/target.go#L700-L711); [target label attachment](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L650-L679).
- E8: [Append benchmark dimensions and fixtures](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape_test.go#L2603-L2672); [scrape/report allocation benchmark](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape_test.go#L2806-L2839).
- E9: [Gzip benchmark fixtures](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape_test.go#L7353-L7376); [timed loop only calls scrape](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape_test.go#L7404-L7427); [scrape returns client.Do response](https://github.com/prometheus/prometheus/blob/fd4847802c65256cb16e34f5e5c0e52a4b584cb6/scrape/scrape.go#L827-L841).

## Lessons

1. Count samples, bytes, churn, scrape interval, and replicas independently.
2. Treat body-size and sample-limit failures as availability events for the entire target's series.
3. Preserve stable label identities and complete partition snapshots across scrapes.
4. Validate ingestion with storage enabled and measure cold and warm behavior separately.

## What not to copy

- Do not claim a byte-slice parser provides bounded network streaming memory.
- Do not rely on `sample_limit` to stop parsing or retain the first N samples.
- Do not return a successful but incomplete population as an overload fallback.
- Do not equate distinct full replicas with sharding or assume label removal provides safe deduplication.
- Do not infer million-sample throughput from fixture size or benchmark names; no such result was measured here.
