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

// Inventory provides both lookup and authoritative snapshots for controller watches.
type Inventory interface {
	inventory.Interface
	Selections() []inventory.Selection
	ReplaceCluster([]*v1alpha1.Karta)
}

type Config struct {
	IgnoredOwnerKinds    []schema.GroupVersionKind
	ResourceRules        []ResourceRule
	ExcludeResourceRules []ResourceRule
	NamespaceSelector    *metav1.LabelSelector
	ObjectSelector       *metav1.LabelSelector
	PodSelector          *metav1.LabelSelector
	Definitions          inventory.Config
	RequiredKinds        []schema.GroupVersionKind
	RefreshInterval      time.Duration
	MaxAge               time.Duration
	Workers              int
	LeafWorkers          int
	RootWorkers          int
	Now                  func() time.Time
}

type ResourceRule struct {
	APIGroups   []string `json:"apiGroups"`
	APIVersions []string `json:"apiVersions"`
	Resources   []string `json:"resources"`
}

// Runtime orders accepted events and final publication under its own guard.
// Store methods acquire only Store's lock, after the Runtime guard.
type Runtime struct {
	pathDependents                                 map[objectKey]map[pathConsumer]pathDependent
	primeAttempts                                  map[watchKey]time.Time
	watchFailures                                  map[discovery.WatchRequest]error
	mu                                             sync.Mutex
	config                                         Config
	deps                                           Dependencies
	reader                                         *Reader
	controlQueue                                   workqueue.TypedRateLimitingInterface[sourceKey]
	rootQueue                                      workqueue.TypedRateLimitingInterface[rootKey]
	inputQueue                                     workqueue.TypedRateLimitingInterface[inputKey]
	watches                                        map[watchKey]*resourceWatch
	roots                                          map[types.UID]*rootState
	candidates                                     map[types.UID]map[objectKey]*rootCandidate
	podRoots                                       map[types.UID]types.UID
	definitionKeys                                 map[schema.GroupKind]string
	selections                                     map[schema.GroupKind]inventory.Selection
	definitionKey                                  string
	definitionError                                error
	definitionsDirty                               bool
	sequence                                       uint64
	generation                                     uint64
	ctx                                            context.Context
	wg                                             sync.WaitGroup
	started                                        bool
	namespaceSelector, objectSelector, podSelector labels.Selector
}

type inputKey struct {
	Object objectKey
	UID    types.UID
}
type rootKey struct{ UID types.UID }
type sourceKey struct {
	Operation sourceOperation
	Watch     watchKey
}
type sourceOperation uint8

const (
	syncSources sourceOperation = iota
	verifySource
	primeOwners
	refreshOwners
	restartSource
)

type rootCandidate struct {
	ref   discovery.ObjectRef
	path  []discovery.ObjectRef
	dirty bool
}
type watchKey struct {
	Resource  schema.GroupVersionResource
	Namespace string
}
type resourceWatch struct {
	pendingRoots map[types.UID]discovery.ObjectRef
	discover     bool
	objects      map[objectKey]struct{}
	key          watchKey
	gvk          schema.GroupVersionKind
	restart      chan struct{}
	relist       bool
	replacement  *sourceRead
	cancel       context.CancelFunc
	ctx          context.Context
	ready        bool
	generation   uint64
	failureEpoch uint64
	verifiedRead uint64
	failureKey   string
	err          error
	verifiedAt   time.Time
}
type rootState struct {
	renewalAt           time.Time
	publicationDeadline time.Time
	publicationHealthy  bool
	resolutionErrors    map[schema.GroupVersionKind]resolutionFailure
	labelFailures       int
	labelObject         discovery.ObjectRef
	resolveLabels       labelresolver.ResolveFunc
	publishedOrigin     inventory.Origin
	publishedGVK        schema.GroupVersionKind
	dirty               map[types.UID]bool
	retired             []string
	pods                map[types.UID]podState
	fullRefresh         bool
	publishedGeneration uint64
	watch               watchKey
	resolution          discovery.OwnerResolution
	ref                 discovery.ObjectRef
	generation          uint64
}
type podState struct {
	path         []discovery.ObjectRef
	proofKey     string
	resolveError error
	cached       *cachedPod
	labelFailure bool
}

type resolutionFailure struct {
	cause       error
	verifyAfter uint64
}

