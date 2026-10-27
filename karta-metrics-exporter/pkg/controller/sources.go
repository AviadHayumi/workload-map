// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

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

// Provenance stays outside Kubernetes fields and follows Reflector initialization copies.
type sourceObject struct {
	*unstructured.Unstructured
	sequence     uint64
	failureEpoch uint64
	expiresAt    time.Time
}

type watchedStream struct {
	source watch.Interface
	events chan watch.Event
	cancel context.CancelFunc
	once   sync.Once
}

type sourceRead struct {
	sequence, generation, failureEpoch, verifiedRead uint64
	started                                          time.Time
}

type reflectorStore struct {
	runtime *Runtime
	source  *resourceWatch
}

var _ cache.ReflectorStore = (*reflectorStore)(nil)

func (o *sourceObject) DeepCopyObject() kruntime.Object {
	return &sourceObject{Unstructured: o.DeepCopy(), sequence: o.sequence, failureEpoch: o.failureEpoch, expiresAt: o.expiresAt}
}

func (s *reflectorStore) Add(object any) error    { return s.accept(object, false) }
func (s *reflectorStore) Update(object any) error { return s.accept(object, false) }
func (s *reflectorStore) Delete(object any) error { return s.accept(object, true) }
func (s *reflectorStore) Resync() error           { return nil }

func (s *reflectorStore) accept(object any, deleted bool) error {
	err := s.runtime.accept(s.source, object, deleted)
	if err != nil {
		s.runtime.mu.Lock()
		s.runtime.requestRelistLocked(s.source)
		s.runtime.mu.Unlock()
	}
	return err
}

// Reflector advances its cursor even after Add/Update/Delete errors. Restarting
// with a new Reflector is the recovery boundary for a rejected current event.
func (r *Runtime) requestRelistLocked(w *resourceWatch) {
	if r.watches[w.key] != w || w.relist {
		return
	}
	w.relist = true
	r.failWatchLocked(w, discovery.ErrStale)
	r.controlQueue.AddRateLimited(sourceKey{Operation: restartSource, Watch: w.key})
}

