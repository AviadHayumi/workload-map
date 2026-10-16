<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# OpenTelemetry Go provider boundaries

Source: verified local origin `https://github.com/open-telemetry/opentelemetry-go.git`,
HEAD `5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6`. Research used the pinned
checkout only. Citations below point to that commit; no network was used.

## TL;DR

- Accept a provider at the consumer boundary. OpenTelemetry recommends injected
  `trace.TracerProvider`; the consumer interface exposes tracer creation, while
  SDK construction, processor management, and shutdown live on the concrete SDK
  provider. trace/provider.go:8-58 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/provider.go#L8-L58) trace/doc.go:24-45 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/doc.go#L24-L45) sdk/trace/provider.go:282-338 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L282-L338)
- Existing tracer handles read the provider's current processor list. Writers
  serialize changes, copy the list, and publish it atomically. This is a useful
  starting point for Karta definition snapshots. sdk/trace/provider.go:213-235 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L213-L235) sdk/trace/tracer.go:66-72 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/tracer.go#L66-L72)
- Dynamic configuration is limited: trace sampler, resource, limits, and ID
  generator fields are immutable after construction; metric readers cannot be
  added later. Do not describe this as a fully dynamic configuration registry.
  sdk/trace/provider.go:70-88 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L70-L88) sdk/metric/provider.go:35-57 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/metric/provider.go#L35-L57)
- Global provider initialization forwards existing placeholder tracers once.
  Later global replacement does not retarget those old handles again. An
  explicitly injected registry is a better basis for repeated Karta definition
  updates. internal/global/state.go:71-98 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/state.go#L71-L98) internal/global/trace.go:52-159 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/trace.go#L52-L159)

## How it works

The public trace API defines `TracerProvider.Tracer`, including concurrent-call
safety and propagation of supported provider configuration changes to existing
tracers. It does not expose processor registration, flush, or shutdown. The API
and SDK have separate modules; the trace module does not require the trace SDK,
while the SDK requires the trace API. trace/provider.go:8-58 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/provider.go#L8-L58) trace/go.mod:1-20 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/go.mod#L1-L20) sdk/go.mod:1-25 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/go.mod#L1-L25)

The SDK constructor applies defaults and options, creates the tracer cache, and
publishes an initial processor list. `Tracer` caches handles by instrumentation
scope under a mutex. Each SDK tracer retains its provider, so an existing tracer
can observe subsequent processor registrations. sdk/trace/provider.go:96-138 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L96-L138) sdk/trace/provider.go:140-210 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L140-L210) sdk/trace/tracer.go:66-72 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/tracer.go#L66-L72)

`RegisterSpanProcessor` checks shutdown before and after acquiring the mutex,
appends to a newly allocated list, and atomically stores it.
`UnregisterSpanProcessor` copies the list, finds the matching processor, shuts
it down once, and publishes the shortened list. There is no keyed upsert or
whole-registry replacement API here. sdk/trace/provider.go:213-235 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L213-L235) sdk/trace/provider.go:237-280 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L237-L280)

A span's start and end each load the current list independently. Inference: a
concurrent registration can therefore receive the end of a span without having
received its start. Snapshot publication gives a consistent list for one load,
not a transaction across an operation's lifetime. sdk/trace/tracer.go:66-72 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/tracer.go#L66-L72) sdk/trace/span.go:468-475 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/span.go#L468-L475)

The concrete SDK exposes `ForceFlush(ctx)` and `Shutdown(ctx)`. Shutdown marks
the provider stopped under a mutex, shuts down registered processors, and joins
errors; new tracer requests after shutdown return a no-op tracer. Registered
processors are owned for shutdown, so passing a dependency does not by itself
mean the provider merely borrows it. sdk/trace/provider.go:282-338 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L282-L338) sdk/trace/provider.go:140-210 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L140-L210) sdk/trace/span_processor.go:31-39 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/span_processor.go#L31-L39)

Global setup is separate from constructor setup. `otel.SetTracerProvider`
invokes a one-time delegation hook and stores the new global provider. The
placeholder locks tracer creation while binding existing tracers. Normally,
spans started before SDK initialization remain non-recording; a test covers the
transition of previously obtained tracers without changing those earlier spans.
The implementation also has a separate auto-instrumentation path. internal/global/state.go:71-98 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/state.go#L71-L98)
internal/global/trace.go:52-159 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/trace.go#L52-L159) internal/global/trace_test.go:45-97 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/trace_test.go#L45-L97)

## Relevance to Karta

These are design transfers, not claims that OpenTelemetry implements Kubernetes
clients, informer caches, watches, or definition reconciliation.

