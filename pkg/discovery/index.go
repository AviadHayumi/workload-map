// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

// Index observes caller-published objects without starting watches. A producer
// supplies complete Pod coverage through Replace, then ordered current objects
// from its canonical cache through Apply. A generation conflict requires reading
// that cache again. Lost watch coverage, ordering, or UID-less tombstones require
// Invalidate followed by a complete Replace.
//
// Published objects are immutable. Readers copy selected objects after releasing
// the lock, so captures retain one generation while later updates proceed.
type Index struct {
	mu         sync.RWMutex
	generation uint64
	state      *indexState
	invalid    error
}

type indexState struct {
	namespace  string
	podVersion string
	mappings   map[schema.GroupVersionKind]*meta.RESTMapping
	objects    map[objectKey]*unstructured.Unstructured
	records    map[types.UID]*indexRecord
	names      map[indexName]types.UID
	graph      *ownership.Index
	pods       map[string]map[types.UID]*corev1.Pod
	rootPods   map[types.UID]map[types.UID]*corev1.Pod
	resolved   map[types.UID]indexResolution
	unresolved map[string]map[types.UID]struct{}
	ownerKinds map[schema.GroupVersionKind]map[types.UID]struct{}
	ownerNames map[indexOwnerName]map[types.UID]struct{}
	retired    map[types.UID]struct{}
}

type indexRecord struct {
	object   *unstructured.Unstructured
	aliases  map[schema.GroupVersionKind]*unstructured.Unstructured // nil when object is the only alias
	pod      *corev1.Pod
	metadata ownership.Object
	revision *indexRevision
}

// A nonzero-sized token identifies one immutable record without retaining its
// object body when derived attribution outlives an index update.
type indexRevision struct{ _ byte }

type indexName struct {
	kind      schema.GroupKind
	namespace string
	name      string
}

type indexOwnerName struct {
	kind schema.GroupKind
	name string
}

type indexResolution struct {
	root      types.UID
	depth     int
	namespace string
}

type indexedWorkload struct {
	object       *unstructured.Unstructured
	pods         []*corev1.Pod
	observation  Observation
	rootRevision *indexRevision
	podRevisions []*indexRevision
}

type indexedPodOrder struct{ snapshot *indexedWorkload }

var _ IndexedReader = (*Index)(nil)

func (p indexedPodOrder) Len() int           { return len(p.snapshot.pods) }
func (p indexedPodOrder) Less(i, j int) bool { return podLess(p.snapshot.pods[i], p.snapshot.pods[j]) }
func (p indexedPodOrder) Swap(i, j int) {
	p.snapshot.pods[i], p.snapshot.pods[j] = p.snapshot.pods[j], p.snapshot.pods[i]
	p.snapshot.podRevisions[i], p.snapshot.podRevisions[j] = p.snapshot.podRevisions[j], p.snapshot.podRevisions[i]
}

func NewIndex() *Index {
	return &Index{}
}

// Replace validates a complete detached dataset before publishing it. It resets
// retired UIDs and restores an invalidated index. Producers serialize initial
// collection and subsequent event publication so an old list cannot replace a
// newer feed state.
func (i *Index) Replace(ctx context.Context, dataset Dataset) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	state := &indexState{
		namespace: dataset.Namespace, podVersion: dataset.PodResourceVersion,
		mappings: make(map[schema.GroupVersionKind]*meta.RESTMapping), objects: make(map[objectKey]*unstructured.Unstructured),
		records: make(map[types.UID]*indexRecord), names: make(map[indexName]types.UID), graph: ownership.New(),
		pods: make(map[string]map[types.UID]*corev1.Pod), rootPods: make(map[types.UID]map[types.UID]*corev1.Pod),
		resolved: make(map[types.UID]indexResolution), unresolved: make(map[string]map[types.UID]struct{}),
		ownerKinds: make(map[schema.GroupVersionKind]map[types.UID]struct{}), ownerNames: make(map[indexOwnerName]map[types.UID]struct{}), retired: make(map[types.UID]struct{}),
	}
	for gvk, mapping := range dataset.Mappings {
		copy, err := indexMapping(gvk, mapping)
		if err != nil {
			return 0, err
		}
		state.mappings[gvk] = copy
	}
	if state.mappings[podGVK] == nil {
		return 0, fmt.Errorf("%w: complete Pod coverage requires the Pod mapping", ErrInvalidInput)
	}
	objects := append([]*unstructured.Unstructured(nil), dataset.Objects...)
	for _, pod := range dataset.Pods {
		object, err := podObject(pod)
		if err != nil {
			return 0, err
		}
		objects = append(objects, object)
	}
	groups, err := state.prepare(ctx, objects, nil)
	if err != nil {
		return 0, err
	}
	for uid, record := range groups {
		name := objectName(record.object)
		if previous, exists := state.names[name]; exists && previous != uid {
			return 0, fmt.Errorf("%w: multiple UIDs for %s/%s", ErrInvalidInput, name.namespace, name.name)
		}
		state.add(uid, record)
	}
	for uid := range state.records {
		if err := state.resolve(ctx, uid); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrIncompleteOwnership, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	i.state, i.invalid = state, nil
	i.generation++
	return i.generation, nil
}