func (s *reflectorStore) Replace(values []any, _ string) error {
	r, w := s.runtime, s.source
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[w.key] != w || (w.ctx != nil && w.ctx.Err() != nil) {
		return nil
	}
	read := w.replacement
	if read == nil || read.failureEpoch != w.failureEpoch || !r.config.Now().Before(read.started.Add(r.config.MaxAge)) {
		r.requestRelistLocked(w)
		return discovery.ErrStale
	}
	acceptedAt := r.config.Now()
	objects := make(map[objectKey]*sourceObject, len(values))
	r.reader.mu.Lock()
	state := r.reader.watchReads[watchReadScope{gvk: w.gvk, namespace: w.key.Namespace}]
	valid := true
	for _, value := range values {
		object, ok := value.(*sourceObject)
		if !ok || object.Unstructured == nil {
			valid = false
			break
		}
		object = object.DeepCopyObject().(*sourceObject)
		object.SetGroupVersionKind(w.gvk)
		object.SetManagedFields(nil)
		key := keyOf(refOf(object.Unstructured))
		if object.GetUID() == "" || object.GetName() == "" || (w.key.Namespace != "" && object.GetNamespace() != w.key.Namespace) {
			valid = false
			break
		}
		if _, duplicate := objects[key]; duplicate {
			valid = false
			break
		}
		if entry := r.reader.objects[key]; entry.sequence > read.generation || entry.invalidated > read.sequence {
			valid = false
			break
		}
		if state != nil && state.invalidated[key].sequence > read.sequence {
			valid = false
			break
		}
		if !acceptedAt.Before(object.expiresAt) || object.failureEpoch != read.failureEpoch || object.sequence < read.sequence {
			valid = false
			break
		}
		objects[key] = object
	}
	// Absence is authoritative only if no event or alias invalidation superseded
	// the complete read, including names that are absent from the new snapshot.
	for key := range w.objects {
		if entry := r.reader.objects[key]; entry.sequence > read.generation || entry.invalidated > read.sequence {
			valid = false
		}
		if state != nil && state.invalidated[key].sequence > read.sequence {
			valid = false
		}
	}
	if !valid {
		r.reader.mu.Unlock()
		r.requestRelistLocked(w)
		return discovery.ErrStale
	}
	r.recoverWatchLocked(w)
	removed := []*unstructured.Unstructured{}
	for key := range w.objects {
		if _, found := objects[key]; found {
			continue
		}
		old := r.reader.objects[key].object
		if old != nil {
			removed = append(removed, old)
			if err := r.acceptObjectLocked(w, &sourceObject{Unstructured: old, sequence: read.sequence, failureEpoch: read.failureEpoch, expiresAt: read.started.Add(r.config.MaxAge)}, true, acceptedAt); err != nil {
				r.reader.mu.Unlock()
				r.requestRelistLocked(w)
				return err
			}
		}
	}
	for _, object := range objects {
		if err := r.acceptObjectLocked(w, object, false, acceptedAt); err != nil {
			r.reader.mu.Unlock()
			r.requestRelistLocked(w)
			return err
		}
	}
	w.ready, w.relist = true, false
	r.reader.mu.Unlock()
	if w.gvk == namespaceGVK {
		for _, object := range removed {
			r.finishSourceObjectLocked(w, object, true)
		}
		for _, object := range objects {
			r.finishSourceObjectLocked(w, object.Unstructured, false)
		}
	}
	w.verifiedAt, w.verifiedRead = read.started, read.verifiedRead
	if w.gvk == kartaGVK {
		r.definitionsDirty = true
		_ = r.refreshDefinitionsLocked()
		r.controlQueue.Add(sourceKey{Operation: syncSources})
	}
	r.publishHealthLocked()
	r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: w.key})
	if w.gvk == podGVK {
		r.controlQueue.Add(sourceKey{Operation: primeOwners})
	}
	return nil
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
		} else if namespace != "" && !slices.Contains(namespaces, namespace) {
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
	scope := watchReadScope{gvk: w.gvk, namespace: w.key.Namespace}
	if read := r.reader.watchReads[scope]; read != nil {
		read.retired = true
		if read.readers == 0 {
			delete(r.reader.watchReads, scope)
		}
	}
	trackVersion(&r.reader.versions, w.gvk, -1)
	delete(r.reader.activeKeys, w.failureKey)
	r.deps.Publications.SetPublicationFailure(w.failureKey, store.ReasonAPIUnavailable)
	for key := range w.objects {
		entry, ok := r.reader.objects[key]
		if !ok || entry.stream != w.key {
			continue
		}
		ref := refOf(entry.object)
		r.removeCandidateLocked(ref)
		r.invalidatePathDependentsLocked(ref, store.ReasonAPIUnavailable)
		r.reader.retireProofLocked(r.reader.objects[key].proofKey, store.ReasonAPIUnavailable)
		delete(r.reader.objects, key)
		if !r.terminalOwnerBoundary(entry.object) {
			r.reader.invalidateAliasesLocked(key, ref.UID)
			for gvk := range r.reader.versions[ref.GVK.GroupKind()] {
				aliasKey := objectKey{GVK: gvk, Namespace: ref.Namespace, Name: ref.Name}
				if alias, found := r.reader.objects[aliasKey]; found && alias.object.GetUID() == ref.UID {
					r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: alias.stream})
				}
			}
		}
		root := r.roots[ref.UID]
		if root != nil && root.watch != w.key {
			if replacement := r.watches[root.watch]; replacement != nil {
				if failure, unresolved := root.resolutionErrors[w.gvk]; unresolved {
					delete(root.resolutionErrors, w.gvk)
					root.failResolution(replacement.gvk, failure.cause)
					failure = root.resolutionErrors[replacement.gvk]
					r.sequence++
					failure.verifyAfter = r.sequence
					root.resolutionErrors[replacement.gvk] = failure
					r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: replacement.key})
				}

				key := objectKey{GVK: replacement.gvk, Namespace: ref.Namespace, Name: ref.Name}
				if observed, ok := r.reader.objects[key]; !ok || observed.object.GetUID() != ref.UID {
					if replacement.pendingRoots == nil {
						replacement.pendingRoots = map[types.UID]discovery.ObjectRef{}
					}
					replacement.pendingRoots[ref.UID] = ref
					r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: replacement.key})
				}
				r.rootQueue.Add(rootKey{UID: ref.UID})
				continue
			}
		}
		r.deleteObjectLocked(ref)
	}
	r.deps.Publications.RetirePublicationFailure(w.failureKey)
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
	w := &resourceWatch{discover: discover, objects: map[objectKey]struct{}{}, key: key, gvk: gvk, cancel: cancel, ctx: child, failureKey: fmt.Sprintf("watch:%s/%s:%d", key.Resource, key.Namespace, r.sequence)}
	lw := &cache.ListWatch{
		ListWithContextFunc: func(requestCtx context.Context, options metav1.ListOptions) (kruntime.Object, error) {
			return r.listWatch(requestCtx, w, options)
		},
		WatchFuncWithContext: func(requestCtx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			return r.watchResource(requestCtx, w, options)
		},
	}
	w.restart = make(chan struct{}, 1)
	r.watches[key] = w
	r.reader.mu.Lock()
	trackVersion(&r.reader.versions, gvk, 1)
	r.reader.watchStateLocked(watchReadScope{gvk: gvk, namespace: key.Namespace})
	r.reader.activeKeys[w.failureKey] = true
	r.reader.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for child.Err() == nil {
			round, stop := context.WithCancel(child)
			reflector := cache.NewReflectorWithOptions(lw, &sourceObject{}, &reflectorStore{runtime: r, source: w}, cache.ReflectorOptions{Name: key.Resource.String() + "/" + key.Namespace})
			done := make(chan struct{})
			go func() { defer close(done); reflector.RunWithContext(round) }()
			select {
			case <-child.Done():
			case <-w.restart:
			}
			stop()
			<-done
		}
	}()
	return key, nil
}

