// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/instructions"
)

type Runner interface {
	Run(context.Context) error
	Ready() bool
}
type Dependencies struct {
	Discovery    discovery.Discoverer
	Inventory    inventory.Observer
	Labels       labelresolver.Resolver
	Publications store.Writer
}
type Config struct {
	IgnoredOwnerKinds                              []schema.GroupVersionKind
	ResourceRules, ExcludeResourceRules            []ResourceRule
	NamespaceSelector, ObjectSelector, PodSelector *metav1.LabelSelector
	ClusterDefinitions                             bool
	RequiredKinds                                  []schema.GroupVersionKind
	Workers, LeafWorkers, RootWorkers              int
}
type ResourceRule struct {
	APIGroups   []string `json:"apiGroups"`
	APIVersions []string `json:"apiVersions"`
	Resources   []string `json:"resources"`
}

type Runtime struct {
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

type inputKey struct {
	Object objectKey
	UID    types.UID
	Rescan bool
}
type rootKey struct {
	UID       types.UID
	Candidate inputKey
}
type watchKey = store.SourceKey
type rootState struct {
	ref        discovery.ObjectRef
	resolution discovery.OwnerResolution
	pods       map[types.UID]inputKey
	candidates map[inputKey]candidateState
}

type selectedDefinition struct {
	inventory.Selection
	summary *instructions.StructureSummary
	err     error
	version uint64
}

type candidateState struct {
	sources []store.SourceKey
	err     error
}

var podGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
var kartaGVK = schema.GroupVersionKind{Group: "run.ai", Version: "v1alpha1", Kind: "Karta"}
var definitionSource = store.SourceKey{Resource: kartaGVK.GroupVersion().WithResource("kartas")}
var namespaceGVK = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}

func New(config Config, deps Dependencies, reader *Reader) (*Runtime, error) {
	if deps.Discovery == nil || deps.Inventory == nil || deps.Labels == nil || deps.Publications == nil || reader == nil {
		return nil, errors.New("runtime dependencies are required")
	}
	if err := ValidateResourceRules(slices.Concat(config.ResourceRules, config.ExcludeResourceRules)); err != nil {
		return nil, err
	}
	if config.Workers == 0 {
		config.Workers = 2
	}
	if config.LeafWorkers == 0 {
		config.LeafWorkers = config.Workers
	}
	if config.RootWorkers == 0 {
		config.RootWorkers = config.Workers
	}
	if config.Workers < 1 || config.LeafWorkers < 1 || config.RootWorkers < 1 {
		return nil, errors.New("worker limits must be positive")
	}
	if config.NamespaceSelector != nil && len(config.NamespaceSelector.MatchLabels) == 0 && len(config.NamespaceSelector.MatchExpressions) == 0 {
		config.NamespaceSelector = nil
	}
	r := &Runtime{
		config:        config,
		deps:          deps,
		reader:        reader,
		inputQueue:    newQueue[inputKey]("exporter-pods"),
		rootQueue:     newQueue[rootKey]("exporter-workloads"),
		controlQueue:  newQueue[string]("exporter-sources"),
		watches:       map[watchKey]*resourceWatch{},
		replacements:  map[watchKey]*resourceWatch{},
		watchFailures: map[discovery.WatchRequest]error{},
		roots:         map[types.UID]*rootState{},
		podRoots:      map[types.UID]types.UID{},
		ancestorPods:  map[types.UID]bool{},
		candidates:    map[inputKey]types.UID{},
		selections:    map[schema.GroupKind]selectedDefinition{},
		scopes:        map[string]bool{},
	}
	if config.NamespaceSelector == nil {
		r.scopes[""] = true
	}
	if config.ClusterDefinitions {
		r.definitionError = inventory.ErrNotReady
		deps.Publications.SetSourceHealth(definitionSource, store.SourceHealth{GVK: kartaGVK})
	}
	var err error
	r.namespaceSelector, err = selector(config.NamespaceSelector)
	if err != nil {
		return nil, err
	}
	r.objectSelector, err = selector(config.ObjectSelector)
	if err != nil {
		return nil, err
	}
	r.podSelector, err = selector(config.PodSelector)
	if err != nil {
		return nil, err
	}
	reader.ensure = r.ensureWatch
	return r, nil
}
func (r *Runtime) Run(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("runtime already started")
	}
	r.started = true
	r.ctx = ctx
	r.mu.Unlock()
	for i := 0; i < r.config.LeafWorkers; i++ {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); runWorker(ctx, r.inputQueue, r.resolveInput) }()
	}
	for i := 0; i < r.config.RootWorkers; i++ {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); runWorker(ctx, r.rootQueue, r.resolveWorkload) }()
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		runWorker(ctx, r.controlQueue, func(ctx context.Context, _ string) error { return r.syncWatches(ctx) })
	}()
	r.controlQueue.Add("sources")
	changes := r.deps.Inventory.Changes()
