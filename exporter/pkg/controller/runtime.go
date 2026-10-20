// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/submitterextractor"
	v1alpha1 "github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

type ResourceRule struct {
	APIGroups   []string `json:"apiGroups"`
	APIVersions []string `json:"apiVersions"`
	Resources   []string `json:"resources"`
}

type RuntimeOptions struct {
	NamespaceSelector    *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	ObjectSelector       *metav1.LabelSelector `json:"objectSelector,omitempty"`
	PodSelector          *metav1.LabelSelector `json:"podSelector,omitempty"`
	ResourceRules        []ResourceRule        `json:"resourceRules,omitempty"`
	ExcludeResourceRules []ResourceRule        `json:"excludeResourceRules,omitempty"`
	Definitions          kartadefinitioninventory.Options
	RequiredKinds        []schema.GroupVersionKind
	Clock                func() time.Time
	RefreshInterval      time.Duration
	MaxAge               time.Duration
	RequestTimeout       time.Duration
	Workers              int
	PageSize             int64
}

// InformerReader contains accepted observations, separate from informer stores.
// Get never falls back to the API and rejects a store that leads its callback.
type InformerReader struct {
	mu            sync.Mutex
	objects       map[objectKey]objectFact
	byUID         map[types.UID]map[objectKey]struct{}
	byStream      map[discovery.CoverageKey]map[objectKey]struct{}
	streams       map[discovery.CoverageKey]*resourceStream
	mappingErrors map[discovery.CoverageRequest]error
}

type objectKey struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
}

type objectFact struct {
	observation discovery.ObservedObject
	revision    uint64
	metadata    *metav1.PartialObjectMetadata
}

type resourceStream struct {
	key        discovery.CoverageKey
	gvk        schema.GroupVersionKind
	run        uint64
	full       bool
	ready      bool
	failure    uint64
	failureKey string
	err        error
	informer   cache.SharedIndexInformer
	handler    cache.ResourceEventHandlerRegistration
	cancel     context.CancelFunc
	verifiedAt time.Time
}

type queuedWork struct {
	Operation string
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	UID       types.UID
	Full      bool
}

type objectProof struct {
	ref       discovery.ObjectRef
	run       uint64
	expiresAt time.Time
	key       string
}

type membershipProof struct {
	revision uint64
	evidence store.Evidence
	pods     map[types.UID][]discovery.ObjectRef
}

type podListProof struct {
	revision  uint64
	expiresAt time.Time
	key       string
	refs      []discovery.ObjectRef
}

type rootHistory struct {
	selection kartadefinitioninventory.Origin
	record    *store.Publication
}

// Runtime connects Discovery, independent inventory and submitter evaluation
// with informer coverage, bounded API verification and immutable publication.
type Runtime struct {
	mu                   *sync.Mutex
	reader               *InformerReader
	discovery            *discovery.Discovery
	inventory            kartadefinitioninventory.Interface
	submitter            submitterextractor.Interface
	store                *store.Store
	dynamic              dynamic.Interface
	metadata             metadata.Interface
	mapper               meta.RESTMapper
	options              RuntimeOptions
	queue                workqueue.TypedRateLimitingInterface[queuedWork]
	wg                   sync.WaitGroup
	sequence             uint64
	started              bool
	namespaces           map[string]bool
	scopeRevision        uint64
	namespaceReady       bool
	namespaceError       error
	namespaceObjects     map[string]*unstructured.Unstructured
	definitionsReady     bool
	definitionsError     error
	definitionRevision   uint64
	namespaceEvidence    store.Evidence
	definitionEvidence   store.Evidence
	namespaceFailureKey  string
	definitionFailureKey string
	definitions          map[types.UID]*v1alpha1.Karta
	selections           map[schema.GroupKind]kartadefinitioninventory.Selection
	knownKinds           map[schema.GroupKind]schema.GroupVersionKind
	associations         map[types.UID]types.UID
	podsByRoot           map[types.UID]map[types.UID]struct{}
	history              map[types.UID]rootHistory
	rootsByNamespace     map[string]map[types.UID]struct{}
	proofs               map[objectKey]objectProof
	podLists             map[string]podListProof
	membership           map[string]membershipProof
	membershipRevision   map[string]uint64
	retired              map[types.UID]map[discovery.CoverageKey]uint64
	namespaceSelector    labels.Selector
	objectSelector       labels.Selector
	podSelector          labels.Selector
}

func NewInformerReader() *InformerReader {
	return &InformerReader{objects: map[objectKey]objectFact{}, byUID: map[types.UID]map[objectKey]struct{}{}, byStream: map[discovery.CoverageKey]map[objectKey]struct{}{}, streams: map[discovery.CoverageKey]*resourceStream{}, mappingErrors: map[discovery.CoverageRequest]error{}}
}

