// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

func (r *Runtime) resolveInput(ctx context.Context, key inputKey) error {
	if !key.Sweep {
		return r.resolveLeaf(ctx, key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reader.mu.RLock()
	defer r.reader.mu.RUnlock()
	for _, entry := range r.reader.objects {
		if key.Object.Namespace == "" || entry.object.GetNamespace() == key.Object.Namespace {
			r.enqueueObjectLocked(entry.object)
		}
	}
	// Include previous memberships so a removed definition can withdraw its roots.
	for candidate := range r.candidates {
		if key.Object.Namespace == "" || candidate.Object.Namespace == key.Object.Namespace {
			r.rootQueue.Add(rootKey{Candidate: candidate})
		}
	}
	return nil
}

func (r *Runtime) resolveLeaf(ctx context.Context, key inputKey) error {
	ref := key.ref()
	if !r.namespaceAdmitted(ref.Namespace) {
		r.mu.Lock()
		r.removePodLocked(key)
		r.mu.Unlock()
		return nil
	}
	object, _, err := r.reader.read(ref, true)
	if apierrors.IsNotFound(err) {
		r.mu.Lock()
		r.removePodLocked(key)
		r.mu.Unlock()
		return nil
	}
	if err != nil {
		return r.failPod(key, nil, err)
	}
	if !r.podSelector.Empty() && !r.podSelector.Matches(labelsFor(object)) {
		r.mu.Lock()
		r.removePodLocked(key)
		r.mu.Unlock()
		return nil
	}
	ref = refOf(object)
	result, err := r.deps.Discovery.Get(ctx, ref)
	if err != nil {
		return r.failPod(key, []discovery.ObjectRef{ref}, err)
	}
	if result.Workload == nil {
		return r.failPod(key, []discovery.ObjectRef{ref}, discovery.ErrUnresolvedOwner)
	}
	workload := result.Workload
	selection, selectionErr := r.lookupDefinition(workload.Root.GVK)
	if workload.Root.GVK == podGVK && selectionErr != nil {
		return r.failPod(key, workload.Path, selectionErr)
	}
	if workload.Root.GVK == podGVK && selection.Definition == nil {
		r.mu.Lock()
		r.removePodLocked(key)
		r.mu.Unlock()
		return nil
	}

	rootRef := workload.Root
	if selection.Definition != nil {
		rootRef.GVK = selection.GVK
	}
	allowed, scopeErr := r.selectedResource(rootRef.GVK)
	if scopeErr != nil {
		return r.failPod(key, workload.Path, scopeErr, rootRef)
	}
	if !allowed {
		r.mu.Lock()
		r.removePodLocked(key)
		r.mu.Unlock()
		return nil
	}
	r.mu.Lock()
	if !r.currentPathLocked(workload.Path) {
		r.mu.Unlock()
		return discovery.ErrStale
	}
	if !r.isRootLocked(workload.Root) {
		r.mu.Unlock()
		return r.failPod(key, workload.Path, discovery.ErrUnresolvedOwner, workload.Root)
	}
	// Register before reading the root body. Root-event fanout takes this same lock.
	previous := r.podRoots[key.UID]
	root := r.rootLocked(workload)
	if previous != root.ref.UID {
		if old := r.roots[previous]; old != nil {
			delete(old.pods, key.UID)
			r.rootQueue.Add(rootKey{UID: previous})
		}
		r.podRoots[key.UID] = root.ref.UID
		root.pods[key.UID] = key
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	rootRef.ResourceVersion = ""
	rootObject, rootSource, err := r.reader.read(rootRef, true)
	_, watchFailed := r.watchFailures[discovery.WatchRequest{GVK: rootRef.GVK, Namespace: rootRef.Namespace}]
	r.mu.Unlock()
	if err != nil || watchFailed {
		if _, err = r.ensureWatch(ctx, rootRef.GVK, rootRef.Namespace, true); err != nil {
			return r.failPod(key, workload.Path, err, workload.Root)
		}
		rootObject, rootSource, err = r.reader.read(rootRef, true)
	}
	if err != nil {
		return r.failPod(key, workload.Path, err, workload.Root)
	}
	selected, err := r.selectedRoot(rootObject)
	if err != nil {
		return r.failPod(key, workload.Path, err, workload.Root)
	}
	if !selected {
		r.mu.Lock()
		r.removePodLocked(key)
		r.mu.Unlock()
		return nil
	}
	rootRef = refOf(rootObject)
	if selectionErr != nil {
		return r.failPodEvaluation(key, workload.Path, selectionErr, rootRef, selection)
	}
	record := store.PodRecord{
		UID:             key.UID,
		Name:            ref.Name,
		Namespace:       ref.Namespace,
		Component:       "<unknown>",
		Instance:        "<unknown>",
		Workload:        storedRef(rootRef),
		WorkloadGVK:     rootRef.GVK,
		OwnerResolution: store.OwnerResolution(workload.Resolution),
	}
	if selection.Definition != nil {
		pod := &corev1.Pod{}
		if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(object.Object, pod); err != nil {
			return r.failPodEvaluation(key, workload.Path, fmt.Errorf("%w: %w", discovery.ErrEvaluation, err), rootRef, selection)
		}
		record.Karta = selection.Origin.Name
		record.KartaSource = string(selection.Origin.Source)
		factory := resource.NewComponentFactoryFromObject(selection.Definition, rootObject)
		record.Component, record.Instance, record.Group, err = mapPod(ctx, factory, selection.summary, pod)
		if err != nil {
			return r.failPodEvaluation(key, workload.Path, fmt.Errorf("%w: %w", discovery.ErrEvaluation, err), rootRef, selection)
		}
	}
	labelResults, err := r.deps.Labels.Resolve(ctx, labelresolver.Input{Metrics: []string{labelresolver.MetricPodWorkloadInfo}, Workload: rootObject, PodObject: object})
	if err != nil {
		return r.failPodEvaluation(key, workload.Path, fmt.Errorf("%w: %w", discovery.ErrEvaluation, err), rootRef, selection)
	}
	labelResult := labelResults[0]
	record.Labels = labelResult.Labels
	sources := r.sourcesFor(workload.Path)
	if !slices.Contains(sources, rootSource) {
		sources = append(sources, rootSource)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.currentPathLocked(workload.Path) || !r.isRootLocked(rootRef) || !r.currentSelectionLocked(rootRef, selection) {
		return discovery.ErrStale
	}
	if current, ok := r.reader.Cached(rootRef); !ok || current.GetResourceVersion() != rootRef.ResourceVersion {
		return discovery.ErrStale
	}
	next := store.PodResult{Record: record, Input: storedRef(ref), HasValue: true, Sources: sources}
	if len(labelResult.Errors) > 0 {
		next.Error = store.ReasonEvaluationFailed
		if previous := r.deps.Publications.Pod(key.UID); previous != nil && previous.HasValue {
			next.Record = previous.Record
			next.Sources = appendSources(previous.Sources, sources)
		}
	}
	r.deps.Publications.UpsertPod(&next)
	return nil
}

func (r *Runtime) failPod(key inputKey, path []discovery.ObjectRef, cause error, root ...discovery.ObjectRef) error {
	sources := r.sourcesFor(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failPodLocked(key, sources, cause, root...)
}

func (r *Runtime) failPodEvaluation(key inputKey, path []discovery.ObjectRef, cause error, root discovery.ObjectRef, selection selectedDefinition) error {
	sources := r.sourcesFor(path)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.currentSelectionLocked(root, selection) || !r.currentPathLocked(path) {
		return discovery.ErrStale
	}
	if _, current := r.reader.Cached(root); !current || !r.isRootLocked(root) {
		return discovery.ErrStale
	}
	return r.failPodLocked(key, sources, cause, root)
}

func (r *Runtime) failPodLocked(key inputKey, sources []store.SourceKey, cause error, root ...discovery.ObjectRef) error {
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	next := store.PodResult{Record: store.PodRecord{UID: key.UID}, Input: storedRef(key.ref())}
	if len(root) > 0 {
		next.Record.Workload = storedRef(root[0])
		next.Record.WorkloadGVK = root[0].GVK
	}

	if previous := r.deps.Publications.Pod(key.UID); previous != nil {
		if previous.HasValue || len(root) == 0 {
			next = *previous
		}
		sources = appendSources(previous.Sources, sources)
	}
	next.Error = failureReason(cause)
	next.Sources = sources
	r.deps.Publications.UpsertPod(&next)
	if errors.Is(cause, discovery.ErrEvaluation) || errors.Is(cause, inventory.ErrInvalidDefinition) || errors.Is(cause, discovery.ErrInvalidDefinition) {
		return nil
	}
	return cause
}
func (r *Runtime) currentPathLocked(path []discovery.ObjectRef) bool {
	for _, ref := range path {
		if _, ok := r.reader.Cached(ref); !ok {
			return false
		}
	}
	return true
}
func (r *Runtime) currentSelectionLocked(ref discovery.ObjectRef, selection selectedDefinition) bool {
	if r.definitionVersion != selection.version {
		return false
	}
	current := r.selections[ref.GVK.GroupKind()]
	return current.GVK == selection.GVK && current.Origin == selection.Origin
}
func labelsFor(object *unstructured.Unstructured) labels.Set { return labels.Set(object.GetLabels()) }