// A LIST can recover only failures that existed before that read started.
func (r *Runtime) listWatch(ctx context.Context, w *resourceWatch, options metav1.ListOptions) (kruntime.Object, error) {
	r.mu.Lock()
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.watches[w.key] != w {
		r.mu.Unlock()
		return nil, discovery.ErrStale
	}
	// Recovery must confirm current state before replacing accepted objects.
	// A fresh Reflector otherwise starts with an arbitrarily old cached LIST.
	if w.relist && options.Continue == "" {
		options.ResourceVersion = ""
		options.ResourceVersionMatch = ""
	}
	epoch := w.failureEpoch
	r.sequence++
	r.reader.mu.Lock()
	readerSequence := r.reader.sequence
	started := r.config.Now()
	scope := watchReadScope{gvk: w.gvk, namespace: w.key.Namespace}
	read := r.reader.watchStateLocked(scope)
	read.readers++
	r.reader.mu.Unlock()
	if options.Continue == "" || w.replacement == nil {
		w.replacement = &sourceRead{sequence: readerSequence, generation: w.generation, failureEpoch: epoch, started: started, verifiedRead: r.sequence}
	} else {
		readerSequence, started, epoch = w.replacement.sequence, w.replacement.started, w.replacement.failureEpoch
	}
	r.mu.Unlock()
	defer func() {
		r.reader.mu.Lock()
		read.readers--
		if read.retired && read.readers == 0 && r.reader.watchReads[scope] == read {
			delete(r.reader.watchReads, scope)
		}
		r.reader.mu.Unlock()
	}()
	requestCtx, cancel := context.WithTimeout(ctx, r.reader.options.RequestTimeout)
	defer cancel()
	list, err := r.reader.clients.Dynamic.Resource(w.key.Resource).Namespace(w.key.Namespace).List(requestCtx, options)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if r.watches[w.key] != w {
		return list, err
	}
	if w.failureEpoch != epoch || !r.config.Now().Before(started.Add(r.config.MaxAge)) {
		return nil, discovery.ErrStale
	}
	if err != nil {
		r.failWatchLocked(w, err)
		return nil, err
	}
	result := &metav1.List{ListMeta: metav1.ListMeta{ResourceVersion: list.GetResourceVersion(), Continue: list.GetContinue(), RemainingItemCount: list.GetRemainingItemCount()}}
	for i := range list.Items {
		result.Items = append(result.Items, kruntime.RawExtension{Object: &sourceObject{Unstructured: &list.Items[i], sequence: readerSequence, failureEpoch: epoch, expiresAt: started.Add(r.config.MaxAge)}})
	}
	return result, nil
}

