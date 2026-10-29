// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// controllerState serializes source events, ownership changes and publication.
// Methods return values or owned snapshots. Mutable membership maps stay here.
// Cache access takes Reader.mu after mu; discovery and evaluation run unlocked.
type controllerState struct {
	mu     sync.Mutex
	config Config
	deps   Dependencies
	reader *Reader

	inputQueue   workqueue.TypedRateLimitingInterface[inputKey]
	rootQueue    workqueue.TypedRateLimitingInterface[rootKey]
	controlQueue workqueue.TypedRateLimitingInterface[string]

	watches       map[watchKey]*resourceWatch
	replacements  map[watchKey]*resourceWatch
	watchFailures map[discovery.WatchRequest]error
	scopes        map[string]bool

	nextRoot     uint64
	roots        map[types.UID]*rootState
	podRoots     map[types.UID]types.UID
	ancestorPods map[types.UID]bool
	candidates   map[inputKey]types.UID

	selections        map[schema.GroupKind]selectedDefinition
	definitionError   error
	definitionVersion uint64
	namespaceSelector labels.Selector
	objectSelector    labels.Selector
	podSelector       labels.Selector

	ctx     context.Context
	started bool
	wg      sync.WaitGroup
}

type rootState struct {
	generation uint64
	ref        discovery.ObjectRef
	resolution discovery.OwnerResolution
	pods       map[types.UID]inputKey
	candidates map[inputKey]candidateState
}

type candidateState struct {
	sources []store.SourceKey
	err     error
}

type rootSnapshot struct {
	ref        discovery.ObjectRef
	resolution discovery.OwnerResolution
	generation uint64
	sources    []store.SourceKey
	err        error
}

func (r *controllerState) lookupDefinition(gvk schema.GroupVersionKind) (selectedDefinition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	selected := r.selections[gvk.GroupKind()]
	selected.version = r.definitionVersion
	if r.definitionError != nil {
		return selected, r.definitionError
	}
	return selected, selected.err
}

func (r *controllerState) namespaceAdmitted(namespace string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config.NamespaceSelector == nil || namespace == "" || r.scopes[namespace]
}

