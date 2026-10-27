// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type pathConsumer struct {
	UID types.UID
	GVK schema.GroupVersionKind
}

type pathDependent struct {
	owner    types.UID
	input    discovery.ObjectRef
	position int
}

type metadataPathFailure struct {
	reason   string
	position int
	input    discovery.ObjectRef
}

// Priming runs on the source queue. Discovery GETs keep making progress and any
// newer accepted per-object result wins over this optional collection read.
func (r *Runtime) primeMetadata(ctx context.Context) error {
	parent := ctx
	deadline := r.reader.options.Now().Add(r.reader.options.TTL)
	ctx, cancelRound := context.WithTimeout(ctx, r.reader.options.TTL)
	defer cancelRound()
	attempted := map[objectKey]types.UID{}
	rounds := map[watchKey]bool{}
	scopes := map[schema.GroupVersionKind]*meta.RESTMapping{}
	for ctx.Err() == nil && r.reader.options.Now().Before(deadline) {
		r.mu.Lock()
		r.reader.mu.RLock()
		demand := r.metadataDemandLocked()
		r.reader.mu.RUnlock()
		r.mu.Unlock()
		groups := map[watchKey]map[objectKey]types.UID{}
		for key, uid := range demand {
			if ctx.Err() != nil || !r.reader.options.Now().Before(deadline) {
				return parent.Err()
			}
			if uid == "" {
				continue
			}
			mapping, found := scopes[key.GVK]
			if !found {
				var err error
				mapping, err = r.reader.mapping(key.GVK)
				if err != nil {
					continue
				}
				scopes[key.GVK] = mapping
			}
			if mapping.Scope.Name() == meta.RESTScopeNameRoot {
				key.Namespace = ""
			}
			if attempted[key] == uid {
				continue
			}
			group := watchKey{Resource: mapping.Resource, Namespace: key.Namespace}
			if groups[group] == nil {
				groups[group] = map[objectKey]types.UID{}
			}
			if previous, found := groups[group][key]; found && previous != uid {
				groups[group][key] = ""
			} else {
				groups[group][key] = uid
			}
		}
		progress := false
		for group, keys := range groups {
			if ctx.Err() != nil || !r.reader.options.Now().Before(deadline) {
				return parent.Err()
			}
			r.mu.Lock()
			if r.primeAttempts == nil {
				r.primeAttempts = map[watchKey]time.Time{}
			}
			allowed := rounds[group] || r.config.Now().Sub(r.primeAttempts[group]) >= r.reader.options.TTL
			if allowed {
				r.primeAttempts[group] = r.config.Now()
				rounds[group] = true
			}
			r.mu.Unlock()
			if !allowed {
				continue
			}
			for key, uid := range keys {
				attempted[key] = uid
			}
			added, err := r.primeMetadataGroup(ctx, group, keys, scopes, deadline)
			if ctx.Err() != nil {
				return parent.Err()
			}
			// A failed optimization leaves ordinary GET workers in charge, including
			// GET-only permission and partial or unavailable collection responses.
			if err == nil && added > 0 {
				progress = true
			}
		}
		if !progress {
			return nil
		}
	}
	return parent.Err()
}

func (r *Runtime) primeMetadataGroup(ctx context.Context, group watchKey, demand map[objectKey]types.UID, scopes map[schema.GroupVersionKind]*meta.RESTMapping, deadline time.Time) (int, error) {
	markers := map[objectKey]metadataRead{}
	byName := map[string]objectKey{}
	r.reader.mu.Lock()
	for key, uid := range demand {
		if uid == "" {
			continue
		}
		if _, watched := r.reader.objects[key]; watched {
			continue
		}
		if entry, found := r.reader.metadata[key]; found && entry.object.GetUID() == uid && r.reader.options.Now().Before(entry.expires) {
			continue
		}
		state := r.reader.metadataReadLocked(key)
		if state.priming {
			continue
		}
		state.priming = true
		markers[key] = metadataRead{state: state, revision: state.revision}
		byName[key.Name] = key
	}
	r.reader.mu.Unlock()
	defer func() {
		r.reader.mu.Lock()
		defer r.reader.mu.Unlock()
		for key, marker := range markers {
			marker.state.priming = false
			r.reader.releaseMetadataReadLocked(key, marker.state)
		}
	}()
	if len(markers) == 0 {
		return 0, nil
	}
	found, started, err := r.reader.readMetadataPages(ctx, group, byName, markers, deadline)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reader.mu.Lock()
	defer r.reader.mu.Unlock()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if !r.reader.options.Now().Before(started.Add(r.reader.options.TTL)) || !r.reader.options.Now().Before(deadline) {
		return 0, discovery.ErrStale
	}
	live := r.metadataDemandLocked()
	canonical := map[objectKey]types.UID{}
	for key, uid := range live {
		if mapping := scopes[key.GVK]; mapping != nil && mapping.Scope.Name() == meta.RESTScopeNameRoot {
			key.Namespace = ""
		}
		if previous, found := canonical[key]; found && previous != uid {
			canonical[key] = ""
		} else {
			canonical[key] = uid
		}
	}
	added := 0
	for key, marker := range markers {
		object := found[key]
		if object == nil || object.GetUID() != demand[key] || canonical[key] != demand[key] || r.reader.metadataReads[key] != marker.state || marker.state.revision != marker.revision {
			continue
		}
		if _, watched := r.reader.objects[key]; watched {
			continue
		}
		r.reader.acceptMetadataLocked(key, demand[key], object, started, nil)
		added++
	}
	return added, nil
}

