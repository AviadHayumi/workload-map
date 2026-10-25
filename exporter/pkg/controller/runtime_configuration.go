// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

var (
	namespaceGVK = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
	kartaGVR     = schema.GroupVersionResource{Group: "run.ai", Version: "v1alpha1", Resource: "kartas"}
)

func (c *Runtime) startConfigurationWatches(ctx context.Context) error {
	start := func(gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, handler func(any, bool), isNamespace bool) error {
		listWatch := &cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
				defer cancel()
				return c.dynamic.Resource(gvr).List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				source, err := c.dynamic.Resource(gvr).Watch(ctx, options)
				if err != nil {
					if ctx.Err() == nil {
						c.failConfiguration(isNamespace, err)
					}
					return nil, err
				}
				return monitorRuntimeWatch(ctx, source, func(err error) { c.failConfiguration(isNamespace, err) }), nil
			},
		}
		informer := cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(listWatch, c.dynamic),
			&unstructured.Unstructured{}, 0, cache.Indexers{})
		registration, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj any) { handler(obj, false) },
			UpdateFunc: func(_, obj any) { handler(obj, false) },
			DeleteFunc: func(obj any) { handler(obj, true) },
		})
		if err != nil {
			return err
		}
		if err := informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) { c.failConfiguration(isNamespace, err) }); err != nil {
			return err
		}
		c.wg.Add(2)
		go func() { defer c.wg.Done(); informer.Run(ctx.Done()) }()
		go func() {
			defer c.wg.Done()
			if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced, registration.HasSynced) {
				return
			}
			c.mu.Lock()
			if isNamespace {
				c.namespaceReady = true
			} else {
				c.definitionsReady = true
			}
			c.mu.Unlock()
			c.queue.Add(queuedWork{Operation: "definitions"})
		}()
		return nil
	}
	if err := start(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, namespaceGVK, c.acceptNamespace, true); err != nil {
		return err
	}
	if c.options.Definitions.Cluster.Enabled {
		if err := start(kartaGVR, v1alpha1.GroupVersion.WithKind("Karta"), c.acceptDefinition, false); err != nil {
			return err
		}
	} else {
		c.mu.Lock()
		c.definitionsReady = true
		c.mu.Unlock()
	}
	c.queue.Add(queuedWork{Operation: "definitions"})
	return nil
}