func (r *controllerState) removePod(key inputKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if root := r.roots[r.podRoots[key.UID]]; root != nil {
		delete(root.pods, key.UID)
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	delete(r.podRoots, key.UID)
	r.deps.Publications.RemovePod(key.UID)
}

func (r *controllerState) failPod(key inputKey, path []discovery.ObjectRef, cause error, root ...discovery.ObjectRef) error {
	sources := r.sourcesFor(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failPodLocked(key, sources, cause, root...)
}

func (r *controllerState) failPodEvaluation(key inputKey, path []discovery.ObjectRef, cause error, root discovery.ObjectRef, selection selectedDefinition) error {
	sources := r.sourcesFor(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.currentSelectionLocked(root, selection) || !r.currentPathLocked(path) {
		return discovery.ErrStale
	}
	if _, current := r.reader.Cached(root); !current || !r.isRootLocked(root) {
		return discovery.ErrStale
	}
	return r.failPodLocked(key, sources, cause, root)
}

func (r *controllerState) failCandidate(key inputKey, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if root := r.roots[r.candidates[key]]; root != nil {
		candidate := root.candidates[key]
		candidate.err = err
		root.candidates[key] = candidate
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	r.deps.Publications.SetDiscoveryFailure(storedRef(key.ref()), failureReason(err))
}

func (r *controllerState) failWorkload(uid types.UID, ref discovery.ObjectRef, cause error) error {
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	sources := r.sourcesFor([]discovery.ObjectRef{ref})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roots[uid] == nil {
		return nil
	}
	next := store.WorkloadResult{Record: store.WorkloadRecord{UID: uid, Ref: storedRef(ref)}, Sources: sources, CreationSources: sources}
	if previous := r.deps.Publications.Workload(uid); previous != nil {
		next = *previous
		next.Sources = appendSources(previous.Sources, sources)
		next.CreationSources = appendSources(previous.CreationSources, sources)
	}
	next.Error = failureReason(cause)
	r.deps.Publications.UpsertWorkload(&next)
	return cause
}

func (r *controllerState) admitPod(key inputKey, workload *discovery.GenericWorkload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.currentPathLocked(workload.Path) {
		return discovery.ErrStale
	}
	if !r.isRootLocked(workload.Root) {
		return discovery.ErrUnresolvedOwner
	}
	// Register before reading the root body. Root-event fanout takes this same lock.
	previous := r.podRoots[key.UID]
	root := r.rootLocked(workload)
	if previous != root.ref.UID {
		if old := r.roots[previous]; old != nil {
			delete(old.pods, key.UID)
			r.rootQueue.Add(rootKey{UID: previous})
		}
		r.podRoots[key.UID] = root.ref.UID
		root.pods[key.UID] = key
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	return nil
}

func (r *controllerState) watchFailed(ref discovery.ObjectRef) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, failed := r.watchFailures[discovery.WatchRequest{GVK: ref.GVK, Namespace: ref.Namespace}]
	return failed
}

func (r *controllerState) publishPod(path []discovery.ObjectRef, rootRef discovery.ObjectRef, selection selectedDefinition, next store.PodResult, labelFailed bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.currentPathLocked(path) || !r.isRootLocked(rootRef) || !r.currentSelectionLocked(rootRef, selection) {
		return discovery.ErrStale
	}
	if current, ok := r.reader.Cached(rootRef); !ok || current.GetResourceVersion() != rootRef.ResourceVersion {
		return discovery.ErrStale
	}
	if labelFailed {
		next.Error = store.ReasonEvaluationFailed
		if previous := r.deps.Publications.Pod(next.Record.UID); previous != nil && previous.HasValue {
			next.Record = previous.Record
			next.Sources = appendSources(previous.Sources, next.Sources)
		}
	}
	r.deps.Publications.UpsertPod(&next)
	return nil
}

func (r *controllerState) rescan(namespace string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reader.mu.RLock()
	defer r.reader.mu.RUnlock()
	for _, entry := range r.reader.objects {
		if namespace == "" || entry.object.GetNamespace() == namespace {
			r.enqueueObjectLocked(entry.object)
		}
	}
	// Include previous memberships so a removed definition can withdraw its roots.
	for candidate := range r.candidates {
		if namespace == "" || candidate.Object.Namespace == namespace {
			r.rootQueue.Add(rootKey{Candidate: candidate})
		}
	}
	return nil
}

func (r *controllerState) trackCandidate(key inputKey) {
	r.mu.Lock()
	if _, known := r.candidates[key]; !known {
		r.candidates[key] = ""
	}
	r.mu.Unlock()
}

func (r *controllerState) replaceCandidate(key inputKey, object *unstructured.Unstructured) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if object != nil {
		replacement := inputFor(refOf(object))
		r.rootQueue.Add(rootKey{Candidate: replacement})
		if r.candidates[replacement] == "" {
			return discovery.ErrPending
		}
	}
	r.removeCandidateLocked(key)
	return nil
}

func (r *controllerState) admitCandidate(key inputKey, workload *discovery.GenericWorkload, selection, rootSelection selectedDefinition, sources []store.SourceKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.currentPathLocked(workload.Path) || !r.currentSelectionLocked(key.ref(), selection) || !r.currentSelectionLocked(workload.Root, rootSelection) {
		return discovery.ErrStale
	}
	previous := r.candidates[key]
	root := r.rootLocked(workload)
	if previous != root.ref.UID {
		if old := r.roots[previous]; old != nil {
			delete(old.candidates, key)
			r.rootQueue.Add(rootKey{UID: previous})
		}
	}
	r.candidates[key] = root.ref.UID
	root.candidates[key] = candidateState{sources: slices.Clone(sources)}
	r.rootQueue.Add(rootKey{UID: root.ref.UID})
	r.deps.Publications.RemoveDiscoveryFailure(key.UID)
	return nil
}

func (r *controllerState) removeCandidate(key inputKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeCandidateLocked(key)
}

func (r *controllerState) snapshotRoot(uid types.UID) (rootSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root := r.roots[uid]
	if root == nil {
		return rootSnapshot{}, false
	}
	if len(root.pods) == 0 && len(root.candidates) == 0 {
		delete(r.roots, uid)
		r.deps.Publications.RemoveWorkload(uid)
		return rootSnapshot{}, false
	}
	snapshot := rootSnapshot{ref: root.ref, resolution: root.resolution, generation: root.generation}
	for _, candidate := range root.candidates {
		snapshot.sources = appendSources(snapshot.sources, candidate.sources)
		snapshot.err = errors.Join(snapshot.err, candidate.err)
	}
	return snapshot, true
}

func (r *controllerState) withdrawWorkload(uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if root := r.roots[uid]; root != nil {
		r.enqueueMembersLocked(root)
	}
	r.deps.Publications.RemoveWorkload(uid)
}

func (r *controllerState) isRoot(ref discovery.ObjectRef) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.isRootLocked(ref)
}

func (r *controllerState) publishWorkload(uid types.UID, generation uint64, ref discovery.ObjectRef, selection selectedDefinition, next store.WorkloadResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.roots[uid]
	if state == nil || state.generation != generation || !r.currentPathLocked([]discovery.ObjectRef{ref}) || !r.isRootLocked(ref) {
		return discovery.ErrStale
	}
	if !r.currentSelectionLocked(ref, selection) {
		return discovery.ErrStale
	}
	if next.StatusError != "" {
		if previous := r.deps.Publications.Workload(uid); previous != nil && previous.HasValue {
			next.Record = previous.Record
			next.Sources = appendSources(previous.Sources, next.Sources)
		}
	}
	state.ref = ref
	r.deps.Publications.UpsertWorkload(&next)
	return nil
}

func (r *controllerState) start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("runtime already started")
	}
	r.started = true
	r.ctx = ctx
	r.mu.Unlock()
	return nil
}

