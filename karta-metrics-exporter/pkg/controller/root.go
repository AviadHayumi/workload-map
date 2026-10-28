// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

func (r *Runtime) resolveWorkload(ctx context.Context, key rootKey) error {
	if key.Candidate.UID != "" {
		return r.admitRoot(ctx, key.Candidate)
	}
	return r.reconcile(ctx, key.UID)
}

func (r *Runtime) admitRoot(ctx context.Context, key inputKey) error {
	if !r.namespaceAdmitted(key.Object.Namespace) {
		r.mu.Lock()
		r.removeCandidateLocked(key)
		r.mu.Unlock()
		return nil
	}

	r.mu.Lock()
	if _, known := r.candidates[key]; !known {
		r.candidates[key] = ""
	}
	r.mu.Unlock()
	selection, selectionErr := r.lookupDefinition(key.Object.GVK)
	if selection.Definition == nil {
		if selectionErr != nil {
			r.deps.Publications.SetDiscoveryFailure(storedRef(key.ref()), failureReason(selectionErr))
			return selectionErr
		}
		r.mu.Lock()
		r.removeCandidateLocked(key)
		r.mu.Unlock()
		return nil
	}
	if selection.GVK != key.Object.GVK {
		alternate := key.ref()
		alternate.GVK = selection.GVK
		if _, err := r.ensureWatch(ctx, alternate.GVK, alternate.Namespace, true); err != nil {
			return err
		}
		object, _, err := r.reader.read(alternate, true)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if err == nil {
			replacement := inputFor(refOf(object))
			r.rootQueue.Add(rootKey{Candidate: replacement})
			if r.candidates[replacement] == "" {
				return discovery.ErrPending
			}
		}
		r.removeCandidateLocked(key)
		return nil
	}
	object, _, err := r.reader.read(key.ref(), true)
	if apierrors.IsNotFound(err) {
		r.mu.Lock()
		r.removeCandidateLocked(key)
		r.mu.Unlock()
		return nil
	}
	if err == nil {
		var result discovery.Result
		result, err = r.deps.Discovery.Get(ctx, refOf(object))
		if err == nil && result.Workload == nil {
			err = discovery.ErrUnresolvedOwner
		}
		if err == nil {
			workload := result.Workload
			rootSelection, _ := r.lookupDefinition(workload.Root.GVK)
			selectedGVK := workload.Root.GVK
			if rootSelection.Definition != nil {
				selectedGVK = rootSelection.GVK
			}
			allowed, scopeErr := r.selectedResource(selectedGVK)
			if scopeErr != nil {
				r.failCandidate(key, scopeErr)
				return scopeErr
			}
			if !allowed {
				r.mu.Lock()
				r.removeCandidateLocked(key)
				r.mu.Unlock()
				return nil
			}
			sources := r.sourcesFor(workload.Path)
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.currentPathLocked(workload.Path) {
				return discovery.ErrStale
			}
			previous := r.candidates[key]
			root := r.rootLocked(workload)
			if previous != root.ref.UID {
				if old := r.roots[previous]; old != nil {
					delete(old.candidates, key)
					r.rootQueue.Add(rootKey{UID: previous})
				}
			}
			r.candidates[key] = root.ref.UID
			root.candidates[key] = candidateState{sources: sources}
			r.rootQueue.Add(rootKey{UID: root.ref.UID})
			r.deps.Publications.RemoveDiscoveryFailure(key.UID)
			return nil
		}
	}
	r.failCandidate(key, err)
	return err
}

func (r *Runtime) failCandidate(key inputKey, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if root := r.roots[r.candidates[key]]; root != nil {
		candidate := root.candidates[key]
		candidate.err = err
		root.candidates[key] = candidate
		r.rootQueue.Add(rootKey{UID: root.ref.UID})
	}
	r.deps.Publications.SetDiscoveryFailure(storedRef(key.ref()), failureReason(err))
}

