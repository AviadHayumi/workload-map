// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ownership

import (
	"reflect"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// maxWalkDepth caps the owner-reference walk so a reference cycle cannot
// loop forever.
const maxWalkDepth = 8

// Ref identifies an object reached through the owner walk.
type Ref struct {
	Namespace string
	UID       types.UID
	GVK       schema.GroupVersionKind
	Name      string
}

// WalkOutcome describes how a RootFor walk ended.
type WalkOutcome string

const (
	// OutcomeFound means the walk reached a registered root kind.
	OutcomeFound WalkOutcome = "found"
	// OutcomeNoController means an object in the chain has no controller owner.
	OutcomeNoController WalkOutcome = "no_controller"
	// OutcomeMissing means a middle owner is not in the index yet; the walk
	// can be retried once that owner is observed.
	OutcomeMissing WalkOutcome = "missing"
	// OutcomeDepthExceeded means the chain is longer than maxWalkDepth.
	OutcomeDepthExceeded WalkOutcome = "depth_exceeded"
)

// WalkResult is the outcome of a RootFor walk. Missing holds the UID of the
// first absent owner when Outcome is OutcomeMissing.
type WalkResult struct {
	Outcome    WalkOutcome
	Root       Ref
	Missing    types.UID
	MissingRef Ref
	Resolution string
}

type node struct {
	owner *metav1.OwnerReference
	ref   Ref
}

// Index tracks controller owner edges for the middle objects between pods
// and workload roots (Job, StatefulSet, ...), fed by metadata informers.
// It also parks pods whose walk dead-ended on a missing owner, so they can
// be re-attributed when that owner is observed.
type Index struct {
	mu           sync.RWMutex
	nodes        map[types.UID]node
	pending      map[types.UID]map[string]struct{}
	pendingOwner map[string]types.UID
	children     map[types.UID]map[types.UID]struct{}
	ownerKinds   map[schema.GroupVersionKind]int
}

func New() *Index {
	return &Index{
		nodes:        make(map[types.UID]node),
		pending:      make(map[types.UID]map[string]struct{}),
		pendingOwner: make(map[string]types.UID),
		children:     make(map[types.UID]map[types.UID]struct{}),
		ownerKinds:   make(map[schema.GroupVersionKind]int),
	}
}

// UpsertObject records the controller owner edge of a middle object and
// returns the namespace/name keys of pods that were waiting for it.
func (i *Index) UpsertObject(uid types.UID, ownerRefs []metav1.OwnerReference) []string {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.upsertLocked(uid, ownerRefs)
}

// UpsertTrackedObject publishes object identity and its owner edge atomically.
func (i *Index) UpsertTrackedObject(obj metav1.Object, gvk schema.GroupVersionKind) (bool, []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	old, exists := i.nodes[obj.GetUID()]
	changed := !exists || !reflect.DeepEqual(old.owner, controllerRef(obj.GetOwnerReferences()))
	old.ref = Ref{UID: obj.GetUID(), Namespace: obj.GetNamespace(), Name: obj.GetName(), GVK: gvk}
	i.nodes[obj.GetUID()] = old
	return changed, i.upsertLocked(obj.GetUID(), obj.GetOwnerReferences())
}

func (i *Index) upsertLocked(uid types.UID, ownerRefs []metav1.OwnerReference) []string {
	old := i.nodes[uid]
	next := controllerRef(ownerRefs)
	var oldKind, nextKind schema.GroupVersionKind
	if old.owner != nil {
		oldKind = refGVK(old.owner)
	}
	if next != nil {
		nextKind = refGVK(next)
	}
	if oldKind != nextKind || (old.owner == nil) != (next == nil) {
		if old.owner != nil {
			i.ownerKinds[oldKind]--
			if i.ownerKinds[oldKind] == 0 {
				delete(i.ownerKinds, oldKind)
			}
		}
		if next != nil {
			i.ownerKinds[nextKind]++
		}
	}
	if old.owner != nil && (next == nil || old.owner.UID != next.UID) {
		delete(i.children[old.owner.UID], uid)
		if len(i.children[old.owner.UID]) == 0 {
			delete(i.children, old.owner.UID)
		}
	}
	if next != nil {
		if i.children[next.UID] == nil {
			i.children[next.UID] = make(map[types.UID]struct{})
		}
		i.children[next.UID][uid] = struct{}{}
	}
	i.nodes[uid] = node{owner: next, ref: old.ref}

	waiting := i.pending[uid]
	delete(i.pending, uid)
	drained := make([]string, 0, len(waiting))
	for podKey := range waiting {
		drained = append(drained, podKey)
		delete(i.pendingOwner, podKey)
	}
	return drained
}

func (i *Index) DeleteObject(uid types.UID) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.deleteObjectLocked(uid)
}