// WatchList is optional. Unsupported initial-event negotiation and expired
// cursors recover through the reflector's LIST path without revoking live proof.
func (r *Runtime) watchResource(ctx context.Context, w *resourceWatch, options metav1.ListOptions) (watch.Interface, error) {
	var initial *sourceRead
	if options.SendInitialEvents != nil && *options.SendInitialEvents {
		r.mu.Lock()
		r.sequence++
		r.reader.mu.RLock()
		initial = &sourceRead{sequence: r.reader.sequence, generation: w.generation, failureEpoch: w.failureEpoch, started: r.config.Now(), verifiedRead: r.sequence}
		r.reader.mu.RUnlock()
		w.replacement = initial
		r.mu.Unlock()
	}
	source, err := r.reader.clients.Dynamic.Resource(w.key.Resource).Namespace(w.key.Namespace).Watch(ctx, options)
	if err != nil {
		r.mu.Lock()
		if options.SendInitialEvents != nil && *options.SendInitialEvents && apierrors.IsBadRequest(err) {
			r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: w.key})
		} else {
			r.watchErrorLocked(w, err)
		}
		r.mu.Unlock()
		return nil, err
	}
	return r.monitorSource(ctx, w, source, initial), nil
}

func (r *Runtime) watchErrorLocked(w *resourceWatch, cause error) {
	if apierrors.IsGone(cause) || apierrors.IsResourceExpired(cause) {
		r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: w.key})
	} else {
		r.failWatchLocked(w, cause)
	}
}