func (r *InformerReader) Get(ctx context.Context, ref discovery.ObjectRef) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.mappingErrors[discovery.CoverageRequest{GVK: ref.GVK, Namespace: ref.Namespace}]; err != nil {
		return nil, err
	}
	fact, exists := r.objects[refKey(ref)]
	if !exists {
		for _, stream := range r.streams {
			if stream.gvk != ref.GVK || (stream.key.Namespace != ref.Namespace && stream.key.Namespace != "") {
				continue
			}
			if stream.err != nil {
				return nil, stream.err
			}
			if !stream.ready {
				return nil, discovery.ErrPending
			}
			key := ref.Name
			if stream.key.Namespace != "" {
				key = stream.key.Namespace + "/" + ref.Name
			}
			if cached, found, err := stream.informer.GetStore().GetByKey(key); err == nil && found {
				body, err := runtimeObjectBody(cached, ref.GVK)
				if err == nil && body.GetUID() == ref.UID {
					return nil, discovery.ErrStale
				}
			}
			return nil, fmt.Errorf("%w: owner absent from synchronized coverage", discovery.ErrUnresolvedOwner)
		}
		return nil, discovery.ErrPending
	}
	if fact.observation.Ref.UID != ref.UID {
		return nil, fmt.Errorf("%w: owner UID was replaced", discovery.ErrUnresolvedOwner)
	}
	if fact.observation.Ref != ref {
		return nil, fmt.Errorf("%w: accepted object differs", discovery.ErrStale)
	}
	stream := r.streams[fact.observation.WatchKey]
	if stream == nil || stream.run != fact.observation.Run {
		return nil, fmt.Errorf("%w: object coverage is not ready", discovery.ErrPending)
	}
	if stream.err != nil {
		return nil, stream.err
	}
	if !stream.ready {
		return nil, discovery.ErrPending
	}
	key := ref.Name
	if ref.Namespace != "" {
		key = ref.Namespace + "/" + ref.Name
	}
	obj, found, err := stream.informer.GetStore().GetByKey(key)
	if err != nil || !found {
		return nil, fmt.Errorf("%w: cached object missing", discovery.ErrStale)
	}
	result, err := runtimeObjectBody(obj, ref.GVK)
	if err != nil {
		return nil, err
	}
	if result.GetUID() != ref.UID || result.GetResourceVersion() != ref.ResourceVersion {
		return nil, fmt.Errorf("%w: informer advanced before callback", discovery.ErrStale)
	}
	return result, nil
}

func NewRuntime(
	client dynamic.Interface, metadataClient metadata.Interface, mapper meta.RESTMapper,
	reader *InformerReader, d *discovery.Discovery, inventory kartadefinitioninventory.Interface,
	submitter submitterextractor.Interface, publication *store.Store, options RuntimeOptions,
) (*Runtime, error) {
	if client == nil || mapper == nil || reader == nil || d == nil || inventory == nil || publication == nil {
		return nil, errors.New("controller dependencies must be configured")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.RefreshInterval == 0 {
		options.RefreshInterval = 45 * time.Second
	}
	if options.MaxAge == 0 {
		options.MaxAge = 120 * time.Second
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.Workers == 0 {
		options.Workers = 4
	}
	if options.PageSize == 0 {
		options.PageSize = 500
	}
	if options.RefreshInterval <= 0 || options.MaxAge <= options.RefreshInterval ||
		options.RequestTimeout <= 0 || options.Workers < 1 || options.PageSize < 1 {
		return nil, errors.New("invalid controller verification limits")
	}
	selectors := make([]labels.Selector, 0, 3)
	for _, selector := range []*metav1.LabelSelector{options.NamespaceSelector, options.ObjectSelector, options.PodSelector} {
		if selector == nil {
			selectors = append(selectors, labels.Everything())
			continue
		}
		compiled, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return nil, fmt.Errorf("invalid selector: %w", err)
		}
		selectors = append(selectors, compiled)
	}
	for _, rules := range [][]ResourceRule{options.ResourceRules, options.ExcludeResourceRules} {
		if err := ValidateResourceRules(rules); err != nil {
			return nil, err
		}
	}
	c := &Runtime{
		mu: &reader.mu, reader: reader, discovery: d, inventory: inventory, submitter: submitter,
		store: publication, dynamic: client, metadata: metadataClient, mapper: mapper, options: options,
		queue:      workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[queuedWork]()),
		namespaces: map[string]bool{}, definitions: map[types.UID]*v1alpha1.Karta{},
		namespaceObjects: map[string]*unstructured.Unstructured{}, namespaceFailureKey: "namespaces:0", definitionFailureKey: "definitions:0",
		selections: map[schema.GroupKind]kartadefinitioninventory.Selection{}, knownKinds: map[schema.GroupKind]schema.GroupVersionKind{},
		associations: map[types.UID]types.UID{}, podsByRoot: map[types.UID]map[types.UID]struct{}{}, history: map[types.UID]rootHistory{},
		rootsByNamespace: map[string]map[types.UID]struct{}{},
		proofs:           map[objectKey]objectProof{}, podLists: map[string]podListProof{}, membership: map[string]membershipProof{},
		membershipRevision: map[string]uint64{}, retired: map[types.UID]map[discovery.CoverageKey]uint64{},
		namespaceSelector: selectors[0], objectSelector: selectors[1], podSelector: selectors[2],
	}
	publication.BindPublication(c.mu, options.Clock)
	return c, nil
}