// Apply atomically publishes changes if expected is still current. Only changed
// objects and their descendants are retraced. Missing owners leave the generation
// published but incomplete until later updates restore the owner closure.
//
// All served-version aliases for a changed UID must be supplied together. Other
// aliases are dropped because the index cannot convert their object bodies.
// Deletes identify a lifetime by UID and cannot delete its replacement. Retired
// lifetimes cannot be resurrected until an authoritative Replace.
func (i *Index) Apply(ctx context.Context, expected uint64, changes Changes) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.ready(); err != nil {
		return i.generation, err
	}
	if expected != i.generation {
		return i.generation, ErrGenerationConflict
	}
	state := i.state
	mappings := make(map[schema.GroupVersionKind]*meta.RESTMapping, len(changes.Mappings))
	for gvk, mapping := range changes.Mappings {
		copy, err := indexMapping(gvk, mapping)
		if err != nil {
			return i.generation, err
		}
		if previous := state.mappings[gvk]; previous != nil && (previous.Resource != copy.Resource || previous.Scope.Name() != copy.Scope.Name()) {
			return i.generation, fmt.Errorf("%w: changing resource scope or identity requires Replace", ErrInvalidInput)
		}
		mappings[gvk] = copy
	}
	groups, err := state.prepare(ctx, changes.Upserts, mappings)
	if err != nil {
		return i.generation, err
	}
	removed := make(map[types.UID]struct{}, len(changes.Deletes))
	for _, ref := range changes.Deletes {
		if ref.UID == "" {
			return i.generation, fmt.Errorf("%w: delete requires a UID", ErrInvalidInput)
		}
		if record := state.records[ref.UID]; record != nil {
			actual := reference(record.object)
			if (ref.Name != "" && (ref.Name != actual.Name || ref.Namespace != actual.Namespace)) || (ref.GVK.Kind != "" && ref.GVK.GroupKind() != actual.GVK.GroupKind()) {
				return i.generation, fmt.Errorf("%w: delete identity disagrees with UID", ErrInvalidInput)
			}
		}
		removed[ref.UID] = struct{}{}
	}
	newNames := make(map[indexName]types.UID, len(groups))
	for uid, record := range groups {
		if _, retired := state.retired[uid]; retired {
			return i.generation, fmt.Errorf("%w: %s", ErrRetiredUID, uid)
		}
		if _, deleted := removed[uid]; deleted {
			return i.generation, fmt.Errorf("%w: UID is both deleted and upserted", ErrInvalidInput)
		}
		name := objectName(record.object)
		if prior, exists := newNames[name]; exists && prior != uid {
			return i.generation, fmt.Errorf("%w: multiple replacement UIDs for one object", ErrInvalidInput)
		}
		newNames[name] = uid
		if prior, exists := state.names[name]; exists && prior != uid {
			removed[prior] = struct{}{}
		}
		if prior := state.records[uid]; prior != nil && objectName(prior.object) != name {
			return i.generation, fmt.Errorf("%w: UID changed object identity", ErrInvalidInput)
		}
	}
	for uid := range groups {
		if _, deleted := removed[uid]; deleted {
			return i.generation, fmt.Errorf("%w: conflicting object lifetimes", ErrInvalidInput)
		}
	}
	affected := make(map[types.UID]struct{}, len(groups)+len(removed))
	mark := func(uid types.UID) {
		if _, exists := affected[uid]; exists {
			return
		}
		affected[uid] = struct{}{}
		for _, descendant := range state.graph.Descendants(uid) {
			affected[descendant.UID] = struct{}{}
		}
	}
	for uid, record := range groups {
		mark(uid)
		name := objectName(record.object)
		for child := range state.ownerNames[indexOwnerName{kind: name.kind, name: name.name}] {
			dependent := state.records[child]
			if name.namespace == "" || dependent.object.GetNamespace() == name.namespace {
				mark(child)
			}
		}
	}
	for uid := range removed {
		mark(uid)
	}
	for gvk := range mappings {
		if state.mappings[gvk] != nil {
			continue
		}
		for uid := range state.ownerKinds[gvk] {
			mark(uid)
		}
	}
	if err := ctx.Err(); err != nil {
		return i.generation, err
	}
	// No fallible operation follows this point: ownership errors become indexed
	// incomplete states, so cancellation cannot leave a partially applied batch.
	for uid := range affected {
		state.forgetResolution(uid)
	}
	for uid := range removed {
		state.remove(uid)
		state.retired[uid] = struct{}{}
	}
	for uid := range groups {
		state.remove(uid)
	}
	for gvk, mapping := range mappings {
		state.mappings[gvk] = mapping
	}
	for uid, record := range groups {
		state.add(uid, record)
		if record.pod != nil {
			state.podVersion = ""
		}
	}
	for uid := range affected {
		if state.records[uid] != nil {
			_ = state.resolve(context.Background(), uid)
		}
	}
	i.generation++
	return i.generation, nil
}