func (r *Runtime) monitorSource(ctx context.Context, w *resourceWatch, source watch.Interface, initial *sourceRead) watch.Interface {
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
						if w.gvk == kartaGVK {
							r.failWatchLocked(w, errors.New("karta watch closed"))
						}
						r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: w.key})
						r.mu.Unlock()
					}
					return
				}
				if event.Type == watch.Error {
					r.mu.Lock()
					r.watchErrorLocked(w, apierrors.FromObject(event.Object))
					r.mu.Unlock()
				}
				if object, ok := event.Object.(*unstructured.Unstructured); ok {
					r.mu.Lock()
					epoch := w.failureEpoch
					r.reader.mu.RLock()
					sequence := r.reader.sequence
					expiresAt := r.config.Now().Add(r.config.MaxAge)
					r.reader.mu.RUnlock()
					r.mu.Unlock()
					if initial != nil {
						epoch = initial.failureEpoch
						sequence, expiresAt = initial.sequence, initial.started.Add(r.config.MaxAge)
						if event.Type == watch.Bookmark {
							initial = nil
						}
					}
					object.SetManagedFields(nil)
					object.SetGroupVersionKind(w.gvk)
					event.Object = &sourceObject{Unstructured: object, sequence: sequence, failureEpoch: epoch, expiresAt: expiresAt}
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
	if r.watches[w.key] != w || errors.Is(err, context.Canceled) {
		return
	}
	w.failureEpoch++
	first := w.err == nil
	r.reader.mu.Lock()
	for key, state := range r.reader.metadataReads {
		if state.priming && key.GVK == w.gvk && (w.key.Namespace == "" || key.Namespace == w.key.Namespace) {
			r.reader.invalidateMetadataReadLocked(key)
		}
	}
	delete(r.reader.activeKeys, w.failureKey)
	podRoots := map[types.UID]bool{}
	for key := range w.objects {
		if entry, ok := r.reader.objects[key]; ok {
			r.invalidatePathDependentsLocked(refOf(entry.object), failureReason(err))
			if w.gvk == podGVK {
				if uid := r.podRoots[entry.object.GetUID()]; uid != "" {
					podRoots[uid] = true
				}
			}
		}
	}
	r.reader.mu.Unlock()
	w.err = err
	if w.gvk == kartaGVK {
		r.deps.Publications.SetPublicationFailure(r.definitionKey, failureReason(err))
		r.definitionError = err
		for uid, root := range r.roots {
			root.generation++
			r.rootQueue.Add(rootKey{UID: uid})
		}
	}
	r.deps.Publications.SetPublicationFailure(w.failureKey, failureReason(err))
	for uid, root := range r.roots {
		if root.watch == w.key || podRoots[uid] {
			root.fullRefresh = true
			r.rootQueue.Add(rootKey{UID: uid})
		}
	}
	r.publishHealthLocked()
	if first {
		r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: w.key})
	}
}
func (r *Runtime) recoverWatchLocked(w *resourceWatch) {
	if w.err != nil {
		r.sequence++
		w.failureKey = fmt.Sprintf("watch:%s/%s:%d", w.key.Resource, w.key.Namespace, r.sequence)
		w.err = nil
		r.reader.activeKeys[w.failureKey] = true
	}
}

