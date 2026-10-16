// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

var kartaGVR = schema.GroupVersionResource{Group: "run.ai", Version: "v1alpha1", Resource: "kartas"}

var podGroupKind = schema.GroupKind{Group: "", Kind: "Pod"}

// Options configures the controller.
type Options struct {
	Identity      *identity.Evaluator
	IdentityKinds []schema.GroupVersionKind
	// FullPodCache disables the pod cache trim, for custom Kartas whose pod
	// selectors read fields outside metadata, spec.nodeName, and status.phase.
	FullPodCache      bool
	Resync            time.Duration
	Namespace         string
	HealthInterval    time.Duration
	HealthMaxAge      time.Duration
	RequiredKinds     []schema.GroupVersionKind
	IgnoredOwnerKinds []schema.GroupVersionKind
}

// Controller watches Karta CRs, Karta-described workloads, their child
// objects, and pods, and keeps the store current. All metric rendering
// happens in the collector; the controller only reacts to watch events.
type Controller struct {
	dynamicClient  dynamic.Interface
	metadataClient metadata.Interface
	kubeClient     kubernetes.Interface
	mapper         *restmapper.DeferredDiscoveryRESTMapper
	options        Options
	logger         *slog.Logger

	registry *registry.Registry
	index    *owner.Index
	store    *store.Store

	podInformer   cache.SharedIndexInformer
	kartaInformer cache.SharedIndexInformer

	mu               sync.Mutex
	watchers         map[schema.GroupVersionKind]*watcher
	discoveredOwners map[schema.GroupVersionKind]struct{}
	pendingWorkloads sync.Map
	deletedWorkloads sync.Map
	podLocks         [256]sync.Mutex
	workloadLocks    [256]sync.Mutex

	startedMu sync.RWMutex
	started   bool
}

type watcher struct {
	gvk      schema.GroupVersionKind
	informer cache.SharedIndexInformer
	stop     chan struct{}
	isRoot   bool
}

func New(
	dynamicClient dynamic.Interface,
	metadataClient metadata.Interface,
	kubeClient kubernetes.Interface,
	mapper *restmapper.DeferredDiscoveryRESTMapper,
	reg *registry.Registry,
	index *owner.Index,
	s *store.Store,
	logger *slog.Logger,
	options Options,
) (*Controller, error) {
	if options.HealthInterval == 0 {
		options.HealthInterval = 30 * time.Second
	}
	if options.HealthMaxAge == 0 {
		options.HealthMaxAge = 2 * time.Minute
	}
	if options.HealthInterval <= 0 || options.HealthMaxAge <= 2*options.HealthInterval {
		return nil, fmt.Errorf("health max age must exceed twice the positive verification interval")
	}
	controller := &Controller{
		dynamicClient:    dynamicClient,
		metadataClient:   metadataClient,
		kubeClient:       kubeClient,
		mapper:           mapper,
		options:          options,
		logger:           logger,
		registry:         reg,
		index:            index,
		store:            s,
		watchers:         make(map[schema.GroupVersionKind]*watcher),
		discoveredOwners: make(map[schema.GroupVersionKind]struct{}),
	}
	controller.initializeHealth()
	return controller, nil
}

