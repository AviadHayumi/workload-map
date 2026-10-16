// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"cmp"
	"context"
	"errors"
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
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/state"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

var (
	podGVK             = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	kartaGVK           = schema.GroupVersionKind{Group: "run.ai", Version: "v1alpha1", Kind: "Karta"}
	errUnresolvedOwner = errors.New("owner missing or identity mismatched")
)

// Verification retains metadata and evaluated records, never full LIST responses.
// Pages and fallback GETs are bounded independently of cluster size.
const verificationPageSize = 1000
const verificationReadLimit = 1024

type ownerVerifier struct {
	controller      *Controller
	objects         map[types.UID]metav1.Object
	entries         map[schema.GroupKind]*registry.Entry
	failures        map[types.UID]error
	reads           int
	dependencies    []store.WorkloadRef
	cacheMismatches map[types.UID]bool
	cacheMismatch   bool
}

func (c *Controller) kindHealth(gvk schema.GroupVersionKind) store.HealthRecord {
	namespace := c.options.Namespace
	if gvk == kartaGVK {
		namespace = ""
	}
	return store.HealthRecord{Scope: "resource_type", Ref: store.WorkloadRef{Namespace: namespace, Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}, Reason: store.ReasonAPIUnavailable, MaxAge: c.options.HealthMaxAge}
}

func (c *Controller) initializeHealth() {
	rows := []store.HealthRecord{c.kindHealth(podGVK), c.kindHealth(kartaGVK)}
	for _, gvk := range append(append([]schema.GroupVersionKind(nil), c.options.RequiredKinds...), c.options.IdentityKinds...) {
		if gvk != podGVK && gvk != kartaGVK {
			rows = append(rows, c.kindHealth(gvk))
		}
	}
	c.store.SetHealth(rows, c.store.HealthEpoch())
}

func failureReason(err error) string {
	switch {
	case err == nil:
		return store.ReasonNone
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return store.ReasonPermissionDenied
	case errors.Is(err, errUnresolvedOwner):
		return store.ReasonUnresolvedOwner
	case apierrors.IsNotFound(err):
		var status apierrors.APIStatus
		if errors.As(err, &status) && status.Status().Details != nil && status.Status().Details.Name != "" {
			return store.ReasonUnresolvedOwner
		}
		return store.ReasonAPIUnavailable
	default:
		return store.ReasonAPIUnavailable
	}
}

func collectionFailureReason(err error) string {
	reason := failureReason(err)
	if reason == store.ReasonUnresolvedOwner {
		return store.ReasonAPIUnavailable
	}
	return reason
}

func (c *Controller) verificationInformer(gvk schema.GroupVersionKind) cache.SharedIndexInformer {
	switch gvk {
	case podGVK:
		return c.podInformer
	case kartaGVK:
		return c.kartaInformer
	default:
		c.mu.Lock()
		defer c.mu.Unlock()
		if watcher := c.watchers[gvk]; watcher != nil {
			return watcher.informer
		}
		return nil
	}
}

func (c *Controller) visitFresh(ctx context.Context, gvk schema.GroupVersionKind, visit func(*unstructured.Unstructured)) error {
	mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return err
	}
	namespace := c.options.Namespace
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		namespace = ""
	}
	continuation := ""
	for {
		list, err := c.dynamicClient.Resource(mapping.Resource).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: verificationPageSize, Continue: continuation})
		if err != nil {
			return err
		}
		for n := range list.Items {
			list.Items[n].SetGroupVersionKind(gvk)
			visit(&list.Items[n])
		}
		if list.GetContinue() == "" {
			return nil
		}
		if list.GetContinue() == continuation {
			return fmt.Errorf("API repeated pagination token")
		}
		continuation = list.GetContinue()
	}
}

// Pods need only owner metadata in the first pass unless they are selected roots.
// Full Pod objects are independently read and evaluated in the second pass.
func (c *Controller) visitPodMetadata(ctx context.Context, visit func(metav1.Object)) error {
	resource := c.metadataClient.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(c.options.Namespace)
	continuation := ""
	for {
		list, err := resource.List(ctx, metav1.ListOptions{Limit: verificationPageSize, Continue: continuation})
		if err != nil {
			return err
		}
		for n := range list.Items {
			list.Items[n].SetGroupVersionKind(podGVK)
			visit(&list.Items[n])
		}
		if list.GetContinue() == "" {
			return nil
		}
		if list.GetContinue() == continuation {
			return fmt.Errorf("API repeated pagination token")
		}
		continuation = list.GetContinue()
	}
}