// Invalidate fences all current generations after loss of feed completeness.
func (i *Index) Invalidate(cause error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if cause == nil {
		cause = ErrNotSynced
	}
	i.invalid = cause
	i.generation++
}

func (i *Index) Mapping(ctx context.Context, gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := i.ready(); err != nil {
		return nil, err
	}
	mapping := i.state.mappings[gvk]
	if mapping == nil {
		return nil, &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
	}
	return copyMapping(mapping)
}

func (i *Index) Get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i.mu.RLock()
	if err := ctx.Err(); err != nil {
		i.mu.RUnlock()
		return nil, err
	}
	if err := i.ready(); err != nil {
		i.mu.RUnlock()
		return nil, err
	}
	object, err := i.state.get(ref)
	i.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return object.DeepCopy(), nil
}

func (i *Index) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i.mu.RLock()
	if err := ctx.Err(); err != nil {
		i.mu.RUnlock()
		return nil, err
	}
	if err := i.ready(); err != nil {
		i.mu.RUnlock()
		return nil, err
	}
	state := i.state
	if state.namespace != "" && state.namespace != namespace {
		i.mu.RUnlock()
		return nil, ErrInsufficientScope
	}
	list := &corev1.PodList{}
	list.ResourceVersion = state.podVersion
	var pods []*corev1.Pod
	if namespace != "" {
		for _, pod := range state.pods[namespace] {
			pods = append(pods, pod)
		}
	} else {
		for _, scoped := range state.pods {
			for _, pod := range scoped {
				pods = append(pods, pod)
			}
		}
	}
	i.mu.RUnlock()
	sort.Slice(pods, func(a, b int) bool { return podLess(pods[a], pods[b]) })
	for _, pod := range pods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		list.Items = append(list.Items, *pod.DeepCopy())
	}
	return list, nil
}

