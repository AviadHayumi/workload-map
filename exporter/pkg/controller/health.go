// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/attribute"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/state"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

var (
	podGVK   = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	kartaGVK = schema.GroupVersionKind{Group: "run.ai", Version: "v1alpha1", Kind: "Karta"}
)

type healthCollection struct {
	record store.HealthRecord
	items  []unstructured.Unstructured
}

func (c *Controller) kindHealth(gvk schema.GroupVersionKind) store.HealthRecord {
	namespace := c.options.Namespace
	if namespace == "" {
		namespace = "*"
	}
	if gvk == kartaGVK {
		namespace = ""
	}
	return store.HealthRecord{Scope: "kind", Ref: store.WorkloadRef{Namespace: namespace, Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}, MaxAge: c.options.HealthMaxAge}
}

func (c *Controller) initializeHealth() {
	rows := []store.HealthRecord{c.kindHealth(podGVK), c.kindHealth(kartaGVK)}
	for _, gvk := range c.options.RequiredKinds {
		if gvk != podGVK && gvk != kartaGVK {
			rows = append(rows, c.kindHealth(gvk))
		}
	}
	c.store.SetHealth(rows, c.store.Snapshot().HealthEpoch)
}

// verifyHealth uses uncached reads, then checks the observed cache and exported
// attribution. Informer resync and quiet watches cannot establish freshness.
func (c *Controller) verifyHealth(parent context.Context) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, c.options.HealthInterval)
	defer cancel()
	before := c.store.Snapshot()
	required := map[schema.GroupVersionKind]bool{podGVK: true, kartaGVK: true}
	for _, gvk := range c.options.RequiredKinds {
		required[gvk] = true
	}
	previousKinds := make(map[schema.GroupVersionKind]bool)
	// A previously observed, still selected catalog kind remains required if its API disappears.
	for _, h := range before.Health {
		if h.Scope == "kind" {
			previousKinds[schema.GroupVersionKind{Group: h.Ref.Group, Version: h.Ref.Version, Kind: h.Ref.Kind}] = true
		}
	}
	entries := map[schema.GroupKind]*registry.Entry{}
	for _, entry := range c.registry.Entries() {
		_, err := c.mapper.RESTMapping(entry.RootGVK.GroupKind(), entry.RootGVK.Version)
		if entry.Catalog && meta.IsNoMatchError(err) && !required[entry.RootGVK] && !previousKinds[entry.RootGVK] {
			continue
		}
		entries[entry.RootGVK.GroupKind()] = entry
		required[entry.RootGVK] = true
		for _, child := range entry.ChildKinds {
			_, err := c.mapper.RESTMapping(child.GroupKind(), child.Version)
			// Definitions can cover several deployment modes. An API that has
			// never existed is optional unless configuration explicitly requires it.
			if meta.IsNoMatchError(err) && !required[child] && !previousKinds[child] {
				continue
			}
			required[child] = true
		}
	}
	collections := make(map[schema.GroupVersionKind]healthCollection, len(required))
	objects := make(map[types.UID]metav1.Object)
	roots := make(map[types.UID]*unstructured.Unstructured)
	for gvk := range required {
		h := c.kindHealth(gvk)
		h.VerifiedAt = started
		mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			collections[gvk] = healthCollection{record: h}
			continue
		}
		namespace := c.options.Namespace
		if mapping.Scope.Name() == meta.RESTScopeNameRoot {
			namespace = ""
			h.Ref.Namespace = ""
		}
		list, err := c.dynamicClient.Resource(mapping.Resource).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			c.logger.Warn("health verification list failed", "gvk", gvk.String(), "error", err)
			collections[gvk] = healthCollection{record: h}
			continue
		}
		var informer cache.SharedIndexInformer
		switch gvk {
		case podGVK:
			informer = c.podInformer
		case kartaGVK:
			informer = c.kartaInformer
		default:
			c.mu.Lock()
			if w := c.watchers[gvk]; w != nil {
				informer = w.informer
			}
			c.mu.Unlock()
		}
		h.Healthy = informer != nil && informer.HasSynced() && cacheMatches(informer.GetStore(), list.Items)
		if gvk == kartaGVK && c.registry.Stats().Invalid > 0 {
			h.Healthy = false
		}
		collections[gvk] = healthCollection{record: h, items: list.Items}
		for i := range list.Items {
			obj := &list.Items[i]
			obj.SetGroupVersionKind(gvk)
			objects[obj.GetUID()] = obj
			if entry := entries[gvk.GroupKind()]; entry != nil && entry.RootGVK == gvk {
				roots[obj.GetUID()] = obj
			}
		}
	}
	snapshot := c.store.Snapshot()
	workloads := make(map[types.UID]store.WorkloadRecord, len(snapshot.Workloads))
	health := make(map[types.UID]store.HealthRecord, len(snapshot.Workloads))
	for _, record := range snapshot.Workloads {
		workloads[record.UID] = record
		h := store.HealthRecord{Scope: "workload", Ref: record.Ref, UID: record.UID, VerifiedAt: started, MaxAge: c.options.HealthMaxAge}
		entry := entries[schema.GroupKind{Group: record.Ref.Group, Kind: record.Ref.Kind}]
		h.Dependencies = []store.WorkloadRef{record.Ref, {Kind: "Pod"}, {Group: "run.ai", Kind: "Karta"}}
		if entry != nil {
			for _, dep := range entry.ChildKinds {
				h.Dependencies = append(h.Dependencies, store.WorkloadRef{Group: dep.Group, Kind: dep.Kind})
			}
		}
		obj := roots[record.UID]
		if entry != nil && obj != nil {
			expected, err := state.Build(ctx, entry, obj, record.UID, record.Ref)
			expected.Generation = obj.GetGeneration()
			h.Healthy = err == nil && !record.EvaluationFailed && sameWorkload(expected, record) && collections[entry.RootGVK].record.Healthy && collections[podGVK].record.Healthy && collections[kartaGVK].record.Healthy
			for _, dep := range entry.ChildKinds {
				if collection, required := collections[dep]; required {
					h.Healthy = h.Healthy && collection.record.Healthy
				}
			}
		}
		health[record.UID] = h
	}
	actualPods := make(map[types.UID]store.PodRecord, len(snapshot.Pods))
	for _, p := range snapshot.Pods {
		actualPods[p.UID] = p
	}
	seen := make(map[types.UID]bool)
	for i := range collections[podGVK].items {
		obj := &collections[podGVK].items[i]
		uid, err := c.verifyRoot(ctx, obj, objects, entries)
		if err != nil {
			// If the root cannot be determined, no workload can claim complete Pod coverage.
			c.logger.Warn("health owner verification failed", "namespace", obj.GetNamespace(), "pod", obj.GetName(), "error", err)
			for k, h := range health {
				if h.Ref.Namespace != obj.GetNamespace() {
					continue
				}
				h.Healthy = false
				health[k] = h
			}
			continue
		}
		if uid == "" {
			continue
		}
		seen[obj.GetUID()] = true
		h, known := health[uid]
		if !known {
			continue
		}
		record := workloads[uid]
		ids := map[string][]string{}
		for _, component := range record.Components {
			ids[component.Component] = append(ids[component.Component], component.Instance)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: obj.GetName(), Namespace: obj.GetNamespace(), UID: obj.GetUID(), Labels: obj.GetLabels(), Annotations: obj.GetAnnotations(), OwnerReferences: obj.GetOwnerReferences()}}
		// Honor full Pod selectors in verification as in normal attribution.
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, pod); err != nil {
			h.Healthy = false
			health[uid] = h
			continue
		}
		expected := attribute.Attribute(ctx, pod, entries[schema.GroupKind{Group: record.Ref.Group, Kind: record.Ref.Kind}], ids)
		actual, exists := actualPods[obj.GetUID()]
		if !exists || actual.WorkloadUID != uid || actual.Reason != "" || expected.Reason != "" || actual.Component != expected.Component || actual.Instance != expected.Instance || actual.Replica != expected.Replica || actual.Phase != pod.Status.Phase {
			h.Healthy = false
		}
		health[uid] = h
	}
	for _, pod := range snapshot.Pods {
		if !seen[pod.UID] {
			h := health[pod.WorkloadUID]
			h.Healthy = false
			health[pod.WorkloadUID] = h
		}
	}
	rows := make([]store.HealthRecord, 0, len(collections)+len(health))
	for _, collection := range collections {
		rows = append(rows, collection.record)
	}
	for _, h := range health {
		rows = append(rows, h)
	}
	c.store.SetHealth(rows, before.HealthEpoch)
}

