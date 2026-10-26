// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

var runtimePodGVK = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}

type runtimeWatch struct {
	source watch.Interface
	events chan watch.Event
	done   chan struct{}
	once   sync.Once
}

func (w *runtimeWatch) Stop() {
	w.once.Do(func() { close(w.done); w.source.Stop() })
}

func (w *runtimeWatch) ResultChan() <-chan watch.Event { return w.events }

func (c *Runtime) ensureCoverage(ctx context.Context, request discovery.CoverageRequest, forceMetadata ...bool) error {
	// Mapping failures apply to both metadata and full-object requests.
	errorKey := discovery.CoverageRequest{GVK: request.GVK, Namespace: request.Namespace}
	mapping, err := c.mapper.RESTMapping(request.GVK.GroupKind(), request.GVK.Version)
	if err != nil {
		if resetter, ok := c.mapper.(meta.ResettableRESTMapper); ok {
			resetter.Reset()
		}
		err = runtimeError(fmt.Errorf("mapping %s: %w", request.GVK, err))
		c.mu.Lock()
		defer c.mu.Unlock()
		if request.Namespace != "" && !c.namespaces[request.Namespace] {
			return nil
		}
		for _, stream := range c.reader.streams {
			if stream.gvk == request.GVK && (stream.key.Namespace == request.Namespace || stream.key.Namespace == "") {
				c.failStreamLocked(stream, err)
				return err
			}
		}
		c.reader.mappingErrors[errorKey] = err
		c.updateHealthLocked()
		return err
	}
	namespace := request.Namespace
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		namespace = ""
	}
	key := discovery.CoverageKey{Resource: mapping.Resource, Namespace: namespace}
	c.mu.Lock()
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace && !c.namespaces[namespace] {
		c.mu.Unlock()
		return nil
	}
	force := len(forceMetadata) != 0 && forceMetadata[0] && request.GVK != runtimePodGVK
	if current := c.reader.streams[key]; current != nil && (current.full || !request.FullObject) && (!force || !current.full) {
		if _, failed := c.reader.mappingErrors[errorKey]; failed {
			delete(c.reader.mappingErrors, errorKey)
			c.updateHealthLocked()
		}
		c.mu.Unlock()
		return nil
	}
	c.sequence++
	watchCtx, cancel := context.WithCancel(ctx)
	// Mapping recovery alone cannot clear a failure before fresh verification.
	stream := &resourceStream{key: key, gvk: request.GVK, run: c.sequence,
		full: request.FullObject || request.GVK == runtimePodGVK, cancel: cancel, err: c.reader.mappingErrors[errorKey]}
	stream.failureKey = fmt.Sprintf("watch:%s:%s:%d:0", key.Resource, key.Namespace, stream.run)
	if previous := c.reader.streams[key]; previous != nil {
		previous.cancel()
		c.store.SetPublicationFailure(previous.failureKey, store.ReasonAPIUnavailable)
	}
	c.reader.streams[key] = stream
	c.routeChangesLocked(c.discovery.CoverageChanged(discovery.CoverageChange{
		Key: key, Run: stream.run, FullObject: stream.full, Err: stream.err,
	}))
	delete(c.reader.mappingErrors, errorKey)
	c.updateHealthLocked()
	c.mu.Unlock()

	listWatch := cache.ToListWatcherWithWatchListSemantics(c.listWatch(watchCtx, stream), c.dynamic)
	var expectedObject runtime.Object = &unstructured.Unstructured{}
	if !stream.full {
		// Metadata negotiation and its full-object fallback return different Go
		// types. The transform below normalizes either before it reaches cache.
		expectedObject = nil
	}
	informer := cache.NewSharedIndexInformer(listWatch, expectedObject, 0, cache.Indexers{})
	if !stream.full {
		if err := informer.SetTransform(func(obj any) (any, error) {
			u, err := runtimeObjectBody(obj, stream.gvk)
			if err != nil {
				return nil, err
			}
			return &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": stream.gvk.GroupVersion().String(), "kind": stream.gvk.Kind, "metadata": u.Object["metadata"],
			}}, nil
		}); err != nil {
			cancel()
			return err
		}
	}
	registration, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.acceptObject(stream, obj, false) },
		UpdateFunc: func(_, obj any) { c.acceptObject(stream, obj, false) },
		DeleteFunc: func(obj any) { c.acceptObject(stream, obj, true) },
	})
	if err != nil {
		cancel()
		return err
	}
	if err := informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) { c.failStream(stream, err) }); err != nil {
		cancel()
		return err
	}
	c.mu.Lock()
	stream.informer, stream.handler = informer, registration
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		informer.Run(watchCtx.Done())
	}()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if !cache.WaitForCacheSync(watchCtx.Done(), informer.HasSynced, registration.HasSynced) {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.reader.streams[key] != stream || watchCtx.Err() != nil {
			return
		}
		stream.ready = true
		c.routeChangesLocked(c.discovery.CoverageChanged(discovery.CoverageChange{
			Key: key, Run: stream.run, FullObject: stream.full, Ready: true, Err: stream.err,
		}))
		for uid, guards := range c.retired {
			if run, exists := guards[key]; exists && run < stream.run {
				delete(guards, key)
				if len(guards) == 0 {
					delete(c.retired, uid)
				}
			}
		}
		c.queue.Add(queuedWork{Operation: "verify", GVK: stream.gvk, Namespace: namespace})
	}()
	return nil
}