func (r *Runtime) accept(w *resourceWatch, value any, deleted bool) error {
	if tombstone, ok := value.(cache.DeletedFinalStateUnknown); ok {
		value = tombstone.Obj
	}
	source, ok := value.(*sourceObject)
	if !ok || source.Unstructured == nil {
		return fmt.Errorf("unexpected source object %T", value)
	}
	source = source.DeepCopyObject().(*sourceObject)
	source.SetGroupVersionKind(w.gvk)
	source.SetManagedFields(nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[w.key] != w || (w.ctx != nil && w.ctx.Err() != nil) {
		return nil
	}
	if w.relist {
		return discovery.ErrStale
	}
	r.reader.mu.Lock()
	if old := r.reader.objects[keyOf(refOf(source.Unstructured))]; deleted && old.object != nil && old.object.GetUID() != source.GetUID() {
		r.reader.mu.Unlock()
		return nil
	}
	err := r.acceptObjectLocked(w, source, deleted, r.config.Now())
	r.reader.mu.Unlock()
	if err == nil {
		r.finishSourceObjectLocked(w, source.Unstructured, deleted)
	}
	return err
}

func (r *Runtime) acceptObjectLocked(w *resourceWatch, source *sourceObject, deleted bool, now time.Time) error {
	object := source.Unstructured
	ref := refOf(object)
	key := keyOf(ref)
	if r.watches[w.key] != w || (w.ctx != nil && w.ctx.Err() != nil) {
		return nil
	}
	w.generation++
	r.generation++
	old := r.reader.objects[key]
	staleRead := !now.Before(source.expiresAt) || source.failureEpoch != w.failureEpoch
	if read := r.reader.watchReads[watchReadScope{gvk: w.gvk, namespace: w.key.Namespace}]; read != nil {
		fence := read.invalidated[key]
		staleRead = staleRead || source.sequence < fence.sequence
	}
	newerProof := old.object != nil && now.Before(old.evidence.ExpiresAt)
	for _, proof := range old.evidence.Dependencies {
		if !r.reader.activeKeys[proof] {
			newerProof = false
		}
	}
	if staleRead && (deleted || newerProof || (old.object != nil && old.object.GetUID() != ref.UID)) {
		r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: w.key})
		return discovery.ErrStale
	}
	if state := r.reader.metadataReads[key]; state != nil && state.priming {
		r.reader.invalidateMetadataReadLocked(key)
	}
	retirementReason := store.ReasonAPIUnavailable
	if old.object != nil {
		retirementReason = objectChangeReason(old.object, object)
	}
	if old.object != nil && (deleted || retirementReason == store.ReasonUnresolvedOwner) {
		r.reader.invalidatePrimeOwnersLocked(old.object)
	}
	if !deleted && old.object != nil && old.object.GetUID() == ref.UID && old.object.GetResourceVersion() != ref.ResourceVersion {
		r.invalidatePathDependentsLocked(refOf(old.object), retirementReason)
	}
	if old.object != nil && old.object.GetUID() != ref.UID {
		r.deleteObjectLocked(refOf(old.object))
		r.reader.retireProofLocked(r.reader.objects[key].proofKey, store.ReasonUnresolvedOwner)
		delete(r.reader.objects, key)
	}
	if deleted {
		if old.object != nil && old.object.GetUID() == ref.UID {
			r.reader.retireProofLocked(r.reader.objects[key].proofKey, store.ReasonUnresolvedOwner)
			delete(r.reader.objects, key)
			delete(w.objects, key)
			r.deleteObjectLocked(ref)
		}
		return nil
	}
	w.objects[key] = struct{}{}
	delete(w.pendingRoots, ref.UID)
	entry := observation{sequence: w.generation, object: object, stream: w.key, evidence: store.Evidence{ExpiresAt: source.expiresAt, Dependencies: []string{w.failureKey}}}
	if old.object != nil && old.object.GetUID() == ref.UID && old.object.GetResourceVersion() == ref.ResourceVersion {
		entry.proofKey, entry.invalidated = old.proofKey, old.invalidated
		if old.evidence.ExpiresAt.IsZero() {
			entry.evidence = store.Evidence{}
		} else if entry.proofKey != "" {
			entry.evidence.Dependencies = append(entry.evidence.Dependencies, entry.proofKey)
		}
	} else {
		r.reader.retireProofLocked(old.proofKey, retirementReason)
	}
	if staleRead {
		r.reader.retireProofLocked(old.proofKey, retirementReason)
		entry.evidence = store.Evidence{}
		r.controlQueue.Add(sourceKey{Operation: verifySource, Watch: w.key})
	}
	r.reader.objects[key] = entry
	if !r.terminalOwnerBoundary(object) {
		r.reader.invalidateAliasesLocked(key, ref.UID)
	}
	if root := r.roots[ref.UID]; root != nil && old.object == nil && metav1.GetControllerOf(object) != nil {
		root.failResolution(ref.GVK, discovery.ErrPending)
		r.failPublicationLocked(ref.UID, discovery.ErrPending)
	}
	if old.object != nil && old.object.GetResourceVersion() != ref.ResourceVersion {
		if root := r.roots[r.podRoots[ref.UID]]; root != nil {
			r.invalidatePodLocked(root, ref.UID, store.ReasonAPIUnavailable)
		}
		if root := r.roots[ref.UID]; root != nil {
			if retirementReason == store.ReasonUnresolvedOwner {
				root.failResolution(ref.GVK, discovery.ErrPending)
			}
			r.failPublicationLocked(ref.UID, discovery.ErrStale)
		}
	}
	r.touchRoot(ref.UID)
	if w.discover || r.roots[ref.UID] != nil {
		candidate := r.candidates[ref.UID][key]
		if ref.GVK != podGVK && !r.terminalOwnerBoundary(object) && old.object != nil && retirementReason != store.ReasonUnresolvedOwner && candidate != nil && !candidate.dirty {
			candidate.ref = ref
			if len(candidate.path) > 0 {
				candidate.path[0] = ref
			}
		} else {
			r.enqueueObjectLocked(ref)
		}
	}
	return nil
}

