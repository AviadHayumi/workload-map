// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

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
	started := r.options.Now()
	found := make(map[objectKey]*metav1.PartialObjectMetadata, len(entries))
	byName := make(map[string]objectKey, len(entries))
	for key := range entries {
		byName[key.Name] = key
	}
	options := metav1.ListOptions{Limit: 500}
	tokens := map[string]bool{}
	version := ""
	var listErr error
	for {
		requestCtx, cancel := context.WithTimeout(ctx, r.options.RequestTimeout)
		page, err := r.clients.Metadata.Resource(group.Resource).Namespace(group.Namespace).List(requestCtx, options)
		cancel()
		if err != nil {
			listErr = err
			break
		}
		if version != "" && page.ResourceVersion != version {
			listErr = discovery.ErrStale
			break
		}
		version = page.ResourceVersion
		for i := range page.Items {
			object := &page.Items[i]
			if key, ok := byName[object.Name]; ok && key.Namespace == object.Namespace {
				copy := object.DeepCopy()
				copy.SetGroupVersionKind(key.GVK)
				found[key] = copy
			}
		}
		options.Continue = page.Continue
		if options.Continue == "" {
			break
		}
		if tokens[options.Continue] {
			listErr = errors.New("repeated owner metadata LIST continuation")
			break
		}
		tokens[options.Continue] = true
	}
	if listErr == nil && !r.options.Now().Before(started.Add(r.options.TTL)) {
		listErr = discovery.ErrStale
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
		object := found[key]
		switch {
		case listErr != nil:
			r.recordMetadataFailureLocked(key, old.object.GetUID(), nil, listErr)
			r.removeMetadataLocked(key, failureReason(listErr))
		case object == nil:
			r.recordMetadataFailureLocked(key, old.object.GetUID(), nil, discovery.ErrUnresolvedOwner)
			r.removeMetadataLocked(key, store.ReasonUnresolvedOwner)
		default:
			r.recordMetadataFailureLocked(key, old.object.GetUID(), object, nil)
			proofKey := current.failureKey
			if current.object.GetUID() != object.GetUID() || current.object.GetResourceVersion() != object.GetResourceVersion() {
				r.retireMetadataLocked(current, store.ReasonUnresolvedOwner)
				r.sequence++
				proofKey = fmt.Sprintf("metadata:%s:%d", key, r.sequence)
			}
			r.activeKeys[proofKey] = true
			r.sequence++
			r.observed[key] = r.sequence
			r.metadata[key] = metadataEntry{sequence: r.sequence, object: object, expires: started.Add(r.options.TTL), failureKey: proofKey}
		}
	}
	return listErr
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
		r.mu.Lock()
		current, ok := r.metadata[key]
		if ok && current.failureKey == old.failureKey && current.sequence == old.sequence {
			r.recordMetadataFailureLocked(key, old.object.GetUID(), object, err)
			if err != nil {
				r.removeMetadataLocked(key, failureReason(err))
			} else {
				proofKey := current.failureKey
				if current.object.GetUID() != object.GetUID() || current.object.GetResourceVersion() != object.GetResourceVersion() {
					r.retireMetadataLocked(current, store.ReasonUnresolvedOwner)
					r.sequence++
					proofKey = fmt.Sprintf("metadata:%s:%d", key, r.sequence)
				}
				r.activeKeys[proofKey] = true
				r.sequence++
				r.observed[key] = r.sequence
				r.metadata[key] = metadataEntry{sequence: r.sequence, object: object.DeepCopy(), expires: started.Add(r.options.TTL), failureKey: proofKey}
			}
		}
		r.mu.Unlock()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