func (c *Runtime) listWatch(ctx context.Context, stream *resourceStream) *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(requestCtx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			requestCtx, cancel := context.WithTimeout(requestCtx, c.options.RequestTimeout)
			defer cancel()
			if !stream.full && c.metadata != nil {
				list, err := c.metadata.Resource(stream.key.Resource).Namespace(stream.key.Namespace).List(requestCtx, options)
				if err == nil {
					return list, nil
				}
				if !apierrors.IsNotAcceptable(err) && !apierrors.IsUnsupportedMediaType(err) {
					return nil, err
				}
			}
			return c.dynamic.Resource(stream.key.Resource).Namespace(stream.key.Namespace).List(requestCtx, options)
		},
		WatchFuncWithContext: func(requestCtx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			var source watch.Interface
			var err error
			if !stream.full && c.metadata != nil {
				source, err = c.metadata.Resource(stream.key.Resource).Namespace(stream.key.Namespace).Watch(requestCtx, options)
				if apierrors.IsNotAcceptable(err) || apierrors.IsUnsupportedMediaType(err) {
					source, err = c.dynamic.Resource(stream.key.Resource).Namespace(stream.key.Namespace).Watch(requestCtx, options)
				}
			} else {
				source, err = c.dynamic.Resource(stream.key.Resource).Namespace(stream.key.Namespace).Watch(requestCtx, options)
			}
			if err != nil {
				if requestCtx.Err() == nil {
					c.failStream(stream, err)
				}
				return nil, err
			}
			return c.monitorWatch(ctx, stream, source), nil
		},
	}
}

func (c *Runtime) monitorWatch(ctx context.Context, stream *resourceStream, source watch.Interface) watch.Interface {
	return monitorRuntimeWatch(ctx, source, func(err error) { c.failStream(stream, err) })
}

func monitorRuntimeWatch(ctx context.Context, source watch.Interface, fail func(error)) watch.Interface {
	monitored := &runtimeWatch{source: source, events: make(chan watch.Event), done: make(chan struct{})}
	started := time.Now()
	go func() {
		defer close(monitored.events)
		defer source.Stop()
		received := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-monitored.done:
				return
			case event, open := <-source.ResultChan():
				if !open {
					select {
					case <-monitored.done:
					default:
						// Reflector reconnects after normal closure. It logs
						// short empty watches without invoking its error handler.
						if !received && time.Since(started) < time.Second && ctx.Err() == nil {
							fail(&cache.VeryShortWatchError{Name: "exporter"})
						}
					}
					return
				}
				received = true
				if event.Type == watch.Error && ctx.Err() == nil {
					fail(apierrors.FromObject(event.Object))
				}
				select {
				case monitored.events <- event:
				case <-ctx.Done():
					return
				case <-monitored.done:
					return
				}
			}
		}
	}()
	return monitored
}

func (c *Runtime) failStream(stream *resourceStream, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failStreamLocked(stream, err)
}

func (c *Runtime) failStreamLocked(stream *resourceStream, err error) {
	if c.reader.streams[stream.key] != stream || err == io.EOF || errors.Is(err, context.Canceled) {
		return
	}
	reason := runtimeFailureReason(err)
	stream.failure++
	if stream.err == nil {
		c.store.SetPublicationFailure(stream.failureKey, reason)
		stream.failureKey = fmt.Sprintf("watch:%s:%s:%d:%d", stream.key.Resource, stream.key.Namespace, stream.run, stream.failure)
	}
	stream.err = runtimeError(err)
	c.routeChangesLocked(c.discovery.CoverageChanged(discovery.CoverageChange{
		Key: stream.key, Run: stream.run, FullObject: stream.full, Ready: stream.ready, Err: stream.err,
	}))
	c.invalidateMembershipLocked(stream.key.Namespace)
	c.updateHealthLocked()
	c.queue.Add(queuedWork{Operation: "verify", GVK: stream.gvk, Namespace: stream.key.Namespace})
}

