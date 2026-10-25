// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type Runner interface{ Run(context.Context) error }
type Dependencies struct {
	Discovery    discovery.Discoverer
	Inventory    Inventory
	Labels       labelresolver.Resolver
	Publications store.Writer
}

// Inventory provides both lookup and authoritative snapshots for controller watches.
type Inventory interface {
	inventory.Interface
	Selections() ([]inventory.Selection, error)
	ClusterOrigins() ([]inventory.Origin, error)
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
	Now                  func() time.Time
}

// Runtime orders accepted events and final publication under its own guard.
// Store methods acquire only Store's lock, after the Runtime guard.
type Runtime struct {
	primeAttempts                                  map[watchKey]time.Time
	watchFailures                                  map[discovery.WatchRequest]error
	mu                                             sync.Mutex
	config                                         Config
	deps                                           Dependencies
	reader                                         *Reader
	controlQueue                                   workqueue.TypedRateLimitingInterface[queueKey]
	rootQueue                                      workqueue.TypedRateLimitingInterface[queueKey]
	queue                                          workqueue.TypedRateLimitingInterface[queueKey]
	watches                                        map[watchKey]*resourceWatch
	roots                                          map[types.UID]*rootState
	podRoots                                       map[types.UID]types.UID
	definitionKeys                                 map[schema.GroupKind]string
	pendingDefinitions                             map[schema.GroupKind]bool
	selections                                     map[schema.GroupKind]inventory.Selection
	definitionKey                                  string
	definitionError                                error
	sequence                                       uint64
	generation                                     uint64
	ctx                                            context.Context
	wg                                             sync.WaitGroup
	started                                        bool
	namespaceSelector, objectSelector, podSelector labels.Selector
}

type queueKey struct {
	Operation string
	Object    objectKey
	UID       types.UID
	Watch     watchKey
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
	informer     cache.SharedIndexInformer
	cancel       context.CancelFunc
	ready        bool
	generation   uint64
	failureEpoch uint64
	verifiedRead uint64
	failureKey   string
	err          error
	verifiedAt   time.Time
}
type rootState struct {
	resolutionErrors    map[schema.GroupVersionKind]resolutionFailure
	labelFailures       map[types.UID]bool
	publishedOrigin     inventory.Origin
	publishedGVK        schema.GroupVersionKind
	dirty               map[types.UID]bool
	podErrors           map[types.UID]error
	podKeys             map[types.UID]string
	retired             map[string]bool
	cached              map[types.UID]cachedPod
	fullRefresh         bool
	publishedGeneration uint64
	watch               watchKey
	resolution          discovery.OwnerResolution
	ref                 discovery.ObjectRef
	paths               map[types.UID][]discovery.ObjectRef
	generation          uint64
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
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RefreshInterval <= 0 || config.MaxAge <= config.RefreshInterval || config.Workers < 1 {
		return nil, errors.New("workers and refresh interval must be positive; max age must exceed refresh interval")
	}
	if config.NamespaceSelector != nil && len(config.NamespaceSelector.MatchLabels) == 0 && len(config.NamespaceSelector.MatchExpressions) == 0 {
		config.NamespaceSelector = nil
	}
	r := &Runtime{watchFailures: map[discovery.WatchRequest]error{}, config: config, deps: deps, reader: reader, watches: map[watchKey]*resourceWatch{}, roots: map[types.UID]*rootState{}, podRoots: map[types.UID]types.UID{}, definitionKeys: map[schema.GroupKind]string{}, pendingDefinitions: map[schema.GroupKind]bool{}, selections: map[schema.GroupKind]inventory.Selection{}, definitionKey: "definitions:0", queue: newQueue("exporter-objects"), rootQueue: newQueue("exporter-roots"), controlQueue: newQueue("exporter-sources")}
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

func newQueue(name string) workqueue.TypedRateLimitingInterface[queueKey] {
	return workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.NewTypedItemExponentialFailureRateLimiter[queueKey](10*time.Millisecond, 10*time.Second), workqueue.TypedRateLimitingQueueConfig[queueKey]{Name: name})
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
	for i := 0; i < r.config.Workers; i++ {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.worker(ctx, r.queue) }()
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.worker(ctx, r.rootQueue) }()
	}
	for i := 0; i < 2; i++ {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.worker(ctx, r.controlQueue) }()
	}
	r.controlQueue.Add(queueKey{Operation: "sync"})
	refresh := time.NewTicker(r.config.RefreshInterval)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			r.queue.ShutDown()
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
			r.controlQueue.Add(queueKey{Operation: "metadata"})
			r.reader.expireMetadata()
			r.mu.Lock()
			for key := range r.watches {
				r.controlQueue.Add(queueKey{Operation: "verify", Watch: key})
			}
			for uid, root := range r.roots {
				root.fullRefresh = true
				for podUID, path := range root.paths {
					if len(path) > 0 {
						r.queue.Add(queueKey{Operation: "object", Object: keyOf(path[0]), UID: podUID})
					}
				}
				r.rootQueue.Add(queueKey{Operation: "root", UID: uid})
			}
			r.mu.Unlock()
			r.controlQueue.Add(queueKey{Operation: "sync"})
		}
	}
}