type cachedPod struct {
	resourceVersion string
	generation      uint64
	record          store.PodRecord
	reason          string
	labelFailure    bool
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
	if config.RefreshInterval == 0 {
		config.RefreshInterval = 45 * time.Second
	}
	if config.MaxAge == 0 {
		config.MaxAge = 2 * time.Minute
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
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RefreshInterval <= 0 || config.MaxAge <= config.RefreshInterval || config.Workers < 1 || config.LeafWorkers < 1 || config.RootWorkers < 1 {
		return nil, errors.New("workers and refresh interval must be positive; max age must exceed refresh interval")
	}
	if config.NamespaceSelector != nil && len(config.NamespaceSelector.MatchLabels) == 0 && len(config.NamespaceSelector.MatchExpressions) == 0 {
		config.NamespaceSelector = nil
	}
	r := &Runtime{pathDependents: map[objectKey]map[pathConsumer]pathDependent{}, watchFailures: map[discovery.WatchRequest]error{}, config: config, deps: deps, reader: reader, watches: map[watchKey]*resourceWatch{}, roots: map[types.UID]*rootState{}, podRoots: map[types.UID]types.UID{}, definitionKeys: map[schema.GroupKind]string{}, selections: map[schema.GroupKind]inventory.Selection{}, definitionKey: "definitions:0", definitionsDirty: true, candidates: map[types.UID]map[objectKey]*rootCandidate{}, inputQueue: newQueue[inputKey]("exporter-inputs"), rootQueue: newQueue[rootKey]("exporter-roots"), controlQueue: newQueue[sourceKey]("exporter-sources")}
	if config.Definitions.Cluster.Enabled {
		r.definitionError = discovery.ErrPending
		deps.Publications.SetPublicationFailure(r.definitionKey, store.ReasonAPIUnavailable)
	}
	reader.publications = deps.Publications
	reader.watchNow, reader.watchMaxAge = config.Now, config.MaxAge
	reader.ignoredOwners = append([]schema.GroupVersionKind(nil), config.IgnoredOwnerKinds...)
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
		go func() {
			defer r.wg.Done()
			runWorker(ctx, r.rootQueue, func(ctx context.Context, key rootKey) error { return r.reconcile(ctx, key.UID) })
		}()
	}
	for i := 0; i < 2; i++ {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); runWorker(ctx, r.controlQueue, r.maintainSource) }()
	}
	r.controlQueue.Add(sourceKey{Operation: syncSources})
	refresh := time.NewTicker(r.config.RefreshInterval)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			r.inputQueue.ShutDown()
			r.rootQueue.ShutDown()
			r.controlQueue.ShutDown()
			r.mu.Lock()
			for _, w := range r.watches {
				w.cancel()
			}
			r.mu.Unlock()
			r.wg.Wait()
			return nil
		case <-refresh.C:
			r.controlQueue.Add(sourceKey{Operation: refreshOwners})
			r.mu.Lock()
			for key := range r.watches {
				r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: key})
			}
			r.mu.Unlock()
			r.controlQueue.Add(sourceKey{Operation: syncSources})
		}
	}
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
func (r *Runtime) maintainSource(ctx context.Context, key sourceKey) error {
	switch key.Operation {
	case syncSources:
		return r.syncWatches(ctx)
	case verifySource:
		return r.verifyWatch(ctx, key.Watch)
	case primeOwners:
		return r.primeMetadata(ctx)
	case refreshOwners:
		return r.refreshMetadata(ctx)
	case restartSource:
		r.mu.Lock()
		defer r.mu.Unlock()
		if w := r.watches[key.Watch]; w != nil && w.relist {
			select {
			case w.restart <- struct{}{}:
			default:
			}
		}
		return nil
	default:
		return nil
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
		if !r.requiredWatch(w.gvk) {
			continue
		}
		if !w.ready || w.relist || w.err != nil || !r.config.Now().Before(w.verifiedAt.Add(r.config.MaxAge)) {
			return false
		}
	}
	return true
}

func (r *Runtime) requiredWatch(gvk schema.GroupVersionKind) bool {
	return gvk == podGVK || (gvk == kartaGVK && r.config.Definitions.Cluster.Enabled) || (gvk == namespaceGVK && r.config.NamespaceSelector != nil) || slices.Contains(r.config.RequiredKinds, gvk)
}

func (r *Runtime) refreshDefinitions() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshDefinitionsLocked()
}

