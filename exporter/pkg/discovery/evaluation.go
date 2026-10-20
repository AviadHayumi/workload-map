// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/pkg/jq"
	"github.com/dsx-ai-factory/workload-map/pkg/jq/execution"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

type evaluationCache struct {
	mu        sync.Mutex
	discovery *Discovery
	roots     map[evaluationKey]*evaluationEntry
}

type evaluationKey struct {
	root       ObjectRef
	gvk        schema.GroupVersionKind
	origin     kartadefinitioninventory.Origin
	definition uint64
}

type evaluationEntry struct {
	done     chan struct{}
	prepared *preparedWorkload
	err      error
}

type preparedWorkload struct {
	gate       chan struct{}
	factory    *resource.ComponentFactory
	summary    *instructions.StructureSummary
	state      CapturedState
	status     StatusResult
	statusErr  error
	statusDone bool
	mappings   map[ObjectRef]PodMapping
}

type instanceResult struct {
	ids []string
	err error
}

type instanceAccessor struct {
	resource.ComponentAccessor
	instances map[string]instanceResult
}

func (a *instanceAccessor) ExtractInstanceIds(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]string, error) {
	if result, ok := a.instances[definition.Name]; ok {
		return slices.Clone(result.ids), result.err
	}
	ids, err := a.ComponentAccessor.ExtractInstanceIds(ctx, definition)
	if ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		a.instances[definition.Name] = instanceResult{ids: slices.Clone(ids), err: err}
	}
	return ids, err
}

func (d *Discovery) NewEvaluationBatch() EvaluationBatch {
	return EvaluationBatch{cache: &evaluationCache{discovery: d, roots: make(map[evaluationKey]*evaluationEntry)}}
}

func (w *KartaWorkload) prepare(ctx context.Context, batch EvaluationBatch) (*preparedWorkload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if batch.cache == nil || batch.cache.discovery != w.discovery || !w.discovery.IsCurrent(w.state) {
		return nil, ErrStale
	}
	key := evaluationKey{root: w.root, gvk: w.selection.GVK, origin: w.selection.Origin, definition: w.state.definitionRevision}
	cache := batch.cache
	cache.mu.Lock()
	entry, exists := cache.roots[key]
	if !exists {
		entry = &evaluationEntry{done: make(chan struct{})}
		cache.roots[key] = entry
	}
	cache.mu.Unlock()
	if exists {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-entry.done:
			if entry.err == nil && !w.discovery.IsCurrent(entry.prepared.state) {
				return nil, ErrStale
			}
			return entry.prepared, entry.err
		}
	}
	entry.prepared, entry.err = w.prepareRoot(ctx)
	cache.mu.Lock()
	if entry.err != nil {
		// Failed or canceled preparation must not poison another caller's retry.
		delete(cache.roots, key)
	}
	close(entry.done)
	cache.mu.Unlock()
	return entry.prepared, entry.err
}

func (w *KartaWorkload) prepareRoot(ctx context.Context) (*preparedWorkload, error) {
	d := w.discovery
	ref := w.root
	ref.GVK = w.selection.GVK
	ref.ResourceVersion = ""
	d.mu.RLock()
	node := d.ownership.nodes[ref.UID]
	if node == nil || !d.isCurrentLocked(w.state) {
		d.mu.RUnlock()
		return nil, ErrStale
	}
	object, ok := node.objects[ref.GVK]
	if !ok {
		d.mu.RUnlock()
		return nil, d.missingObject(ctx, ref, true)
	}
	if err := d.coverageErrorLocked(object, true); err != nil {
		d.mu.RUnlock()
		return nil, err
	}
	if _, _, err := d.parentLocked(node, object.Ref); err != nil {
		d.mu.RUnlock()
		return nil, err
	}
	state := cloneState(w.state)
	d.captureLocked(&state, node, object)
	d.mu.RUnlock()
	body, err := d.readObject(ctx, object)
	if err != nil {
		return nil, err
	}
	definition := w.selection.Definition
	if err := validateStructure(definition, w.selection.GVK); err != nil {
		return nil, err
	}
	summary, err := instructions.NewStructureSummary(definition)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDefinition, err)
	}
	accessor := &instanceAccessor{
		ComponentAccessor: resource.NewAccessor(execution.NewDefaultRunner(body)),
		instances:         make(map[string]instanceResult),
	}
	if !d.IsCurrent(state) {
		return nil, ErrStale
	}
	return &preparedWorkload{
		gate:    make(chan struct{}, 1),
		factory: resource.NewComponentFactory(definition, accessor),
		summary: summary, state: state, mappings: make(map[ObjectRef]PodMapping),
	}, nil
}

func (w *KartaWorkload) Status(ctx context.Context, batch EvaluationBatch) (StatusResult, error) {
	prepared, err := w.prepare(ctx, batch)
	if err != nil {
		return StatusResult{}, err
	}
	select {
	case <-ctx.Done():
		return StatusResult{}, ctx.Err()
	case prepared.gate <- struct{}{}:
	}
	defer func() { <-prepared.gate }()
	if !w.discovery.IsCurrent(prepared.state) {
		return StatusResult{}, ErrStale
	}
	if !prepared.statusDone {
		component, componentErr := prepared.factory.GetRootComponent()
		if componentErr != nil {
			return StatusResult{}, evaluationError(ctx, componentErr)
		}
		status, statusErr := component.GetStatus(ctx)
		result := StatusResult{state: prepared.state}
		if status != nil {
			result.Defined = true
			result.Matched = slices.Clone(status.MatchedStatuses)
		}
		if ctx.Err() != nil {
			return StatusResult{}, ctx.Err()
		}
		prepared.status, prepared.statusErr = result, evaluationError(ctx, statusErr)
		prepared.statusDone = true
	}
	if !w.discovery.IsCurrent(prepared.state) {
		return StatusResult{}, ErrStale
	}
	result := prepared.status
	result.Matched = slices.Clone(result.Matched)
	return result, prepared.statusErr
}