// Only canonical UID keys publish workload results. Candidate keys only admit
// or withdraw membership, including selected children that have no Pods.
func (r *Runtime) reconcile(ctx context.Context, uid types.UID) error {
	r.mu.Lock()
	state := r.roots[uid]
	if state == nil {
		r.mu.Unlock()
		return nil
	}
	if len(state.pods) == 0 && len(state.candidates) == 0 {
		delete(r.roots, uid)
		r.deps.Publications.RemoveWorkload(uid)
		r.mu.Unlock()
		return nil
	}
	ref, resolution := state.ref, state.resolution
	var candidateSources []store.SourceKey
	var candidateError error
	for _, candidate := range state.candidates {
		candidateSources = appendSources(candidateSources, candidate.sources)
		candidateError = errors.Join(candidateError, candidate.err)
	}
	r.mu.Unlock()
	selection, selectionErr := r.lookupDefinition(ref.GVK)
	if selection.Definition != nil {
		ref.GVK = selection.GVK
	}
	ref.ResourceVersion = ""
	if _, err := r.ensureWatch(ctx, ref.GVK, ref.Namespace, true); err != nil {
		return r.failWorkload(uid, ref, err)
	}
	object, _, err := r.reader.read(ref, true)
	if apierrors.IsNotFound(err) {
		r.mu.Lock()
		r.enqueueMembersLocked(state)
		r.deps.Publications.RemoveWorkload(uid)
		r.mu.Unlock()
		return nil
	}
	if err != nil {
		return r.failWorkload(uid, ref, err)
	}
	ref = refOf(object)
	owner, valid := discovery.ControllerOwner(object)
	if !valid {
		return r.failWorkload(uid, ref, discovery.ErrUnresolvedOwner)
	}
	if owner.UID != "" && !slices.Contains(r.config.IgnoredOwnerKinds, owner.GVK) {
		r.mu.Lock()
		r.enqueueMembersLocked(state)
		r.deps.Publications.RemoveWorkload(uid)
		r.mu.Unlock()
		return nil
	}
	r.mu.Lock()
	terminal := r.isRootLocked(ref)
	r.mu.Unlock()
	if !terminal {
		return r.failWorkload(uid, ref, discovery.ErrUnresolvedOwner)
	}
	selected, err := r.selectedRoot(object)
	if err != nil {
		return r.failWorkload(uid, ref, err)
	}
	if !selected || (ref.GVK == podGVK && selection.Definition == nil && selectionErr == nil) {
		r.deps.Publications.RemoveWorkload(uid)
		return nil
	}
	record := store.WorkloadRecord{UID: uid, Ref: storedRef(ref), OwnerResolution: store.OwnerResolution(resolution), CreatedTimestamp: object.GetCreationTimestamp().Unix()}
	statusErr := selectionErr
	if selection.Definition != nil && statusErr == nil {
		record.Karta = selection.Origin.Name
		record.KartaSource = string(selection.Origin.Source)
		factory := resource.NewComponentFactoryFromObject(selection.Definition, object)
		component, componentErr := factory.GetRootComponent()
		statusErr = componentErr
		if statusErr == nil {
			status, evaluationErr := component.GetStatus(ctx)
			statusErr = evaluationErr
			if status != nil {
				record.HasStatus = true
				for _, phase := range status.MatchedStatuses {
					record.Phases = append(record.Phases, string(phase))
				}
			}
		}
		if statusErr != nil && !errors.Is(statusErr, discovery.ErrInvalidDefinition) {
			statusErr = fmt.Errorf("%w: %w", discovery.ErrEvaluation, statusErr)
		}
	}
	labels, labelErr := r.deps.Labels.ForWorkload(object)(ctx, "karta_workload_status", nil)
	if labelErr != nil {
		statusErr = labelErr
	} else if len(labels.Errors) > 0 {
		statusErr = discovery.ErrEvaluation
	} else {
		record.StatusLabels = labels.Labels
	}
	sources := appendSources(r.sourcesFor([]discovery.ObjectRef{ref}), candidateSources)
	creationSources := slices.DeleteFunc(slices.Clone(sources), func(key store.SourceKey) bool {
		return key.Resource.Group == kartaGVK.Group && key.Resource.Resource == "kartas"
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roots[uid] != state || !r.currentPathLocked([]discovery.ObjectRef{ref}) || !r.isRootLocked(ref) {
		return discovery.ErrStale
	}
	if selectionErr == nil && !r.currentSelectionLocked(ref, selection) {
		return discovery.ErrStale
	}
	next := store.WorkloadResult{Record: record, HasValue: true, Error: failureReason(candidateError), StatusError: failureReason(statusErr), Sources: sources, CreationSources: creationSources}
	if statusErr != nil {
		if previous := r.deps.Publications.Workload(uid); previous != nil && previous.HasValue {
			next.Record = previous.Record
			next.Sources = appendSources(previous.Sources, sources)
		}
	}
	state.ref = ref
	r.deps.Publications.UpsertWorkload(&next)
	return nil
}
func (r *Runtime) failWorkload(uid types.UID, ref discovery.ObjectRef, cause error) error {
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	sources := r.sourcesFor([]discovery.ObjectRef{ref})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roots[uid] == nil {
		return nil
	}
	next := store.WorkloadResult{Record: store.WorkloadRecord{UID: uid, Ref: storedRef(ref)}, Sources: sources, CreationSources: sources}
	if previous := r.deps.Publications.Workload(uid); previous != nil {
		next = *previous
		next.Sources = appendSources(previous.Sources, sources)
		next.CreationSources = appendSources(previous.CreationSources, sources)
	}
	next.Error = failureReason(cause)
	r.deps.Publications.UpsertWorkload(&next)
	return cause
}

func mapPod(ctx context.Context, factory *resource.ComponentFactory, summary *instructions.StructureSummary, object *unstructured.Unstructured) (string, string, string, error) {
	pod := &corev1.Pod{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(object.Object, pod); err != nil {
		return "", "", "", err
	}
	query := resource.NewPodQuerier(pod)
	name, err := instructions.InferPodComponent(ctx, query, summary)
	if err != nil {
		return "", "", "", err
	}
	component, err := factory.GetComponent(name)
	if err != nil {
		return "", "", "", err
	}
	id, err := instructions.InferPodComponentInstance(ctx, query, name, factory)
	if err != nil {
		return "", "", "", err
	}
	instance := ""
	if id != nil {
		instance = *id
	}
	if component.Definition().InstanceIdPath != nil && instance == "" {
		return "", "", "", errors.New("required component instance is missing")
	}
	group := ""
	for {
		selector := component.GetPodSelector()
		if selector != nil && selector.ReplicaSelector != nil && selector.ReplicaSelector.KeyPath != "" {
			group, _, err = query.ExtractReplicaKey(ctx, selector.ReplicaSelector)
			if err != nil {
				return "", "", "", err
			}
			break
		}
		owner := component.Definition().OwnerRef
		if owner == nil {
			break
		}
		component, err = factory.GetComponent(*owner)
		if err != nil {
			return "", "", "", err
		}
	}
	return name, instance, group, nil
}

func ValidateResourceRules(rules []ResourceRule) error {
	for index, rule := range rules {
		if len(rule.APIGroups) == 0 || len(rule.APIVersions) == 0 || len(rule.Resources) == 0 {
			return fmt.Errorf("resource rule %d requires apiGroups, apiVersions and resources", index)
		}
		for _, version := range rule.APIVersions {
			if version == "" || strings.ContainsAny(version, "/ \t\n") {
				return errors.New("resource rule API versions must name a version or *")
			}
		}
		for _, resource := range rule.Resources {
			if resource == "" || strings.ContainsAny(resource, "/ \t\n") {
				return errors.New("resource rules must name whole resources or *")
			}
		}
	}
	return nil
}

func matchesResource(rule ResourceRule, gvr schema.GroupVersionResource) bool {
	return (slices.Contains(rule.APIGroups, "*") || slices.Contains(rule.APIGroups, gvr.Group)) &&
		(slices.Contains(rule.APIVersions, "*") || slices.Contains(rule.APIVersions, gvr.Version)) &&
		(slices.Contains(rule.Resources, "*") || slices.Contains(rule.Resources, gvr.Resource))
}

func (r *Runtime) selectedRoot(object *unstructured.Unstructured) (bool, error) {
	if !r.objectSelector.Empty() && !r.objectSelector.Matches(labelsFor(object)) {
		return false, nil
	}
	return r.selectedResource(object.GroupVersionKind())
}

func (r *Runtime) selectedResource(gvk schema.GroupVersionKind) (bool, error) {
	if len(r.config.ResourceRules) == 0 && len(r.config.ExcludeResourceRules) == 0 {
		return true, nil
	}
	mapping, err := r.reader.clients.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, err
	}
	for _, rule := range r.config.ExcludeResourceRules {
		if matchesResource(rule, mapping.Resource) {
			return false, nil
		}
	}
	if len(r.config.ResourceRules) == 0 {
		return true, nil
	}
	for _, rule := range r.config.ResourceRules {
		if matchesResource(rule, mapping.Resource) {
			return true, nil
		}
	}
	return false, nil
}
