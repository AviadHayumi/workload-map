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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func (c *Runtime) queueVerification() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, stream := range c.reader.streams {
		c.queue.Add(queuedWork{Operation: "verify", GVK: stream.gvk, Namespace: stream.key.Namespace})
	}
	c.queue.Add(queuedWork{Operation: "definitions"})
}

func (c *Runtime) verifyStream(ctx context.Context, key queuedWork) error {
	c.mu.Lock()
	var stream *resourceStream
	for _, candidate := range c.reader.streams {
		if candidate.gvk == key.GVK && candidate.key.Namespace == key.Namespace {
			stream = candidate
			break
		}
	}
	if stream == nil {
		c.mu.Unlock()
		return nil
	}
	if !stream.ready {
		c.mu.Unlock()
		return discovery.ErrPending
	}
	run, failure, scope := stream.run, stream.failure, c.scopeRevision
	captured := map[objectKey]objectFact{}
	for k := range c.reader.byStream[stream.key] {
		captured[k] = c.reader.objects[k]
	}
	membershipRevision := c.membershipRevision[stream.key.Namespace]
	c.mu.Unlock()

	started := c.options.Clock()
	requestCtx, cancel := context.WithTimeout(ctx, c.options.MaxAge)
	defer cancel()
	refs, err := c.listReferences(requestCtx, stream)
	if err != nil {
		c.failStream(stream, err)
		return err
	}
	expires := started.Add(c.options.MaxAge)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reader.streams[stream.key] != stream || run != stream.run ||
		failure != stream.failure || scope != c.scopeRevision || !c.options.Clock().Before(expires) {
		return discovery.ErrStale
	}
	if stream.err != nil {
		stream.err = nil
		c.routeChangesLocked(c.discovery.CoverageChanged(discovery.CoverageChange{
			Key: stream.key, Run: stream.run, FullObject: stream.full, Ready: true,
		}))
	}
	stream.verifiedAt = started
	seen := map[objectKey]types.UID{}
	for _, ref := range refs {
		objectKey := refKey(ref)
		seen[objectKey] = ref.UID
		current, exists := c.reader.objects[objectKey]
		old, wasCaptured := captured[objectKey]
		if !exists || !wasCaptured || current.revision != old.revision ||
			current.observation.Run != run {
			continue
		}
		if current.observation.Ref != ref {
			c.queue.Add(queuedWork{Operation: "object", GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID})
			if current.observation.Ref.UID == ref.UID {
				delete(c.proofs, objectKey)
				// Reuse the current dependency set without adopting the API body.
				changes := c.discovery.Upsert(current.observation)
				for _, changed := range changes.Objects {
					c.suppressObjectLocked(changed, false)
				}
				c.routeChangesLocked(changes)
				c.invalidateMembershipLocked(ref.Namespace)
			}
			continue
		}
		c.proofs[objectKey] = objectProof{ref: ref, run: run, expiresAt: expires, key: stream.failureKey}
		c.queue.Add(queuedWork{Operation: "object", GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID})
	}
	for k, old := range captured {
		if current, exists := c.reader.objects[k]; exists && seen[k] != old.observation.Ref.UID &&
			current.revision == old.revision && current.observation.Run == old.observation.Run {
			c.deleteObjectLocked(current.observation.Ref, stream)
		}
	}
	if stream.gvk == runtimePodGVK && membershipRevision == c.membershipRevision[stream.key.Namespace] {
		c.podLists[stream.key.Namespace] = podListProof{
			revision: membershipRevision, expiresAt: expires, key: stream.failureKey, refs: refs,
		}
	}
	c.queue.Add(queuedWork{Operation: "membership", Namespace: stream.key.Namespace})
	c.updateHealthLocked()
	return nil
}

// listReferences discards page bodies as soon as compact facts are copied. No
// selectors are sent: hidden Pods remain part of complete membership evidence.
func (c *Runtime) listReferences(ctx context.Context, stream *resourceStream) ([]discovery.ObjectRef, error) {
	var result []discovery.ObjectRef
	var resourceVersion string
	var hasPage bool
	options := metav1.ListOptions{Limit: c.options.PageSize}
	continuations := map[string]bool{}
	for {
		var refs []discovery.ObjectRef
		var continuation, version string
		pageCtx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
		fullObjectList := c.metadata == nil
		if c.metadata != nil {
			page, err := c.metadata.Resource(stream.key.Resource).Namespace(stream.key.Namespace).List(pageCtx, options)
			if err == nil {
				for i := range page.Items {
					refs = append(refs, objectReference(&page.Items[i], stream.gvk))
				}
				continuation, version = page.GetContinue(), page.GetResourceVersion()
			} else if !apierrors.IsNotAcceptable(err) && !apierrors.IsUnsupportedMediaType(err) {
				cancel()
				return nil, err
			} else {
				fullObjectList = true
			}
		}
		if fullObjectList {
			page, err := c.dynamic.Resource(stream.key.Resource).Namespace(stream.key.Namespace).List(pageCtx, options)
			if err != nil {
				cancel()
				return nil, err
			}
			for i := range page.Items {
				refs = append(refs, objectReference(&page.Items[i], stream.gvk))
			}
			continuation, version = page.GetContinue(), page.GetResourceVersion()
		}
		cancel()
		if hasPage && resourceVersion != version {
			return nil, fmt.Errorf("%w: LIST resourceVersion changed between pages", discovery.ErrUnavailable)
		}
		resourceVersion = version
		hasPage = true
		result = append(result, refs...)
		if continuation == "" {
			return result, nil
		}
		if continuations[continuation] {
			return nil, fmt.Errorf("%w: repeated LIST continuation token", discovery.ErrUnavailable)
		}
		continuations[continuation] = true
		options.Continue = continuation
	}
}

