<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Discovery

For a Pod owned by a Job owned by a JobSet, discovery resolves the JobSet UID.
It follows controller-owner references before selecting a Karta. A definition
on the Job does not make the Job the root.

The controller supplies compact observed facts and a cache-only reader:

```go
d := discovery.New(reader, inventory)
d.CoverageChanged(discovery.CoverageChange{
    Key: podStream, Run: 1, Ready: false, FullObject: true,
})
// The controller accepts initial objects with Upsert before declaring readiness.
d.Upsert(discovery.ObservedObject{
    Ref: podRef, Owners: pod.OwnerReferences, WatchKey: podStream, Run: 1,
})
d.CoverageChanged(discovery.CoverageChange{
    Key: podStream, Run: 1, Ready: true, FullObject: true,
})

workload, err := d.Resolve(ctx, podRef)
if err != nil {
    // A selection error can preserve a verified Root and State.
    // It does not confirm that no definition exists.
    return err
}
if karta := workload.KartaWorkload(); karta != nil {
    batch := d.NewEvaluationBatch()
    status, statusErr := karta.Status(ctx, batch)
    mapping, mappingErr := karta.PodMapping(ctx, pod, batch)
    // Check each successful result's State independently before publication.
    _, _, _, _ = status, statusErr, mapping, mappingErr
}
```

The JobSet Karta reads declared instance names from the full JobSet object.
Its selectors read the full Pod. A Pod labeled with replicated-job name
`workers` and job index `2` maps to component `replicatedjob`, instance
`workers`, and group `2`. The instance must be declared by the JobSet.

## Ownership and coverage

`Root()` retains the API representation used by ownership. `Selection().GVK`
identifies the representation needed for Karta evaluation. Both must describe
the same UID. `Selection()` returns a caller-owned definition snapshot.

`Upsert` accepts only the registered stream generation. `CoverageChanged` must
register a generation before its initial objects arrive. Readiness means that
both the replacement store and initial callbacks have synchronized. At that
point, observations absent from the replacement generation retire. Other
representations of the same UID survive.

`Delete` removes one UID, not its descendants. A delayed delete cannot remove
a replacement UID. Pods can be intermediate owners. `Walk` visits the observed
root subtree, including such Pods, and runs callbacks outside the ownership lock.
It does not prove complete Pod membership.

Only an exact ignored-owner GVK stops traversal. Cycles, multiple controller
owners, missing owners and wrong UIDs fail resolution. A missing unsynchronized
stream returns `ErrPending`. A reported permission or availability failure is
not pending. `CoverageError.Request` identifies needed coverage. Missing coverage
also appears in event `ChangeSet` results.

The reader must return accepted full bodies for evaluation. UID, resourceVersion
and GVK must match. A cache ahead of its callback returns `ErrStale`. For an
unobserved owner, the reader returns `ErrPending` while coverage synchronizes,
or Kubernetes NotFound once a ready store confirms absence. The controller
owns watch-run checks, deletion guards and all fresh API reads.

## Evaluation and acceptance

With confirmed definition absence, `Resolve` returns a generic workload and
`KartaWorkload()` is nil. The controller supplies generic metric coordinates and
applies bare-Pod opt-in and output filters. Ownership failures return no workload.
Selection failures return an error with the verified generic root and state.
That partial result supports independent creation metadata, not generic fallback.

Status and mapping use the actual Karta SDK. No status definition gives
`Defined=false`. Successful rules without a match give `Undefined`. An expression
failure is an error. Mapping selects the nearest configured replica selector in
the logical Karta hierarchy. Errors never fall back to an ancestor.

One evaluation batch shares the root body, structure summary, instance extraction
and status across sibling Pods. A new verification round requires a new batch,
even when resourceVersions are unchanged. Batch caches live only as long as the
caller retains the batch. Cancellation does not poison later preparation.

Ownership resolution walks the in-memory UID graph without API calls. Each result
captures the object, edge, coverage and root-definition revisions used by the walk.
A state remains current while those inputs are unchanged. An unrelated update does
not invalidate it. A subtree walk also captures the root membership revision.

`IsCurrent` checks only locally accepted discovery inputs. Zero tokens and tokens
from another discovery instance are invalid. The controller must check tokens,
selection provenance, scope and fresh verification under the same publication
lock that installs output. Discovery does not call dependencies while holding its
ownership lock. Scraping does not call discovery.