func (r *controllerState) stopWatches() {
	r.mu.Lock()
	for _, w := range r.watches {
		w.cancel()
	}
	for _, w := range r.replacements {
		w.cancel()
	}
	r.mu.Unlock()
}
func (r *controllerState) enqueueRescanLocked(namespace string) {
	r.inputQueue.Add(inputKey{Object: objectKey{Namespace: namespace}, Rescan: true})
}

func (r *controllerState) enqueueObjectLocked(object *unstructured.Unstructured) {
	r.enqueuePodLocked(object)
	if r.isCandidateLocked(object.GroupVersionKind()) {
		r.rootQueue.Add(rootKey{Candidate: inputFor(refOf(object))})
	}
}

func (r *controllerState) enqueuePodLocked(object *unstructured.Unstructured) {
	if object == nil || object.GroupVersionKind() != podGVK {
		return
	}
	uid := object.GetUID()
	_, member := r.podRoots[uid]
	// Previously tracked Pods still need a worker to remove their result.
	if r.podSelector.Empty() || r.podSelector.Matches(labelsFor(object)) || member || r.deps.Publications.Pod(uid) != nil {
		r.inputQueue.Add(inputFor(refOf(object)))
	}
}

func (r *controllerState) isCandidateLocked(gvk schema.GroupVersionKind) bool {
	selection := r.selections[gvk.GroupKind()]
	return selection.Definition != nil && selection.GVK == gvk
}

func (r *controllerState) fanoutRootLocked(uid types.UID) {
	root := r.roots[uid]
	if root == nil {
		return
	}
	r.rootQueue.Add(rootKey{UID: uid})
	r.enqueueMembersLocked(root)
}

func (r *controllerState) enqueueMembersLocked(root *rootState) {
	for _, key := range root.pods {
		r.inputQueue.Add(key)
	}
	for key := range root.candidates {
		r.rootQueue.Add(rootKey{Candidate: key})
	}
}

