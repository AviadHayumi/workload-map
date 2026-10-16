<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Gatekeeper: live definitions and dynamic registries

## TL;DR

- Gatekeeper separates desired watches from active watches, both keyed by exact GVK. Watch registration is idempotent and shared across consumers. Replacement can partially succeed; it is not a transaction.
- Template installation precedes watch creation. Watch removal precedes template removal. Retaining the installed template gives deletion enough information to remove the correct watch.
- Readiness is a startup barrier. It becomes permanently satisfied and can disregard invalid resources. It does not prove that every later definition update is usable.
- Event delivery snapshots recipients before releasing the registry lock. Other callbacks run under locks. A public SDK should specify callback reentrancy and avoid running consumer code under its registry lock.
- For Karta, keep watches, retries, status writes, and readiness policy in the consumer. Give the SDK exact-GVK lookup and explicit, atomic definition application with visible errors.

## How it works

The constraint-template controller owns two dynamic watch registrars and creates a subordinate constraint controller. The latter receives generic events through a controller-runtime channel. Its reader is the manager cache; its writer and status client are separate dependencies. Watch creation, controller registration, Kubernetes writes, and engine ingestion are separate operations. See controller construction (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/controller/constrainttemplate/constrainttemplate_controller.go#L141) and reader/writer separation (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/controller/constraint/constraint_controller.go#L194).

A registrar records desired GVKs before asking the watch manager to realize them. The manager keeps a separate active-watch map. A first subscriber acquires an informer with `BlockUntilSynced(false)`. Additional subscribers share it and asynchronously replay existing cached objects. Removing the last subscriber cancels replays, waits for them to exit, then removes the informer. Waiting matters because a concurrent replay list could otherwise recreate the informer. `ReplaceWatch` replaces desired state, removes obsolete watches, attempts additions, and aggregates failures by GVK and operation. Successful operations remain applied when another operation fails. See registration (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/registrar.go#L223), watch realization (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/manager.go#L142), and replay (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/replay.go#L123).

Template reconciliation first creates and validates a proposed CRD through the constraint framework. It then adds the template to the engine, observes it for readiness, creates or updates the Kubernetes CRD where assigned, and registers the constraint watches. Later failures do not roll back earlier successful steps. Deletion recovers the installed template from the engine, removes its watches, cancels readiness expectations, and finally removes the template. This is ordered reconciliation, not an atomic replacement across the engine, API server, and watch manager. The constraint-framework implementation is an external Go dependency and is absent from this checkout, so its internal replacement guarantees were not verified. See template reconciliation (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/controller/constrainttemplate/constrainttemplate_controller.go#L325), update and deletion ordering (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/controller/constrainttemplate/constrainttemplate_controller.go#L510), and dependency pin (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/go.mod#L18).

A constraint event carries GVK through a packed reconcile key. The reconciler checks whether its registrar still watches that GVK before reading. A missing object, missing resource mapping, or removed watch becomes deletion; other reader errors are returned for retry. The watch guard prevents a cache read from accidentally creating a removed watch. Constraints enter readiness only after successful engine ingestion. See constraint reconciliation (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/controller/constraint/constraint_controller.go#L300) and ingestion (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/controller/constraint/constraint_controller.go#L605).

Readiness lists initial expectations through an injected lister. The manager adapter supplies its API reader. Observations may precede expectations; `ExpectationsDone` closes the initial set. Once satisfied, the tracker stays satisfied. Invalid template or constraint ingestion calls `TryCancel`; the default readiness retry allowance is zero. Data tracking treats an unregistered GVK as terminal, while constraint-list tracking retries all errors until success or cancellation. These choices allow startup readiness despite some invalid resources. They are application policy. See setup (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/readiness/setup.go#L45), expectation lifecycle (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/readiness/object_tracker.go#L37), and list policies (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/readiness/ready_tracker.go#L941).

## Relevance to Karta

The useful boundary is between applying definitions and obtaining them. A consumer can watch Karta definitions with controller-runtime, client-go, or another mechanism, then submit definition snapshots to a controller-runtime-independent SDK. A reader dependency alone does not establish that its cache is synchronized, nor that the current definitions were successfully applied. Gatekeeper's separate reader, watcher, and readiness objects make those distinctions concrete.

Exact GVK should be part of Karta's lookup contract. Gatekeeper's watch maps and event identity use full GVK. Its template helper fixes the constraint group and version, while some readiness dispatch deliberately ignores version. Its event packer even substitutes `v1` for an empty version. Those domain-specific shortcuts do not justify guessing a version for arbitrary workload definitions. See template GVK construction (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/controller/constrainttemplate/constrainttemplate_controller.go#L738), event identity (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/manager.go#L318), and event packing (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/util/pack.go#L41).

Callback safety needs an explicit contract. `distributeEvent` copies subscriber channels under the watch lock and sends after unlocking. In contrast, `Registrar.IfWatching` calls its callback under a read lock, and `Set.Replace` calls callbacks under a write lock. Inference from the locking: a callback that requests a conflicting lock can deadlock. Slow channels can also stall event delivery after unlocking. Replay requests can block before the manager starts. These are reasons to keep public SDK notification callbacks outside internal locks and to make delivery ownership explicit. See delivery (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/manager.go#L375), read-locked callback (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/registrar.go#L267), and write-locked callbacks (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/set.go#L165).

## Evidence

Origin and HEAD were checked locally:

- Origin: `https://github.com/open-policy-agent/gatekeeper.git`.
- HEAD: `16b4c2ecdc34650530253d6404a429c6733f6e08`.
- Source was read only in the assigned reference checkout. No network or dependency-cache inspection was used. The checkout had an untracked `.greenlight-worktree.json` file.
- Read `pkg/watch/manager_test.go`: tests cover idempotent add/remove, subscriber replay, retry, asynchronous cancellation, and replacement with overlapping subscribers. Immutable source (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/manager_test.go#L321).
- Read `pkg/watch/set_test.go`: concurrent replacement tests preserve callback ordering, but do not establish that callbacks may reenter the same set. Immutable source (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/watch/set_test.go#L18).
- Read `pkg/readiness/ready_tracker.go`: a permanent satisfied flag makes readiness a startup check. Immutable source (https://github.com/open-policy-agent/gatekeeper/blob/16b4c2ecdc34650530253d6404a429c6733f6e08/pkg/readiness/ready_tracker.go#L251).

These are source observations. Tests were inspected, not executed.

## Lessons for Karta

1. Accept exact GVKs at the SDK boundary. Reject incomplete identities instead of defaulting versions. Keep resource lookup errors distinguishable from an absent definition.
2. Stage validation and compilation before replacing the published registry. Publish one immutable snapshot so a reader sees a complete old or new registry. This is a Karta recommendation; Gatekeeper's watch replacement does not provide it.
3. Retain applied identity and revision separately from the latest observed definition. A failed update should return a precise error and preserve the prior applied snapshot if that is the chosen contract. Make that retention visible to callers.
4. Apply deletion using the installed record's complete identity. An update that changes target GVK must remove the old index entry and install the new one in the same publication. Define how stale events and UID replacement are rejected.
5. Let the consumer own initial list/watch synchronization, reconciliation retries, event ordering, readiness endpoints, and Kubernetes status writes. SDK results should let it distinguish initial sync incomplete, invalid definition, reader failure, and successfully applied revision.
6. Run notification callbacks after publication and unlocking. Give each notification the applied revision. Define whether in-flight readers may finish against an older immutable snapshot after deletion.

## What NOT to copy

- Do not put a controller-runtime manager, informer cache, generic event channel, or packed reconcile key in the core SDK API. Gatekeeper's small interfaces still import controller-runtime types.
- Do not describe desired-watch replacement as transactional. It changes intent before realizing changes and returns aggregate partial failures.
- Do not use startup readiness as continuous proof of fresh definitions. Do not silently discard invalid definitions merely to make readiness pass.
- Do not infer that a reader error means deletion. Gatekeeper limits this to explicit missing-object or mapping cases within its controller ownership model.
- Do not copy callbacks under registry locks, guessed versions, or the assumption that a cache read cannot start an informer.