func (r *Reader) hasPrimeDemand(markers map[objectKey]metadataRead) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for key, marker := range markers {
		if r.metadataReads[key] != marker.state || marker.state.revision != marker.revision {
			continue
		}
		if _, watched := r.objects[key]; !watched {
			return true
		}
	}
	return false
}

// A changed input cancels only its primed owner chain. Shared consumers can still
// use ordinary GETs; existing accepted metadata is unaffected.
func (r *Reader) invalidatePrimeOwnersLocked(object metav1.Object) {
	seen := map[objectKey]bool{}
	for object != nil {
		owner, valid := discovery.ControllerOwner(object)
		if !valid || owner.UID == "" {
			return
		}
		key := objectKey{GVK: owner.GVK, Namespace: object.GetNamespace(), Name: owner.Name}
		cluster := key
		cluster.Namespace = ""
		state := r.metadataReads[key]
		if state == nil || !state.priming {
			if candidate := r.metadataReads[cluster]; candidate != nil && candidate.priming {
				key, state = cluster, candidate
			}
		}
		if seen[key] {
			return
		}
		seen[key] = true
		if state != nil && state.priming {
			r.invalidateMetadataReadLocked(key)
		}
		entry, found := r.metadata[key]
		if !found {
			return
		}
		object = entry.object
	}
}

// Called with Runtime and Reader locked. Conflicting UIDs retain the key for
// ordinary GET verification but cannot authorize a primed observation.
func (r *Runtime) metadataDemandLocked() map[objectKey]types.UID {
	needed := map[objectKey]types.UID{}
	pending := []objectKey{}
	addOwners := func(object metav1.Object) {
		owner, valid := discovery.ControllerOwner(object)
		if !valid || owner.UID == "" || slices.Contains(r.config.IgnoredOwnerKinds, owner.GVK) {
			return
		}
		key := objectKey{GVK: owner.GVK, Namespace: object.GetNamespace(), Name: owner.Name}
		if _, found := r.reader.metadata[key]; !found {
			cluster := key
			cluster.Namespace = ""
			_, cached := r.reader.metadata[cluster]
			_, failed := r.reader.failures[cluster]
			if cached || failed || r.reader.metadataReads[cluster] != nil {
				key = cluster
			}
		}
		if prior, exists := needed[key]; exists {
			if prior != owner.UID {
				needed[key] = ""
			}
			return
		}
		needed[key] = owner.UID
		pending = append(pending, key)
	}
	for _, w := range r.watches {
		for key := range w.objects {
			entry, found := r.reader.objects[key]
			if !found {
				continue
			}
			if r.config.NamespaceSelector != nil && entry.object.GetNamespace() != "" {
				namespace, found := r.reader.objects[objectKey{GVK: namespaceGVK, Name: entry.object.GetNamespace()}]
				if !found || !r.namespaceSelector.Matches(labelsFor(namespace.object)) {
					continue
				}
			}
			if w.gvk == podGVK {
				if !r.podSelector.Matches(labelsFor(entry.object)) {
					continue
				}
			} else if r.roots[entry.object.GetUID()] == nil && !w.discover {
				continue
			}
			addOwners(entry.object)
		}
	}
	for len(pending) > 0 {
		key := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if entry, found := r.reader.metadata[key]; found && entry.object.GetUID() == needed[key] {
			addOwners(entry.object)
		}
	}
	return needed
}

// Pruning also fences pending GET and LIST results after the last input leaves.
func (r *Runtime) pruneMetadata() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reader.mu.Lock()
	defer r.reader.mu.Unlock()
	r.pruneMetadataLocked()
}