func (c *Runtime) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started || !c.namespaceReady || c.namespaceError != nil ||
		!c.definitionsReady || c.definitionsError != nil ||
		!c.options.Clock().Before(c.namespaceEvidence.ExpiresAt) || !c.options.Clock().Before(c.definitionEvidence.ExpiresAt) {
		return false
	}
	for request, err := range c.reader.mappingErrors {
		if err != nil && slices.Contains(c.options.RequiredKinds, request.GVK) {
			return false
		}
	}
	for _, gvk := range append([]schema.GroupVersionKind{runtimePodGVK}, c.options.RequiredKinds...) {
		for namespace := range c.namespaces {
			found := false
			for _, stream := range c.reader.streams {
				if stream.gvk == gvk && (stream.key.Namespace == namespace || stream.key.Namespace == "") {
					found = stream.ready && stream.err == nil
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func (c *Runtime) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("controller already started")
	}
	c.started = true
	c.mu.Unlock()
	if err := c.startConfigurationWatches(ctx); err != nil {
		return err
	}
	for range c.options.Workers {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			for c.processNext(ctx) {
			}
		}()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			delay := c.options.RefreshInterval + time.Duration(rand.Int64N(int64(c.options.RefreshInterval/3)+1))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				c.queueVerification()
			}
		}
	}()
	<-ctx.Done()
	c.queue.ShutDown()
	c.mu.Lock()
	for _, stream := range c.reader.streams {
		stream.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
	return nil
}

func (c *Runtime) processNext(ctx context.Context) bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(key)
	var err error
	switch key.Operation {
	case "coverage":
		err = c.ensureCoverage(ctx, discovery.CoverageRequest{GVK: key.GVK, Namespace: key.Namespace, FullObject: key.Full})
	case "metadata":
		err = c.ensureCoverage(ctx, discovery.CoverageRequest{GVK: key.GVK, Namespace: key.Namespace}, true)
	case "object":
		err = c.reconcileObject(ctx, key)
	case "root":
		err = c.reconcileRoot(ctx, key.UID)
	case "verify":
		err = c.verifyStream(ctx, key)
	case "membership":
		err = c.verifyMembership(ctx, key.Namespace)
	case "definitions":
		err = c.refreshSelections(ctx)
	}
	if ctx.Err() != nil {
		c.queue.Forget(key)
		return true
	}
	if err != nil {
		c.enqueueCoverageError(err)
		c.queue.AddRateLimited(key)
	} else {
		c.queue.Forget(key)
	}
	return true
}

func (c *Runtime) enqueueCoverageError(err error) {
	var coverage *discovery.CoverageError
	if errors.As(err, &coverage) {
		request := coverage.Request
		c.queue.Add(queuedWork{Operation: "coverage", GVK: request.GVK, Namespace: request.Namespace, Full: request.FullObject})
	}
}

func refKey(ref discovery.ObjectRef) objectKey {
	return objectKey{GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name}
}

func runtimeObjectBody(obj any, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	if value, ok := obj.(*unstructured.Unstructured); ok {
		result := value.DeepCopy()
		result.SetGroupVersionKind(gvk)
		return result, nil
	}
	value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("convert cached object: %w", err)
	}
	result := &unstructured.Unstructured{Object: value}
	result.SetGroupVersionKind(gvk)
	return result, nil
}

func objectReference(obj metav1.Object, gvk schema.GroupVersionKind) discovery.ObjectRef {
	return discovery.ObjectRef{GVK: gvk, Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: obj.GetUID(), ResourceVersion: obj.GetResourceVersion()}
}
