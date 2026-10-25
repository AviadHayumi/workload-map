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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type watchedStream struct {
	source watch.Interface
	events chan watch.Event
	cancel context.CancelFunc
	once   sync.Once
}

func (r *Runtime) syncWatches(ctx context.Context) error {
	if r.config.Definitions.Cluster.Enabled {
		if _, err := r.ensureWatch(ctx, kartaGVK, "", true); err != nil {
			return err
		}
	}
	definitionErr := r.refreshDefinitions()
	namespaces := []string{""}
	if r.config.NamespaceSelector != nil {
		if _, err := r.ensureWatch(ctx, namespaceGVK, "", true); err != nil {
			return err
		}
		namespaces = nil
		r.mu.Lock()
		r.reader.mu.RLock()
		for _, w := range r.watches {
			if w.gvk == namespaceGVK {
				for key := range w.objects {
					entry := r.reader.objects[key]
					if entry.object != nil && r.namespaceSelector.Matches(labelsFor(entry.object)) {
						namespaces = append(namespaces, entry.object.GetName())
					}
				}
			}
		}
		r.reader.mu.RUnlock()
		r.mu.Unlock()
	}
	r.mu.Lock()
	kinds := map[schema.GroupVersionKind]bool{podGVK: true}
	for _, selection := range r.selections {
		if selection.GVK.Kind == "" || selection.GVK.Version == "" {
			continue
		}
		kinds[selection.GVK] = true
	}
	for _, gvk := range r.config.RequiredKinds {
		kinds[gvk] = true
	}
	roots := make([]discovery.ObjectRef, 0, len(r.roots))
	for _, root := range r.roots {
		ref := root.ref
		if selection := r.selections[ref.GVK.GroupKind()]; selection.Definition != nil {
			ref.GVK = selection.GVK
		}
		roots = append(roots, ref)
	}
	r.mu.Unlock()
	desired := map[watchKey]bool{}
	requested := map[discovery.WatchRequest]bool{}
	var errs []error
	if definitionErr != nil {
		errs = append(errs, definitionErr)
	}
	if r.config.Definitions.Cluster.Enabled {
		key, err := r.ensureWatch(ctx, kartaGVK, "", true)
		if err == nil {
			desired[key] = true
		}
	}
	if r.config.NamespaceSelector != nil {
		key, err := r.ensureWatch(ctx, namespaceGVK, "", true)
		if err == nil {
			desired[key] = true
		}
	}
	for gvk := range kinds {
		for _, namespace := range namespaces {
			requested[discovery.WatchRequest{GVK: gvk, Namespace: namespace}] = true
			key, err := r.ensureWatch(ctx, gvk, namespace, true)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			desired[key] = true
			if _, lookupErr := r.deps.Inventory.Lookup(gvk); errors.Is(lookupErr, inventory.ErrInvalidDefinition) {
				r.mu.Lock()
				r.watchFailures[discovery.WatchRequest{GVK: gvk, Namespace: key.Namespace}] = lookupErr
				r.mu.Unlock()
			}
		}
	}
	seenRoots := map[discovery.WatchRequest]watchKey{}
	rootSources := map[types.UID]watchKey{}
	for _, root := range roots {
		namespace := root.Namespace
		if r.config.NamespaceSelector == nil {
			namespace = ""
		} else if namespace != "" && !containsNamespace(namespaces, namespace) {
			r.mu.Lock()
			r.removeRoot(root.UID)
			r.mu.Unlock()
			continue
		}
		request := discovery.WatchRequest{GVK: root.GVK, Namespace: namespace}
		requested[request] = true
		if key, found := seenRoots[request]; found {
			rootSources[root.UID] = key
			continue
		}
		key, err := r.ensureWatch(ctx, root.GVK, namespace, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		desired[key] = true
		seenRoots[request] = key
		rootSources[root.UID] = key
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for uid, root := range r.roots {
		if replacement, ok := rootSources[uid]; ok {
			root.watch = replacement
		}
		requested[discovery.WatchRequest{GVK: root.ref.GVK, Namespace: root.watch.Namespace}] = true
		desired[root.watch] = true
	}
	for request := range r.watchFailures {
		if !requested[request] && request.GVK != kartaGVK && request.GVK != namespaceGVK {
			delete(r.watchFailures, request)
		}
	}
	for key, w := range r.watches {
		if desired[key] {
			continue
		}
		w.cancel()
		delete(r.watches, key)
		r.reader.mu.Lock()
		r.retireWatchObjectsLocked(w)
		r.reader.mu.Unlock()
	}
	r.publishHealthLocked()
	return errors.Join(errs...)
}

// Retiring a representation does not retire a UID already assigned to another source.
func (r *Runtime) retireWatchObjectsLocked(w *resourceWatch) {
	delete(r.reader.activeKeys, w.failureKey)
	r.deps.Publications.SetPublicationFailure(w.failureKey, store.ReasonAPIUnavailable)
	for key := range w.objects {
		entry, ok := r.reader.objects[key]
		if !ok || entry.stream != w.key {
			continue
		}
		ref := refOf(entry.object)
		delete(r.reader.objects, key)
		root := r.roots[ref.UID]
		if root != nil && root.watch != w.key {
			if replacement := r.watches[root.watch]; replacement != nil {
				key := objectKey{GVK: replacement.gvk, Namespace: ref.Namespace, Name: ref.Name}
				if observed, ok := r.reader.objects[key]; !ok || observed.object.GetUID() != ref.UID {
					if replacement.pendingRoots == nil {
						replacement.pendingRoots = map[types.UID]discovery.ObjectRef{}
					}
					replacement.pendingRoots[ref.UID] = ref
					r.controlQueue.Add(queueKey{Operation: "verify", Watch: replacement.key})
				}
				r.rootQueue.Add(queueKey{Operation: "root", UID: ref.UID})
				continue
			}
		}
		r.deleteObjectLocked(ref)
	}
	r.deps.Publications.RetirePublicationFailure(w.failureKey)
}

func containsNamespace(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (r *Runtime) ensureWatch(ctx context.Context, gvk schema.GroupVersionKind, namespace string, discover bool) (watchKey, error) {
	requestedNamespace := namespace
	mapping, err := r.reader.mapping(gvk)
	if err != nil {
		r.mu.Lock()
		r.watchFailures[discovery.WatchRequest{GVK: gvk, Namespace: namespace}] = err
		r.publishHealthLocked()
		r.mu.Unlock()
		return watchKey{}, fmt.Errorf("map watch %s: %w", gvk, err)
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		namespace = ""
	}
	key := watchKey{Resource: mapping.Resource, Namespace: namespace}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.watchFailures, discovery.WatchRequest{GVK: gvk, Namespace: namespace})
	delete(r.watchFailures, discovery.WatchRequest{GVK: gvk, Namespace: requestedNamespace})
	if existing, ok := r.watches[key]; ok {
		existing.discover = existing.discover || discover
		return key, nil
	}
	if ctx.Err() != nil {
		return key, ctx.Err()
	}
	child, cancel := context.WithCancel(ctx)
	r.sequence++
	w := &resourceWatch{discover: discover, objects: map[objectKey]struct{}{}, key: key, gvk: gvk, cancel: cancel, failureKey: fmt.Sprintf("watch:%s/%s:%d", key.Resource, key.Namespace, r.sequence)}
	client := r.reader.clients.Dynamic.Resource(key.Resource).Namespace(key.Namespace)
	lw := &cache.ListWatch{
		ListWithContextFunc: func(requestCtx context.Context, options metav1.ListOptions) (kruntime.Object, error) {
			requestCtx, cancel := context.WithTimeout(requestCtx, r.reader.options.RequestTimeout)
			defer cancel()
			list, err := client.List(requestCtx, options)
			r.mu.Lock()
			if r.watches[key] == w {
				if err != nil {
					r.failWatchLocked(w, err)
				} else {
					r.recoverWatchLocked(w)
					w.verifiedAt = r.config.Now()
				}
			}
			r.mu.Unlock()
			return list, err
		},
		WatchFuncWithContext: func(requestCtx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			source, err := client.Watch(requestCtx, options)
			if err != nil {
				r.mu.Lock()
				r.failWatchLocked(w, err)
				r.mu.Unlock()
				return nil, err
			}
			return r.monitorWatch(requestCtx, w, source), nil
		},
	}
	w.informer = cache.NewSharedIndexInformer(lw, &unstructured.Unstructured{}, 0, cache.Indexers{})
	if err := w.informer.SetTransform(func(object any) (any, error) {
		body, ok := object.(*unstructured.Unstructured)
		if !ok {
			return object, nil
		}
		copy := body.DeepCopy()
		copy.SetManagedFields(nil)
		copy.SetGroupVersionKind(gvk)
		return copy, nil
	}); err != nil {
		cancel()
		return key, err
	}
	handler, err := w.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(object any) { r.accept(w, object, false) },
		UpdateFunc: func(_, object any) { r.accept(w, object, false) },
		DeleteFunc: func(object any) { r.accept(w, object, true) },
	})
	if err != nil {
		cancel()
		return key, err
	}
	r.watches[key] = w
	r.reader.mu.Lock()
	r.reader.activeKeys[w.failureKey] = true
	r.reader.mu.Unlock()
	r.wg.Add(1)
	go func() { defer r.wg.Done(); w.informer.Run(child.Done()) }()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if !cache.WaitForCacheSync(child.Done(), handler.HasSynced) {
			return
		}
		r.mu.Lock()
		if r.watches[key] == w {
			w.ready = true
			r.publishHealthLocked()
		}
		r.mu.Unlock()
		r.controlQueue.Add(queueKey{Operation: "verify", Watch: key})
	}()
	return key, nil
}