func (r *Runtime) worker(ctx context.Context, queue workqueue.TypedRateLimitingInterface[queueKey]) {
	for {
		key, shutdown := queue.Get()
		if shutdown {
			return
		}
		var err error
		switch key.Operation {
		case "sync":
			err = r.syncWatches(ctx)
		case "verify":
			err = r.verifyWatch(ctx, key.Watch)
		case "prime":
			err = r.primeMetadata(ctx)
		case "metadata":
			r.pruneMetadata()
			err = r.reader.refreshMetadata(ctx)
			r.mu.Lock()
			for uid, root := range r.roots {
				for pod, path := range root.paths {
					if len(path) > 0 {
						r.queue.Add(queueKey{Operation: "object", Object: keyOf(path[0]), UID: pod})
					}
				}
				r.rootQueue.Add(queueKey{Operation: "root", UID: uid})
			}
			r.publishHealthLocked()
			r.mu.Unlock()
		case "object":
			err = r.resolve(ctx, key)
		case "root":
			err = r.reconcile(ctx, key.UID)
		}
		if err != nil && ctx.Err() == nil {
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
	if !r.started || r.definitionError != nil || len(r.watches) == 0 || len(r.pendingDefinitions) > 0 {
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
		if !w.ready || w.err != nil || !r.config.Now().Before(w.verifiedAt.Add(r.config.MaxAge)) {
			return false
		}
	}
	return true
}

func (r *Runtime) requiredWatch(gvk schema.GroupVersionKind) bool {
	return gvk == podGVK || (gvk == kartaGVK && r.config.Definitions.Cluster.Enabled) || (gvk == namespaceGVK && r.config.NamespaceSelector != nil) || slices.Contains(r.config.RequiredKinds, gvk)
}

func (r *Runtime) refreshDefinitions() error {
	selected, err := r.deps.Inventory.Selections()
	next := make(map[schema.GroupKind]inventory.Selection, len(selected))
	for _, selection := range selected {
		next[selection.GVK.GroupKind()] = selection
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		if pending := r.definitionsConvergedLocked(); pending != nil {
			return pending
		}
	}
	if err != nil {
		if r.definitionError == nil {
			r.deps.Publications.SetPublicationFailure(r.definitionKey, failureReason(err))
			r.definitionError = err
			for uid, root := range r.roots {
				root.generation++
				r.rootQueue.AddAfter(queueKey{Operation: "root", UID: uid}, 20*time.Millisecond)
			}
		}
		return err
	}
	recovered := r.definitionError != nil
	if recovered {
		r.sequence++
		r.definitionKey = fmt.Sprintf("definitions:%d", r.sequence)
		r.definitionError = nil
	}
	changed := recovered || len(r.pendingDefinitions) > 0 || !sameSelections(r.selections, next)
	if !changed {
		return nil
	}
	previous := r.selections
	r.selections = next
	for uid, root := range r.roots {
		gk := root.ref.GVK.GroupKind()
		if recovered || r.pendingDefinitions[gk] || definitionChanged(previous[gk], next[gk]) {
			root.generation++
			r.rootQueue.AddAfter(queueKey{Operation: "root", UID: uid}, 20*time.Millisecond)
		}
	}
	clear(r.pendingDefinitions)
	usedKinds := map[schema.GroupKind]bool{}
	for _, root := range r.roots {
		usedKinds[root.ref.GVK.GroupKind()] = true
	}
	for gk, key := range r.definitionKeys {
		if _, selected := r.selections[gk]; !selected && !usedKinds[gk] {
			r.deps.Publications.RetirePublicationFailure(key)
			delete(r.definitionKeys, gk)
		}
	}
	r.controlQueue.Add(queueKey{Operation: "sync"})
	return nil
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

func sameSelections(a, b map[schema.GroupKind]inventory.Selection) bool {
	return maps.EqualFunc(a, b, func(x, y inventory.Selection) bool { return x.GVK == y.GVK && x.Origin == y.Origin })
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
		r.rootQueue.AddAfter(queueKey{Operation: "root", UID: uid}, 20*time.Millisecond)
	}
}
func (r *Runtime) dirtyPod(root *rootState, uid types.UID) {
	root.dirty[uid] = true
	r.rootQueue.AddAfter(queueKey{Operation: "root", UID: root.ref.UID}, 20*time.Millisecond)
}

func (r *Runtime) invalidatePodLocked(root *rootState, uid types.UID, reason string) {
	if key := root.podKeys[uid]; key != "" {
		r.deps.Publications.SetPublicationFailure(key, reason)
		root.retired[key] = true
	}
	r.sequence++
	root.podKeys[uid] = fmt.Sprintf("pod:%s:%d", uid, r.sequence)
	r.dirtyPod(root, uid)
}

func (r *Runtime) retireFailuresLocked(root *rootState) {
	for key := range root.retired {
		r.deps.Publications.RetirePublicationFailure(key)
		delete(root.retired, key)
	}
}

func (r *Runtime) removeRoot(uid types.UID) {
	if root := r.roots[uid]; root != nil {
		for pod := range root.paths {
			delete(r.podRoots, pod)
		}
	}
	root := r.roots[uid]
	delete(r.roots, uid)
	r.deps.Publications.RemovePublication(uid)
	if root != nil {
		for _, key := range root.podKeys {
			r.deps.Publications.RetirePublicationFailure(key)
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

func (r *Runtime) definitionsConvergedLocked() error {
	if !r.config.Definitions.Cluster.Enabled {
		return nil
	}
	origins, err := r.deps.Inventory.ClusterOrigins()
	if err != nil {
		return err
	}
	expected := map[string]inventory.Origin{}
	filter, err := selector(r.config.Definitions.Cluster.LabelSelector)
	if err != nil {
		return err
	}
	ready := false
	var definitionObjects map[objectKey]struct{}
	for _, w := range r.watches {
		if w.gvk == kartaGVK {
			ready = w.ready && w.err == nil
			definitionObjects = w.objects
		}
	}
	if !ready {
		return discovery.ErrPending
	}
	r.reader.mu.RLock()
	defer r.reader.mu.RUnlock()
	cfg := r.config.Definitions.Cluster
	for key := range definitionObjects {
		entry := r.reader.objects[key]
		object := entry.object
		if object == nil {
			continue
		}
		if slices.Contains(cfg.Exclude, object.GetName()) || (len(cfg.Include) > 0 && !slices.Contains(cfg.Include, object.GetName())) || !filter.Matches(labelsFor(object)) {
			continue
		}
		if entry.evidence.ExpiresAt.IsZero() || !r.config.Now().Before(entry.evidence.ExpiresAt) {
			return discovery.ErrPending
		}
		expected[object.GetName()] = inventory.Origin{Source: inventory.SourceCluster, Name: object.GetName(), UID: object.GetUID(), ResourceVersion: object.GetResourceVersion()}
	}
	if len(expected) != len(origins) {
		return discovery.ErrPending
	}
	for _, origin := range origins {
		if expected[origin.Name] != origin {
			return discovery.ErrPending
		}
	}
	return nil
}

func (r *rootState) failResolution(gvk schema.GroupVersionKind, cause error) {
	if r.resolutionErrors == nil {
		r.resolutionErrors = map[schema.GroupVersionKind]resolutionFailure{}
	}
	failure := r.resolutionErrors[gvk]
	failure.cause = cause
	r.resolutionErrors[gvk] = failure
}