running:
	for {
		select {
		case <-ctx.Done():
			break running
		case _, open := <-changes:
			if !open {
				changes = nil
				continue
			}
			r.controlQueue.Add("sources")
		}
	}
	r.inputQueue.ShutDown()
	r.rootQueue.ShutDown()
	r.controlQueue.ShutDown()
	r.mu.Lock()
	for _, w := range r.watches {
		w.cancel()
	}
	for _, w := range r.replacements {
		w.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}
func (r *Runtime) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || (r.ctx != nil && r.ctx.Err() != nil) || r.definitionError != nil || len(r.watches) == 0 {
		return false
	}
	for request := range r.watchFailures {
		if r.requiredWatch(request.GVK) {
			return false
		}
	}
	for _, w := range r.watches {
		if r.requiredWatch(w.gvk) && (!w.ready || w.err != nil || r.replacements[w.key] != nil) {
			return false
		}
	}
	return true
}
func newQueue[T comparable](name string) workqueue.TypedRateLimitingInterface[T] {
	return workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.NewTypedItemExponentialFailureRateLimiter[T](10*time.Millisecond, 10*time.Second), workqueue.TypedRateLimitingQueueConfig[T]{Name: name})
}
func selector(value *metav1.LabelSelector) (labels.Selector, error) {
	if value == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(value)
}
func runWorker[T comparable](ctx context.Context, queue workqueue.TypedRateLimitingInterface[T], reconcile func(context.Context, T) error) {
	for {
		key, shutdown := queue.Get()
		if shutdown {
			return
		}
		if err := reconcile(ctx, key); err != nil && ctx.Err() == nil {
			queue.AddRateLimited(key)
		} else {
			queue.Forget(key)
		}
		queue.Done(key)
	}
}
func (r *Runtime) requiredWatch(gvk schema.GroupVersionKind) bool {
	return gvk == podGVK || (gvk == namespaceGVK && r.config.NamespaceSelector != nil) || slices.Contains(r.config.RequiredKinds, gvk)
}

func (r *Runtime) refreshDefinitionsLocked() error {
	results, sourceErr := r.deps.Inventory.List()
	next := r.selections
	if sourceErr == nil {
		next = make(map[schema.GroupKind]selectedDefinition, len(results))
		for _, result := range results {
			selected := selectedDefinition{Selection: result.Selection, err: result.Err}
			if selected.err == nil && selected.Definition != nil {
				var err error
				selected.summary, err = instructions.NewStructureSummary(selected.Definition)
				if err != nil {
					selected.err = fmt.Errorf("%w: %w", discovery.ErrInvalidDefinition, err)
				}
			}
			next[result.GVK.GroupKind()] = selected
		}
	}
	healthChanged := failureReason(r.definitionError) != failureReason(sourceErr)
	changedKinds := map[schema.GroupKind]bool{}
	for kind, selected := range next {
		if !reflect.DeepEqual(r.selections[kind], selected) {
			changedKinds[kind] = true
		}
	}
	for kind := range r.selections {
		if _, exists := next[kind]; !exists {
			changedKinds[kind] = true
		}
	}
	r.definitionError = sourceErr
	r.selections = next
	if r.config.ClusterDefinitions {
		r.deps.Publications.SetSourceHealth(definitionSource, store.SourceHealth{GVK: kartaGVK, Synced: sourceErr == nil, Error: failureReason(sourceErr)})
	}
	if healthChanged || len(changedKinds) > 0 {
		r.definitionVersion++
		if healthChanged {
			r.enqueueRescanLocked("")
		} else {
			r.reader.mu.RLock()
			for _, entry := range r.reader.objects {
				ref := refOf(entry.object)
				if changedKinds[ref.GVK.GroupKind()] {
					r.enqueueObjectLocked(entry.object)
				}
				if ref.GVK == podGVK {
					root := r.roots[r.podRoots[ref.UID]]
					if root == nil || changedKinds[root.ref.GVK.GroupKind()] {
						r.enqueuePodLocked(entry.object)
					}
				}
			}
			r.reader.mu.RUnlock()
		}
		for uid, root := range r.roots {
			if healthChanged || changedKinds[root.ref.GVK.GroupKind()] {
				r.rootQueue.Add(rootKey{UID: uid})
				r.enqueueMembersLocked(root)
			}
		}
		for candidate := range r.candidates {
			if changedKinds[candidate.Object.GVK.GroupKind()] {
				r.rootQueue.Add(rootKey{Candidate: candidate})
			}
		}
	}
	return sourceErr
}
func (r *Runtime) lookupDefinition(gvk schema.GroupVersionKind) (selectedDefinition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	selected := r.selections[gvk.GroupKind()]
	selected.version = r.definitionVersion
	if r.definitionError != nil {
		return selected, r.definitionError
	}
	return selected, selected.err
}