func (r *Runtime) monitorWatch(ctx context.Context, w *resourceWatch, source watch.Interface) watch.Interface {
	child, cancel := context.WithCancel(ctx)
	stream := &watchedStream{source: source, events: make(chan watch.Event), cancel: cancel}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(stream.events)
		defer source.Stop()
		for {
			select {
			case <-child.Done():
				return
			case event, ok := <-source.ResultChan():
				if !ok {
					if child.Err() == nil {
						r.mu.Lock()
						r.controlQueue.Add(queueKey{Operation: "verify", Watch: w.key})
						r.mu.Unlock()
					}
					return
				}
				if event.Type == watch.Error {
					r.mu.Lock()
					cause := apierrors.FromObject(event.Object)
					if apierrors.IsGone(cause) || apierrors.IsResourceExpired(cause) {
						r.controlQueue.Add(queueKey{Operation: "verify", Watch: w.key})
					} else {
						r.failWatchLocked(w, cause)
					}
					r.mu.Unlock()
				}
				select {
				case <-child.Done():
					return
				case stream.events <- event:
				}
			}
		}
	}()
	return stream
}
func (w *watchedStream) Stop()                          { w.once.Do(func() { w.cancel(); w.source.Stop() }) }
func (w *watchedStream) ResultChan() <-chan watch.Event { return w.events }

