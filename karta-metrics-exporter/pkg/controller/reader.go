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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

// Reader shares accepted watch objects with discovery and caches metadata GETs.
type Reader struct {
	versions        versionCounts
	mapperMu        sync.Mutex
	lastMapperReset time.Time
	failures        map[objectKey]metadataFailure
	observed        map[objectKey]uint64
	activeKeys      map[string]bool
	sequence        uint64
	publications    store.Writer
	mu              sync.RWMutex
	clients         Clients
	options         ReaderOptions
	objects         map[objectKey]observation
	metadata        map[objectKey]metadataEntry
	pending         map[objectKey]chan struct{}
	priming         map[objectKey]uint64
}

type metadataFailure struct {
	cause   error
	expires time.Time
}

type Clients struct {
	Dynamic  dynamic.Interface
	Metadata metadata.Interface
	Mapper   meta.RESTMapper
}

type ReaderOptions struct {
	TTL            time.Duration
	RequestTimeout time.Duration
	Now            func() time.Time
}

type objectKey struct {
	GVK             schema.GroupVersionKind
	Namespace, Name string
}
type observation struct {
	sequence uint64
	object   *unstructured.Unstructured
	stream   watchKey
	evidence store.Evidence
}
type metadataEntry struct {
	sequence   uint64
	failureKey string
	object     *metav1.PartialObjectMetadata
	expires    time.Time
}

func NewReader(clients Clients, options ReaderOptions) (*Reader, error) {
	if clients.Dynamic == nil || clients.Metadata == nil || clients.Mapper == nil {
		return nil, errors.New("dynamic client, metadata client and REST mapper are required")
	}
	if options.TTL == 0 {
		options.TTL = 45 * time.Second
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.TTL < 0 || options.RequestTimeout < 0 {
		return nil, errors.New("reader durations must be positive")
	}
	return &Reader{clients: clients, options: options, objects: map[objectKey]observation{}, metadata: map[objectKey]metadataEntry{}, pending: map[objectKey]chan struct{}{}, priming: map[objectKey]uint64{}, failures: map[objectKey]metadataFailure{}, observed: map[objectKey]uint64{}, activeKeys: map[string]bool{}}, nil
}

func (r *Reader) Cached(ref discovery.ObjectRef) (*unstructured.Unstructured, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.objects[keyOf(ref)]
	if !ok && ref.Namespace != "" {
		clusterRef := ref
		clusterRef.Namespace = ""
		value, ok = r.objects[keyOf(clusterRef)]
	}
	if !ok || value.object.GetUID() != ref.UID || (ref.ResourceVersion != "" && value.object.GetResourceVersion() != ref.ResourceVersion) {
		return nil, false
	}
	return value.object, true
}

func (r *Reader) GetMetadata(ctx context.Context, ref discovery.ObjectRef) (*metav1.PartialObjectMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mapping, err := r.mapping(ref.GVK)
	if err != nil {
		r.mu.Lock()
		key := keyOf(ref)
		cluster := key
		cluster.Namespace = ""
		if _, found := r.metadata[cluster]; found || r.priming[cluster] != 0 || r.pending[cluster] != nil {
			key = cluster
		}
		r.recordMetadataFailureLocked(key, ref.UID, nil, err)
		r.removeMetadataLocked(key, failureReason(err))
		r.mu.Unlock()
		return nil, err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		ref.Namespace = ""
	}
	key := keyOf(ref)
	r.mu.RLock()
	entry, found := r.metadata[key]
	_, failed := r.failures[key]
	if found && !failed && r.options.Now().Before(entry.expires) && (ref.UID == "" || entry.object.GetUID() == ref.UID) && (ref.ResourceVersion == "" || entry.object.GetResourceVersion() == ref.ResourceVersion) {
		r.mu.RUnlock()
		return entry.object.DeepCopy(), nil
	}
	r.mu.RUnlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.mu.Lock()
		if entry, ok := r.metadata[key]; ok && r.options.Now().Before(entry.expires) && (ref.UID == "" || entry.object.GetUID() == ref.UID) && (ref.ResourceVersion == "" || entry.object.GetResourceVersion() == ref.ResourceVersion) {
			delete(r.failures, key)
			r.mu.Unlock()
			return entry.object.DeepCopy(), nil
		}
		if pending := r.pending[key]; pending != nil {
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-pending:
				continue
			}
		}
		pending := make(chan struct{})
		r.pending[key] = pending
		r.sequence++
		requestSequence := r.sequence
		r.mu.Unlock()
		started := r.options.Now()
		object, err := r.fetchMetadata(ctx, ref)
		if err == nil && !r.options.Now().Before(started.Add(r.options.TTL)) {
			err = discovery.ErrStale
		}
		r.mu.Lock()
		if r.observed[key] > requestSequence {
			if current, ok := r.metadata[key]; ok {
				object = current.object.DeepCopy()
				err = nil
			} else {
				object = nil
				err = discovery.ErrStale
			}
			delete(r.pending, key)
			close(pending)
			if _, ok := r.metadata[key]; !ok && r.priming[key] == 0 {
				delete(r.observed, key)
			}
			r.mu.Unlock()
			return object, err
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			delete(r.pending, key)
			close(pending)
			r.mu.Unlock()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		previous := r.metadata[key]
		r.recordMetadataFailureLocked(key, ref.UID, object, err)
		r.sequence++
		acceptedSequence := r.sequence
		r.observed[key] = acceptedSequence
		if err == nil {
			proofKey := previous.failureKey
			if previous.object == nil || previous.object.GetUID() != object.GetUID() || previous.object.GetResourceVersion() != object.GetResourceVersion() {
				r.retireMetadataLocked(previous, store.ReasonUnresolvedOwner)
				r.sequence++
				proofKey = fmt.Sprintf("metadata:%s:%d", key, r.sequence)
			}
			r.activeKeys[proofKey] = true
			r.saveMetadataLocked(key, metadataEntry{sequence: acceptedSequence, object: object.DeepCopy(), expires: started.Add(r.options.TTL), failureKey: proofKey})
		} else {
			r.retireMetadataLocked(previous, failureReason(err))
			r.deleteMetadataLocked(key)
		}
		delete(r.pending, key)
		if _, ok := r.metadata[key]; !ok && r.priming[key] == 0 {
			delete(r.observed, key)
		}
		close(pending)
		r.mu.Unlock()
		return object, err
	}
}