func inputFor(ref discovery.ObjectRef) inputKey { return inputKey{Object: keyOf(ref), UID: ref.UID} }
func (key inputKey) ref() discovery.ObjectRef {
	return discovery.ObjectRef{GVK: key.Object.GVK, Namespace: key.Object.Namespace, Name: key.Object.Name, UID: key.UID}
}
func (r *Runtime) enqueueRescanLocked(namespace string) {
	r.inputQueue.Add(inputKey{Object: objectKey{Namespace: namespace}, Rescan: true})
}
func (r *Runtime) enqueueObjectLocked(object *unstructured.Unstructured) {
	r.enqueuePodLocked(object)
	if r.isCandidateLocked(object.GroupVersionKind()) {
		r.rootQueue.Add(rootKey{Candidate: inputFor(refOf(object))})
	}
}
func (r *Runtime) enqueuePodLocked(object *unstructured.Unstructured) {
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
func (r *Runtime) isCandidateLocked(gvk schema.GroupVersionKind) bool {
	selection := r.selections[gvk.GroupKind()]
	return selection.Definition != nil && selection.GVK == gvk
}
func (r *Runtime) fanoutRootLocked(uid types.UID) {
	root := r.roots[uid]
	if root == nil {
		return
	}
	r.rootQueue.Add(rootKey{UID: uid})
	r.enqueueMembersLocked(root)
}
func (r *Runtime) enqueueMembersLocked(root *rootState) {
	for _, key := range root.pods {
		r.inputQueue.Add(key)
	}
	for key := range root.candidates {
		r.rootQueue.Add(rootKey{Candidate: key})
	}
}
func (r *Runtime) rootLocked(workload *discovery.GenericWorkload) *rootState {
	for _, ref := range workload.Path[1:] {
		if ref.GVK == podGVK {
			r.ancestorPods[ref.UID] = true
		}
	}
	root := r.roots[workload.Root.UID]
	if root == nil {
		root = &rootState{ref: workload.Root, pods: map[types.UID]inputKey{}, candidates: map[inputKey]candidateState{}}
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
func (r *Runtime) removePodLocked(key inputKey) {
	if root := r.roots[r.podRoots[key.UID]]; root != nil {
		delete(root.pods, key.UID)
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	delete(r.podRoots, key.UID)
	r.deps.Publications.RemovePod(key.UID)
}
func (r *Runtime) removeCandidateLocked(key inputKey) {
	if root := r.roots[r.candidates[key]]; root != nil {
		delete(root.candidates, key)
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	delete(r.candidates, key)
	r.deps.Publications.RemoveDiscoveryFailure(key.UID)
}
func (r *Runtime) sourcesFor(path []discovery.ObjectRef) []store.SourceKey {
	r.reader.mu.RLock()
	defer r.reader.mu.RUnlock()
	var sources []store.SourceKey
	for _, ref := range path {
		if entry, ok := r.reader.objects[keyOf(ref)]; ok && !slices.Contains(sources, entry.source) {
			sources = append(sources, entry.source)
		}
	}
	for key, source := range r.reader.sources {
		if source.gvk == namespaceGVK && r.config.NamespaceSelector != nil {
			if !slices.Contains(sources, key) {
				sources = append(sources, key)
			}
		}
	}
	if r.config.ClusterDefinitions {
		sources = append(sources, definitionSource)
	}
	return sources
}
func appendSources(sources, additional []store.SourceKey) []store.SourceKey {
	result := slices.Clone(sources)
	for _, key := range additional {
		if !slices.Contains(result, key) {
			result = append(result, key)
		}
	}
	return result
}

func (r *Runtime) namespaceAdmitted(namespace string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config.NamespaceSelector == nil || namespace == "" || r.scopes[namespace]
}
