// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"container/list"
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/itchyny/gojq"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
)

type attributionCache struct {
	mu                          sync.Mutex
	entries                     map[attributionKey]*list.Element
	recency                     list.List
	maxWorkloads, maxPods, pods int
}

type attributionKey struct {
	root *indexRevision
	gvk  schema.GroupVersionKind
	plan *attribution.Plan
}

type cachedAttribution struct {
	key        attributionKey
	revisions  []*indexRevision
	generation uint64
	data       *derivedAttribution
}

// Only derived data belongs here. Object bodies, typed Pods, contexts and
// observations belong to each newly acquired Workload.
type derivedAttribution struct {
	definition  *v1alpha1.Karta
	prepared    *attribution.Prepared
	memberships []attribution.Membership
	components  map[string]v1alpha1.ComponentDefinition
	paths       map[string][]string
	postings    map[postingKey][]int
}

func newAttributionCache(options CacheOptions) *attributionCache {
	return &attributionCache{entries: make(map[attributionKey]*list.Element), maxWorkloads: options.MaxWorkloads, maxPods: options.MaxPods}
}

func (r *Resolver) cachedWorkload(ctx context.Context, snapshot *indexedWorkload, plan *attribution.Plan) (*Workload, error) {
	if len(snapshot.pods) > r.cache.maxPods {
		return buildWorkload(ctx, snapshot.object, snapshot.pods, plan, snapshot.observation)
	}
	key := attributionKey{root: snapshot.rootRevision, gvk: snapshot.object.GroupVersionKind(), plan: plan}
	data, err := r.cache.get(ctx, key, snapshot.podRevisions)
	if err != nil {
		return nil, err
	}
	if data != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &Workload{object: snapshot.object, pods: snapshot.pods, observation: snapshot.observation,
			definition: data.definition, prepared: data.prepared, memberships: data.memberships,
			components: data.components, paths: data.paths, postings: data.postings}, nil
	}
	w, err := buildWorkload(ctx, snapshot.object, snapshot.pods, plan, snapshot.observation)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(w.issues) != 0 {
		return w, nil
	}
	// No Pods need use a component for its preparation to have failed.
	if w.prepared != nil {
		for name := range w.components {
			if _, err := w.prepared.InstanceIDs(name); err != nil {
				return w, nil
			}
		}
	}
	data = &derivedAttribution{definition: w.definition, prepared: w.prepared, memberships: w.memberships,
		components: w.components, paths: w.paths, postings: w.postings}
	if err := r.cache.put(ctx, key, snapshot.podRevisions, snapshot.observation.Generation, data); err != nil {
		return nil, err
	}
	return w, nil
}

func (c *attributionCache) get(ctx context.Context, key attributionKey, revisions []*indexRevision) (*derivedAttribution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry := c.entries[key]
	if entry == nil {
		return nil, nil
	}
	cached := entry.Value.(*cachedAttribution)
	if !slices.Equal(cached.revisions, revisions) {
		return nil, nil
	}
	c.recency.MoveToFront(entry)
	return cached.data, nil
}

func (c *attributionCache) put(ctx context.Context, key attributionKey, revisions []*indexRevision, generation uint64, data *derivedAttribution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(revisions) > c.maxPods {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if old := c.entries[key]; old != nil {
		cached := old.Value.(*cachedAttribution)
		if cached.generation > generation {
			return nil
		}
		c.remove(old)
	}
	for len(c.entries) >= c.maxWorkloads || c.pods > c.maxPods-len(revisions) {
		c.remove(c.recency.Back())
	}
	entry := &cachedAttribution{key: key, revisions: slices.Clone(revisions), generation: generation, data: data}
	c.entries[key] = c.recency.PushFront(entry)
	c.pods += len(revisions)
	return nil
}

func (c *attributionCache) remove(entry *list.Element) {
	cached := entry.Value.(*cachedAttribution)
	delete(c.entries, cached.key)
	c.pods -= len(cached.revisions)
	c.recency.Remove(entry)
}

func cacheableAttribution(definition *v1alpha1.Karta) bool {
	if definition == nil {
		return false
	}
	components := append([]v1alpha1.ComponentDefinition{definition.Spec.StructureDefinition.RootComponent}, definition.Spec.StructureDefinition.ChildComponents...)
	for _, component := range components {
		if component.InstanceIdPath != nil && !structuralAttributionPath(*component.InstanceIdPath, true) {
			return false
		}
		selector := component.PodSelector
		if selector == nil {
			continue
		}
		if selector.ComponentTypeSelector != nil && !staticAttributionPath(selector.ComponentTypeSelector.KeyPath) {
			return false
		}
		if selector.ComponentInstanceSelector != nil && selector.ComponentInstanceSelector.IdPath != "" && !staticAttributionPath(selector.ComponentInstanceSelector.IdPath) {
			return false
		}
		if selector.ReplicaSelector != nil && selector.ReplicaSelector.KeyPath != "" && !staticAttributionPath(selector.ReplicaSelector.KeyPath) {
			return false
		}
	}
	return true
}

func staticAttributionPath(expression string) bool {
	return structuralAttributionPath(expression, false)
}

func structuralAttributionPath(expression string, allowIteration bool) bool {
	query, err := gojq.Parse(expression)
	if err != nil || query.Meta != nil || len(query.Imports) != 0 || len(query.FuncDefs) != 0 || len(query.Patterns) != 0 || query.Left != nil || query.Right != nil || query.Term == nil {
		return false
	}
	term := query.Term
	if term.Type != gojq.TermTypeIdentity && (term.Type != gojq.TermTypeIndex || !staticAttributionIndex(term.Index)) {
		return false
	}
	for _, suffix := range term.SuffixList {
		if suffix.Optional {
			return false
		}
		if suffix.Iter {
			if !allowIteration || suffix.Index != nil {
				return false
			}
			continue
		}
		if !staticAttributionIndex(suffix.Index) {
			return false
		}
	}
	return true
}

func staticAttributionIndex(index *gojq.Index) bool {
	if index == nil || index.IsSlice || index.End != nil {
		return false
	}
	if index.Name != "" {
		return true
	}
	if index.Str != nil {
		return index.Str.Queries == nil
	}
	query := index.Start
	if query == nil || query.Meta != nil || len(query.Imports) != 0 || len(query.FuncDefs) != 0 || len(query.Patterns) != 0 || query.Left != nil || query.Right != nil || query.Term == nil {
		return false
	}
	term := query.Term
	if len(term.SuffixList) != 0 {
		return false
	}
	if term.Type == gojq.TermTypeString {
		return term.Str != nil && term.Str.Queries == nil
	}
	if term.Type != gojq.TermTypeNumber {
		return false
	}
	_, err := strconv.Atoi(term.Number)
	return err == nil
}