// ownershipPath holds one generation for the entire chain. Pod coverage and
// unrelated owner closure are not needed to establish this object's owners.
func (i *Index) ownershipPath(ctx context.Context, object ObjectRef, maxDepth int) ([]ObjectRef, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := i.ready(); err != nil {
		return nil, err
	}
	start, err := i.state.get(object)
	if err != nil {
		return nil, err
	}
	if reference(start) != object {
		return nil, fmt.Errorf("%w: starting object identity or namespace differs", ownership.ErrScope)
	}
	path, err := i.state.traceDepth(ctx, start, maxDepth)
	if err != nil {
		return nil, err
	}
	refs := make([]ObjectRef, len(path))
	for index, entry := range path {
		refs[index] = entry.Ref
	}
	return refs, nil
}

func (i *Index) Capture(ctx context.Context, seed ObjectRef) (*Capture, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now()
	i.mu.RLock()
	capture, err := i.captureLocked(ctx, seed)
	i.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	capture.Observation.Started = started
	for n, object := range capture.Objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		capture.Objects[n] = object.DeepCopy()
	}
	for n, pod := range capture.Pods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		capture.Pods[n] = pod.DeepCopy()
	}
	sort.Slice(capture.Objects, func(a, b int) bool {
		left, right := capture.Objects[a], capture.Objects[b]
		if left.GetUID() != right.GetUID() {
			return left.GetUID() < right.GetUID()
		}
		return left.GetAPIVersion() < right.GetAPIVersion()
	})
	sort.Slice(capture.Pods, func(a, b int) bool { return podLess(capture.Pods[a], capture.Pods[b]) })
	capture.Observation.Completed = time.Now()
	return capture, nil
}

// snapshotWorkload shares immutable records only with this package. Public
// Capture still returns detached copies for arbitrary callers.
func (i *Index) snapshotWorkload(ctx context.Context, seed ObjectRef, requireRoot bool, maxDepth int, withRevisions bool) (*indexedWorkload, error) {
	started := time.Now()
	i.mu.RLock()
	snapshot, err := i.workloadLocked(ctx, seed, requireRoot, maxDepth, withRevisions)
	i.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	snapshot.object = snapshot.object.DeepCopy()
	if withRevisions {
		sort.Sort(indexedPodOrder{snapshot})
	} else {
		sort.Slice(snapshot.pods, func(a, b int) bool { return podLess(snapshot.pods[a], snapshot.pods[b]) })
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot.observation.Started = started
	snapshot.observation.Completed = time.Now()
	return snapshot, nil
}

func (i *Index) workloadLocked(ctx context.Context, seed ObjectRef, requireRoot bool, maxDepth int, withRevisions bool) (*indexedWorkload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := i.ready(); err != nil {
		return nil, err
	}
	state := i.state
	object, err := state.get(seed)
	if err != nil {
		return nil, err
	}
	path, err := state.trace(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIncompleteOwnership, err)
	}
	terminal := path[len(path)-1].Ref
	root, err := state.get(terminal)
	if err != nil {
		return nil, err
	}
	if terminal.Namespace == "" && state.namespace != "" {
		return nil, ErrInsufficientScope
	}
	if err := state.complete(ctx, terminal.Namespace); err != nil {
		return nil, err
	}
	if reference(object) != seed {
		return nil, fmt.Errorf("%w: seed identity or namespace differs", ownership.ErrScope)
	}
	if len(path)-1 > maxDepth {
		if _, err := state.traceDepth(ctx, object, maxDepth); err != nil {
			return nil, err
		}
		return nil, ErrIncompleteOwnership
	}
	if requireRoot && terminal.UID != seed.UID {
		return nil, ErrNotWorkloadRoot
	}
	snapshot := &indexedWorkload{object: root, rootRevision: state.records[terminal.UID].revision, pods: make([]*corev1.Pod, 0, len(state.rootPods[terminal.UID])), observation: Observation{
		Source: "indexed", Namespace: terminal.Namespace, Generation: i.generation, PodResourceVersion: state.podVersion,
	}}
	if withRevisions {
		snapshot.podRevisions = make([]*indexRevision, 0, len(state.rootPods[terminal.UID]))
	}
	var excessive *corev1.Pod
	for uid, pod := range state.rootPods[terminal.UID] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if state.resolved[uid].depth > maxDepth && (excessive == nil || podLess(pod, excessive)) {
			excessive = pod
		}
		snapshot.pods = append(snapshot.pods, pod)
		if withRevisions {
			snapshot.podRevisions = append(snapshot.podRevisions, state.records[uid].revision)
		}
	}
	if excessive != nil {
		pod := state.records[excessive.UID].alias(podGVK)
		if _, err := state.traceDepth(ctx, pod, maxDepth); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrIncompleteOwnership, err)
		}
		return nil, ErrIncompleteOwnership
	}
	return snapshot, nil
}

