// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Called with Runtime and Reader locked. Conflicting UIDs retain the key for
// ordinary GET verification but cannot authorize a primed observation.
func (r *Runtime) metadataDemandLocked() map[objectKey]types.UID {
	needed := map[objectKey]types.UID{}
	pending := []objectKey{}
	addOwners := func(object metav1.Object) {
		var controller *metav1.OwnerReference
		for _, owner := range object.GetOwnerReferences() {
			if owner.Controller == nil || !*owner.Controller {
				continue
			}
			if controller != nil {
				return
			}
			candidate := owner
			controller = &candidate
		}
		if controller == nil || controller.Name == "" || controller.UID == "" || controller.Kind == "" {
			return
		}
		version, err := schema.ParseGroupVersion(controller.APIVersion)
		if err != nil || version.Version == "" {
			return
		}
		gvk := version.WithKind(controller.Kind)
		if slices.Contains(r.config.IgnoredOwnerKinds, gvk) {
			return
		}
		key := objectKey{GVK: gvk, Namespace: object.GetNamespace(), Name: controller.Name}
		if _, found := r.reader.metadata[key]; !found {
			cluster := key
			cluster.Namespace = ""
			_, cached := r.reader.metadata[cluster]
			_, failed := r.reader.failures[cluster]
			if cached || failed || r.reader.pending[cluster] != nil || r.reader.priming[cluster] != 0 {
				key = cluster
			}
		}
		if prior, exists := needed[key]; exists {
			if prior != controller.UID {
				needed[key] = ""
			}
			return
		}
		needed[key] = controller.UID
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
	for key := range r.reader.pending {
		if _, found := needed[key]; !found {
			r.reader.removeMetadataLocked(key, store.ReasonUnresolvedOwner)
		}
	}
	for key := range r.reader.priming {
		if _, found := needed[key]; !found {
			r.reader.removeMetadataLocked(key, store.ReasonUnresolvedOwner)
		}
	}
	for key := range r.reader.failures {
		if _, found := needed[key]; !found {
			delete(r.reader.failures, key)
		}
	}
}