func (r *Runtime) failWatchLocked(w *resourceWatch, err error) {
	if r.watches[w.key] != w {
		return
	}
	first := w.err == nil
	r.reader.mu.Lock()
	delete(r.reader.activeKeys, w.failureKey)
	r.reader.mu.Unlock()
	w.err = err
	if w.gvk == kartaGVK {
		r.deps.Publications.SetPublicationFailure(r.definitionKey, failureReason(err))
		r.definitionError = err
		for uid, root := range r.roots {
			root.generation++
			r.rootQueue.Add(queueKey{Operation: "root", UID: uid})
		}
	}
	r.deps.Publications.SetPublicationFailure(w.failureKey, failureReason(err))
	for uid, root := range r.roots {
		if root.watch == w.key || w.gvk == podGVK {
			root.fullRefresh = true
			r.rootQueue.Add(queueKey{Operation: "root", UID: uid})
		}
	}
	r.publishHealthLocked()
	if first {
		r.controlQueue.Add(queueKey{Operation: "verify", Watch: w.key})
	}
}
func (r *Runtime) recoverWatchLocked(w *resourceWatch) {
	if w.err != nil {
		r.sequence++
		w.failureKey = fmt.Sprintf("watch:%s/%s:%d", w.key.Resource, w.key.Namespace, r.sequence)
		w.err = nil
		r.reader.mu.Lock()
		r.reader.activeKeys[w.failureKey] = true
		r.reader.mu.Unlock()
	}
}