// verifyHealth uses direct reads. A watch event or scrape cannot recover health.
func (c *Controller) verifyHealth(parent context.Context) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, c.options.HealthMaxAge/2)
	defer cancel()
	snapshot := c.store.Snapshot()
	required := map[schema.GroupVersionKind]bool{podGVK: true, kartaGVK: true}
	explicit := map[schema.GroupVersionKind]bool{}
	for _, gvk := range append(append([]schema.GroupVersionKind(nil), c.options.RequiredKinds...), c.options.IdentityKinds...) {
		required[gvk] = true
		explicit[gvk] = true
	}
	c.mu.Lock()
	for gvk := range c.discoveredOwners {
		if selected, ok := c.registry.EntryFor(gvk.GroupKind()); ok && selected.RootGVK != gvk {
			continue
		}
		required[gvk] = true
	}
	c.mu.Unlock()
	previous := map[schema.GroupVersionKind]bool{}
	for _, h := range snapshot.Health {
		if h.Scope == "resource_type" {
			previous[schema.GroupVersionKind{Group: h.Ref.Group, Version: h.Ref.Version, Kind: h.Ref.Kind}] = true
		}
	}
	entries := map[schema.GroupKind]*registry.Entry{}
	for _, entry := range c.registry.Entries() {
		_, err := c.mapper.RESTMapping(entry.RootGVK.GroupKind(), entry.RootGVK.Version)
		if entry.Catalog && meta.IsNoMatchError(err) && !required[entry.RootGVK] && !previous[entry.RootGVK] {
			continue
		}
		entries[entry.RootGVK.GroupKind()] = entry
		required[entry.RootGVK] = true
		for _, child := range entry.ChildKinds {
			if c.isIgnored(child) && !explicit[child] {
				continue
			}
			_, err := c.mapper.RESTMapping(child.GroupKind(), child.Version)
			if meta.IsNoMatchError(err) && !required[child] && !previous[child] {
				continue
			}
			required[child] = true
		}
	}
	invalid := map[schema.GroupKind]bool{}
	for _, gvk := range c.registry.InvalidKinds() {
		invalid[gvk.GroupKind()] = true
		required[gvk] = true
	}
	collections := map[schema.GroupVersionKind]store.HealthRecord{}
	collectionFailures := map[schema.GroupVersionKind]string{}
	definitionMismatches := map[schema.GroupKind]bool{}
	markDefinitionMismatch := func(obj metav1.Object) {
		if kind, ok := definitionGroupKind(obj); ok {
			definitionMismatches[kind] = true
			return
		}
		// An invalid definition cannot select a new root kind, but may replace
		// a previously selected definition whose kind is still known locally.
		for kind, entry := range entries {
			if entry.Karta.Name == obj.GetName() {
				definitionMismatches[kind] = true
			}
		}
	}
	verifier := ownerVerifier{controller: c, objects: map[types.UID]metav1.Object{}, entries: entries, failures: map[types.UID]error{}, cacheMismatches: map[types.UID]bool{}}
	expected := map[types.UID]store.WorkloadRecord{}
	evaluationFailed := map[types.UID]bool{}
	var cachedOnlyPods []metav1.Object
	for gvk := range required {
		h := c.kindHealth(gvk)
		h.VerifiedAt = started
		informer := c.verificationInformer(gvk)
		synced := informer != nil && informer.HasSynced()
		matched := synced
		cachedObjects := map[types.UID]metav1.Object{}
		if informer != nil {
			for _, item := range informer.GetStore().List() {
				obj, err := meta.Accessor(item)
				if err != nil {
					synced = false
					matched = false
					continue
				}
				cachedObjects[obj.GetUID()] = obj
			}
		}
		visit := func(obj metav1.Object) {
			verifier.objects[obj.GetUID()] = &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind}, ObjectMeta: metav1.ObjectMeta{Name: obj.GetName(), Namespace: obj.GetNamespace(), UID: obj.GetUID(), ResourceVersion: obj.GetResourceVersion(), OwnerReferences: obj.GetOwnerReferences()}}
			cached, exists := cachedObjects[obj.GetUID()]
			if !exists || cached.GetNamespace() != obj.GetNamespace() || cached.GetName() != obj.GetName() || cached.GetResourceVersion() != obj.GetResourceVersion() {
				matched = false
				verifier.cacheMismatches[obj.GetUID()] = true
				if gvk == kartaGVK {
					for _, definition := range []metav1.Object{obj, cached} {
						if definition == nil {
							continue
						}
						markDefinitionMismatch(definition)
					}
				}
			}
			if c.options.Identity != nil && gvk != kartaGVK {
				identityRecord, observed := snapshot.Identities[obj.GetUID()]
				if !observed || identityRecord.InputHash != identity.Fingerprint(obj, gvk) {
					matched = false
					verifier.cacheMismatches[obj.GetUID()] = true
				}
			}
			delete(cachedObjects, obj.GetUID())
			if entry := entries[gvk.GroupKind()]; entry != nil && entry.RootGVK == gvk {
				ref := store.WorkloadRef{Namespace: obj.GetNamespace(), Name: obj.GetName(), Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}
				record, evalErr := state.Build(ctx, entry, obj.(*unstructured.Unstructured), obj.GetUID(), ref)
				record.Generation = obj.GetGeneration()
				record.KartaSource = "cluster"
				if entry.Catalog {
					record.KartaSource = "catalog"
				}
				expected[obj.GetUID()] = record
				evaluationFailed[obj.GetUID()] = evalErr != nil
			}
		}
		var err error
		if gvk == podGVK && entries[podGVK.GroupKind()] == nil {
			err = c.visitPodMetadata(ctx, visit)
		} else {
			err = c.visitFresh(ctx, gvk, func(obj *unstructured.Unstructured) { visit(obj) })
		}
		if len(cachedObjects) != 0 {
			matched = false
			for uid, obj := range cachedObjects {
				verifier.cacheMismatches[uid] = true
				if gvk == podGVK {
					cachedOnlyPods = append(cachedOnlyPods, obj)
				}
				if gvk == kartaGVK {
					markDefinitionMismatch(obj)
				}
			}
		}
		h.Healthy = err == nil && matched
		if h.Healthy {
			h.Reason = store.ReasonNone
		} else if err != nil {
			h.Reason = collectionFailureReason(err)
			c.logger.Warn("health collection failed", "gvk", gvk.String(), "error", err)
		}
		if invalid[gvk.GroupKind()] {
			h.Healthy = false
			h.Reason = store.ReasonInvalidDefinition
		}
		collections[gvk] = h
		// A completed collection with local cache lag does not make unrelated
		// observations unreadable. Definition mismatches are scoped by root kind.
		if err != nil || !synced || invalid[gvk.GroupKind()] {
			collectionFailures[gvk] = h.Reason
		}
	}
	workloads := map[types.UID]store.WorkloadRecord{}
	health := map[types.UID]store.HealthRecord{}
	namespaceFailures := map[string]string{}
	for _, record := range snapshot.Workloads {
		workloads[record.UID] = record
		h := store.HealthRecord{Scope: "workload", Ref: record.Ref, UID: record.UID, VerifiedAt: started, MaxAge: c.options.HealthMaxAge, Healthy: true, Reason: store.ReasonNone}
		entry := entries[schema.GroupKind{Group: record.Ref.Group, Kind: record.Ref.Kind}]
		h.Dependencies = []store.WorkloadRef{record.Ref, {Kind: "Pod"}, {Group: "run.ai", Kind: "Karta"}}
		fresh, exists := expected[record.UID]
		obj := verifier.objects[record.UID]
		if !exists || obj == nil || entry == nil {
			h.Healthy = false
			h.Reason = store.ReasonAPIUnavailable
		} else {
			root, resolution, err := verifier.root(ctx, obj, record.UID)
			h.Dependencies = appendDependencies(h.Dependencies, verifier.dependencies)
			if verifier.cacheMismatch {
				h.Healthy = false
				h.Reason = store.ReasonAPIUnavailable
			}
			if err != nil {
				h.Healthy = false
				h.Reason = failureReason(err)
				fresh.OwnerFailure = h.Reason
				namespaceFailures[record.Ref.Namespace] = h.Reason
				c.logger.Warn("workload owner verification failed", "namespace", record.Ref.Namespace, "workload", record.Ref.Name, "error", err)
			} else if root == record.UID {
				fresh.OwnerResolution = resolution
			} else {
				continue
			}
			if !sameWorkload(fresh, record) || evaluationFailed[record.UID] || record.EvaluationFailed {
				h.Healthy = false
				if h.Reason == store.ReasonNone {
					h.Reason = store.ReasonEvaluationFailed
				}
			}
		}
		dependencies := []schema.GroupVersionKind{podGVK, kartaGVK, {Group: record.Ref.Group, Version: record.Ref.Version, Kind: record.Ref.Kind}}
		if entry != nil {
			for _, dep := range entry.ChildKinds {
				if c.isIgnored(dep) && !explicit[dep] {
					continue
				}
				dependencies = append(dependencies, dep)
				h.Dependencies = append(h.Dependencies, store.WorkloadRef{Group: dep.Group, Version: dep.Version, Kind: dep.Kind})
			}
		}
		for _, dep := range dependencies {
			if reason := collectionFailures[dep]; reason != "" {
				h.Healthy = false
				h.Reason = reason
			}
		}
		if definitionMismatches[schema.GroupKind{Group: record.Ref.Group, Kind: record.Ref.Kind}] {
			h.Healthy = false
			h.Reason = store.ReasonAPIUnavailable
		}
		if invalid[schema.GroupKind{Group: record.Ref.Group, Kind: record.Ref.Kind}] {
			h.Healthy = false
			h.Reason = store.ReasonInvalidDefinition
		}
		health[record.UID] = h
	}
	actualPods := make(map[types.UID]*store.PodRecord, len(snapshot.Pods))
	for n := range snapshot.Pods {
		pod := &snapshot.Pods[n]
		actualPods[pod.UID] = pod
	}
	for _, pod := range cachedOnlyPods {
		var candidate types.UID
		if entries[podGVK.GroupKind()] != nil {
			candidate = pod.GetUID()
		}
		uid, _, err := verifier.root(ctx, pod, candidate)
		if err != nil {
			namespaceFailures[pod.GetNamespace()] = failureReason(err)
			continue
		}
		if h, exists := health[uid]; exists {
			h.Healthy = false
			h.Reason = store.ReasonAPIUnavailable
			health[uid] = h
		}
	}
	seen := map[types.UID]bool{}
	err := c.visitFresh(ctx, podGVK, func(obj *unstructured.Unstructured) {
		var candidate types.UID
		if entries[podGVK.GroupKind()] != nil {
			candidate = obj.GetUID()
		}
		uid, resolution, err := verifier.root(ctx, obj, candidate)
		if err != nil {
			namespaceFailures[obj.GetNamespace()] = failureReason(err)
			c.logger.Warn("pod owner verification failed", "namespace", obj.GetNamespace(), "pod", obj.GetName(), "error", err)
			return
		}
		actual, exists := actualPods[obj.GetUID()]
		if exists && actual.WorkloadUID != uid {
			if old, known := health[actual.WorkloadUID]; known {
				old.Healthy = false
				old.Reason = store.ReasonAPIUnavailable
				health[actual.WorkloadUID] = old
			}
		}
		if uid == "" {
			return
		}
		seen[obj.GetUID()] = true
		h, known := health[uid]
		if !known {
			return
		}
		h.Dependencies = appendDependencies(h.Dependencies, verifier.dependencies)
		if verifier.cacheMismatch {
			h.Healthy = false
			h.Reason = store.ReasonAPIUnavailable
		}
		record := workloads[uid]
		ids := map[string][]string{}
		for _, component := range record.Components {
			ids[component.Component] = append(ids[component.Component], component.Instance)
		}
		pod := &corev1.Pod{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, pod); err != nil {
			h.Healthy = false
			h.Reason = store.ReasonEvaluationFailed
			health[uid] = h
			return
		}
		entry := entries[schema.GroupKind{Group: record.Ref.Group, Kind: record.Ref.Kind}]
		if entry == nil {
			h.Healthy = false
			h.Reason = store.ReasonInvalidDefinition
			health[uid] = h
			return
		}
		attribution := attribute.Attribute(ctx, pod, entry, ids)
		first := verifier.objects[obj.GetUID()]
		if first == nil || first.GetResourceVersion() != obj.GetResourceVersion() || !exists || actual.WorkloadUID != uid || actual.Workload != record.Ref || actual.OwnerResolution != resolution || actual.Reason != "" || attribution.Reason != "" || actual.Component != attribution.Component || actual.Instance != attribution.Instance || actual.Replica != attribution.Replica || actual.Phase != pod.Status.Phase {
			h.Healthy = false
			if h.Reason == store.ReasonNone {
				h.Reason = store.ReasonEvaluationFailed
			}
		}
		health[uid] = h
	})
	if err != nil {
		h := collections[podGVK]
		h.Healthy = false
		h.Reason = collectionFailureReason(err)
		collections[podGVK] = h
		for uid, row := range health {
			row.Healthy = false
			row.Reason = h.Reason
			health[uid] = row
		}
	}
	for _, pod := range snapshot.Pods {
		if !seen[pod.UID] {
			if h, exists := health[pod.WorkloadUID]; exists {
				h.Healthy = false
				if h.Reason == store.ReasonNone {
					h.Reason = store.ReasonEvaluationFailed
				}
				health[pod.WorkloadUID] = h
			}
		}
	}
	rows := make([]store.HealthRecord, 0, len(collections)+len(health))
	for _, h := range collections {
		rows = append(rows, h)
	}
	for _, h := range health {
		if reason := namespaceFailures[h.Ref.Namespace]; reason != "" {
			h.Healthy = false
			h.Reason = reason
		}
		rows = append(rows, h)
	}
	c.store.SetHealth(rows, snapshot.HealthEpoch)
}

