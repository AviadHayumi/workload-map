// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"
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
	markers := map[objectKey]uint64{}
	byName := map[string]objectKey{}
	r.reader.mu.Lock()
	for key, uid := range demand {
		if uid == "" || r.reader.priming[key] != 0 {
			continue
		}
		if _, watched := r.reader.objects[key]; watched {
			continue
		}
		if entry, found := r.reader.metadata[key]; found && entry.object.GetUID() == uid && r.reader.options.Now().Before(entry.expires) {
			continue
		}
		r.reader.sequence++
		markers[key] = r.reader.sequence
		r.reader.priming[key] = r.reader.sequence
		trackVersion(&r.reader.versions, key.GVK, 1)
		byName[key.Name] = key
	}
	r.reader.mu.Unlock()
	defer func() {
		r.reader.mu.Lock()
		defer r.reader.mu.Unlock()
		for key, marker := range markers {
			if r.reader.priming[key] == marker {
				delete(r.reader.priming, key)
				trackVersion(&r.reader.versions, key.GVK, -1)
			}
			if _, cached := r.reader.metadata[key]; !cached && r.reader.pending[key] == nil {
				delete(r.reader.observed, key)
			}
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
		if object == nil || object.GetUID() != demand[key] || canonical[key] != demand[key] || r.reader.priming[key] != marker || r.reader.observed[key] > marker {
			continue
		}
		if _, watched := r.reader.objects[key]; watched {
			continue
		}
		r.reader.retireMetadataLocked(r.reader.metadata[key], store.ReasonAPIUnavailable)
		proof := fmt.Sprintf("metadata:%s:%d", key, marker)
		r.reader.saveMetadataLocked(key, metadataEntry{object: object, sequence: marker, failureKey: proof, expires: started.Add(r.reader.options.TTL)})
		r.reader.observed[key] = marker
		r.reader.activeKeys[proof] = true
		delete(r.reader.failures, key)
		added++
	}
	return added, nil
}

func (r *Reader) hasPrimeDemand(markers map[objectKey]uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for key, marker := range markers {
		if r.priming[key] != marker || r.observed[key] > marker {
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
		owner := metav1.GetControllerOf(object)
		if owner == nil {
			return
		}
		version, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil {
			return
		}
		key := objectKey{GVK: version.WithKind(owner.Kind), Namespace: object.GetNamespace(), Name: owner.Name}
		cluster := key
		cluster.Namespace = ""
		if r.priming[key] == 0 && r.priming[cluster] != 0 {
			key = cluster
		}
		if seen[key] {
			return
		}
		seen[key] = true
		if r.priming[key] != 0 {
			r.sequence++
			r.observed[key] = r.sequence
		}
		entry, found := r.metadata[key]
		if !found {
			return
		}
		object = entry.object
	}
}