// Ready reports whether all informers, including per-workload-kind watchers,
// have synced at least once.
func (c *Controller) Ready() bool {
	c.startedMu.RLock()
	started := c.started
	c.startedMu.RUnlock()
	if !started {
		return false
	}
	if !c.kartaInformer.HasSynced() || !c.podInformer.HasSynced() {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.watchers {
		if !w.informer.HasSynced() {
			return false
		}
	}
	return true
}

// Run starts the Karta and pod informers and blocks until the context ends.
func (c *Controller) Run(ctx context.Context) error {
	factory := informers.NewSharedInformerFactoryWithOptions(
		c.kubeClient, c.options.Resync,
		informers.WithTransform(c.podTransform()),
		informers.WithNamespace(c.options.Namespace),
	)
	c.podInformer = factory.Core().V1().Pods().Informer()
	if _, err := c.podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.onPodEvent(obj) },
		UpdateFunc: func(oldObj, newObj any) { c.onPodUpdate(oldObj, newObj) },
		DeleteFunc: func(obj any) { c.onPodDelete(obj) },
	}); err != nil {
		return fmt.Errorf("failed to add pod handler: %w", err)
	}

	c.kartaInformer = c.newDynamicInformer(kartaGVR, false, "")
	if _, err := c.kartaInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.onKartaEvent(obj) },
		UpdateFunc: func(_, newObj any) { c.onKartaEvent(newObj) },
		DeleteFunc: func(obj any) { c.onKartaDelete(obj) },
	}); err != nil {
		return fmt.Errorf("failed to add karta handler: %w", err)
	}

	if err := c.podInformer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.store.InvalidateHealth("", "Pod", collectionFailureReason(err))
		c.logger.Warn("pod observation failed", "error", err)
	}); err != nil {
		return err
	}
	if err := c.kartaInformer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.store.InvalidateHealth("run.ai", "Karta", collectionFailureReason(err))
		c.logger.Warn("definition observation failed", "error", err)
	}); err != nil {
		return err
	}
	factory.Start(ctx.Done())
	go c.kartaInformer.RunWithContext(ctx)

	// Verification also runs during failed initial sync so startup failures remain visible.
	c.reconcileWatchers()

	c.startedMu.Lock()
	c.started = true
	c.startedMu.Unlock()

	c.verifyHealth(ctx)
	timer := time.NewTimer(c.options.HealthInterval)
	defer timer.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-timer.C:
			c.mapper.Reset()
			c.reconcileWatchers()
			c.retryPendingPods()
			c.retryPendingWorkloads()
			c.verifyHealth(ctx)
			timer.Reset(c.options.HealthInterval)
		}
	}
	c.stopAllWatchers()
	return nil
}

// podTransform trims cached pods to what pod selectors read: metadata,
// spec.nodeName, and status.phase. Custom Kartas reading other pod fields
// need the FullPodCache escape hatch.
func (c *Controller) podTransform() cache.TransformFunc {
	if c.options.FullPodCache {
		return func(obj any) (any, error) { return obj, nil }
	}
	return func(obj any) (any, error) {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return obj, nil
		}
		return &corev1.Pod{
			TypeMeta: pod.TypeMeta,
			ObjectMeta: metav1.ObjectMeta{
				Name:              pod.Name,
				Namespace:         pod.Namespace,
				UID:               pod.UID,
				ResourceVersion:   pod.ResourceVersion,
				Labels:            pod.Labels,
				Annotations:       pod.Annotations,
				OwnerReferences:   pod.OwnerReferences,
				CreationTimestamp: pod.CreationTimestamp,
			},
			Spec:   corev1.PodSpec{NodeName: pod.Spec.NodeName},
			Status: corev1.PodStatus{Phase: pod.Status.Phase},
		}, nil
	}
}

func (c *Controller) newDynamicInformer(gvr schema.GroupVersionResource, isMetadata bool, namespace string) cache.SharedIndexInformer {
	if isMetadata {
		return cache.NewSharedIndexInformer(&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return c.metadataClient.Resource(gvr).Namespace(namespace).List(context.Background(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return c.metadataClient.Resource(gvr).Namespace(namespace).Watch(context.Background(), options)
			},
		}, &metav1.PartialObjectMetadata{}, c.options.Resync, cache.Indexers{})
	}
	return cache.NewSharedIndexInformer(&cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return c.dynamicClient.Resource(gvr).Namespace(namespace).List(context.Background(), options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			return c.dynamicClient.Resource(gvr).Namespace(namespace).Watch(context.Background(), options)
		},
	}, &unstructured.Unstructured{}, c.options.Resync, cache.Indexers{})
}

