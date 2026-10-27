// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

func labelsFor(object *unstructured.Unstructured) labels.Set { return labels.Set(object.GetLabels()) }

func (r *Runtime) resolve(ctx context.Context, key queueKey) error {
	ref := discovery.ObjectRef{GVK: key.Object.GVK, Namespace: key.Object.Namespace, Name: key.Object.Name, UID: key.UID}
	object, ok := r.reader.Cached(ref)
	if !ok {
		return nil
	}
	ref = refOf(object)
	if ref.GVK == namespaceGVK || ref.GVK == kartaGVK {
		return nil
	}
	if ref.GVK == podGVK && !r.podSelector.Matches(labelsFor(object)) {
		r.mu.Lock()
		if uid := r.podRoots[ref.UID]; uid != "" {
			r.invalidatePodLocked(r.roots[uid], ref.UID, store.ReasonUnresolvedOwner)
			r.removePodPathLocked(r.roots[uid], ref.UID)
			delete(r.podRoots, ref.UID)
		}
		r.mu.Unlock()
		return nil
	}
	var rootWatch watchKey
	result, err := r.deps.Discovery.Get(ctx, ref)
	if result.Workload != nil && result.Workload.Root.UID != "" {
		root := result.Workload.Root
		gvk := root.GVK
		if selection, lookupErr := r.lookupDefinition(root.GVK); lookupErr == nil && selection.Definition != nil {
			gvk = selection.GVK
		} else {
			selected, selectionErr := r.selectedResource(gvk)
			if selectionErr != nil {
				return selectionErr
			}
			if !selected {
				r.mu.Lock()
				if previous := r.roots[r.podRoots[ref.UID]]; previous != nil {
					r.invalidatePodLocked(previous, ref.UID, store.ReasonUnresolvedOwner)
					r.removePodPathLocked(previous, ref.UID)
					delete(r.podRoots, ref.UID)
				}
				r.mu.Unlock()
				return nil
			}
			r.mu.Lock()
			if existing := r.roots[root.UID]; existing != nil {
				gvk = existing.ref.GVK
			}
			r.mu.Unlock()
		}
		namespace := root.Namespace
		if r.config.NamespaceSelector == nil {
			namespace = ""
		}
		var watchErr error
		rootWatch, watchErr = r.ensureWatch(ctx, gvk, namespace, false)
		if watchErr != nil {
			err = errors.Join(err, watchErr)
		}
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		r.mu.Lock()
		if root := r.roots[ref.UID]; root != nil {
			if current, ok := r.reader.Cached(ref); ok && current.GetResourceVersion() == ref.ResourceVersion {
				root.failResolution(ref.GVK, err)
				r.failPublicationLocked(ref.UID, err)
				r.rootQueue.Add(queueKey{Operation: "root", UID: ref.UID})
			}
		}
		r.mu.Unlock()
		if errors.Is(err, discovery.ErrStale) || errors.Is(err, discovery.ErrPending) {
			return err
		}
		r.mu.Lock()
		if root := r.roots[r.podRoots[ref.UID]]; root != nil {
			r.invalidatePodLocked(root, ref.UID, failureReason(err))
			pod := root.pods[ref.UID]
			pod.resolveError = err
			root.pods[ref.UID] = pod
			r.deps.Publications.SetPublicationFailure(pod.proofKey, failureReason(err))
		}
		r.failPublicationLocked(ref.UID, err)
		r.publishHealthLocked()
		r.mu.Unlock()
		return err
	}
	if result.Workload == nil {
		return errors.New("discovery returned no workload")
	}
	workload := result.Workload
	if workload.Root.GVK == podGVK {
		if selection, err := r.lookupDefinition(podGVK); err == nil && selection.Definition == nil {
			r.mu.Lock()
			r.removeRoot(workload.Root.UID)
			r.mu.Unlock()
			return nil
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, dependency := range workload.Path {
		if _, err := r.reader.evidence(dependency); err != nil {
			return discovery.ErrStale
		}
	}
	if current, ok := r.reader.Cached(ref); !ok || current.GetResourceVersion() != ref.ResourceVersion {
		return discovery.ErrStale
	}
	r.reader.mu.RLock()
	terminal := r.rootBoundaryLocked(workload.Root)
	r.reader.mu.RUnlock()
	if !terminal {
		return discovery.ErrStale
	}
	if old := r.roots[ref.UID]; old != nil && workload.Root.UID != ref.UID {
		for uid, pod := range old.pods {
			if len(pod.path) > 0 {
				r.queue.Add(queueKey{Operation: "object", Object: keyOf(pod.path[0]), UID: uid})
			}
		}
		r.removeRoot(ref.UID)
	}
	if r.watches[rootWatch] == nil {
		r.controlQueue.Add(queueKey{Operation: "sync"})
		return discovery.ErrPending
	}
	root := r.roots[workload.Root.UID]
	if root == nil {
		root = &rootState{ref: workload.Root, pods: map[types.UID]podState{}, dirty: map[types.UID]bool{}, fullRefresh: true}
		r.roots[workload.Root.UID] = root
	}
	if failure, unresolved := root.resolutionErrors[workload.Root.GVK]; unresolved {
		if failure.verifyAfter > 0 && r.watches[rootWatch].verifiedRead <= failure.verifyAfter {
			return discovery.ErrPending
		}
		delete(root.resolutionErrors, workload.Root.GVK)
	}
	if len(root.resolutionErrors) > 0 {
		return discovery.ErrStale
	}
	root.watch = rootWatch
	if root.ref.GVK == workload.Root.GVK {
		root.ref = workload.Root
	}
	root.resolution = workload.Resolution
	if ref.GVK == podGVK {
		if previous := r.podRoots[ref.UID]; previous != "" && previous != workload.Root.UID {
			if old := r.roots[previous]; old != nil {
				r.invalidatePodLocked(old, ref.UID, store.ReasonUnresolvedOwner)
				r.removePodPathLocked(old, ref.UID)
			}
		}
		if root.pods[ref.UID].resolveError != nil {
			r.invalidatePodLocked(root, ref.UID, store.ReasonAPIUnavailable)
		}
		pod := root.pods[ref.UID]
		pod.resolveError = nil
		if pod.proofKey == "" {
			r.sequence++
			pod.proofKey = fmt.Sprintf("pod:%s:%d", ref.UID, r.sequence)
		}
		root.pods[ref.UID] = pod
		r.setPodPathLocked(root, ref.UID, workload.Path)
		r.dirtyPod(root, ref.UID)
		r.podRoots[ref.UID] = workload.Root.UID
	}
	if ref.GVK != podGVK {
		root.fullRefresh = true
		r.rootQueue.Add(queueKey{Operation: "root", UID: workload.Root.UID})
	}
	return nil
}

func (r *Runtime) reconcile(ctx context.Context, uid types.UID) error {
	r.mu.Lock()
	state := r.roots[uid]
	if state == nil {
		r.mu.Unlock()
		return nil
	}
	if !state.renewalAt.IsZero() && !r.config.Now().Before(state.renewalAt) {
		state.fullRefresh = true
		state.renewalAt = time.Time{}
	} else if !state.renewalAt.IsZero() {
		// An urgent AddAfter may have consumed the queue's earlier renewal entry.
		r.rootQueue.AddAfter(queueKey{Operation: "root", UID: uid}, state.renewalAt.Sub(r.config.Now()))
	}
	ref, generation, resolution := state.ref, state.generation, state.resolution
	var rootFailure error
	for _, failure := range state.resolutionErrors {
		rootFailure = errors.Join(rootFailure, failure.cause)
	}
	if w := r.watches[state.watch]; w != nil {
		rootFailure = errors.Join(rootFailure, w.err)
		if rootFailure == nil && !r.config.Now().Before(w.verifiedAt.Add(r.config.MaxAge)) {
			rootFailure = discovery.ErrUnavailable
		}
	}
	failedSources := map[string]error{}
	for _, w := range r.watches {
		if w.err != nil {
			failedSources[w.failureKey] = w.err
		}
	}
	r.reader.mu.RLock()
	if !r.rootBoundaryLocked(ref) {
		rootFailure = errors.Join(rootFailure, discovery.ErrUnresolvedOwner)
	}
	r.reader.mu.RUnlock()
	previous := r.deps.Publications.Published(uid)
	resolutionChanged := previous != nil && ((rootFailure != nil && previous.Workload.OwnerResolution != store.UnknownOwner) || (rootFailure == nil && previous.Workload.OwnerResolution == store.UnknownOwner))
	full := resolutionChanged || state.fullRefresh || previous == nil || state.publishedGeneration != generation || len(state.dirty) > store.PodPageSize
	dirty := state.dirty
	state.dirty = map[types.UID]bool{}
	state.fullRefresh = false
	pods := map[types.UID]podState{}
	if full {
		pods = maps.Clone(state.pods)
	} else {
		for uid := range dirty {
			pods[uid] = state.pods[uid]
		}
	}
	publishedOrigin, publishedGVK := state.publishedOrigin, state.publishedGVK
	labelObject, resolveLabels := state.labelObject, state.resolveLabels
	labelFailures := state.labelFailures
	definitionKey, definitionError := r.definitionKey, r.definitionError
	selection, selectionErr := r.lookupDefinitionLocked(ref.GVK)
	kindDefinitionKey := r.definitionProofLocked(ref.GVK.GroupKind())
	definitionEvidence := store.Evidence{}
	for _, w := range r.watches {
		if w.gvk == kartaGVK {
			definitionEvidence = store.Evidence{ExpiresAt: w.verifiedAt.Add(r.config.MaxAge), Dependencies: []string{w.failureKey}}
		}
	}
	r.mu.Unlock()
	committed := false
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !committed && r.roots[uid] == state {
			for pod := range dirty {
				state.dirty[pod] = true
			}
			state.fullRefresh = state.fullRefresh || full
		}
		r.retireFailuresLocked(state)
	}()
	definitionError = errors.Join(definitionError, selectionErr)
	interpretationOrigin, interpretationGVK := selection.Origin, selection.GVK
	if definitionError != nil && interpretationOrigin == (inventory.Origin{}) {
		interpretationOrigin, interpretationGVK = publishedOrigin, publishedGVK
	}
	compatible := previous != nil && interpretationOrigin == publishedOrigin && interpretationGVK == publishedGVK
	if !compatible {
		for uid, pod := range pods {
			pod.cached = nil
			pods[uid] = pod
		}
	}
	if ref.GVK == podGVK && selection.Definition == nil && definitionError == nil {
		r.mu.Lock()
		r.deps.Publications.RemovePublication(uid)
		r.mu.Unlock()
		committed = true
		return nil
	}
	selectedRef := ref
	selectedRef.ResourceVersion = ""
	if selection.Definition != nil {
		selectedRef.GVK = selection.GVK
	} else if definitionError != nil && interpretationOrigin != (inventory.Origin{}) {
		selectedRef.GVK = interpretationGVK
	}
	object, ok := r.reader.Cached(selectedRef)
	if !ok {
		return discovery.ErrPending
	}
	selectedRef = refOf(object)
	selected, err := r.selectedRoot(object)
	if err != nil {
		return err
	}
	if !selected {
		r.mu.Lock()
		if current := r.roots[uid]; current == state && current.generation == generation {
			r.deps.Publications.RemovePublication(uid)
		}
		r.mu.Unlock()
		return nil
	}
	rootEvidence, err := r.reader.objectEvidence(selectedRef)
	if err != nil {
		if rootFailure == nil || previous == nil {
			return err
		}
		rootEvidence = previous.Creation
	}
	rootEvidence.Dependencies = append(slices.Clone(rootEvidence.Dependencies), fmt.Sprintf("root:%s:%d", uid, generation))
	if rootFailure != nil {
		rootEvidence.Reason = failureReason(rootFailure)
	}
	statusEvidence := rootEvidence
	statusEvidence.Dependencies = append(slices.Clone(rootEvidence.Dependencies), definitionKey, kindDefinitionKey)
	if r.config.Definitions.Cluster.Enabled {
		statusEvidence = mergeEvidence(statusEvidence, definitionEvidence)
	}
	if definitionError != nil {
		statusEvidence.Reason = failureReason(definitionError)
	}
	record := &store.Publication{Workload: store.WorkloadRecord{UID: uid, Ref: store.ObjectRef{GVK: selectedRef.GVK, Namespace: selectedRef.Namespace, Name: selectedRef.Name, UID: uid, ResourceVersion: selectedRef.ResourceVersion}, OwnerResolution: store.OwnerResolution(resolution), CreatedTimestamp: object.GetCreationTimestamp().Unix()}, Creation: rootEvidence, Status: statusEvidence}
	var factory *resource.ComponentFactory
	var summary *instructions.StructureSummary
	if rootFailure != nil {
		record.Workload.OwnerResolution = store.UnknownOwner
	}
	if selection.Definition != nil && definitionError == nil && rootFailure == nil {
		record.Workload.Karta = selection.Origin.Name
		record.Workload.KartaSource = string(selection.Origin.Source)
		factory, summary, err = prepareWorkload(selection, object)
		if err == nil {
			var component *resource.Component
			component, err = factory.GetRootComponent()
			if err == nil {
				status, statusErr := component.GetStatus(ctx)
				err = statusErr
				if status != nil {
					record.Workload.HasStatus = true
					for _, phase := range status.MatchedStatuses {
						record.Workload.Phases = append(record.Workload.Phases, string(phase))
					}
				}
			}
		}
		if err != nil {
			record.Status.Reason = store.ReasonEvaluationFailed
			record.FailureReasons = append(record.FailureReasons, store.ReasonEvaluationFailed)
		}
	}
	if resolveLabels == nil || labelObject != selectedRef {
		resolveLabels = r.deps.Labels.ForWorkload(object)
		labelObject = selectedRef
	}
	statusLabels, labelErr := resolveLabels(ctx, "karta_workload_status", nil)
	if labelErr != nil {
		return labelErr
	}
	record.Workload.StatusLabels = statusLabels.Labels
	if len(statusLabels.Errors) > 0 {
		record.FailureReasons = append(record.FailureReasons, store.ReasonEvaluationFailed)
	}
	if record.Status.Reason != "" && compatible && previous.Workload.HasStatus {
		record.Workload.HasStatus = true
		record.Workload.Phases = slices.Clone(previous.Workload.Phases)
		record.Workload.StatusLabels = previous.Workload.StatusLabels
		record.Workload.Karta = previous.Workload.Karta
		record.Workload.KartaSource = previous.Workload.KartaSource
	}
	rows := make([]store.PublishedPod, 0, len(pods))
	for podUID, pod := range pods {
		path := pod.path
		if len(path) == 0 {
			continue
		}
		podObject, found := r.reader.Cached(path[0])
		if !found {
			r.queue.Add(queueKey{Operation: "object", Object: keyOf(path[0]), UID: podUID})
			if compatible {
				if row, found := previous.Pod(podUID); found {
					row.Mapping.Reason = store.ReasonAPIUnavailable
					row.Record.OwnerResolution = store.UnknownOwner
					rows = append(rows, row)
				}
			}
			continue
		}
		evidence := statusEvidence
		evidence.Dependencies = append(slices.Clone(evidence.Dependencies), pod.proofKey)
		valid := true
		var pathFailure error
		for _, dependency := range path {
			// Root updates are evaluated with their current selected representation.
			if dependency.UID == uid && (interpretationOrigin != (inventory.Origin{}) || dependency.GVK == selectedRef.GVK) {
				continue
			}
			var proof store.Evidence
			var proofErr error
			if dependency.UID == podUID {
				proof, proofErr = r.reader.evidence(dependency)
			} else {
				proof, proofErr = r.reader.objectEvidence(dependency)
			}
			if proofErr == nil {
				for _, key := range proof.Dependencies {
					if cause := failedSources[key]; cause != nil {
						proofErr = cause
						break
					}
				}
			}
			if proofErr != nil {
				pathFailure = proofErr
				valid = false
				r.queue.Add(queueKey{Operation: "object", Object: keyOf(path[0]), UID: podUID})
				break
			}
			evidence = mergeEvidence(evidence, proof)
		}
		var cached cachedPod
		if pod.cached != nil {
			cached = *pod.cached
		}
		reuse := pod.cached != nil && cached.resourceVersion == podObject.GetResourceVersion() && (cached.generation == generation || definitionError != nil)
		if !reuse {
			cached = cachedPod{resourceVersion: podObject.GetResourceVersion(), generation: generation, record: store.PodRecord{UID: podUID, Name: podObject.GetName(), Namespace: podObject.GetNamespace(), Instance: "<unknown>", Component: "<unknown>", OwnerResolution: store.OwnerResolution(resolution)}}
			if factory != nil && summary != nil && valid {
				component, instance, group, mappingErr := mapPod(ctx, factory, summary, podObject)
				if mappingErr != nil {
					cached.reason = store.ReasonEvaluationFailed
				} else {
					cached.record.Component = component
					cached.record.Instance = instance
					cached.record.Group = group
				}
			}
			resolved, labelErr := resolveLabels(ctx, "karta_pod_workload_info", podObject)
			if labelErr != nil {
				return labelErr
			}
			cached.record.Labels = resolved.Labels
			cached.labelFailure = len(resolved.Errors) > 0
			if valid && definitionError == nil && rootFailure == nil {
				pod.cached = &cached
			}

		}
		podRecord := cached.record
		podRecord.WorkloadGVK = selectedRef.GVK
		if interpretationOrigin == (inventory.Origin{}) && len(path) > 0 {
			podRecord.WorkloadGVK = path[len(path)-1].GVK
		}
		if !valid || pod.resolveError != nil || rootFailure != nil {
			podRecord.OwnerResolution = store.UnknownOwner
		}
		if !valid {
			evidence.Reason = failureReason(pathFailure)
			if cause := pod.resolveError; cause != nil {
				evidence.Reason = failureReason(cause)
			}
		} else if cached.reason != "" {
			evidence.Reason = cached.reason
		}
		if pod.labelFailure != cached.labelFailure {
			if cached.labelFailure {
				labelFailures++
			} else {
				labelFailures--
			}
		}
		pod.labelFailure = cached.labelFailure
		pods[podUID] = pod
		rows = append(rows, store.PublishedPod{Record: podRecord, Mapping: evidence})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if full {
		record.PodPages = store.BuildPodPages(rows, previous)
	} else {
		record.PodPages = previous.PodPages
		replacements := make(map[types.UID]*store.PublishedPod, len(rows))
		for i := range rows {
			replacements[rows[i].Record.UID] = &rows[i]
		}
		for uid := range pods {
			record = record.WithPod(uid, replacements[uid])
		}
	}
	for uid, pod := range pods {
		if len(pod.path) == 0 {
			if pod.labelFailure {
				labelFailures--
			}
			pod.labelFailure = false
			pod.cached = nil
			pods[uid] = pod
		}
	}
	if labelFailures > 0 {
		record.FailureReasons = append(record.FailureReasons, store.ReasonEvaluationFailed)
	}
	proofKeys := map[string]bool{}
	proofs := []store.Evidence{rootEvidence, record.Status}
	for _, row := range rows {
		proofs = append(proofs, row.Mapping)
	}
	for _, proof := range proofs {
		if proof.Reason != "" && proof.Reason != store.ReasonNone {
			continue
		}
		for _, key := range proof.Dependencies {
			if strings.HasPrefix(key, "watch:") || strings.HasPrefix(key, "metadata:") || strings.HasPrefix(key, "ownership:") {
				proofKeys[key] = true
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.roots[uid]
	if current != state || current.generation != generation || definitionKey != r.definitionKey || kindDefinitionKey != r.definitionProofLocked(ref.GVK.GroupKind()) {
		return discovery.ErrStale
	}
	latest, latestErr := r.lookupDefinitionLocked(ref.GVK)
	if (latestErr == nil) != (selectionErr == nil) {
		return discovery.ErrStale
	}
	if latestErr == nil && (latest.Origin != selection.Origin || latest.GVK != selection.GVK) {
		return discovery.ErrStale
	}
	if object, ok := r.reader.Cached(selectedRef); !ok || object.GetResourceVersion() != selectedRef.ResourceVersion {
		return discovery.ErrStale
	}
	r.reader.mu.RLock()
	defer r.reader.mu.RUnlock()
	if rootFailure == nil && !r.rootBoundaryLocked(selectedRef) {
		return discovery.ErrStale
	}
	for key := range proofKeys {
		if !r.reader.activeKeys[key] {
			return discovery.ErrStale
		}
	}
	r.deps.Publications.Publish(record)
	// Changed rows can introduce an earlier path deadline between source rounds.
	// Inspect only the proofs already evaluated by this reconciliation.
	if !current.renewalAt.IsZero() {
		now := r.config.Now()
		for _, proof := range proofs {
			if !proof.ExpiresAt.IsZero() && !proof.ExpiresAt.After(current.renewalAt) {
				current.renewalAt = now.Add(max(time.Duration(0), proof.ExpiresAt.Sub(now)/2))
			}
		}
		r.rootQueue.AddAfter(queueKey{Operation: "root", UID: uid}, current.renewalAt.Sub(now))
	}
	for uid, evaluated := range pods {
		pod, exists := current.pods[uid]
		if !exists || pod.proofKey != evaluated.proofKey {
			continue
		}
		if pod.labelFailure != evaluated.labelFailure {
			if evaluated.labelFailure {
				current.labelFailures++
			} else {
				current.labelFailures--
			}
		}
		pod.cached, pod.labelFailure = evaluated.cached, evaluated.labelFailure
		if len(pod.path) == 0 {
			delete(current.pods, uid)
		} else {
			current.pods[uid] = pod
		}
	}
	current.labelObject, current.resolveLabels = labelObject, resolveLabels
	current.publishedGeneration = generation
	current.publishedOrigin, current.publishedGVK = interpretationOrigin, interpretationGVK
	committed = true
	return nil
}

func (r *Runtime) failPublicationLocked(uid types.UID, cause error) {
	if uid == "" {
		return
	}
	previous := r.deps.Publications.Published(uid)
	if previous != nil {
		for _, key := range previous.Creation.Dependencies {
			if strings.HasPrefix(key, "root:") {
				r.deps.Publications.SetPublicationFailure(key, failureReason(cause))
			}
		}
	}
	if root := r.roots[uid]; root != nil {
		root.generation++
	}

}

func prepareWorkload(selection inventory.Selection, object *unstructured.Unstructured) (*resource.ComponentFactory, *instructions.StructureSummary, error) {
	summary, err := instructions.NewStructureSummary(selection.Definition)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", discovery.ErrInvalidDefinition, err)
	}
	return resource.NewComponentFactoryFromObject(selection.Definition, object), summary, nil
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