func (c *Runtime) acceptNamespace(object any, deleted bool) {
	namespace, err := runtimeObjectBody(object, namespaceGVK)
	if err != nil {
		c.failConfiguration(true, err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.namespaceObjects[namespace.GetName()]
	if old != nil && old.GetUID() != namespace.GetUID() && deleted {
		return
	}
	if old != nil && old.GetUID() == namespace.GetUID() && old.GetResourceVersion() == namespace.GetResourceVersion() && !deleted {
		return
	}
	if old != nil && old.GetUID() != namespace.GetUID() && c.namespaces[namespace.GetName()] {
		c.removeNamespaceLocked(namespace.GetName())
	}
	if deleted {
		delete(c.namespaceObjects, namespace.GetName())
	} else {
		c.namespaceObjects[namespace.GetName()] = namespace
	}
	selected := !deleted && c.namespaceSelector.Matches(labels.Set(namespace.GetLabels()))
	previouslySelected := c.namespaces[namespace.GetName()]
	if !deleted && old != nil && old.GetUID() == namespace.GetUID() &&
		selected == previouslySelected && c.namespaceError == nil {
		return
	}
	c.scopeRevision++
	c.namespaceEvidence = store.Evidence{}
	if selected && !previouslySelected && c.namespaceError == nil {
		c.namespaces[namespace.GetName()] = true
		c.enqueueNamespaceCoverageLocked(namespace.GetName())
	} else if !selected && previouslySelected {
		c.removeNamespaceLocked(namespace.GetName())
	}
	c.queue.Add(queuedWork{Operation: "definitions"})
}

func (c *Runtime) enqueueNamespaceCoverageLocked(namespace string) {
	c.queue.Add(queuedWork{Operation: "coverage", GVK: runtimePodGVK, Namespace: namespace, Full: true})
	for _, gvk := range c.options.RequiredKinds {
		c.queue.Add(queuedWork{Operation: "coverage", GVK: gvk, Namespace: namespace, Full: gvk == runtimePodGVK})
	}
	for groupKind, gvk := range c.knownKinds {
		selection := c.selections[groupKind]
		if selection.Definition != nil {
			gvk = selection.GVK
		}
		c.queue.Add(queuedWork{Operation: "coverage", GVK: gvk, Namespace: namespace, Full: selection.Definition != nil})
	}
}

func (c *Runtime) removeNamespaceLocked(namespace string) {
	delete(c.namespaces, namespace)
	c.invalidateMembershipLocked(namespace)
	delete(c.podLists, namespace)
	for key, stream := range c.reader.streams {
		if key.Namespace != namespace {
			continue
		}
		stream.cancel()
		c.store.SetPublicationFailure(stream.failureKey, store.ReasonAPIUnavailable)
		delete(c.reader.streams, key)
		c.routeChangesLocked(c.discovery.CoverageChanged(discovery.CoverageChange{Key: key, Run: stream.run, Removed: true}))
		for objectKey := range c.reader.byStream[key] {
			fact := c.reader.objects[objectKey]
			c.deleteObjectLocked(fact.observation.Ref, nil)
		}
		delete(c.reader.byStream, key)
	}
	for uid := range c.rootsByNamespace[namespace] {
		c.removeRootLocked(uid, namespace)
	}
	delete(c.rootsByNamespace, namespace)
	delete(c.membershipRevision, namespace)
	for uid, guards := range c.retired {
		for key := range guards {
			if key.Namespace == namespace {
				delete(guards, key)
			}
		}
		if len(guards) == 0 {
			delete(c.retired, uid)
		}
	}
	c.updateHealthLocked()
}

func (c *Runtime) acceptDefinition(object any, deleted bool) {
	body, err := runtimeObjectBody(object, v1alpha1.GroupVersion.WithKind("Karta"))
	if err != nil {
		c.failConfiguration(false, err)
		return
	}
	definition := &v1alpha1.Karta{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(body.Object, definition); err != nil {
		c.failConfiguration(false, err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.definitions[definition.UID]
	if previous != nil && previous.ResourceVersion == definition.ResourceVersion && !deleted {
		return
	}
	affected := map[schema.GroupKind]bool{catalog.RootKey(definition).GroupKind(): true}
	if previous != nil {
		affected[catalog.RootKey(previous).GroupKind()] = true
	}
	if deleted {
		delete(c.definitions, definition.UID)
	} else {
		for uid, old := range c.definitions {
			if old.Name == definition.Name && uid != definition.UID {
				affected[catalog.RootKey(old).GroupKind()] = true
				delete(c.definitions, uid)
			}
		}
		c.definitions[definition.UID] = definition
	}
	c.definitionRevision++
	c.definitionEvidence = store.Evidence{}
	accepted := make([]*v1alpha1.Karta, 0, len(c.definitions))
	for _, candidate := range c.definitions {
		accepted = append(accepted, candidate)
	}
	snapshot, err := kartadefinitioninventory.NewSnapshot(c.options.Definitions, accepted)
	if err != nil {
		c.definitionsError = err
		c.store.SetPublicationFailure(c.definitionFailureKey, runtimeFailureReason(err))
		c.queue.Add(queuedWork{Operation: "definitions"})
		return
	}
	for groupKind := range affected {
		selected, _ := snapshot.Candidate(groupKind.WithVersion(""))
		c.acceptSelectionLocked(groupKind, selected)
	}
	c.queue.Add(queuedWork{Operation: "definitions"})
}

func (c *Runtime) acceptSelectionLocked(kind schema.GroupKind, selection kartadefinitioninventory.Selection) {
	previous := c.selections[kind]
	if sameSelection(previous, selection) {
		return
	}
	c.withdrawInterpretationLocked(kind)
	c.selections[kind] = selection
	if previous.Definition != nil && (selection.Definition == nil || previous.GVK != selection.GVK) {
		for namespace := range c.namespaces {
			c.queue.Add(queuedWork{Operation: "metadata", GVK: previous.GVK, Namespace: namespace})
		}
	}
	if selection.Definition != nil {
		c.knownKinds[kind] = selection.GVK
		for namespace := range c.namespaces {
			c.queue.Add(queuedWork{Operation: "coverage", GVK: selection.GVK, Namespace: namespace, Full: true})
		}
	}
}

func (c *Runtime) withdrawInterpretationLocked(groupKind schema.GroupKind) {
	c.routeChangesLocked(c.discovery.DefinitionChanged(groupKind))
	for uid, history := range c.history {
		if history.record.Workload.Ref.Group != groupKind.Group || history.record.Workload.Ref.Kind != groupKind.Kind {
			continue
		}
		if previous := c.store.Published(uid); previous != nil {
			next := *previous
			next.Workload.HasStatus = false
			next.Workload.Karta, next.Workload.KartaSource = "", ""
			next.PodPages = nil
			next.Membership = store.Evidence{}
			c.store.Publish(&next)
		}
		delete(c.history, uid)
		c.queue.Add(queuedWork{Operation: "root", UID: uid})
	}
}

func (c *Runtime) failConfiguration(namespace bool, err error) {
	if err == io.EOF || errors.Is(err, context.Canceled) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if namespace {
		c.scopeRevision++
		if c.namespaceError == nil {
			c.store.SetPublicationFailure(c.namespaceFailureKey, runtimeFailureReason(err))
			c.namespaceFailureKey = fmt.Sprintf("namespaces:%d", c.scopeRevision)
		}
		c.namespaceError = runtimeError(err)
		c.namespaceEvidence = store.Evidence{}
	} else {
		c.definitionRevision++
		if c.definitionsError == nil {
			c.store.SetPublicationFailure(c.definitionFailureKey, runtimeFailureReason(err))
			c.definitionFailureKey = fmt.Sprintf("definitions:%d", c.definitionRevision)
		}
		c.definitionsError = runtimeError(err)
		c.definitionEvidence = store.Evidence{}
		for kind := range c.knownKinds {
			c.routeChangesLocked(c.discovery.DefinitionChanged(kind))
		}
	}
	c.queue.Add(queuedWork{Operation: "definitions"})
}

func (c *Runtime) refreshSelections(ctx context.Context) error {
	c.mu.Lock()
	if !c.namespaceReady || !c.definitionsReady {
		c.mu.Unlock()
		return discovery.ErrPending
	}
	scope, definitionRevision := c.scopeRevision, c.definitionRevision
	definitions := make([]*v1alpha1.Karta, 0, len(c.definitions))
	for _, definition := range c.definitions {
		definitions = append(definitions, definition.DeepCopy())
	}
	namespaceRefs := make(map[string]discovery.ObjectRef, len(c.namespaceObjects))
	for name, namespace := range c.namespaceObjects {
		namespaceRefs[name] = objectReference(namespace, namespaceGVK)
	}
	kinds := make(map[schema.GroupKind]schema.GroupVersionKind, len(c.knownKinds))
	for kind, gvk := range c.knownKinds {
		kinds[kind] = gvk
	}
	c.mu.Unlock()
	expected, err := kartadefinitioninventory.NewSnapshot(c.options.Definitions, definitions)
	if err != nil {
		return err
	}
	for _, definition := range catalog.List() {
		gvk := catalog.RootKey(definition)
		kinds[gvk.GroupKind()] = gvk
	}
	for _, definition := range definitions {
		gvk := catalog.RootKey(definition)
		kinds[gvk.GroupKind()] = gvk
	}
	selections := map[schema.GroupKind]kartadefinitioninventory.Selection{}
	raw, rawErr := expected.Selections()
	if rawErr != nil && !errors.Is(rawErr, discovery.ErrInvalidDefinition) {
		return rawErr
	}
	for _, selected := range raw {
		selections[selected.GVK.GroupKind()] = selected
	}
	actualSelections, enumerates := c.inventory.(interface {
		Selections() ([]kartadefinitioninventory.Selection, error)
	})
	actualRaw := map[schema.GroupKind]kartadefinitioninventory.Selection{}
	if enumerates {
		raw, err := actualSelections.Selections()
		if err != nil && !errors.Is(err, discovery.ErrInvalidDefinition) {
			c.failConfiguration(false, err)
			return err
		}
		for _, selected := range raw {
			actualRaw[selected.GVK.GroupKind()] = selected
		}
	}
	for kind, gvk := range kinds {
		want, selectErr := expected.Lookup(gvk)
		actual, actualErr := c.inventory.Lookup(gvk)
		if selectErr != nil || actualErr != nil {
			if errors.Is(selectErr, discovery.ErrInvalidDefinition) && errors.Is(actualErr, discovery.ErrInvalidDefinition) {
				if !enumerates || !sameSelection(selections[kind], actualRaw[kind]) {
					return discovery.ErrStale
				}
				continue
			}
			if actualErr != nil && !errors.Is(actualErr, discovery.ErrInvalidDefinition) {
				c.failConfiguration(false, actualErr)
				return actualErr
			}
			return discovery.ErrStale
		}
		if !sameSelection(want, actual) {
			return discovery.ErrStale
		}
		selections[kind] = want
	}
	started := c.options.Clock()
	requestCtx, cancel := context.WithTimeout(ctx, c.options.MaxAge)
	defer cancel()
	namespaceRead, err := c.listReferences(requestCtx, &resourceStream{key: discovery.CoverageKey{
		Resource: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"},
	}, gvk: namespaceGVK})
	if err != nil {
		c.failConfiguration(true, err)
		return err
	}
	if len(namespaceRead) != len(namespaceRefs) {
		return discovery.ErrStale
	}
	for _, ref := range namespaceRead {
		if namespaceRefs[ref.Name] != ref {
			return discovery.ErrStale
		}
	}
	if c.options.Definitions.Cluster.Enabled {
		read, err := c.listReferences(requestCtx, &resourceStream{key: discovery.CoverageKey{Resource: kartaGVR}, gvk: v1alpha1.GroupVersion.WithKind("Karta")})
		if err != nil {
			c.failConfiguration(false, err)
			return err
		}
		accepted := map[types.UID]discovery.ObjectRef{}
		for _, definition := range definitions {
			accepted[definition.UID] = objectReference(definition, v1alpha1.GroupVersion.WithKind("Karta"))
		}
		if len(read) != len(accepted) {
			return discovery.ErrStale
		}
		for _, ref := range read {
			if accepted[ref.UID] != ref {
				return discovery.ErrStale
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if scope != c.scopeRevision || definitionRevision != c.definitionRevision || !c.options.Clock().Before(started.Add(c.options.MaxAge)) {
		return discovery.ErrStale
	}
	c.namespaceError, c.definitionsError = nil, nil
	c.namespaceEvidence = store.Evidence{ExpiresAt: started.Add(c.options.MaxAge), Dependencies: []string{c.namespaceFailureKey}}
	c.definitionEvidence = store.Evidence{ExpiresAt: started.Add(c.options.MaxAge), Dependencies: []string{c.definitionFailureKey}}
	for kind, selection := range selections {
		c.acceptSelectionLocked(kind, selection)
	}
	for name, namespace := range c.namespaceObjects {
		if c.namespaceSelector.Matches(labels.Set(namespace.GetLabels())) && !c.namespaces[name] {
			c.namespaces[name] = true
		}
		if c.namespaces[name] {
			c.enqueueNamespaceCoverageLocked(name)
		}
	}
	return nil
}