// reconcileWatchers aligns the running per-kind informers with the registry:
// one full informer per chosen root kind, one metadata informer per child
// kind. Pods are excluded; the dedicated pod informer covers them.
func (c *Controller) reconcileWatchers() {
	desiredRoots := make(map[schema.GroupVersionKind]struct{})
	desiredChildren := make(map[schema.GroupVersionKind]struct{})
	for _, entry := range c.registry.Entries() {
		if entry.Catalog {
			if _, err := c.mapper.RESTMapping(entry.RootGVK.GroupKind(), entry.RootGVK.Version); meta.IsNoMatchError(err) {
				continue
			}
		}
		desiredRoots[entry.RootGVK] = struct{}{}
		for _, child := range entry.ChildKinds {
			if child.GroupKind() == podGroupKind || c.isIgnored(child) {
				continue
			}
			desiredChildren[child] = struct{}{}
		}
	}
	for _, gvk := range c.options.RequiredKinds {
		if gvk != podGVK && gvk != kartaGVK {
			desiredChildren[gvk] = struct{}{}
		}
	}
	var restoredOwners []owner.Ref
	var pendingPods []string
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		for _, ref := range restoredOwners {
			c.reattributeDescendants(ref.UID)
		}
		for _, key := range pendingPods {
			c.attributePodByKey(key)
		}
	}()
	for _, gvk := range c.index.OwnerKinds() {
		if gvk.Kind != "" && gvk != podGVK && !c.isIgnored(gvk) {
			c.discoveredOwners[gvk] = struct{}{}
		}
	}
	for gvk := range c.discoveredOwners {
		if !c.index.HasOwnerKind(gvk) {
			delete(c.discoveredOwners, gvk)
			continue
		}
		if selected, ok := c.registry.EntryFor(gvk.GroupKind()); ok && selected.RootGVK != gvk {
			continue
		}
		desiredChildren[gvk] = struct{}{}
	}
	// A formerly described workload can remain an intermediate owner.
	for gvk, watcher := range c.watchers {
		if watcher.isRoot && !c.registry.IsRoot(gvk.GroupKind()) && c.index.HasOwnerKind(gvk) {
			desiredChildren[gvk] = struct{}{}
			c.discoveredOwners[gvk] = struct{}{}
		}
	}
	for _, gvk := range c.options.IdentityKinds {
		if gvk != podGVK {
			desiredChildren[gvk] = struct{}{}
		}
	}
	for gvk := range desiredRoots {
		delete(desiredChildren, gvk)
	}

	for gvk, w := range c.watchers {
		_, wantRoot := desiredRoots[gvk]
		_, wantChild := desiredChildren[gvk]
		if (w.isRoot && wantRoot) || (!w.isRoot && wantChild) {
			continue
		}
		close(w.stop)
		delete(c.watchers, gvk)
		if gvk != podGVK {
			c.store.DeleteIdentitiesForKind(gvk)
		}
		if gvk != podGVK {
			for _, ref := range c.index.ForgetKind(gvk) {
				if _, deleted := c.deletedWorkloads.Load(ref.UID); deleted {
					continue
				}
				key := ref.Name
				if ref.Namespace != "" {
					key = ref.Namespace + "/" + ref.Name
				}
				for aliasGVK, alias := range c.watchers {
					_, keepRoot := desiredRoots[aliasGVK]
					_, keepChild := desiredChildren[aliasGVK]
					if aliasGVK.GroupKind() != gvk.GroupKind() ||
						(alias.isRoot && !keepRoot) || (!alias.isRoot && !keepChild) {
						continue
					}
					item, exists, err := alias.informer.GetStore().GetByKey(key)
					if err != nil || !exists {
						continue
					}
					current, err := meta.Accessor(item)
					if err != nil || current.GetUID() != ref.UID || current.GetName() != ref.Name || current.GetNamespace() != ref.Namespace {
						continue
					}
					// A surviving alias may already be synced and never send another
					// Add for this UID. Reuse its cache before retiring the last node.
					_, drained := c.index.UpsertTrackedObject(current, aliasGVK)
					pendingPods = append(pendingPods, drained...)
					c.recordIdentity(current, aliasGVK)
					restoredOwners = append(restoredOwners, ref)
					break
				}
			}
		}

		c.logger.Info("stopped watcher", "gvk", gvk.String())
	}

	for gvk := range desiredRoots {
		if _, ok := c.watchers[gvk]; !ok {
			c.startWatcherLocked(gvk, true)
		}
	}
	for gvk := range desiredChildren {
		if _, ok := c.watchers[gvk]; !ok {
			c.startWatcherLocked(gvk, false)
		}
	}
}