func (i *Index) captureLocked(ctx context.Context, seed ObjectRef) (*Capture, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := i.ready(); err != nil {
		return nil, err
	}
	state := i.state
	object, err := state.get(seed)
	if err != nil {
		return nil, err
	}
	path, err := state.trace(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIncompleteOwnership, err)
	}
	root := path[len(path)-1]
	if !root.Namespaced && state.namespace != "" {
		return nil, ErrInsufficientScope
	}
	if err := state.complete(ctx, root.Ref.Namespace); err != nil {
		return nil, err
	}
	capture := &Capture{Seed: seed, Mappings: make(map[schema.GroupVersionKind]*meta.RESTMapping), Observation: Observation{
		Source: "indexed", Namespace: root.Ref.Namespace, Generation: i.generation, PodResourceVersion: state.podVersion,
	}}
	refs := append([]ObjectRef{root.Ref}, state.graph.Descendants(root.Ref.UID)...)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record := state.records[ref.UID]
		if record == nil {
			return nil, fmt.Errorf("%w: absent descendant %s", ErrIncompleteOwnership, ref.UID)
		}
		for gvk, alias := range record.eachAlias {
			mapping, err := copyMapping(state.mappings[gvk])
			if err != nil {
				return nil, err
			}
			capture.Mappings[gvk] = mapping
			if record.pod == nil {
				capture.Objects = append(capture.Objects, alias)
			}
		}
	}
	for _, pod := range state.rootPods[root.Ref.UID] {
		capture.Pods = append(capture.Pods, pod)
	}
	// Empty workloads still carry the Pod mapping used by their coverage claim.
	capture.Mappings[podGVK], _ = copyMapping(state.mappings[podGVK])
	return capture, nil
}

func (i *Index) ready() error {
	if i.invalid != nil {
		return fmt.Errorf("%w: %w", ErrNotSynced, i.invalid)
	}
	if i.state == nil {
		return ErrNotSynced
	}
	return nil
}

func indexMapping(gvk schema.GroupVersionKind, mapping *meta.RESTMapping) (*meta.RESTMapping, error) {
	copy, err := copyMapping(mapping)
	if err != nil {
		return nil, err
	}
	if copy.GroupVersionKind != gvk || copy.Resource.Group != gvk.Group || copy.Resource.Version != gvk.Version {
		return nil, fmt.Errorf("%w: mapping disagrees with %s", ErrInvalidInput, gvk)
	}
	if gvk == podGVK && copy.Scope.Name() != meta.RESTScopeNameNamespace {
		return nil, fmt.Errorf("%w: Pods must be namespaced", ownership.ErrScope)
	}
	return copy, nil
}

func objectName(object *unstructured.Unstructured) indexName {
	return indexName{kind: object.GroupVersionKind().GroupKind(), namespace: object.GetNamespace(), name: object.GetName()}
}