func (i *Index) deleteObjectLocked(uid types.UID) {
	if old := i.nodes[uid]; old.owner != nil {
		kind := refGVK(old.owner)
		i.ownerKinds[kind]--
		if i.ownerKinds[kind] == 0 {
			delete(i.ownerKinds, kind)
		}
		delete(i.children[old.owner.UID], uid)
		if len(i.children[old.owner.UID]) == 0 {
			delete(i.children, old.owner.UID)
		}
	}
	delete(i.nodes, uid)
}

// HasOwnerKind reports whether an indexed object still references this exact API.
func (i *Index) HasOwnerKind(gvk schema.GroupVersionKind) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.ownerKinds[gvk] > 0
}

// OwnerKinds snapshots the distinct APIs referenced by current owner edges.
func (i *Index) OwnerKinds() []schema.GroupVersionKind {
	i.mu.RLock()
	defer i.mu.RUnlock()
	kinds := make([]schema.GroupVersionKind, 0, len(i.ownerKinds))
	for kind := range i.ownerKinds {
		kinds = append(kinds, kind)
	}
	return kinds
}

// ForgetKind retires cached metadata when its watcher is no longer needed.
// Other served versions can retain records published by their own watcher.
func (i *Index) ForgetKind(gvk schema.GroupVersionKind) []Ref {
	i.mu.Lock()
	defer i.mu.Unlock()
	var retired []Ref
	for uid, node := range i.nodes {
		if node.ref.GVK == gvk {
			retired = append(retired, node.ref)
			i.deleteObjectLocked(uid)
		}
	}
	return retired
}

// MarkPending parks a pod (by namespace/name key) on the missing owner that
// blocked its walk.
func (i *Index) MarkPending(missingOwner types.UID, podKey string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if old, ok := i.pendingOwner[podKey]; ok {
		delete(i.pending[old], podKey)
		if len(i.pending[old]) == 0 {
			delete(i.pending, old)
		}
	}
	i.pendingOwner[podKey] = missingOwner
	waiting, ok := i.pending[missingOwner]
	if !ok {
		waiting = make(map[string]struct{})
		i.pending[missingOwner] = waiting
	}
	waiting[podKey] = struct{}{}
}

// ForgetPending removes a pod from every pending bucket, for pod deletion.
func (i *Index) ForgetPending(podKey string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if missingOwner, ok := i.pendingOwner[podKey]; ok {
		delete(i.pending[missingOwner], podKey)
		if len(i.pending[missingOwner]) == 0 {
			delete(i.pending, missingOwner)
		}
		delete(i.pendingOwner, podKey)
	}

}

// DrainPendingPods returns distinct pending Pod keys for a retry pass.
// A failed retry parks the Pod again, including when its missing owner changes.
func (i *Index) DrainPendingPods() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	seen := make(map[string]struct{})
	for _, waiting := range i.pending {
		for key := range waiting {
			seen[key] = struct{}{}
		}
	}
	clear(i.pending)
	clear(i.pendingOwner)
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	return keys
}

// PendingCount returns the number of parked pods.
func (i *Index) PendingCount() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	count := 0
	for _, waiting := range i.pending {
		count += len(waiting)
	}
	return count
}