func cacheMatches(cached cache.Store, fresh []unstructured.Unstructured) bool {
	items := cached.List()
	if len(items) != len(fresh) {
		return false
	}
	versions := make(map[types.UID]string, len(items))
	for _, item := range items {
		obj, err := meta.Accessor(item)
		if err != nil {
			return false
		}
		versions[obj.GetUID()] = obj.GetResourceVersion()
	}
	for _, item := range fresh {
		rv, exists := versions[item.GetUID()]
		if !exists || rv != item.GetResourceVersion() {
			return false
		}
	}
	return true
}

// verifyRoot follows missing intermediate owners independently of the watch
// index. This detects incomplete definitions without silently fixing attribution.
func (c *Controller) verifyRoot(ctx context.Context, obj metav1.Object, objects map[types.UID]metav1.Object, entries map[schema.GroupKind]*registry.Entry) (types.UID, error) {
	var root types.UID
	for depth := 0; depth < 8; depth++ {
		ref := metav1.GetControllerOf(obj)
		if ref == nil {
			return root, nil
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return "", err
		}
		gvk := gv.WithKind(ref.Kind)
		if entries[gvk.GroupKind()] != nil {
			root = ref.UID
		}
		next := objects[ref.UID]
		if next == nil {
			mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return "", err
			}
			namespace := obj.GetNamespace()
			if mapping.Scope.Name() == meta.RESTScopeNameRoot {
				namespace = ""
			}
			fetched, err := c.metadataClient.Resource(mapping.Resource).Namespace(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			if fetched.GetUID() != ref.UID {
				return "", nil
			}
			next = fetched
			objects[ref.UID] = next
		}
		obj = next
	}
	return "", fmt.Errorf("owner chain exceeds verification depth")
}

// Selectors return sets whose iteration order has no metric meaning.
func sameWorkload(a, b store.WorkloadRecord) bool {
	a.Phases = slices.Clone(a.Phases)
	b.Phases = slices.Clone(b.Phases)
	slices.Sort(a.Phases)
	slices.Sort(b.Phases)
	a.Components = slices.Clone(a.Components)
	b.Components = slices.Clone(b.Components)
	compare := func(a, b store.ComponentState) int {
		if n := cmp.Compare(a.Component, b.Component); n != 0 {
			return n
		}
		return cmp.Compare(a.Instance, b.Instance)
	}
	slices.SortFunc(a.Components, compare)
	slices.SortFunc(b.Components, compare)
	return reflect.DeepEqual(a, b)
}
