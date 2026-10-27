// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

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
		owner, valid := controllerOwner(object)
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
		owner, valid := controllerOwner(object)
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