func (r *Reader) fetchMetadata(ctx context.Context, ref discovery.ObjectRef) (*metav1.PartialObjectMetadata, error) {
	mapping, err := r.mapping(ref.GVK)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", ref.GVK, err)
	}
	namespace := ref.Namespace
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		namespace = ""
	} else if namespace == "" {
		return nil, fmt.Errorf("%w: namespaced owner lacks a namespace", discovery.ErrUnresolvedOwner)
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.options.RequestTimeout)
	defer cancel()
	object, err := r.getMetadata(requestCtx, mapping.Resource, namespace, ref.Name)
	if err != nil {
		return nil, err
	}
	object.SetGroupVersionKind(ref.GVK)
	return object, nil
}

func (r *Reader) evidence(ref discovery.ObjectRef) (store.Evidence, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.objects[keyOf(ref)]; ok {
		if entry.object.GetUID() != ref.UID || entry.object.GetResourceVersion() != ref.ResourceVersion {
			return store.Evidence{}, discovery.ErrStale
		}
		if entry.evidence.ExpiresAt.IsZero() {
			return store.Evidence{}, discovery.ErrPending
		}
		return entry.evidence, nil
	}
	if entry, ok := r.metadata[keyOf(ref)]; ok && entry.object.GetUID() == ref.UID && entry.object.GetResourceVersion() == ref.ResourceVersion && r.options.Now().Before(entry.expires) {
		return store.Evidence{ExpiresAt: entry.expires, Dependencies: []string{entry.failureKey}}, nil
	}
	return store.Evidence{}, discovery.ErrPending
}

func (r *Reader) expireMetadata() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.options.Now()
	for key, failure := range r.failures {
		if !now.Before(failure.expires) {
			delete(r.failures, key)
		}
	}
	for key, entry := range r.metadata {
		if !now.Before(entry.expires) {
			r.removeMetadataLocked(key, store.ReasonAPIUnavailable)
		}
	}
}

