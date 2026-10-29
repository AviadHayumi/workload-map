// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Reader exposes the accepted shared collection caches to ownership discovery.
// controllerState takes its lock before Reader's lock; readers never call it
// while holding this lock.
type Reader struct {
	mu              sync.RWMutex
	clients         Clients
	options         ReaderOptions
	objects         map[objectKey]observation
	sources         map[watchKey]readSource
	ensure          func(context.Context, schema.GroupVersionKind, string, bool) (watchKey, error)
	mapperMu        sync.Mutex
	lastMapperReset time.Time
}
type Clients struct {
	Dynamic  dynamic.Interface
	Metadata metadata.Interface
	Mapper   meta.RESTMapper
}
type ReaderOptions struct{ RequestTimeout time.Duration }
type objectKey struct {
	GVK             schema.GroupVersionKind
	Namespace, Name string
}
type observation struct {
	object *unstructured.Unstructured
	source watchKey
}
type readSource struct {
	gvk          schema.GroupVersionKind
	full, synced bool
	err          error
}

func NewReader(clients Clients, options ReaderOptions) (*Reader, error) {
	if clients.Dynamic == nil || clients.Metadata == nil || clients.Mapper == nil {
		return nil, errors.New("dynamic client, metadata client and REST mapper are required")
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.RequestTimeout < 0 {
		return nil, errors.New("request timeout must be positive")
	}
	return &Reader{clients: clients, options: options, objects: map[objectKey]observation{}, sources: map[watchKey]readSource{}}, nil
}

func (r *Reader) Cached(ref discovery.ObjectRef) (*unstructured.Unstructured, bool) {
	object, _, err := r.read(ref, false)
	return object, err == nil
}

func (r *Reader) GetMetadata(ctx context.Context, ref discovery.ObjectRef) (*metav1.PartialObjectMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.ensure == nil {
		return nil, discovery.ErrPending
	}
	if _, err := r.ensure(ctx, ref.GVK, ref.Namespace, false); err != nil {
		return nil, err
	}
	object, _, err := r.read(ref, false)
	if err != nil {
		return nil, err
	}
	result := &metav1.PartialObjectMetadata{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(object.Object, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Reader) read(ref discovery.ObjectRef, full bool) (*unstructured.Unstructured, watchKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := keyOf(ref)
	entry, ok := r.objects[key]
	if !ok && key.Namespace != "" {
		key.Namespace = ""
		entry, ok = r.objects[key]
	}
	if ok {
		source := r.sources[entry.source]
		if source.err != nil {
			return nil, entry.source, source.err
		}
		if !source.synced || (full && !source.full) {
			return nil, entry.source, discovery.ErrPending
		}
		object := entry.object
		if ref.UID != "" && object.GetUID() != ref.UID {
			return nil, entry.source, apierrors.NewNotFound(entry.source.Resource.GroupResource(), ref.Name)
		}
		if ref.ResourceVersion != "" && object.GetResourceVersion() != ref.ResourceVersion {
			return nil, entry.source, discovery.ErrStale
		}
		return object, entry.source, nil
	}
	for sourceKey, source := range r.sources {
		if source.gvk != ref.GVK || (sourceKey.Namespace != "" && sourceKey.Namespace != ref.Namespace) {
			continue
		}
		if source.err != nil {
			return nil, sourceKey, source.err
		}
		if !source.synced || (full && !source.full) {
			return nil, sourceKey, discovery.ErrPending
		}
		return nil, sourceKey, apierrors.NewNotFound(sourceKey.Resource.GroupResource(), ref.Name)
	}
	return nil, watchKey{}, discovery.ErrPending
}

func (r *Reader) mapping(gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	mapping, err := r.clients.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if meta.IsNoMatchError(err) {
		r.mapperMu.Lock()
		if time.Since(r.lastMapperReset) >= 45*time.Second {
			if mapper, ok := r.clients.Mapper.(meta.ResettableRESTMapper); ok {
				mapper.Reset()
				r.lastMapperReset = time.Now()
			}
		}
		r.mapperMu.Unlock()
	}
	if err != nil {
		return nil, fmt.Errorf("map %s: %w", gvk, err)
	}
	return mapping, nil
}

func keyOf(ref discovery.ObjectRef) objectKey {
	return objectKey{GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name}
}
func refOf(object *unstructured.Unstructured) discovery.ObjectRef {
	return discovery.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID(), ResourceVersion: object.GetResourceVersion()}
}
func storedRef(ref discovery.ObjectRef) store.ObjectRef {
	return store.ObjectRef{GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID, ResourceVersion: ref.ResourceVersion}
}
func failureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err), errors.Is(err, discovery.ErrPermissionDenied), errors.Is(err, inventory.ErrPermissionDenied):
		return store.ReasonPermissionDenied
	case errors.Is(err, discovery.ErrUnresolvedOwner), apierrors.IsNotFound(err):
		return store.ReasonUnresolvedOwner
	case errors.Is(err, discovery.ErrInvalidDefinition), errors.Is(err, inventory.ErrInvalidDefinition):
		return store.ReasonInvalidDefinition
	case errors.Is(err, discovery.ErrEvaluation):
		return store.ReasonEvaluationFailed
	default:
		return store.ReasonAPIUnavailable
	}
}