func (r *Runtime) accept(w *resourceWatch, value any, deleted bool) {
	if tombstone, ok := value.(cache.DeletedFinalStateUnknown); ok {
		value = tombstone.Obj
	}
	object, ok := value.(*unstructured.Unstructured)
	if !ok {
		return
	}
	ref := refOf(object)
	key := keyOf(ref)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[w.key] != w {
		return
	}
	w.generation++
	r.generation++
	r.reader.mu.Lock()
	old := r.reader.objects[key]
	if deleted && old.object != nil && old.object.GetUID() != ref.UID {
		r.reader.mu.Unlock()
		return
	}
	if old.object != nil && old.object.GetUID() != ref.UID {
		r.deleteObjectLocked(refOf(old.object))
		delete(r.reader.objects, key)
	}
	if deleted {
		if old.object != nil && old.object.GetUID() == ref.UID {
			delete(r.reader.objects, key)
			delete(w.objects, key)
			r.deleteObjectLocked(ref)
		}
	} else {
		w.objects[key] = struct{}{}
		delete(w.pendingRoots, ref.UID)
		r.reader.objects[key] = observation{sequence: w.generation, object: object, stream: w.key, evidence: store.Evidence{ExpiresAt: r.config.Now().Add(r.config.MaxAge), Dependencies: []string{w.failureKey}}}
		if old.object != nil && old.object.GetResourceVersion() != ref.ResourceVersion {
			if root := r.roots[r.podRoots[ref.UID]]; root != nil {
				r.invalidatePodLocked(root, ref.UID, store.ReasonAPIUnavailable)
			}
			if r.roots[ref.UID] != nil {
				r.failPublicationLocked(ref.UID, discovery.ErrStale)
			}
		}
		r.touchRoot(ref.UID)
		if w.discover || r.roots[ref.UID] != nil {
			r.queue.Add(queueKey{Operation: "object", Object: key, UID: ref.UID})
		}
	}
	r.reader.mu.Unlock()
	if w.gvk == kartaGVK {
		oldSelected := r.acceptsDefinition(old.object)
		newSelected := !deleted && r.acceptsDefinition(object)
		if oldSelected != newSelected || (newSelected && (old.object == nil || !reflect.DeepEqual(old.object.Object["spec"], object.Object["spec"]))) {
			for _, candidate := range []*unstructured.Unstructured{old.object, object} {
				if candidate == nil {
					continue
				}
				group, _, _ := unstructured.NestedString(candidate.Object, "spec", "structureDefinition", "rootComponent", "kind", "group")
				kind, _, _ := unstructured.NestedString(candidate.Object, "spec", "structureDefinition", "rootComponent", "kind", "kind")
				if kind == "" {
					continue
				}
				gk := schema.GroupKind{Group: group, Kind: kind}
				r.pendingDefinitions[gk] = true
				r.invalidateDefinitionLocked(gk)
				for uid, root := range r.roots {
					if root.ref.GVK.GroupKind() == gk {
						r.touchRoot(uid)
					}
				}
			}
		}
	}
	if w.gvk == namespaceGVK && (deleted || !r.namespaceSelector.Matches(labelsFor(object))) {
		for uid, root := range r.roots {
			if root.ref.Namespace == object.GetName() {
				r.removeRoot(uid)
			}
		}
	}
	if w.gvk == namespaceGVK || w.gvk == kartaGVK {
		r.controlQueue.Add(queueKey{Operation: "sync"})
	}
}

func (r *Runtime) acceptsDefinition(object *unstructured.Unstructured) bool {
	if object == nil {
		return false
	}
	cfg := r.config.Definitions.Cluster
	filter, err := selector(cfg.LabelSelector)
	return err == nil && !slices.Contains(cfg.Exclude, object.GetName()) && (len(cfg.Include) == 0 || slices.Contains(cfg.Include, object.GetName())) && filter.Matches(labelsFor(object))
}