func (r *Reader) recordMetadataFailureLocked(key objectKey, expected types.UID, object *metav1.PartialObjectMetadata, cause error) {
	if cause == nil && object != nil && expected != "" && expected != object.GetUID() {
		cause = discovery.ErrUnresolvedOwner
	}
	if cause == nil {
		delete(r.failures, key)
	} else {
		r.failures[key] = metadataFailure{cause: cause, expires: r.options.Now().Add(r.options.TTL)}
	}
}

func (r *Reader) failureHealth() map[discovery.WatchRequest]error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := map[discovery.WatchRequest]error{}
	for key, failure := range r.failures {
		if r.options.Now().Before(failure.expires) {
			request := discovery.WatchRequest{GVK: key.GVK, Namespace: key.Namespace}
			if result[request] == nil || failureReason(failure.cause) == store.ReasonPermissionDenied {
				result[request] = failure.cause
			}
		}
	}
	return result
}

func (r *Reader) removeMetadataLocked(key objectKey, reason string) {
	r.retireMetadataLocked(r.metadata[key], reason)
	r.deleteMetadataLocked(key)
	if r.pending[key] != nil || r.priming[key] != 0 {
		r.sequence++
		r.observed[key] = r.sequence
	} else {
		delete(r.observed, key)
	}
}

func (r *Reader) retireMetadataLocked(entry metadataEntry, reason string) {
	if entry.failureKey == "" {
		return
	}
	delete(r.activeKeys, entry.failureKey)
	if r.publications != nil {
		r.publications.SetPublicationFailure(entry.failureKey, reason)
		r.publications.RetirePublicationFailure(entry.failureKey)
	}
}

func (r *Reader) mapping(gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	mapping, err := r.clients.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if meta.IsNoMatchError(err) {
		r.mapperMu.Lock()
		if r.options.Now().Sub(r.lastMapperReset) >= 45*time.Second {
			if mapper, ok := r.clients.Mapper.(meta.ResettableRESTMapper); ok {
				mapper.Reset()
				r.lastMapperReset = r.options.Now()
			}
		}
		r.mapperMu.Unlock()
	}
	return mapping, err
}

func keyOf(ref discovery.ObjectRef) objectKey {
	return objectKey{GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name}
}
func refOf(object *unstructured.Unstructured) discovery.ObjectRef {
	return discovery.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID(), ResourceVersion: object.GetResourceVersion()}
}
func failureReason(err error) string {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err), errors.Is(err, discovery.ErrPermissionDenied):
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

func (r *Reader) saveMetadataLocked(key objectKey, entry metadataEntry) {
	if _, found := r.metadata[key]; !found {
		trackVersion(&r.versions, key.GVK, 1)
	}
	r.metadata[key] = entry
}

func (r *Reader) deleteMetadataLocked(key objectKey) {
	entry, found := r.metadata[key]
	if !found {
		return
	}
	// An alias cannot become affirmative root proof merely because a conflicting
	// metadata observation expires or its last dependent input disappears.
	if _, watched := r.objects[key]; !watched && metav1.GetControllerOf(entry.object) != nil {
		r.invalidateAliasesLocked(key, entry.object.GetUID())
	}
	delete(r.metadata, key)
	trackVersion(&r.versions, key.GVK, -1)
}

// Retiring a contradictory representation requires a fresh observation of its aliases.
func (r *Reader) invalidateAliasesLocked(key objectKey, uid types.UID) {
	for gvk := range r.versions[key.GVK.GroupKind()] {
		aliasKey := objectKey{GVK: gvk, Namespace: key.Namespace, Name: key.Name}
		if aliasKey == key {
			continue
		}
		if alias, exists := r.objects[aliasKey]; exists && alias.object.GetUID() == uid {
			alias.evidence = store.Evidence{}
			r.objects[aliasKey] = alias
		}
		if alias, exists := r.metadata[aliasKey]; exists && alias.object.GetUID() == uid {
			r.retireMetadataLocked(alias, store.ReasonUnresolvedOwner)
			delete(r.metadata, aliasKey)
			trackVersion(&r.versions, gvk, -1)
			if r.pending[aliasKey] != nil || r.priming[aliasKey] != 0 {
				r.sequence++
				r.observed[aliasKey] = r.sequence
			} else {
				delete(r.observed, aliasKey)
			}
		}
	}
}
