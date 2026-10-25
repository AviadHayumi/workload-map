// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Retain only metadata reachable from current selected inputs. Pruning also
// advances pending GET generations so a removed descendant cannot refill its cache.
func (r *Runtime) pruneMetadata() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reader.mu.Lock()
	defer r.reader.mu.Unlock()
	needed := map[objectKey]bool{}
	pending := []objectKey{}
	addOwners := func(object metav1.Object) {
		for _, owner := range object.GetOwnerReferences() {
			if owner.Controller == nil || !*owner.Controller {
				continue
			}
			version, err := schema.ParseGroupVersion(owner.APIVersion)
			if err != nil {
				continue
			}
			key := objectKey{GVK: version.WithKind(owner.Kind), Namespace: object.GetNamespace(), Name: owner.Name}
			// Cached cluster-scoped owners have an empty namespace.
			if _, found := r.reader.metadata[key]; !found {
				cluster := key
				cluster.Namespace = ""
				_, cached := r.reader.metadata[cluster]
				_, failed := r.reader.failures[cluster]
				if cached || failed || r.reader.pending[cluster] != nil {
					key = cluster
				}
			}
			if !needed[key] {
				needed[key] = true
				pending = append(pending, key)
			}
		}
	}
	for _, w := range r.watches {
		for key := range w.objects {
			entry, found := r.reader.objects[key]
			if !found {
				continue
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
		if entry, found := r.reader.metadata[key]; found {
			addOwners(entry.object)
		}
	}
	for key := range r.reader.metadata {
		if !needed[key] {
			r.reader.removeMetadataLocked(key, store.ReasonUnresolvedOwner)
		}
	}
	for key := range r.reader.pending {
		if !needed[key] {
			r.reader.removeMetadataLocked(key, store.ReasonUnresolvedOwner)
		}
	}
	for key := range r.reader.failures {
		if !needed[key] {
			delete(r.reader.failures, key)
		}
	}
}