func (c *Runtime) verifyMembership(ctx context.Context, namespace string) error {
	c.mu.Lock()
	proof, exists := c.podLists[namespace]
	revision := c.membershipRevision[namespace]
	if !exists || proof.revision != revision || !c.options.Clock().Before(proof.expiresAt) {
		c.mu.Unlock()
		return discovery.ErrPending
	}
	c.mu.Unlock()
	groups := map[types.UID][]discovery.ObjectRef{}
	tokens := make([]discovery.CapturedState, 0, len(proof.refs))
	paths := make([]discovery.ObjectRef, 0, len(proof.refs))
	for _, ref := range proof.refs {
		workload, err := c.discovery.Resolve(ctx, ref)
		if err != nil && workload.Root().UID == "" {
			if errors.Is(err, discovery.ErrStale) {
				return err
			}
			c.mu.Lock()
			if c.membershipRevision[namespace] == revision {
				c.invalidateMembershipLocked(namespace)
			}
			c.mu.Unlock()
			return err
		}
		root := workload.Root()
		groups[root.UID] = append(groups[root.UID], ref)
		tokens = append(tokens, workload.State())
		paths = append(paths, workload.Path...)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.membershipRevision[namespace] != revision || (len(tokens) != 0 && !c.discovery.IsCurrent(tokens...)) {
		return discovery.ErrStale
	}
	evidence := store.Evidence{ExpiresAt: proof.expiresAt, Dependencies: []string{proof.key, fmt.Sprintf("membership:%s:%d", namespace, revision)}}
	for _, ref := range paths {
		current, ok := c.proofLocked(ref)
		if !ok {
			return discovery.ErrPending
		}
		evidence.ExpiresAt = earlier(evidence.ExpiresAt, current.expiresAt)
		if !slices.Contains(evidence.Dependencies, current.key) {
			evidence.Dependencies = append(evidence.Dependencies, current.key)
		}
	}
	c.membership[namespace] = membershipProof{revision: revision, evidence: evidence, pods: groups}
	for uid := range groups {
		c.queue.Add(queuedWork{Operation: "root", UID: uid})
	}
	for uid := range c.rootsByNamespace[namespace] {
		c.queue.Add(queuedWork{Operation: "root", UID: uid})
	}
	return nil
}

func (c *Runtime) proofLocked(ref discovery.ObjectRef) (objectProof, bool) {
	proof, exists := c.proofs[refKey(ref)]
	fact, observed := c.reader.objects[refKey(ref)]
	if !exists || !observed || proof.ref != ref || fact.observation.Ref != ref ||
		proof.run != fact.observation.Run || !c.options.Clock().Before(proof.expiresAt) {
		return objectProof{}, false
	}
	stream := c.reader.streams[fact.observation.WatchKey]
	if stream == nil || stream.run != proof.run || !stream.ready || stream.err != nil || proof.key != stream.failureKey {
		return objectProof{}, false
	}
	return proof, true
}

func (c *Runtime) evidenceLocked(refs []discovery.ObjectRef) (store.Evidence, bool) {
	evidence := store.Evidence{}
	for _, ref := range refs {
		proof, exists := c.proofLocked(ref)
		if !exists {
			return store.Evidence{}, false
		}
		evidence.ExpiresAt = earlier(evidence.ExpiresAt, proof.expiresAt)
		if !slices.Contains(evidence.Dependencies, proof.key) {
			evidence.Dependencies = append(evidence.Dependencies, proof.key)
		}
	}
	return evidence, len(refs) != 0
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func (c *Runtime) updateHealthLocked() {
	records := make([]store.HealthRecord, 0, len(c.reader.streams)+len(c.reader.mappingErrors))
	for _, stream := range c.reader.streams {
		reason := store.ReasonNone
		healthy := stream.ready && stream.err == nil && !stream.verifiedAt.IsZero()
		if !healthy {
			reason = runtimeFailureReason(stream.err)
		}
		records = append(records, store.HealthRecord{
			Scope: "resource_type", Ref: store.WorkloadRef{Namespace: stream.key.Namespace,
				Group: stream.gvk.Group, Version: stream.gvk.Version, Kind: stream.gvk.Kind},
			Healthy: healthy, Reason: reason, VerifiedAt: stream.verifiedAt, MaxAge: c.options.MaxAge,
		})
	}
	for request, err := range c.reader.mappingErrors {
		if request.Namespace != "" && !c.namespaces[request.Namespace] {
			delete(c.reader.mappingErrors, request)
			continue
		}
		records = append(records, store.HealthRecord{
			Scope: "resource_type", Ref: gvkRef(request.GVK, request.Namespace, ""),
			Reason: runtimeFailureReason(err),
		})
	}
	c.store.SetPublicationHealth(records)
}

func gvkRef(gvk schema.GroupVersionKind, namespace, name string) store.WorkloadRef {
	return store.WorkloadRef{Namespace: namespace, Name: name, Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}
}