| Karta requirement | Transfer from the inspected design |
| --- | --- |
| Keep the SDK independent of controller-runtime | Put the definition-reading interface in the core library; put watch and manager integration in an adapter. The model is the consumer API / concrete SDK boundary. trace/provider.go:8-58 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/provider.go#L8-L58) trace/go.mod:1-20 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/go.mod#L1-L20) sdk/go.mod:1-25 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/go.mod#L1-L25) |
| Accept caller clients and cache | Inject dependencies explicitly and document who starts and stops each one. Unlike OTel-owned processors, caller clients/cache should remain caller-owned unless ownership is explicitly transferred. trace/doc.go:24-45 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/doc.go#L24-L45) sdk/trace/provider.go:282-338 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L282-L338) |
| Externally watched live definitions | Let an external watcher write to one stable registry instance; let SDK operations read immutable snapshots from it. This extends OTel's processor snapshot technique rather than relying on its one-time global delegation. sdk/trace/provider.go:213-235 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L213-L235) internal/global/state.go:71-98 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/state.go#L71-L98) |
| Keep one operation coherent during updates | Acquire one registry snapshot per discovery or mutation operation. OTel's independent start/end loads illustrate why atomic publication alone does not guarantee operation-wide consistency. sdk/trace/tracer.go:66-72 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/tracer.go#L66-L72) sdk/trace/span.go:468-475 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/span.go#L468-L475) |

## Evidence

- `trace.TracerProvider`: a small consumer contract with documented identity,
  concurrent lookup, and configuration propagation semantics. trace/provider.go:8-58 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/provider.go#L8-L58)
- `sdk/trace.TracerProvider`: mutex-protected tracer cache and processor updates,
  atomic processor snapshot reads, immutable remaining configuration.
  sdk/trace/provider.go:70-88 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L70-L88) sdk/trace/provider.go:140-210 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L140-L210) sdk/trace/provider.go:213-235 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L213-L235) sdk/trace/provider.go:336-338 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L336-L338)
- `TestRegisterNilSpanProcessorPreservesProcessors` checks registration order,
  start/end delivery, flush, and shutdown. `TestRegisterAfterShutdown...` checks
  that registration after shutdown has no effect. These tests were read, not
  executed. sdk/trace/provider_test.go:178-203 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider_test.go#L178-L203) sdk/trace/provider_test.go:346-370 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider_test.go#L346-L370)
- `TestTraceProviderDelegation` checks behavior on both sides of first global
  initialization; the `sync.Once` in global state establishes the one-time limit.
  internal/global/trace_test.go:45-97 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/trace_test.go#L45-L97) internal/global/state.go:71-98 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/state.go#L71-L98)
- `sdk/metric.NewMeterProvider` explicitly disallows adding readers after
  construction, despite also implementing a narrow API provider interface.
  sdk/metric/provider.go:35-57 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/metric/provider.go#L35-L57) metric/meter.go:12-37 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/metric/meter.go#L12-L37)

## Lessons for Karta

1. Separate read capability, update capability, and lifecycle ownership. The
   consumer should not need a manager or shutdown method merely to resolve a
   definition. This follows the API / SDK split. trace/provider.go:8-58 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace/provider.go#L8-L58) sdk/trace/provider.go:282-338 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L282-L338)
2. Specify snapshot ownership and freshness in the registry contract. Copy
   before publication, keep published contents immutable, and define whether
   an operation uses one version throughout. OTel demonstrates safe list
   publication but not operation-wide version pinning. sdk/trace/provider.go:213-235 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L213-L235) sdk/trace/tracer.go:66-72 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/tracer.go#L66-L72)
   sdk/trace/span.go:468-475 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/span.go#L468-L475)
3. Define readiness, invalid-update behavior, deletion, and last-good state
   explicitly for Karta. These are additional registry semantics; constructor
   options and processor append/remove are insufficient evidence for them.
   sdk/trace/provider.go:96-138 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L96-L138) sdk/trace/provider.go:213-235 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L213-L235) sdk/trace/provider.go:237-280 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L237-L280)
4. Test updates racing with reads and shutdown, plus updates observed through
   already-created SDK handles. OTel's lifecycle guards and first-initialization
   tests provide concrete cases to adapt. sdk/trace/provider.go:213-235 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L213-L235) sdk/trace/provider.go:140-210 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L140-L210) internal/global/trace_test.go:45-97 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/trace_test.go#L45-L97)

## What NOT to copy

- Do not use the process-global setter as a repeatable refresh bus. Delegation
  of previously obtained placeholder handles happens only once. internal/global/state.go:71-98 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/internal/global/state.go#L71-L98)
- Do not imply all provider configuration supports runtime changes. The trace
  fields and metric reader set have explicit construction-time limits.
  sdk/trace/provider.go:70-88 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L70-L88) sdk/metric/provider.go:35-57 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/metric/provider.go#L35-L57)
- Do not copy cleanup while holding the registry update mutex. OTel unregisters
  a processor by calling `Shutdown(context.Background())` under that mutex,
  before publishing removal. For Karta, publish the new immutable definition
  state without running external cleanup under the registry lock. sdk/trace/provider.go:237-280 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L237-L280)
- Do not silently turn an unavailable definition provider into successful empty
  discovery just because telemetry can use no-op providers. Karta should define
  an explicit unavailable or not-ready result. The no-op behavior is specific
  to the inspected telemetry API. trace.go:19-35 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/trace.go#L19-L35) sdk/trace/provider.go:140-210 (https://github.com/open-telemetry/opentelemetry-go/blob/5d3fed7f57c15e55167bbf9b1e0d6e5bb307dfa6/sdk/trace/provider.go#L140-L210)
