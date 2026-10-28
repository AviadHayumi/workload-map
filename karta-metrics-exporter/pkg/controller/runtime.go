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
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/instructions"
)

type Runner interface {
	Run(context.Context) error
	Ready() bool
}
type LabelResolver interface {
	ForWorkload(*unstructured.Unstructured) labelresolver.ResolveFunc
}
type Dependencies struct {
	Discovery    discovery.Discoverer
	Inventory    Inventory
	Labels       LabelResolver
	Publications store.Writer
}
type Inventory interface {
	inventory.Interface
	Selections() []inventory.Selection
	ReplaceCluster([]*v1alpha1.Karta)
}
type Config struct {
	IgnoredOwnerKinds                              []schema.GroupVersionKind
	ResourceRules, ExcludeResourceRules            []ResourceRule
	NamespaceSelector, ObjectSelector, PodSelector *metav1.LabelSelector
	Definitions                                    inventory.Config
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
	Sweep  bool
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
}

type candidateState struct {
	sources []store.SourceKey
	err     error
}

var podGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
var kartaGVK = schema.GroupVersionKind{Group: "run.ai", Version: "v1alpha1", Kind: "Karta"}
var namespaceGVK = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}

func New(config Config, deps Dependencies, reader *Reader) (*Runtime, error) {
	if deps.Discovery == nil || deps.Inventory == nil || deps.Labels == nil || deps.Publications == nil || reader == nil {
		return nil, errors.New("runtime dependencies are required")
	}
	if err := ValidateResourceRules(append(append([]ResourceRule{}, config.ResourceRules...), config.ExcludeResourceRules...)); err != nil {
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
	if config.Definitions.Cluster.Enabled {
		r.definitionError = discovery.ErrPending
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
func newQueue[T comparable](name string) workqueue.TypedRateLimitingInterface[T] {
	return workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.NewTypedItemExponentialFailureRateLimiter[T](10*time.Millisecond, 10*time.Second), workqueue.TypedRateLimitingQueueConfig[T]{Name: name})
}
func selector(value *metav1.LabelSelector) (labels.Selector, error) {
	if value == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(value)
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
	<-ctx.Done()
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
func (r *Runtime) requiredWatch(gvk schema.GroupVersionKind) bool {
	return gvk == podGVK || (gvk == kartaGVK && r.config.Definitions.Cluster.Enabled) || (gvk == namespaceGVK && r.config.NamespaceSelector != nil) || slices.Contains(r.config.RequiredKinds, gvk)
}

func (r *Runtime) refreshDefinitionsLocked() error {
	var definitions []*v1alpha1.Karta
	if r.config.Definitions.Cluster.Enabled {
		found := false
		for _, w := range r.watches {
			if w.gvk != kartaGVK {
				continue
			}
			found = true
			if !w.ready {
				r.definitionError = discovery.ErrPending
				return r.definitionError
			}
			if w.err != nil {
				r.definitionError = w.err
				return w.err
			}
			r.reader.mu.RLock()
			for key := range w.objects {
				object := r.reader.objects[key].object
				definition := &v1alpha1.Karta{}
				if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(object.Object, definition); err != nil {
					r.reader.mu.RUnlock()
					r.definitionError = err
					return err
				}
				definitions = append(definitions, definition)
			}
			r.reader.mu.RUnlock()
		}
		if !found {
			r.definitionError = discovery.ErrPending
			return r.definitionError
		}
		r.deps.Inventory.ReplaceCluster(definitions)
	}
	next := map[schema.GroupKind]selectedDefinition{}
	for _, selection := range r.deps.Inventory.Selections() {
		accepted, err := r.deps.Inventory.Lookup(selection.GVK)
		selected := selectedDefinition{Selection: accepted, err: err}
		if err == nil && accepted.Definition != nil {
			selected.summary, err = instructions.NewStructureSummary(accepted.Definition)
			if err != nil {
				selected.err = fmt.Errorf("%w: %w", discovery.ErrInvalidDefinition, err)
			}
		}
		next[selection.GVK.GroupKind()] = selected
	}
	changed := r.definitionError != nil || !reflect.DeepEqual(r.selections, next)
	r.definitionError = nil
	r.selections = next
	if changed {
		r.enqueueSweepLocked("")
		for uid := range r.roots {
			r.rootQueue.Add(rootKey{UID: uid})
		}
	}
	return nil
}
func (r *Runtime) lookupDefinition(gvk schema.GroupVersionKind) (selectedDefinition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.definitionError != nil {
		return r.selections[gvk.GroupKind()], r.definitionError
	}
	selected := r.selections[gvk.GroupKind()]
	return selected, selected.err
}

func inputFor(ref discovery.ObjectRef) inputKey { return inputKey{Object: keyOf(ref), UID: ref.UID} }
func (key inputKey) ref() discovery.ObjectRef {
	return discovery.ObjectRef{GVK: key.Object.GVK, Namespace: key.Object.Namespace, Name: key.Object.Name, UID: key.UID}
}
func (r *Runtime) enqueueSweepLocked(namespace string) {
	r.inputQueue.Add(inputKey{Object: objectKey{Namespace: namespace}, Sweep: true})
}
func (r *Runtime) enqueueObjectLocked(ref discovery.ObjectRef) {
	key := inputFor(ref)
	if ref.GVK == podGVK {
		r.inputQueue.Add(key)
	}
	if selection := r.selections[ref.GVK.GroupKind()]; selection.Definition != nil && selection.GVK == ref.GVK {
		r.rootQueue.Add(rootKey{Candidate: key})
	}
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
		if (source.gvk == kartaGVK && r.config.Definitions.Cluster.Enabled) || (source.gvk == namespaceGVK && r.config.NamespaceSelector != nil) {
			if !slices.Contains(sources, key) {
				sources = append(sources, key)
			}
		}
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