// Runtime holds the only cluster snapshot. Reader's lock is released before
// replacing the inventory; the inventory never calls back into Runtime.
func (r *Runtime) refreshDefinitionsLocked() error {
	var definitions []*v1alpha1.Karta
	var sourceErr error
	if r.config.Definitions.Cluster.Enabled {
		sourceErr = discovery.ErrPending
		for _, w := range r.watches {
			if w.gvk != kartaGVK {
				continue
			}
			switch {
			case w.err != nil:
				sourceErr = w.err
			case !w.ready:
				sourceErr = discovery.ErrPending
			case !r.config.Now().Before(w.verifiedAt.Add(r.config.MaxAge)):
				sourceErr = discovery.ErrUnavailable
			default:
				sourceErr = nil
				r.reader.mu.RLock()
				for key := range w.objects {
					entry := r.reader.objects[key]
					if entry.object == nil || !r.config.Now().Before(entry.evidence.ExpiresAt) {
						sourceErr = discovery.ErrPending
						break
					}
					for _, proof := range entry.evidence.Dependencies {
						if !r.reader.activeKeys[proof] {
							sourceErr = discovery.ErrPending
						}
					}
					if !r.definitionsDirty {
						continue
					}
					definition := &v1alpha1.Karta{}
					if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(entry.object.Object, definition); err != nil {
						sourceErr = fmt.Errorf("decode Karta %s: %w", entry.object.GetName(), err)
						break
					}
					definitions = append(definitions, definition)
				}
				r.reader.mu.RUnlock()
			}
			break
		}
	}
	if sourceErr != nil {
		if r.definitionError == nil {
			r.deps.Publications.SetPublicationFailure(r.definitionKey, failureReason(sourceErr))
			for uid := range r.roots {
				r.touchRoot(uid)
			}
		}
		r.definitionError = sourceErr
		return sourceErr
	}
	if !r.definitionsDirty && r.definitionError == nil {
		return nil
	}
	if r.definitionsDirty {
		r.deps.Inventory.ReplaceCluster(definitions)
		r.definitionsDirty = false
	}
	selected := r.deps.Inventory.Selections()
	next := make(map[schema.GroupKind]inventory.Selection, len(selected))
	for _, selection := range selected {
		next[selection.GVK.GroupKind()] = selection
	}
	recovered := r.definitionError != nil
	if recovered {
		r.deps.Publications.RetirePublicationFailure(r.definitionKey)
		r.sequence++
		r.definitionKey = fmt.Sprintf("definitions:%d", r.sequence)
		r.definitionError = nil
	}
	changedKinds := make(map[schema.GroupKind]bool)
	for gk, previous := range r.selections {
		if definitionChanged(previous, next[gk]) {
			changedKinds[gk] = true
		}
	}
	for gk, selection := range next {
		if definitionChanged(r.selections[gk], selection) {
			changedKinds[gk] = true
		}
	}
	for gk := range changedKinds {
		r.invalidateDefinitionLocked(gk)
	}
	r.selections = next
	usedKinds := map[schema.GroupKind]bool{}
	for uid, root := range r.roots {
		gk := root.ref.GVK.GroupKind()
		usedKinds[gk] = true
		if recovered || changedKinds[gk] {
			r.touchRoot(uid)
		}
	}
	for gk, key := range r.definitionKeys {
		if _, selected := next[gk]; !selected && !usedKinds[gk] {
			r.deps.Publications.RetirePublicationFailure(key)
			delete(r.definitionKeys, gk)
		}
	}
	if recovered || len(changedKinds) > 0 {
		r.controlQueue.Add(sourceKey{Operation: syncSources})
	}
	return nil
}

func (r *Runtime) lookupDefinition(gvk schema.GroupVersionKind) (inventory.Selection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookupDefinitionLocked(gvk)
}

func (r *Runtime) lookupDefinitionLocked(gvk schema.GroupVersionKind) (inventory.Selection, error) {
	if r.definitionError != nil {
		return r.selections[gvk.GroupKind()], r.definitionError
	}
	return r.deps.Inventory.Lookup(gvk)
}

func definitionChanged(a, b inventory.Selection) bool {
	if a.GVK != b.GVK || a.Origin.Source != b.Origin.Source || a.Origin.Name != b.Origin.Name || a.Origin.UID != b.Origin.UID {
		return true
	}
	if a.Definition == nil || b.Definition == nil {
		return a.Definition != b.Definition
	}
	return !reflect.DeepEqual(a.Definition.Spec, b.Definition.Spec)
}

func (r *Runtime) definitionProofLocked(gk schema.GroupKind) string {
	if r.definitionKeys[gk] == "" {
		r.sequence++
		r.definitionKeys[gk] = fmt.Sprintf("definition:%s:%d", gk, r.sequence)
	}
	return r.definitionKeys[gk]
}
func (r *Runtime) invalidateDefinitionLocked(gk schema.GroupKind) {
	key := r.definitionProofLocked(gk)
	r.deps.Publications.SetPublicationFailure(key, store.ReasonAPIUnavailable)
	r.deps.Publications.RetirePublicationFailure(key)
	delete(r.definitionKeys, gk)
	r.definitionProofLocked(gk)
}