func (s *indexState) prepare(ctx context.Context, objects []*unstructured.Unstructured, additions map[schema.GroupVersionKind]*meta.RESTMapping) (map[types.UID]*indexRecord, error) {
	groups := make(map[types.UID]*indexRecord, len(objects))
	for _, source := range objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source == nil {
			return nil, fmt.Errorf("%w: nil indexed object", ErrInvalidInput)
		}
		object := source.DeepCopy()
		gvk := object.GroupVersionKind()
		mapping := additions[gvk]
		if mapping == nil {
			mapping = s.mappings[gvk]
		}
		metadata, err := ownershipObject(object, mapping)
		if err != nil {
			return nil, err
		}
		if s.namespace != "" && object.GetNamespace() != "" && object.GetNamespace() != s.namespace {
			return nil, ErrInsufficientScope
		}
		uid := object.GetUID()
		record := groups[uid]
		if record == nil {
			record = &indexRecord{object: object, metadata: metadata, revision: &indexRevision{}}
			groups[uid] = record
		} else {
			if objectName(record.object) != objectName(object) || record.object.GetResourceVersion() != object.GetResourceVersion() || !sameOwnerReferences(record.metadata.Owners, metadata.Owners) {
				return nil, fmt.Errorf("%w: inconsistent served-version aliases for UID %s", ErrStaleInput, uid)
			}
			if record.alias(gvk) != nil {
				return nil, fmt.Errorf("%w: duplicate object coordinates", ErrInvalidInput)
			}
			if record.aliases == nil {
				record.aliases = map[schema.GroupVersionKind]*unstructured.Unstructured{record.metadata.Ref.GVK: record.object}
			}
			record.aliases[gvk] = object
		}
		if gvk == podGVK {
			pod, err := objectPod(object)
			if err != nil {
				return nil, err
			}
			record.pod = pod
			record.metadata.Owners = pod.OwnerReferences
		}
	}
	return groups, nil
}

func (r *indexRecord) alias(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	if r.aliases == nil {
		if gvk == r.metadata.Ref.GVK {
			return r.object
		}
		return nil
	}
	return r.aliases[gvk]
}

func (r *indexRecord) eachAlias(yield func(schema.GroupVersionKind, *unstructured.Unstructured) bool) {
	if r.aliases == nil {
		yield(r.metadata.Ref.GVK, r.object)
		return
	}
	for gvk, object := range r.aliases {
		if !yield(gvk, object) {
			return
		}
	}
}

func (s *indexState) get(ref ObjectRef) (*unstructured.Unstructured, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	mapping := s.mappings[ref.GVK]
	if mapping == nil {
		return nil, &meta.NoKindMatchError{GroupKind: ref.GVK.GroupKind(), SearchedVersions: []string{ref.GVK.Version}}
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		ref.Namespace = ""
	} else if ref.Namespace == "" {
		return nil, fmt.Errorf("%w: namespaced lookup has no dependent namespace", ownership.ErrScope)
	} else if s.namespace != "" && ref.Namespace != s.namespace {
		return nil, ErrInsufficientScope
	}
	object := s.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}]
	if object == nil {
		return nil, apierrors.NewNotFound(mapping.Resource.GroupResource(), ref.Name)
	}
	if object.GetUID() != ref.UID {
		return nil, &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: ref, Actual: reference(object)}
	}
	return object, nil
}

func (s *indexState) trace(ctx context.Context, object *unstructured.Unstructured) ([]ownership.Object, error) {
	return s.traceDepth(ctx, object, len(s.records))
}

func (s *indexState) traceDepth(ctx context.Context, object *unstructured.Unstructured, maxDepth int) ([]ownership.Object, error) {
	start := s.records[object.GetUID()].metadata
	// Aliases share validated ownership metadata, but retain the exact served
	// version selected by get for this traversal.
	start.Ref.GVK = object.GroupVersionKind()
	return ownership.Trace(ctx, start, func(ctx context.Context, ref ObjectRef) (ownership.Object, error) {
		if err := ctx.Err(); err != nil {
			return ownership.Object{}, err
		}
		owner, err := s.get(ref)
		if err != nil {
			return ownership.Object{}, err
		}
		metadata := s.records[owner.GetUID()].metadata
		metadata.Ref.GVK = owner.GroupVersionKind()
		return metadata, nil
	}, maxDepth)
}