func (r *Runtime) pruneMetadataLocked() {
	needed := r.metadataDemandLocked()
	for group, attempted := range r.primeAttempts {
		if r.config.Now().Sub(attempted) >= r.reader.options.TTL {
			delete(r.primeAttempts, group)
		}
	}
	for key := range r.reader.metadata {
		if _, found := needed[key]; !found {
			r.reader.removeMetadataLocked(key, store.ReasonUnresolvedOwner)
		}
	}
	for key := range r.reader.metadataReads {
		if _, found := needed[key]; !found {
			r.reader.invalidateMetadataReadLocked(key)
		}
	}
	for key := range r.reader.failures {
		if _, found := needed[key]; !found {
			delete(r.reader.failures, key)
		}
	}
}

// refreshMetadata verifies only retained owners. Unrelated LIST items are discarded.
func (r *Reader) refreshMetadata(ctx context.Context) error {
	r.mu.RLock()
	snapshot := make(map[objectKey]metadataEntry, len(r.metadata))
	for key, entry := range r.metadata {
		snapshot[key] = entry
	}
	r.mu.RUnlock()
	groups := map[watchKey]map[objectKey]metadataEntry{}
	resources := map[schema.GroupVersionKind]schema.GroupVersionResource{}
	mappingErrors := map[schema.GroupVersionKind]error{}
	var failures []error
	for key, entry := range snapshot {
		resource, ok := resources[key.GVK]
		if !ok && mappingErrors[key.GVK] == nil {
			mapping, err := r.mapping(key.GVK)
			if err != nil {
				mappingErrors[key.GVK] = err
				failures = append(failures, err)
			} else {
				resource = mapping.Resource
				resources[key.GVK] = resource
			}
		}
		if err := mappingErrors[key.GVK]; err != nil {
			r.mu.Lock()
			if current, ok := r.metadata[key]; ok && current.sequence == entry.sequence {
				r.recordMetadataFailureLocked(key, entry.object.GetUID(), nil, err)
				r.removeMetadataLocked(key, failureReason(err))
			}
			r.mu.Unlock()
			continue
		}
		group := watchKey{Resource: resource, Namespace: key.Namespace}
		if groups[group] == nil {
			groups[group] = map[objectKey]metadataEntry{}
		}
		groups[group][key] = entry
	}
	for group, entries := range groups {
		if err := r.refreshMetadataGroup(ctx, group, entries); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (r *Reader) refreshMetadataGroup(ctx context.Context, group watchKey, entries map[objectKey]metadataEntry) error {
	byName := make(map[string]objectKey, len(entries))
	for key := range entries {
		byName[key.Name] = key
	}
	found, started, listErr := r.readMetadataPages(ctx, group, byName, nil, time.Time{})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if apierrors.IsForbidden(listErr) || apierrors.IsUnauthorized(listErr) {
		return r.refreshMetadataByGet(ctx, entries)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, old := range entries {
		current, ok := r.metadata[key]
		if !ok || current.failureKey != old.failureKey || current.sequence != old.sequence {
			continue
		}
		object, err := found[key], listErr
		if err == nil && object == nil {
			err = discovery.ErrUnresolvedOwner
		}
		r.acceptMetadataLocked(key, old.object.GetUID(), object, started, err)
	}
	return listErr
}

// Only requested names are retained from each page; unrelated objects are discarded.
func (r *Reader) readMetadataPages(ctx context.Context, group watchKey, byName map[string]objectKey, markers map[objectKey]metadataRead, deadline time.Time) (map[objectKey]*metav1.PartialObjectMetadata, time.Time, error) {
	started := r.options.Now()
	if deadline.IsZero() || started.Add(r.options.TTL).Before(deadline) {
		deadline = started.Add(r.options.TTL)
	}
	found := make(map[objectKey]*metav1.PartialObjectMetadata, len(byName))
	err := r.readMetadataList(ctx, group, markers, deadline, r.options.Now, func(object *metav1.PartialObjectMetadata) {
		if key, ok := byName[object.Name]; ok && key.Namespace == object.Namespace {
			copy := object.DeepCopy()
			copy.SetGroupVersionKind(key.GVK)
			found[key] = copy
		}
	})
	return found, started, err
}

// Each caller stages only the fields it needs. A partial round never authorizes
// acceptance; the common loop enforces one RV and a bounded read-start deadline.
func (r *Reader) readMetadataList(ctx context.Context, group watchKey, markers map[objectKey]metadataRead, deadline time.Time, now func() time.Time, visit func(*metav1.PartialObjectMetadata)) error {
	ctx, cancelRound := context.WithTimeout(ctx, deadline.Sub(now()))
	defer cancelRound()
	options := metav1.ListOptions{Limit: 500}
	tokens := map[string]bool{}
	version := ""
	for {
		remaining := deadline.Sub(now())
		if remaining <= 0 || (markers != nil && !r.hasPrimeDemand(markers)) {
			return discovery.ErrStale
		}
		requestCtx, cancel := context.WithTimeout(ctx, min(r.options.RequestTimeout, remaining))
		page, err := r.listMetadata(requestCtx, group.Resource, group.Namespace, options)
		cancel()
		if err != nil {
			return err
		}
		if version != "" && page.ResourceVersion != version {
			return discovery.ErrStale
		}
		version = page.ResourceVersion
		for i := range page.Items {
			visit(&page.Items[i])
		}
		options.Continue = page.Continue
		if options.Continue == "" {
			break
		}
		if tokens[options.Continue] {
			return fmt.Errorf("%w: repeated metadata LIST continuation", discovery.ErrStale)
		}
		tokens[options.Continue] = true
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !now().Before(deadline) {
		return discovery.ErrStale
	}
	return nil
}

// A GET-only owner permission remains sufficient when collection LIST is denied.
func (r *Reader) refreshMetadataByGet(ctx context.Context, entries map[objectKey]metadataEntry) error {
	var failures []error
	for key, old := range entries {
		started := r.options.Now()
		object, err := r.fetchMetadata(ctx, discovery.ObjectRef{GVK: key.GVK, Namespace: key.Namespace, Name: key.Name})
		if err == nil && !r.options.Now().Before(started.Add(r.options.TTL)) {
			err = discovery.ErrStale
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.mu.Lock()
		current, ok := r.metadata[key]
		if ok && current.failureKey == old.failureKey && current.sequence == old.sequence {
			r.acceptMetadataLocked(key, old.object.GetUID(), object, started, err)
		}
		r.mu.Unlock()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Retained paths still need recovery when expiry or pruning removed their last
// metadata entry. Roots without intermediate inputs have no work in this lane.
func (r *Runtime) refreshMetadata(ctx context.Context) error {
	r.reader.mu.RLock()
	hadInputs := len(r.reader.metadata) > 0 || len(r.reader.failures) > 0 || len(r.reader.metadataReads) > 0
	r.reader.mu.RUnlock()
	if hadInputs {
		r.pruneMetadata()
	}
	r.reader.expireMetadata()
	var err error
	if hadInputs {
		err = r.reader.refreshMetadata(ctx)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !hadInputs && len(r.pathDependents) == 0 {
		return nil
	}
	r.reader.mu.RLock()
	r.scheduleMetadataPathsLocked()
	r.reader.mu.RUnlock()
	r.publishHealthLocked()
	return err
}

// Runtime and Reader are locked. Each edge is inspected once and each root is
// scheduled once, so a shared ancestor cannot cause repeated Pod-page scans.
func (r *Runtime) scheduleMetadataPathsLocked() {
	roots := map[types.UID]bool{}
	invalid := map[types.UID]metadataPathFailure{}
	failedSources := map[string]string{}
	for _, source := range r.watches {
		if source.err != nil {
			failedSources[source.failureKey] = failureReason(source.err)
		}
	}
	for key, dependents := range r.pathDependents {
		for consumer, dependency := range dependents {
			pod := consumer.UID
			if dependency.input.GVK != podGVK {
				candidate := r.candidates[pod][keyOf(dependency.input)]
				if candidate != nil && dependency.position+1 < len(candidate.path) {
					position := dependency.position
					if r.metadataPathReasonLocked(key, candidate.path[position], candidate.path[position+1], failedSources) != "" {
						r.enqueueObjectLocked(candidate.ref)
					}
				}
				continue
			}
			root := r.roots[r.podRoots[pod]]
			if root == nil {
				continue
			}
			path := root.pods[pod].path
			position := dependency.position
			if position <= 0 || position+1 >= len(path) {
				continue
			}
			reason := r.metadataPathReasonLocked(key, path[position], path[position+1], failedSources)
			if reason == "" && root.pods[pod].resolveError != nil {
				reason = failureReason(root.pods[pod].resolveError)
			}
			if reason == "" && len(root.resolutionErrors) > 0 {
				reason = store.ReasonAPIUnavailable
			}
			uid := root.ref.UID
			roots[uid] = roots[uid] || reason != ""
			if previous, found := invalid[pod]; reason != "" && (!found || position < previous.position) {
				invalid[pod] = metadataPathFailure{reason: reason, position: position, input: dependency.input}
			}
		}
	}
	for pod, failure := range invalid {
		r.invalidatePodLocked(r.roots[r.podRoots[pod]], pod, failure.reason)
		r.inputQueue.Add(inputKey{Object: keyOf(failure.input), UID: pod})
	}
	for uid, urgent := range roots {
		if urgent {
			r.rootQueue.Add(rootKey{UID: uid})
		} else {
			r.scheduleRenewalLocked(uid)
		}
	}
}

func (r *Runtime) metadataPathReasonLocked(key objectKey, expected, parent discovery.ObjectRef, failedSources map[string]string) string {
	var object metav1.Object
	var proof store.Evidence
	if entry, watched := r.reader.objects[key]; watched {
		object, proof = entry.object, entry.evidence
	} else {
		if failure, failed := r.reader.failures[key]; failed && r.reader.options.Now().Before(failure.expires) {
			return failureReason(failure.cause)
		}
		if entry, found := r.reader.metadata[key]; found {
			object = entry.object
			proof = store.Evidence{ExpiresAt: entry.expires, Dependencies: []string{entry.failureKey}}
		}
	}
	if object == nil {
		return store.ReasonAPIUnavailable
	}
	owner, valid := discovery.ControllerOwner(object)
	if object.GetUID() != expected.UID || !valid || owner.GVK != parent.GVK || owner.Name != parent.Name || owner.UID != parent.UID {
		return store.ReasonUnresolvedOwner
	}
	for _, key := range proof.Dependencies {
		if reason := failedSources[key]; reason != "" {
			return reason
		}
	}
	if object.GetResourceVersion() != expected.ResourceVersion || !r.config.Now().Before(proof.ExpiresAt) {
		return store.ReasonAPIUnavailable
	}
	if proof.Reason != "" && proof.Reason != store.ReasonNone {
		return proof.Reason
	}
	for _, key := range proof.Dependencies {
		if !r.reader.activeKeys[key] {
			return store.ReasonAPIUnavailable
		}
	}
	return ""
}

// Runtime owns this index. Leaf and root changes already have direct generation
// guards; only intermediate inputs need reverse lookup to reach affected Pods.
func (r *Runtime) setPodPathLocked(root *rootState, pod types.UID, path []discovery.ObjectRef) {
	entry := root.pods[pod]
	r.replacePathLocked(entry.path, path)
	entry.path, entry.resolveError = slices.Clone(path), nil
	root.pods[pod] = entry
}

func (r *Runtime) removePodPathLocked(root *rootState, pod types.UID) {
	r.setPodPathLocked(root, pod, nil)
}

func (r *Runtime) replacePathLocked(previous, next []discovery.ObjectRef) {
	for i := 1; i+1 < len(previous); i++ {
		key := keyOf(previous[i])
		delete(r.pathDependents[key], pathConsumer{UID: previous[0].UID, GVK: previous[0].GVK})
		if len(r.pathDependents[key]) == 0 {
			delete(r.pathDependents, key)
		}
	}
	for i := 1; i+1 < len(next); i++ {
		key := keyOf(next[i])
		if r.pathDependents[key] == nil {
			r.pathDependents[key] = map[pathConsumer]pathDependent{}
		}
		r.pathDependents[key][pathConsumer{UID: next[0].UID, GVK: next[0].GVK}] = pathDependent{owner: next[i].UID, input: next[0], position: i}
	}
}

func (r *Runtime) invalidatePathDependentsLocked(ref discovery.ObjectRef, reason string) {
	for consumer, dependency := range r.pathDependents[keyOf(ref)] {
		pod := consumer.UID
		if dependency.owner != ref.UID {
			continue
		}
		if dependency.input.GVK != podGVK {
			if candidate := r.candidates[pod][keyOf(dependency.input)]; candidate != nil {
				r.enqueueObjectLocked(candidate.ref)
			}
			continue
		}
		if root := r.roots[r.podRoots[pod]]; root != nil {
			r.invalidatePodLocked(root, pod, reason)
			r.inputQueue.Add(inputKey{Object: keyOf(dependency.input), UID: pod})
		}
	}
}

func (r *Runtime) setCandidatePathLocked(candidate *rootCandidate, path []discovery.ObjectRef) {
	r.replacePathLocked(candidate.path, path)
	candidate.path = slices.Clone(path)
}