func (r *Runtime) finishSourceObjectLocked(w *resourceWatch, object *unstructured.Unstructured, deleted bool) {
	if w.gvk == kartaGVK {
		r.definitionsDirty = true
		_ = r.refreshDefinitionsLocked()
	}
	if w.gvk == namespaceGVK && (deleted || !r.namespaceSelector.Matches(labelsFor(object))) {
		r.reader.mu.Lock()
		r.pruneMetadataLocked()
		r.reader.mu.Unlock()
		for uid, root := range r.roots {
			if root.ref.Namespace == object.GetName() {
				r.removeRoot(uid)
			}
		}
	}
	if w.gvk == namespaceGVK || w.gvk == kartaGVK {
		r.controlQueue.Add(sourceKey{Operation: syncSources})
	}
}

func (r *Runtime) removeCandidateLocked(ref discovery.ObjectRef) {
	if candidate := r.candidates[ref.UID][keyOf(ref)]; candidate != nil {
		r.setCandidatePathLocked(candidate, nil)
		delete(r.candidates[ref.UID], keyOf(ref))
		if len(r.candidates[ref.UID]) == 0 {
			delete(r.candidates, ref.UID)
		}
	}
}

func (r *Runtime) deleteObjectLocked(ref discovery.ObjectRef) {
	r.removeCandidateLocked(ref)
	r.invalidatePathDependentsLocked(ref, store.ReasonUnresolvedOwner)
	if rootUID := r.podRoots[ref.UID]; rootUID != "" {
		if root := r.roots[rootUID]; root != nil {
			r.invalidatePodLocked(root, ref.UID, store.ReasonUnresolvedOwner)
			r.removePodPathLocked(root, ref.UID)
		}
		delete(r.podRoots, ref.UID)
	}
	if r.roots[ref.UID] != nil {
		r.removeRoot(ref.UID)
		r.controlQueue.Add(sourceKey{Operation: syncSources})
	}
}