func (c *Controller) startWatcherLocked(gvk schema.GroupVersionKind, isRoot bool) {
	mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		c.mapper.Reset()
		mapping, err = c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}
	if err != nil {
		c.logger.Error("cannot map kind to a resource, skipping watcher", "gvk", gvk.String(), "error", err)
		return
	}

	namespace := c.options.Namespace
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		namespace = ""
	}
	informer := c.newDynamicInformer(mapping.Resource, !isRoot, namespace)
	w := &watcher{gvk: gvk, informer: informer, stop: make(chan struct{}), isRoot: isRoot}

	var handlerErr error
	if isRoot {
		_, handlerErr = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { c.onWorkloadEvent(obj) },
			UpdateFunc: c.onWorkloadUpdate,
			DeleteFunc: func(obj any) { c.onWorkloadDelete(obj) },
		})
	} else {
		_, handlerErr = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { c.onChildEvent(obj, gvk) },
			UpdateFunc: func(oldObj, newObj any) { c.onChildUpdate(oldObj, newObj, gvk) },
			DeleteFunc: func(obj any) { c.onChildDelete(obj, gvk) },
		})
	}
	if handlerErr != nil {
		c.logger.Error("failed to add watcher handler", "gvk", gvk.String(), "error", handlerErr)
		return
	}

	if err := informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.store.InvalidateHealth(gvk.Group, gvk.Kind, collectionFailureReason(err))
		c.logger.Warn("resource observation failed", "gvk", gvk.String(), "error", err)
	}); err != nil {
		c.logger.Error("cannot register watch error handler", "error", err)
		return
	}
	c.watchers[gvk] = w
	go informer.Run(w.stop)
	c.logger.Info("started watcher", "gvk", gvk.String(), "resource", mapping.Resource.String(), "root", isRoot)
}

func (c *Controller) stopAllWatchers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for gvk, w := range c.watchers {
		close(w.stop)
		delete(c.watchers, gvk)
		if gvk != podGVK {
			c.store.DeleteIdentitiesForKind(gvk)
		}
	}
}

func (c *Controller) rootWatcherStore(groupKind schema.GroupKind) cache.Store {
	c.mu.Lock()
	defer c.mu.Unlock()
	for gvk, w := range c.watchers {
		if w.isRoot && gvk.GroupKind() == groupKind {
			return w.informer.GetStore()
		}
	}
	return nil
}

func (c *Controller) isIgnored(gvk schema.GroupVersionKind) bool {
	for _, ignored := range c.options.IgnoredOwnerKinds {
		if ignored == gvk {
			return true
		}
	}
	return false
}

// Observe unknown intermediate kinds so a recovered owner updates descendants.
func (c *Controller) observeOwner(gvk schema.GroupVersionKind) {
	if gvk.Kind == "" || gvk == podGVK || c.isIgnored(gvk) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discoveredOwners == nil {
		c.discoveredOwners = make(map[schema.GroupVersionKind]struct{})
	}
	if _, discovered := c.discoveredOwners[gvk]; discovered {
		return
	}
	c.discoveredOwners[gvk] = struct{}{}
	if c.watchers[gvk] == nil {
		c.startWatcherLocked(gvk, false)
	}
}
