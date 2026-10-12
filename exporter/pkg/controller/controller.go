// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
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

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

var kartaGVR = schema.GroupVersionResource{Group: "run.ai", Version: "v1alpha1", Resource: "kartas"}

var podGroupKind = schema.GroupKind{Group: "", Kind: "Pod"}

// Options configures the controller.
type Options struct {
	// FullPodCache disables the pod cache trim, for custom Kartas whose pod
	// selectors read fields outside metadata, spec.nodeName, and status.phase.
	FullPodCache   bool
	Resync         time.Duration
	Namespace      string
	HealthInterval time.Duration
	HealthMaxAge   time.Duration
	RequiredKinds  []schema.GroupVersionKind
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

	mu       sync.Mutex
	watchers map[schema.GroupVersionKind]*watcher

	attributionErrors metric.Int64Counter
	lastEvent         metric.Float64Gauge

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
	meter metric.Meter,
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
	attributionErrors, err := meter.Int64Counter(collector.MetricExporterAttributionErrors,
		metric.WithDescription("Attribution and status evaluation errors, by reason."))
	if err != nil {
		return nil, fmt.Errorf("create attribution error counter: %w", err)
	}
	lastEvent, err := meter.Float64Gauge(collector.MetricExporterLastEventSeconds,
		metric.WithDescription("Unix timestamp of the last processed watch event. Event activity only; not evidence of fresh observation."))
	if err != nil {
		return nil, fmt.Errorf("create last event gauge: %w", err)
	}
	lastEvent.Record(context.Background(), 0)
	controller := &Controller{
		dynamicClient:     dynamicClient,
		metadataClient:    metadataClient,
		kubeClient:        kubeClient,
		mapper:            mapper,
		options:           options,
		logger:            logger,
		registry:          reg,
		index:             index,
		store:             s,
		watchers:          make(map[schema.GroupVersionKind]*watcher),
		attributionErrors: attributionErrors,
		lastEvent:         lastEvent,
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
		c.store.InvalidateHealth("", "Pod")
		c.logger.Warn("pod observation failed", "error", err)
	}); err != nil {
		return err
	}
	if err := c.kartaInformer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.store.InvalidateHealth("run.ai", "Karta")
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
	ticker := time.NewTicker(c.options.HealthInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-ticker.C:
			c.mapper.Reset()
			c.reconcileWatchers()
			c.retryPendingPods()
			c.verifyHealth(ctx)
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
			if child.GroupKind() == podGroupKind {
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
	for gvk := range desiredRoots {
		delete(desiredChildren, gvk)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for gvk, w := range c.watchers {
		_, wantRoot := desiredRoots[gvk]
		_, wantChild := desiredChildren[gvk]
		if (w.isRoot && wantRoot) || (!w.isRoot && wantChild) {
			continue
		}
		close(w.stop)
		delete(c.watchers, gvk)
		// a stopped informer fires no delete events; drop its owner edges
		for _, item := range w.informer.GetStore().List() {
			if objectMeta, ok := item.(metav1.Object); ok {
				c.index.DeleteObject(objectMeta.GetUID())
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
			UpdateFunc: func(_, newObj any) { c.onWorkloadEvent(newObj) },
			DeleteFunc: func(obj any) { c.onWorkloadDelete(obj) },
		})
	} else {
		_, handlerErr = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { c.onChildEvent(obj) },
			UpdateFunc: func(_, newObj any) { c.onChildEvent(newObj) },
			DeleteFunc: func(obj any) { c.onChildDelete(obj) },
		})
	}
	if handlerErr != nil {
		c.logger.Error("failed to add watcher handler", "gvk", gvk.String(), "error", handlerErr)
		return
	}

	if err := informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		c.store.InvalidateHealth(gvk.Group, gvk.Kind)
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

func (c *Controller) markEvent() {
	c.lastEvent.Record(context.Background(), float64(time.Now().UnixNano())/1e9)
}