// RootFor walks controller owner references to the outermost kind for
// which isRoot returns true: a JobSet child Job can itself be a described
// kind, and the pod belongs to the JobSet. Roots are matched by group and
// kind only: owner references may carry any served version. A chain that
// dead-ends above a found root parks rather than settling for the inner
// root, so the answer never flips once the missing owner arrives.
// Track records lookup identity and reports changes to an object's owner edge.
func (i *Index) Track(obj metav1.Object, gvk schema.GroupVersionKind) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	old, exists := i.nodes[obj.GetUID()]
	changed := !exists || !reflect.DeepEqual(old.owner, controllerRef(obj.GetOwnerReferences()))
	old.ref = Ref{UID: obj.GetUID(), Namespace: obj.GetNamespace(), Name: obj.GetName(), GVK: gvk}
	i.nodes[obj.GetUID()] = old
	return changed
}

// Descendants visits only the affected subtree, including unresolved children.
func (i *Index) Descendants(uid types.UID) []Ref {
	i.mu.RLock()
	defer i.mu.RUnlock()
	seen := map[types.UID]bool{uid: true}
	queue := []types.UID{uid}
	var result []Ref
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for child := range i.children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			queue = append(queue, child)
			if n, ok := i.nodes[child]; ok && n.ref.Name != "" {
				result = append(result, n.ref)
			}
		}
	}
	return result
}

func (i *Index) RootFor(ownerRefs []metav1.OwnerReference, isRoot func(schema.GroupKind) bool) WalkResult {
	return i.Resolve(ownerRefs, isRoot, nil, nil)
}

// Resolve stops before reading an exact ignored GVK. Candidate allows root
// selection for workloads with no Pods without consuming an owner hop.
func (i *Index) Resolve(ownerRefs []metav1.OwnerReference, isRoot func(schema.GroupKind) bool, ignored func(schema.GroupVersionKind) bool, candidate *Ref, namespaces ...string) WalkResult {
	i.mu.RLock()
	defer i.mu.RUnlock()
	namespace := ""
	if candidate != nil {
		namespace = candidate.Namespace
	}
	if len(namespaces) > 0 {
		namespace = namespaces[0]
	}
	var best Ref
	hasRoot := candidate != nil
	if hasRoot {
		best = *candidate
	}
	var current *metav1.OwnerReference
	if selected, _ := selectController(ownerRefs, false); selected >= 0 {
		current = &ownerRefs[selected]
	}
	resolution := "resolved"
	// This bounded index walk preserves the exporter's first-controller policy.
	// It needs neither Trace's verified path nor its external lookup adapter.
	for depth := 0; current != nil; depth++ {
		// Stored references retain the caller's controller flag. Reselect the
		// edge on every hop, matching the compatibility walker's behavior.
		if current.Controller == nil || !*current.Controller {
			break
		}
		gvk := refGVK(current)
		if ignored != nil && ignored(gvk) {
			resolution = "ignored_owner"
			break
		}
		if depth == maxWalkDepth {
			return WalkResult{Outcome: OutcomeDepthExceeded}
		}
		middle, ok := i.nodes[current.UID]
		if !ok || (middle.ref.Name != "" && (middle.ref.Name != current.Name || middle.ref.GVK.GroupKind() != gvk.GroupKind() || (namespace != "" && middle.ref.Namespace != namespace))) {
			return WalkResult{Outcome: OutcomeMissing, Missing: current.UID, MissingRef: Ref{UID: current.UID, Name: current.Name, GVK: gvk}}
		}
		if isRoot(gvk.GroupKind()) {
			best = Ref{UID: current.UID, GVK: gvk, Name: current.Name}
			hasRoot = true
		}
		current = middle.owner
	}
	if hasRoot {
		return WalkResult{Outcome: OutcomeFound, Root: best, Resolution: resolution}
	}
	return WalkResult{Outcome: OutcomeNoController, Resolution: resolution}
}

func controllerRef(ownerRefs []metav1.OwnerReference) *metav1.OwnerReference {
	index, _ := selectController(ownerRefs, false)
	if index < 0 {
		return nil
	}
	ref := ownerRefs[index]
	return &ref
}

func refGVK(ref *metav1.OwnerReference) schema.GroupVersionKind {
	groupVersion, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return schema.GroupVersionKind{Kind: ref.Kind}
	}
	return groupVersion.WithKind(ref.Kind)
}