func (r *Runtime) verifyWatch(ctx context.Context, key watchKey) error {
	r.mu.Lock()
	w := r.watches[key]
	if w == nil {
		r.mu.Unlock()
		return nil
	}
	if w.relist {
		r.mu.Unlock()
		return discovery.ErrPending
	}
	generation, failureEpoch := w.generation, w.failureEpoch
	r.sequence++
	readSequence := r.sequence
	r.reader.mu.RLock()
	readerSequence := r.reader.sequence
	r.reader.mu.RUnlock()
	r.mu.Unlock()
	verificationStarted := r.config.Now()
	deadline := verificationStarted.Add(r.config.MaxAge)
	refs := map[types.UID]string{}
	err := r.reader.readMetadataList(ctx, key, nil, deadline, r.config.Now, func(object *metav1.PartialObjectMetadata) { refs[object.UID] = object.ResourceVersion })
	if err != nil {
		r.mu.Lock()
		if ctx.Err() == nil && w.failureEpoch == failureEpoch && !errors.Is(err, discovery.ErrStale) {
			r.failWatchLocked(w, err)
		}
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[key] != w {
		return nil
	}
	if w.failureEpoch != failureEpoch {
		return discovery.ErrStale
	}

	recovered := w.err != nil
	w.verifiedAt = verificationStarted
	w.verifiedRead = readSequence
	r.reader.mu.Lock()
	r.recoverWatchLocked(w)
	read := r.reader.watchReads[watchReadScope{gvk: w.gvk, namespace: w.key.Namespace}]
	renewals := map[types.UID]bool{}
	for objectKey := range w.objects {
		entry, ok := r.reader.objects[objectKey]
		if !ok {
			continue
		}
		if entry.sequence > generation || entry.invalidated > readerSequence || (read != nil && read.invalidated[objectKey].sequence > readerSequence) {
			continue
		}
		ref := refOf(entry.object)
		version, found := refs[ref.UID]
		if !found {
			r.reader.retireProofLocked(entry.proofKey, store.ReasonUnresolvedOwner)
			delete(r.reader.objects, objectKey)
			delete(w.objects, objectKey)
			if w.gvk == kartaGVK {
				r.definitionsDirty = true
			}
			r.deleteObjectLocked(ref)
			continue
		}
		unchanged := !recovered && version == ref.ResourceVersion && r.config.Now().Before(entry.evidence.ExpiresAt)
		for _, key := range entry.evidence.Dependencies {
			if !r.reader.activeKeys[key] {
				unchanged = false
			}
		}
		if version != ref.ResourceVersion {
			r.reader.retireProofLocked(entry.proofKey, store.ReasonAPIUnavailable)
			entry.evidence = store.Evidence{}
			r.failPublicationLocked(ref.UID, discovery.ErrStale)
			if root := r.roots[r.podRoots[ref.UID]]; root != nil {
				r.invalidatePodLocked(root, ref.UID, store.ReasonAPIUnavailable)
			}
		} else {
			entry.evidence = store.Evidence{ExpiresAt: w.verifiedAt.Add(r.config.MaxAge), Dependencies: []string{w.failureKey}}
			if entry.proofKey != "" {
				if !r.reader.activeKeys[entry.proofKey] {
					r.reader.sequence++
					entry.proofKey = fmt.Sprintf("ownership:%s:%d", ref.UID, r.reader.sequence)
					r.reader.activeKeys[entry.proofKey] = true
				}
				entry.evidence.Dependencies = append(entry.evidence.Dependencies, entry.proofKey)
			}
		}
		r.reader.objects[objectKey] = entry
		if !unchanged {
			r.invalidatePathDependentsLocked(ref, store.ReasonAPIUnavailable)
		}
		rootUID := ref.UID
		root := r.roots[rootUID]
		if root == nil {
			rootUID = r.podRoots[ref.UID]
			root = r.roots[rootUID]
		}
		if root != nil && unchanged {
			renewals[rootUID] = true
			continue
		}
		if root != nil {
			root.fullRefresh = true
			r.rootQueue.Add(rootKey{UID: rootUID})
			if ref.GVK == podGVK {
				r.dirtyPod(root, ref.UID)
			}
		}
		if w.discover || root != nil {
			r.enqueueObjectLocked(ref)
		}

	}
	for uid := range renewals {
		if !r.scheduleRenewalLocked(uid) {
			root := r.roots[uid]
			if len(root.resolutionErrors) > 0 {
				r.enqueueObjectLocked(root.ref)
			}
			for pod, state := range root.pods {
				if path := state.path; state.resolveError != nil && len(path) > 0 {
					r.inputQueue.Add(inputKey{Object: keyOf(path[0]), UID: pod})
				}
			}
		}
	}

	for uid, ref := range w.pendingRoots {
		if _, found := refs[uid]; !found {
			r.deleteObjectLocked(ref)
			delete(w.pendingRoots, uid)
		}
	}
	var definitionsMissing bool
	if w.gvk == kartaGVK {
		for objectKey := range w.objects {
			if entry := r.reader.objects[objectKey]; entry.object != nil {
				delete(refs, entry.object.GetUID())
			}
		}
		definitionsMissing = len(refs) > 0
	}
	r.reader.mu.Unlock()
	if w.gvk == kartaGVK {
		if definitionsMissing {
			r.failWatchLocked(w, discovery.ErrPending)
		}
		_ = r.refreshDefinitionsLocked()
	}
	r.publishHealthLocked()
	return nil
}