func (r *Runtime) deleteObjectLocked(ref discovery.ObjectRef) {
	if rootUID := r.podRoots[ref.UID]; rootUID != "" {
		if root := r.roots[rootUID]; root != nil {
			r.invalidatePodLocked(root, ref.UID, store.ReasonUnresolvedOwner)
			delete(root.paths, ref.UID)
		}
		delete(r.podRoots, ref.UID)
	}
	if r.roots[ref.UID] != nil {
		r.removeRoot(ref.UID)
		r.controlQueue.Add(queueKey{Operation: "sync"})
	}
}

func (r *Runtime) verifyWatch(ctx context.Context, key watchKey) error {
	r.mu.Lock()
	w := r.watches[key]
	if w == nil {
		r.mu.Unlock()
		return nil
	}
	generation := w.generation
	r.mu.Unlock()
	verificationStarted := r.config.Now()
	refs := map[types.UID]string{}
	options := metav1.ListOptions{Limit: 500}
	tokens := map[string]bool{}
	resourceVersion := ""
	for {
		requestCtx, cancel := context.WithTimeout(ctx, r.reader.options.RequestTimeout)
		list, err := r.reader.listMetadata(requestCtx, key.Resource, key.Namespace, options)
		cancel()
		if err != nil {
			r.mu.Lock()
			r.failWatchLocked(w, err)
			r.mu.Unlock()
			return err
		}
		if resourceVersion != "" && resourceVersion != list.GetResourceVersion() {
			return discovery.ErrStale
		}
		resourceVersion = list.GetResourceVersion()
		for _, object := range list.Items {
			refs[object.GetUID()] = object.GetResourceVersion()
		}
		options.Continue = list.GetContinue()
		if options.Continue == "" {
			break
		}
		if tokens[options.Continue] {
			return errors.New("repeated metadata LIST continuation")
		}
		tokens[options.Continue] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[key] != w {
		return nil
	}

	r.recoverWatchLocked(w)
	w.verifiedAt = verificationStarted
	r.reader.mu.Lock()
	for objectKey := range w.objects {
		entry, ok := r.reader.objects[objectKey]
		if !ok {
			continue
		}
		if entry.sequence > generation {
			continue
		}
		ref := refOf(entry.object)
		version, found := refs[ref.UID]
		if !found {
			delete(r.reader.objects, objectKey)
			delete(w.objects, objectKey)
			r.deleteObjectLocked(ref)
			continue
		}
		if version != ref.ResourceVersion {
			entry.evidence = store.Evidence{}
			r.failPublicationLocked(ref.UID, discovery.ErrStale)
			if w.gvk == kartaGVK {
				r.deps.Publications.SetPublicationFailure(r.definitionKey, store.ReasonAPIUnavailable)
				r.definitionError = discovery.ErrPending
				r.controlQueue.Add(queueKey{Operation: "sync"})
			}
			if root := r.roots[r.podRoots[ref.UID]]; root != nil {
				r.invalidatePodLocked(root, ref.UID, store.ReasonAPIUnavailable)
			}
		} else {
			entry.evidence = store.Evidence{ExpiresAt: w.verifiedAt.Add(r.config.MaxAge), Dependencies: []string{w.failureKey}}
		}
		r.reader.objects[objectKey] = entry
		if root := r.roots[ref.UID]; root != nil {
			root.fullRefresh = true
			r.rootQueue.Add(queueKey{Operation: "root", UID: ref.UID})
		}
		if root := r.roots[r.podRoots[ref.UID]]; root != nil {
			r.dirtyPod(root, ref.UID)
		}
		if w.discover || r.roots[ref.UID] != nil {
			r.queue.Add(queueKey{Operation: "object", Object: objectKey, UID: ref.UID})
		}
	}
	for uid, ref := range w.pendingRoots {
		if _, found := refs[uid]; !found {
			r.deleteObjectLocked(ref)
			delete(w.pendingRoots, uid)
		}
	}
	r.reader.mu.Unlock()
	r.publishHealthLocked()
	return nil
}