func (w *KartaWorkload) PodMapping(ctx context.Context, pod *corev1.Pod, batch EvaluationBatch) (PodMapping, error) {
	if err := ctx.Err(); err != nil {
		return PodMapping{}, err
	}
	if pod == nil {
		return PodMapping{}, fmt.Errorf("%w: nil Pod", ErrEvaluation)
	}
	prepared, err := w.prepare(ctx, batch)
	if err != nil {
		return PodMapping{}, err
	}
	ref := ObjectRef{GVK: corev1.SchemeGroupVersion.WithKind("Pod"), Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID, ResourceVersion: pod.ResourceVersion}
	d := w.discovery
	d.mu.RLock()
	workload, missing, err := d.resolveLocked(ref)
	if err == nil {
		err = d.coverageErrorLocked(d.ownership.nodes[pod.UID].objects[ref.GVK], true)
	}
	d.mu.RUnlock()
	if missing != nil {
		err = d.missingObject(ctx, *missing, false)
	}
	if err != nil {
		return PodMapping{}, err
	}
	if workload.root.UID != w.root.UID {
		return PodMapping{}, fmt.Errorf("%w: Pod belongs to another workload", ErrStale)
	}
	select {
	case <-ctx.Done():
		return PodMapping{}, ctx.Err()
	case prepared.gate <- struct{}{}:
	}
	defer func() { <-prepared.gate }()
	if mapping, exists := prepared.mappings[ref]; exists && d.IsCurrent(mapping.state, prepared.state) {
		return mapping, nil
	}
	query := resource.NewPodQuerier(pod.DeepCopy())
	componentName, err := instructions.InferPodComponent(ctx, query, prepared.summary)
	if err != nil {
		return PodMapping{}, evaluationError(ctx, err)
	}
	component, err := prepared.factory.GetComponent(componentName)
	if err != nil {
		return PodMapping{}, evaluationError(ctx, err)
	}
	instance, err := instructions.InferPodComponentInstance(ctx, query, componentName, prepared.factory)
	if err != nil {
		return PodMapping{}, evaluationError(ctx, err)
	}
	if component.Definition().InstanceIdPath != nil && (instance == nil || *instance == "") {
		return PodMapping{}, fmt.Errorf("%w: required instance for component %s is missing or undeclared", ErrEvaluation, componentName)
	}
	mapping := PodMapping{Component: componentName, state: cloneState(workload.state)}
	if instance != nil {
		mapping.Instance = *instance
	}
	for {
		selector := component.GetPodSelector()
		if selector != nil && selector.ReplicaSelector != nil && selector.ReplicaSelector.KeyPath != "" {
			group, _, err := query.ExtractReplicaKey(ctx, selector.ReplicaSelector)
			if err != nil {
				return PodMapping{}, evaluationError(ctx, err)
			}
			mapping.Group = group
			break
		}
		parent := component.Definition().OwnerRef
		if parent == nil {
			break
		}
		component, err = prepared.factory.GetComponent(*parent)
		if err != nil {
			return PodMapping{}, evaluationError(ctx, err)
		}
	}
	mapping.state.objects = append(mapping.state.objects, prepared.state.objects...)
	if err := ctx.Err(); err != nil {
		return PodMapping{}, err
	}
	if !d.IsCurrent(mapping.state, prepared.state) {
		return PodMapping{}, ErrStale
	}
	prepared.mappings[ref] = mapping
	return mapping, nil
}

func evaluationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrEvaluation, err)
}

func validateStructure(definition *v1alpha1.Karta, gvk schema.GroupVersionKind) error {
	root := definition.Spec.StructureDefinition.RootComponent
	if root.Kind == nil || root.Kind.Group != gvk.Group || root.Kind.Version != gvk.Version || root.Kind.Kind != gvk.Kind || root.OwnerRef != nil {
		return fmt.Errorf("%w: root kind or parent", ErrInvalidDefinition)
	}
	components := make(map[string]v1alpha1.ComponentDefinition)
	for _, component := range append([]v1alpha1.ComponentDefinition{root}, definition.Spec.StructureDefinition.ChildComponents...) {
		if _, duplicate := components[component.Name]; component.Name == "" || duplicate {
			return fmt.Errorf("%w: empty or duplicate component name", ErrInvalidDefinition)
		}
		components[component.Name] = component
	}
	for _, component := range components {
		seen := make(map[string]struct{})
		for component.Name != root.Name {
			if _, cyclic := seen[component.Name]; cyclic {
				return fmt.Errorf("%w: cyclic component hierarchy", ErrInvalidDefinition)
			}
			seen[component.Name] = struct{}{}
			if component.OwnerRef == nil {
				return fmt.Errorf("%w: disconnected component", ErrInvalidDefinition)
			}
			var ok bool
			component, ok = components[*component.OwnerRef]
			if !ok {
				return fmt.Errorf("%w: missing parent component", ErrInvalidDefinition)
			}
		}
	}
	if errs := jq.ValidateJQExpressions(definition); len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalidDefinition, errors.Join(errs...))
	}
	return nil
}