func (r *Runtime) touchRoot(uid types.UID) {
	if root := r.roots[uid]; root != nil {
		root.generation++
		r.rootQueue.AddAfter(rootKey{UID: uid}, 20*time.Millisecond)
	}
}
func (r *Runtime) dirtyPod(root *rootState, uid types.UID) {
	root.dirty[uid] = true
	r.rootQueue.AddAfter(rootKey{UID: root.ref.UID}, 20*time.Millisecond)
}

func (r *Runtime) invalidatePodLocked(root *rootState, uid types.UID, reason string) {
	pod := root.pods[uid]
	if key := pod.proofKey; key != "" {
		r.deps.Publications.SetPublicationFailure(key, reason)
		root.retired = append(root.retired, key)
	}
	r.sequence++
	pod.proofKey = fmt.Sprintf("pod:%s:%d", uid, r.sequence)
	root.pods[uid] = pod
	r.dirtyPod(root, uid)
}

func (r *Runtime) retireFailuresLocked(root *rootState) {
	for _, key := range root.retired {
		r.deps.Publications.RetirePublicationFailure(key)
	}
	root.retired = nil
}

func (r *Runtime) removeRoot(uid types.UID) {
	if root := r.roots[uid]; root != nil {
		for pod := range root.pods {
			r.removePodPathLocked(root, pod)
			delete(r.podRoots, pod)
		}
	}
	root := r.roots[uid]
	delete(r.roots, uid)
	r.deps.Publications.RemovePublication(uid)
	if root != nil {
		for _, pod := range root.pods {
			r.deps.Publications.RetirePublicationFailure(pod.proofKey)
		}
		r.retireFailuresLocked(root)
	}
}

func (r *Runtime) publishHealthLocked() {
	health := make([]store.HealthRecord, 0, len(r.watches))
	for _, w := range r.watches {
		evidence := store.Evidence{ExpiresAt: w.verifiedAt.Add(r.config.MaxAge), Dependencies: []string{w.failureKey}}
		if w.err != nil {
			evidence.Reason = failureReason(w.err)
		} else if !w.ready {
			evidence.Reason = store.ReasonAPIUnavailable
		} else if _, err := r.deps.Inventory.Lookup(w.gvk); errors.Is(err, inventory.ErrInvalidDefinition) {
			evidence.Reason = store.ReasonInvalidDefinition
		}
		if w.gvk == kartaGVK {
			evidence.Dependencies = append(evidence.Dependencies, r.definitionKey)
			if r.definitionError != nil {
				evidence.Reason = failureReason(r.definitionError)
			}
		}
		health = append(health, store.HealthRecord{GVK: w.gvk, Namespace: w.key.Namespace, Evidence: evidence})
	}

	failures := r.reader.failureHealth()
	for key, err := range r.watchFailures {
		failures[key] = err
	}
	for key, err := range failures {
		merged := false
		for i := range health {
			if health[i].GVK == key.GVK && health[i].Namespace == key.Namespace {
				health[i].Evidence.Reason = failureReason(err)
				merged = true
				break
			}
		}
		if merged {
			continue
		}
		health = append(health, store.HealthRecord{GVK: key.GVK, Namespace: key.Namespace, Evidence: store.Evidence{Reason: failureReason(err)}})
	}
	r.deps.Publications.SetPublicationHealth(health)
}

func mergeEvidence(values ...store.Evidence) store.Evidence {
	result := store.Evidence{}
	for _, value := range values {
		if result.ExpiresAt.IsZero() || value.ExpiresAt.Before(result.ExpiresAt) {
			result.ExpiresAt = value.ExpiresAt
		}
		if value.Reason != "" {
			result.Reason = value.Reason
		}
		for _, dependency := range value.Dependencies {
			if !slices.Contains(result.Dependencies, dependency) {
				result.Dependencies = append(result.Dependencies, dependency)
			}
		}
	}
	return result
}

func (r *rootState) failResolution(gvk schema.GroupVersionKind, cause error) {
	if r.resolutionErrors == nil {
		r.resolutionErrors = map[schema.GroupVersionKind]resolutionFailure{}
	}
	failure := r.resolutionErrors[gvk]
	failure.cause = cause
	r.resolutionErrors[gvk] = failure
}