func (c *Runtime) acceptObject(stream *resourceStream, object any, deleted bool) {
	u, err := runtimeObjectBody(object, stream.gvk)
	if err != nil {
		c.failStream(stream, err)
		return
	}
	ref := objectReference(u, stream.gvk)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reader.streams[stream.key] != stream {
		return
	}
	if deleted {
		c.deleteObjectLocked(ref, stream)
		return
	}
	if guards := c.retired[ref.UID]; len(guards) != 0 {
		return
	}
	key := refKey(ref)
	previous, hadPrevious := c.reader.objects[key]
	if hadPrevious && previous.observation.Ref == ref && previous.observation.Run == stream.run {
		return
	}
	if hadPrevious && previous.observation.Ref.UID != ref.UID {
		c.deleteObjectLocked(previous.observation.Ref, stream)
		hadPrevious = false
	}
	c.sequence++
	metadata := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Name: u.GetName(), Namespace: u.GetNamespace(), UID: u.GetUID(), ResourceVersion: u.GetResourceVersion(),
		CreationTimestamp: u.GetCreationTimestamp(), Labels: u.GetLabels(), Annotations: u.GetAnnotations(), OwnerReferences: slices.Clone(u.GetOwnerReferences()),
	}}
	observation := discovery.ObservedObject{Ref: ref, Owners: slices.Clone(u.GetOwnerReferences()), WatchKey: stream.key, Run: stream.run}
	c.reader.objects[key] = objectFact{observation: observation, revision: c.sequence, metadata: metadata}
	if c.reader.byUID[ref.UID] == nil {
		c.reader.byUID[ref.UID] = map[objectKey]struct{}{}
	}
	c.reader.byUID[ref.UID][key] = struct{}{}
	if c.reader.byStream[stream.key] == nil {
		c.reader.byStream[stream.key] = map[objectKey]struct{}{}
	}
	c.reader.byStream[stream.key][key] = struct{}{}
	delete(c.proofs, key)
	if !hadPrevious || !reflect.DeepEqual(previous.metadata.OwnerReferences, metadata.OwnerReferences) ||
		!reflect.DeepEqual(previous.metadata.Labels, metadata.Labels) ||
		!reflect.DeepEqual(previous.metadata.Annotations, metadata.Annotations) {
		c.invalidateMembershipLocked(ref.Namespace)
	}
	ownerChanged := hadPrevious && !reflect.DeepEqual(previous.metadata.OwnerReferences, metadata.OwnerReferences)
	c.suppressObjectLocked(ref, ownerChanged)
	changes := c.discovery.Upsert(observation)
	for _, changed := range changes.Objects {
		if changed != ref {
			c.suppressObjectLocked(changed, ownerChanged)
		}
	}
	c.routeChangesLocked(changes)
	c.queue.Add(queuedWork{Operation: "object", GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID})
	c.queue.Add(queuedWork{Operation: "verify", GVK: stream.gvk, Namespace: stream.key.Namespace})
}

func (c *Runtime) deleteObjectLocked(ref discovery.ObjectRef, stream *resourceStream) {
	key := refKey(ref)
	if current, exists := c.reader.objects[key]; exists && current.observation.Ref.UID == ref.UID {
		delete(c.reader.objects, key)
		delete(c.reader.byStream[current.observation.WatchKey], key)
		delete(c.reader.byUID[ref.UID], key)
		if len(c.reader.byUID[ref.UID]) == 0 {
			delete(c.reader.byUID, ref.UID)
		}
		delete(c.proofs, key)
	}
	guards := c.retired[ref.UID]
	if guards == nil {
		guards = map[discovery.CoverageKey]uint64{}
		for coverage, candidate := range c.reader.streams {
			if candidate.gvk.GroupKind() == ref.GVK.GroupKind() && candidate.key.Namespace == ref.Namespace {
				guards[coverage] = candidate.run
			}
		}
		c.retired[ref.UID] = guards
	}
	if stream != nil {
		delete(guards, stream.key)
	}
	if len(guards) == 0 {
		delete(c.retired, ref.UID)
	}
	c.sequence++
	c.invalidateMembershipLocked(ref.Namespace)
	if root := c.associations[ref.UID]; root != "" {
		if p := c.store.Published(root); p != nil {
			c.store.Publish(p.WithPod(ref.UID, nil))
		}
		if history, exists := c.history[root]; exists {
			history.record = history.record.WithPod(ref.UID, nil)
			c.history[root] = history
		}
		c.unlinkPodLocked(ref.UID)
	}
	c.removeRootLocked(ref.UID, ref.Namespace)
	changes := c.discovery.Delete(ref)
	for _, changed := range changes.Objects {
		c.suppressObjectLocked(changed, false)
	}
	c.routeChangesLocked(changes)
	if stream != nil && c.reader.streams[stream.key] == stream {
		c.queue.Add(queuedWork{Operation: "verify", GVK: stream.gvk, Namespace: stream.key.Namespace})
	}
}