func definitionGroupKind(obj metav1.Object) (schema.GroupKind, bool) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return schema.GroupKind{}, false
	}
	kind, found, err := unstructured.NestedString(raw, "spec", "structureDefinition", "rootComponent", "kind", "kind")
	if err != nil || !found || kind == "" {
		return schema.GroupKind{}, false
	}
	group, _, err := unstructured.NestedString(raw, "spec", "structureDefinition", "rootComponent", "kind", "group")
	return schema.GroupKind{Group: group, Kind: kind}, err == nil
}

func cacheMatches(cached cache.Store, fresh []unstructured.Unstructured) bool {
	if len(cached.ListKeys()) != len(fresh) {
		return false
	}
	for _, obj := range fresh {
		key, err := cache.MetaNamespaceKeyFunc(&obj)
		if err != nil {
			return false
		}
		item, exists, err := cached.GetByKey(key)
		if err != nil || !exists {
			return false
		}
		current, err := meta.Accessor(item)
		if err != nil || current.GetUID() != obj.GetUID() || current.GetResourceVersion() != obj.GetResourceVersion() {
			return false
		}
	}
	return true
}

func (v *ownerVerifier) root(ctx context.Context, obj metav1.Object, candidate types.UID) (types.UID, string, error) {
	v.dependencies = nil
	v.cacheMismatch = v.cacheMismatches[obj.GetUID()]
	root := candidate
	namespace := obj.GetNamespace()
	for depth := 0; ; depth++ {
		ref := metav1.GetControllerOf(obj)
		if ref == nil {
			return root, "resolved", nil
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return "", "", fmt.Errorf("%w: malformed owner GVK", errUnresolvedOwner)
		}
		gvk := gv.WithKind(ref.Kind)
		if v.controller.isIgnored(gvk) {
			return root, "ignored_owner", nil
		}
		v.dependencies = append(v.dependencies, store.WorkloadRef{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind})
		if depth == 8 {
			return "", "", fmt.Errorf("%w: owner chain exceeds eight steps", errUnresolvedOwner)
		}
		if failed := v.failures[ref.UID]; failed != nil {
			return "", "", failed
		}
		next := v.objects[ref.UID]
		if next == nil {
			if v.reads >= verificationReadLimit {
				return "", "", fmt.Errorf("owner verification read budget exhausted")
			}
			v.reads++
			mapping, err := v.controller.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				v.failures[ref.UID] = err
				return "", "", err
			}
			ownerNamespace := namespace
			if mapping.Scope.Name() == meta.RESTScopeNameRoot {
				ownerNamespace = ""
			} else if namespace == "" {
				return "", "", fmt.Errorf("%w: cluster-scoped dependent has a namespaced owner", errUnresolvedOwner)
			}
			fetched, err := v.controller.metadataClient.Resource(mapping.Resource).Namespace(ownerNamespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				v.failures[ref.UID] = err
				return "", "", err
			}
			// Metadata responses identify the meta.k8s.io envelope; the mapped
			// request resource supplies the actual object kind.
			fetched.SetGroupVersionKind(gvk)
			next = fetched
			v.objects[ref.UID] = next
		}
		nextObject, hasKind := next.(runtime.Object)
		if next.GetUID() != ref.UID || next.GetName() != ref.Name || (next.GetNamespace() != "" && next.GetNamespace() != namespace) || !hasKind || nextObject.GetObjectKind().GroupVersionKind().GroupKind() != gvk.GroupKind() {
			return "", "", fmt.Errorf("%w: %s/%s %s", errUnresolvedOwner, namespace, ref.Name, gvk.String())
		}
		if v.cacheMismatches[next.GetUID()] {
			v.cacheMismatch = true
		}
		if v.entries[gvk.GroupKind()] != nil {
			root = ref.UID
		}
		namespace = next.GetNamespace()
		obj = next
	}
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

func appendDependencies(existing, added []store.WorkloadRef) []store.WorkloadRef {
	for _, dep := range added {
		found := false
		for _, current := range existing {
			if current.Group == dep.Group && current.Kind == dep.Kind {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, dep)
		}
	}
	return existing
}
