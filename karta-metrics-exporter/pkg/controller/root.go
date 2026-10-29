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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
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
	if !r.state.namespaceAdmitted(key.Object.Namespace) {
		r.state.removeCandidate(key)
		return nil
	}

	r.state.trackCandidate(key)
	selection, selectionErr := r.state.lookupDefinition(key.Object.GVK)
	if selection.Definition == nil {
		if selectionErr != nil {
			r.state.deps.Publications.SetDiscoveryFailure(storedRef(key.ref()), failureReason(selectionErr))
			return selectionErr
		}
		r.state.removeCandidate(key)
		return nil
	}
	if selection.GVK != key.Object.GVK {
		alternate := key.ref()
		alternate.GVK = selection.GVK
		if _, err := r.state.ensureWatch(ctx, alternate.GVK, alternate.Namespace, true); err != nil {
			return err
		}
		object, _, err := r.state.reader.read(alternate, true)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return r.state.replaceCandidate(key, object)
	}
	object, _, err := r.state.reader.read(key.ref(), true)
	if apierrors.IsNotFound(err) {
		r.state.removeCandidate(key)
		return nil
	}
	if err != nil {
		r.state.failCandidate(key, err)
		return err
	}
	result, err := r.state.deps.Discovery.Get(ctx, refOf(object))
	if err == nil && result.Workload == nil {
		err = discovery.ErrUnresolvedOwner
	}
	if err != nil {
		r.state.failCandidate(key, err)
		return err
	}
	workload := result.Workload
	rootSelection, _ := r.state.lookupDefinition(workload.Root.GVK)
	selectedGVK := workload.Root.GVK
	if rootSelection.Definition != nil {
		selectedGVK = rootSelection.GVK
	}
	allowed, scopeErr := r.state.selectedResource(selectedGVK)
	if scopeErr != nil {
		r.state.failCandidate(key, scopeErr)
		return scopeErr
	}
	if !allowed {
		r.state.removeCandidate(key)
		return nil
	}
	sources := r.state.sourcesFor(workload.Path)
	return r.state.admitCandidate(key, workload, selection, rootSelection, sources)
}

// Only canonical UID keys publish workload results. Candidate keys only admit
// or withdraw membership, including selected children that have no Pods.
func (r *Runtime) reconcile(ctx context.Context, uid types.UID) error {
	snapshot, exists := r.state.snapshotRoot(uid)
	if !exists {
		return nil
	}
	ref := snapshot.ref
	selection, selectionErr := r.state.lookupDefinition(ref.GVK)
	if selection.Definition != nil {
		ref.GVK = selection.GVK
	}
	ref.ResourceVersion = ""
	if _, err := r.state.ensureWatch(ctx, ref.GVK, ref.Namespace, true); err != nil {
		return r.state.failWorkload(uid, ref, err)
	}
	object, _, err := r.state.reader.read(ref, true)
	if apierrors.IsNotFound(err) {
		r.state.withdrawWorkload(uid)
		return nil
	}
	if err != nil {
		return r.state.failWorkload(uid, ref, err)
	}
	ref = refOf(object)
	owner, valid := discovery.ControllerOwner(object)
	if !valid {
		return r.state.failWorkload(uid, ref, discovery.ErrUnresolvedOwner)
	}
	if owner.UID != "" && !slices.Contains(r.state.config.IgnoredOwnerKinds, owner.GVK) {
		r.state.withdrawWorkload(uid)
		return nil
	}
	terminal := r.state.isRoot(ref)
	if !terminal {
		return r.state.failWorkload(uid, ref, discovery.ErrUnresolvedOwner)
	}
	selected, err := r.state.selectedRoot(object)
	if err != nil {
		return r.state.failWorkload(uid, ref, err)
	}
	if !selected || (ref.GVK == podGVK && selection.Definition == nil && selectionErr == nil) {
		r.state.deps.Publications.RemoveWorkload(uid)
		return nil
	}
	record := store.WorkloadRecord{UID: uid, Ref: storedRef(ref), OwnerResolution: store.OwnerResolution(snapshot.resolution), CreatedTimestamp: object.GetCreationTimestamp().Unix()}
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
	labelResults, labelErr := r.state.deps.Labels.Resolve(ctx, labelresolver.Input{Metrics: []string{labelresolver.MetricWorkloadStatus}, Workload: object})
	if labelErr != nil {
		statusErr = fmt.Errorf("%w: %w", discovery.ErrEvaluation, labelErr)
	} else if len(labelResults[0].Errors) > 0 {
		statusErr = discovery.ErrEvaluation
	} else {
		record.StatusLabels = labelResults[0].Labels
	}
	sources := appendSources(r.state.sourcesFor([]discovery.ObjectRef{ref}), snapshot.sources)
	creationSources := slices.DeleteFunc(slices.Clone(sources), func(key store.SourceKey) bool {
		return key.Resource.Group == kartaGVK.Group && key.Resource.Resource == "kartas"
	})
	next := store.WorkloadResult{Record: record, HasValue: true, Error: failureReason(snapshot.err), StatusError: failureReason(statusErr), Sources: sources, CreationSources: creationSources}
	return r.state.publishWorkload(uid, snapshot.generation, ref, selection, next)
}

func mapPod(ctx context.Context, factory *resource.ComponentFactory, summary *instructions.StructureSummary, pod *corev1.Pod) (string, string, string, error) {
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

func (r *controllerState) selectedRoot(object *unstructured.Unstructured) (bool, error) {
	if !r.objectSelector.Empty() && !r.objectSelector.Matches(labelsFor(object)) {
		return false, nil
	}
	return r.selectedResource(object.GroupVersionKind())
}

func (r *controllerState) selectedResource(gvk schema.GroupVersionKind) (bool, error) {
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