func (s *indexState) add(uid types.UID, record *indexRecord) {
	s.records[uid] = record
	s.names[objectName(record.object)] = uid
	for gvk, object := range record.eachAlias {
		s.objects[objectKey{gvk, object.GetNamespace(), object.GetName()}] = object
	}
	var tracked metav1.Object = record.object
	if record.pod != nil {
		tracked = record.pod
	}
	s.graph.UpsertTrackedObject(tracked, record.metadata.Ref.GVK)
	for _, owner := range record.metadata.Owners {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil {
			continue
		}
		gvk := gv.WithKind(owner.Kind)
		if s.ownerKinds[gvk] == nil {
			s.ownerKinds[gvk] = make(map[types.UID]struct{})
		}
		s.ownerKinds[gvk][uid] = struct{}{}
		name := indexOwnerName{kind: gvk.GroupKind(), name: owner.Name}
		if s.ownerNames[name] == nil {
			s.ownerNames[name] = make(map[types.UID]struct{})
		}
		s.ownerNames[name][uid] = struct{}{}
	}
	if record.pod != nil {
		if s.pods[record.pod.Namespace] == nil {
			s.pods[record.pod.Namespace] = make(map[types.UID]*corev1.Pod)
		}
		s.pods[record.pod.Namespace][uid] = record.pod
	}
}

func (s *indexState) remove(uid types.UID) {
	record := s.records[uid]
	if record == nil {
		return
	}
	delete(s.records, uid)
	delete(s.names, objectName(record.object))
	for gvk, object := range record.eachAlias {
		delete(s.objects, objectKey{gvk, object.GetNamespace(), object.GetName()})
	}
	s.graph.DeleteObject(uid)
	for _, owner := range record.metadata.Owners {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil {
			continue
		}
		gvk := gv.WithKind(owner.Kind)
		delete(s.ownerKinds[gvk], uid)
		if len(s.ownerKinds[gvk]) == 0 {
			delete(s.ownerKinds, gvk)
		}
		name := indexOwnerName{kind: gvk.GroupKind(), name: owner.Name}
		delete(s.ownerNames[name], uid)
		if len(s.ownerNames[name]) == 0 {
			delete(s.ownerNames, name)
		}
	}
	if record.pod != nil {
		delete(s.pods[record.pod.Namespace], uid)
		if len(s.pods[record.pod.Namespace]) == 0 {
			delete(s.pods, record.pod.Namespace)
		}
		s.podVersion = ""
	}
}

func (s *indexState) resolve(ctx context.Context, uid types.UID) error {
	record := s.records[uid]
	namespace := record.object.GetNamespace()
	path, err := s.trace(ctx, record.object)
	resolution := indexResolution{namespace: namespace}
	if err != nil {
		if s.unresolved[namespace] == nil {
			s.unresolved[namespace] = make(map[types.UID]struct{})
		}
		s.unresolved[namespace][uid] = struct{}{}
	} else {
		resolution.root = path[len(path)-1].Ref.UID
		resolution.depth = len(path) - 1
		if record.pod != nil {
			if s.rootPods[resolution.root] == nil {
				s.rootPods[resolution.root] = make(map[types.UID]*corev1.Pod)
			}
			s.rootPods[resolution.root][uid] = record.pod
		}
	}
	s.resolved[uid] = resolution
	return err
}

func (s *indexState) forgetResolution(uid types.UID) {
	resolution, exists := s.resolved[uid]
	if !exists {
		return
	}
	delete(s.rootPods[resolution.root], uid)
	if len(s.rootPods[resolution.root]) == 0 {
		delete(s.rootPods, resolution.root)
	}
	delete(s.unresolved[resolution.namespace], uid)
	if len(s.unresolved[resolution.namespace]) == 0 {
		delete(s.unresolved, resolution.namespace)
	}
	delete(s.resolved, uid)
}

func (s *indexState) complete(ctx context.Context, namespace string) error {
	var missing types.UID
	if namespace == "" {
		for _, scoped := range s.unresolved {
			for uid := range scoped {
				missing = uid
				break
			}
			if missing != "" {
				break
			}
		}
	} else {
		for _, scope := range []string{"", namespace} {
			for uid := range s.unresolved[scope] {
				missing = uid
				break
			}
			if missing != "" {
				break
			}
		}
	}
	if missing == "" {
		return nil
	}
	// Return fresh diagnostic fields instead of exposing a cached error through
	// errors.As, which would let a caller mutate subsequent observations.
	_, err := s.trace(ctx, s.records[missing].object)
	if err == nil {
		return ErrIncompleteOwnership
	}
	return fmt.Errorf("%w: %w", ErrIncompleteOwnership, err)
}