func (r *controllerState) rootLocked(workload *discovery.GenericWorkload) *rootState {
	for _, ref := range workload.Path[1:] {
		if ref.GVK == podGVK {
			r.ancestorPods[ref.UID] = true
		}
	}
	root := r.roots[workload.Root.UID]
	if root == nil {
		r.nextRoot++
		root = &rootState{generation: r.nextRoot, ref: workload.Root, pods: map[types.UID]inputKey{}, candidates: map[inputKey]candidateState{}}
		r.roots[workload.Root.UID] = root
	}
	root.resolution = workload.Resolution
	if selection := r.selections[workload.Root.GVK.GroupKind()]; selection.Definition != nil {
		root.ref.GVK = selection.GVK
	} else if root.ref.GVK != workload.Root.GVK {
		current := root.ref
		current.ResourceVersion = ""
		if _, _, err := r.reader.read(current, false); err != nil {
			root.ref = workload.Root
			r.rootQueue.Add(rootKey{UID: root.ref.UID})
		}
	}
	return root
}

func (r *controllerState) removeCandidateLocked(key inputKey) {
	if root := r.roots[r.candidates[key]]; root != nil {
		delete(root.candidates, key)
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	delete(r.candidates, key)
	r.deps.Publications.RemoveDiscoveryFailure(key.UID)
}

func (r *controllerState) failPodLocked(key inputKey, sources []store.SourceKey, cause error, root ...discovery.ObjectRef) error {
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	next := store.PodResult{Record: store.PodRecord{UID: key.UID}, Input: storedRef(key.ref())}
	if len(root) > 0 {
		next.Record.Workload = storedRef(root[0])
		next.Record.WorkloadGVK = root[0].GVK
	}

	if previous := r.deps.Publications.Pod(key.UID); previous != nil {
		if previous.HasValue || len(root) == 0 {
			next = *previous
		}
		sources = appendSources(previous.Sources, sources)
	}
	next.Error = failureReason(cause)
	next.Sources = sources
	r.deps.Publications.UpsertPod(&next)
	if errors.Is(cause, discovery.ErrEvaluation) || errors.Is(cause, inventory.ErrInvalidDefinition) || errors.Is(cause, discovery.ErrInvalidDefinition) {
		return nil
	}
	return cause
}

func (r *controllerState) currentPathLocked(path []discovery.ObjectRef) bool {
	for _, ref := range path {
		if _, ok := r.reader.Cached(ref); !ok {
			return false
		}
	}
	return true
}

func (r *controllerState) currentSelectionLocked(ref discovery.ObjectRef, selection selectedDefinition) bool {
	if r.definitionVersion != selection.version {
		return false
	}
	current := r.selections[ref.GVK.GroupKind()]
	return current.GVK == selection.GVK && current.Origin == selection.Origin
}

func (r *controllerState) isRootLocked(ref discovery.ObjectRef) bool {
	r.reader.mu.RLock()
	defer r.reader.mu.RUnlock()
	selected := r.selections[ref.GVK.GroupKind()]
	if selected.Definition != nil && selected.err == nil {
		entry := r.reader.objects[objectKey{GVK: selected.GVK, Namespace: ref.Namespace, Name: ref.Name}]
		source := r.reader.sources[entry.source]
		if entry.object != nil && entry.object.GetUID() == ref.UID && source.full && source.synced && source.err == nil {
			owner, valid := discovery.ControllerOwner(entry.object)
			return valid && (owner.UID == "" || slices.Contains(r.config.IgnoredOwnerKinds, owner.GVK))
		}
	}
	for _, source := range r.reader.sources {
		if !source.synced || source.err != nil || source.gvk.GroupKind() != ref.GVK.GroupKind() {
			continue
		}
		entry := r.reader.objects[objectKey{GVK: source.gvk, Namespace: ref.Namespace, Name: ref.Name}]
		if entry.object == nil || entry.object.GetUID() != ref.UID {
			continue
		}
		owner, valid := discovery.ControllerOwner(entry.object)
		if !valid || (owner.UID != "" && !slices.Contains(r.config.IgnoredOwnerKinds, owner.GVK)) {
			return false
		}
	}
	return true
}