func (c *Runtime) unlinkPodLocked(uid types.UID) {
	root := c.associations[uid]
	delete(c.associations, uid)
	delete(c.podsByRoot[root], uid)
	if len(c.podsByRoot[root]) == 0 {
		delete(c.podsByRoot, root)
	}
}

func (c *Runtime) removeRootLocked(uid types.UID, namespace string) {
	c.store.RemovePublication(uid)
	delete(c.history, uid)
	delete(c.rootsByNamespace[namespace], uid)
	if len(c.rootsByNamespace[namespace]) == 0 {
		delete(c.rootsByNamespace, namespace)
	}
	for pod := range c.podsByRoot[uid] {
		if c.associations[pod] == uid {
			delete(c.associations, pod)
		}
	}
	delete(c.podsByRoot, uid)
}

func (c *Runtime) routeChangesLocked(changes discovery.ChangeSet) {
	for _, request := range changes.MissingCoverage {
		c.queue.Add(queuedWork{Operation: "coverage", GVK: request.GVK, Namespace: request.Namespace, Full: request.FullObject})
	}
	for _, ref := range changes.Objects {
		c.queue.Add(queuedWork{Operation: "object", GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID})
	}
	for _, ref := range changes.Roots {
		c.queue.Add(queuedWork{Operation: "root", UID: ref.UID})
	}
}

func (c *Runtime) suppressObjectLocked(ref discovery.ObjectRef, ownerChanged bool) {
	if root := c.associations[ref.UID]; root != "" {
		if record := c.store.Published(root); record != nil {
			c.store.Publish(record.WithPod(ref.UID, nil))
		}
		discardHistory := ownerChanged
		if fact, observed := c.reader.objects[refKey(ref)]; observed && ref.GVK == runtimePodGVK {
			discardHistory = discardHistory || !c.podSelector.Matches(labels.Set(fact.metadata.Labels))
		}
		if discardHistory {
			if history, exists := c.history[root]; exists {
				history.record = history.record.WithPod(ref.UID, nil)
				c.history[root] = history
			}
		}
		if ownerChanged {
			c.unlinkPodLocked(ref.UID)
		}
	}
	if record := c.store.Published(ref.UID); record != nil {
		copy := *record
		copy.Workload.HasStatus = false
		copy.PodPages = nil
		copy.Creation = store.Evidence{}
		c.store.Publish(&copy)
	}
}

func (c *Runtime) invalidateMembershipLocked(namespace string) {
	if proof, exists := c.membership[namespace]; exists {
		c.store.SetPublicationFailure(fmt.Sprintf("membership:%s:%d", namespace, proof.revision), store.ReasonAPIUnavailable)
	}
	c.membershipRevision[namespace]++
	delete(c.membership, namespace)
	if c.namespaces[namespace] {
		c.queue.Add(queuedWork{Operation: "verify", GVK: runtimePodGVK, Namespace: namespace})
	}
}

func runtimeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, discovery.ErrPermissionDenied), apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return fmt.Errorf("%w: %w", discovery.ErrPermissionDenied, err)
	case errors.Is(err, discovery.ErrUnavailable):
		return err
	default:
		return fmt.Errorf("%w: %w", discovery.ErrUnavailable, err)
	}
}

func runtimeFailureReason(err error) string {
	switch {
	case errors.Is(err, discovery.ErrPermissionDenied), apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return store.ReasonPermissionDenied
	case errors.Is(err, discovery.ErrInvalidDefinition):
		return store.ReasonInvalidDefinition
	case errors.Is(err, discovery.ErrEvaluation):
		return store.ReasonEvaluationFailed
	case errors.Is(err, discovery.ErrUnresolvedOwner):
		return store.ReasonUnresolvedOwner
	default:
		return store.ReasonAPIUnavailable
	}
}
