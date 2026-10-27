<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Simplification verification

This records verification of the component changes from baseline `31be33e7`.
The [ownership and measurement report](simplification.md) explains the design
changes and scoped microbenchmark results. Production source and module files
are recorded separately from documentation, tests and harness files in the local
evidence manifest.

The candidate result marker `exporter-simple-final-a8cc8ffca0d0` identifies the
frozen production/module fingerprint, not a Git commit. Its complete SHA-256 is
`a8cc8ffca0d03e13dc113ccf5d32ac0856b43b27494e74f026f25a6f27f063f1`.
The measured source was baseline `31be33e7` plus changes that were uncommitted
at measurement time. The manifest and binary build information identify that
snapshot. Documentation is outside this fingerprint.

## Automated checks

| Check | Result and scope |
| --- | --- |
| Baseline unit suite | Passed, including a separate fresh run. |
| Baseline envtest | Passed for Discovery, inventory and Runtime integration. |
| Candidate `make check-exporter` | Passed: vet, lint, module tidiness, unit tests, envtest, build and version check. |
| Pre-push repository `make check`, 2026-10-27 | Passed for implementation commit `46758ee1`: library, CLI, operator, exporter, WASM and release helper. Generators left the tree clean. Some unit tests used Go's cache. |
| Candidate module race tests | Passed. Some unchanged packages used Go's test cache. |
| Final controller/inventory race check | Passed after the definition-change fix. |
| Candidate envtest race tests | Passed for Discovery, inventory and Runtime integration. |
| Deployed operator and monitoring suites | Compiled by `check-exporter`; not executed as part of this comparison. |

Lifecycle tests exercise registration before workers start, metrics before
readiness, HTTP failure cancellation, unregistering before provider shutdown and
combined runtime/shutdown errors. Configuration tests retain strict unknown-key,
duplicate-key and document validation, explicit-empty precedence and Kubernetes
selector decoding.

The definition regressions distinguish metadata-only Karta updates from changes
to its specification or identity. Metadata-only changes preserve existing
publication proof. An in-flight calculation still checks the complete selected
origin, including resourceVersion, before committing.

## Independent review

The code review covered 65 changed files and approved the result after its
findings were closed. A separate three-file follow-up approved the final
definition-change fix with no remaining findings. The architecture review is
clear. Its source review covered component ownership, lock ordering, input
checks, failure lifetimes, immutable publications and root label scopes.

The invariant traceability audit passed after two identified coverage gaps were
closed with regression tests: unrelated workload kinds remain valid after a
Karta change, and exact Karta evidence expiry invalidates mapping/status while
fresh creation remains valid. Expiry covers selected cluster definitions, empty
cluster/catalog selection and empty cluster/generic ownership, with verified
recovery. The tests are in
[definition_lifecycle_test.go](../../../karta-metrics-exporter/pkg/controller/definition_lifecycle_test.go).

Cleanup merged four thin files while retaining package boundaries. Metadata
representation negotiation, permission-aware LIST-to-GET refresh, confirmed-absence
source selection and ordered label fallback remain. Factory layers and the
obsolete zero-GVK collector fallback were removed. Identity, freshness, ordering
and error checks remain documented in the
[guard table](simplification.md#guards-that-remain).

These reviews are source assessments. They do not replace test execution or the
separate runtime performance comparison.

## Runtime comparison

The original build passed the modeled-API runs for 10 roots with one Pod each,
1,000 roots with one Pod each under sustained updates, and 10,000 roots with 0, 1
and 10 Pods per root. Each saved result completed its stages with return code
zero and all measured gates true.

The candidate normal-command smoke run passed. Its matched runs for 1,000 roots
with one Pod under sustained updates and 10,000 roots with 0, 1 and 10 Pods per
root also completed with all measured gates true. Their
[runtime measurements](simplification.md#modeled-api-runtime) include the CPU
increase in the sustained-update run.

Both giant-root runs, one root with 100,000 Pods under sustained updates,
completed their correctness stages and passed every other measured gate. Both
failed `delete_all_heap`. Post-delete live heap was 649,147,912 bytes before and
655,640,192 bytes after, against initial baselines of 2,627,096 and 2,618,360 bytes.
The cause of this existing retention is not proven. Completion is not an
all-gates pass.

The independent performance audit checked matched fixture settings, configuration,
Go build information, harness and binary hashes, API counts and reported gates.
It found no material measured regression across these shapes while retaining the
observed increases and both heap failures. Each runtime profile has one run per
version; these measurements do not establish a statistical performance claim or
general production capacity.

## Evidence retention

The ignored `.context/exporter-simplification-evidence/` directory holds complete
raw logs and modeled-API JSON outputs. Timestamped snapshots preserve earlier
outputs, including failures, rather than replacing them with reruns. Each capture
records artifact checksums and a production/module source fingerprint. The
baseline label benchmark adapter and original Go overlay are retained for
reproduction. The final capture includes matched owner-traversal fixtures,
overlays and raw outputs, the completed runtime comparison and its independent
performance audit. A retained draft quality-gate file is explicitly marked as a
draft; the final acceptance receipt is separate. Repository documents
contain the compact conclusions above.
