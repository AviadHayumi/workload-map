// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"hash/crc32"
	"reflect"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/attribute"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/state"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func (c *Controller) onKartaEvent(obj any) {
	u, ok := asUnstructured(obj)
	if !ok {
		return
	}

	karta := &v1alpha1.Karta{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, karta); err != nil {
		c.logger.Error("failed to decode karta", "name", u.GetName(), "error", err)
		return
	}

	c.mu.Lock()
	previousEntries := c.registry.Entries()
	c.registry.Set(karta)
	c.mu.Unlock()
	c.reconcileDefinitions(previousEntries)
}

func (c *Controller) onKartaDelete(obj any) {
	u, ok := asUnstructured(obj)
	if !ok {
		if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
			if inner, innerOK := asUnstructured(tombstone.Obj); innerOK {
				u = inner
				ok = true
			}
		}
	}
	if !ok {
		return
	}

	c.mu.Lock()
	previous := c.registry.Entries()
	c.registry.Remove(u.GetName())
	c.mu.Unlock()
	c.reconcileDefinitions(previous)
}

// rebuildGroupKind reprocesses the cached workloads of one kind with forced
// pod re-attribution, so a live Karta update never leaves stale attribution.
func (c *Controller) rebuildGroupKind(groupKind schema.GroupKind) {
	workloadStore := c.rootWatcherStore(groupKind)
	if workloadStore == nil {
		return
	}
	for _, item := range workloadStore.List() {
		c.processWorkload(item, true)
	}
}

// Rebuild only definition selections that changed, including their descendants.
func (c *Controller) reconcileDefinitions(previous []*registry.Entry) {
	before := map[schema.GroupKind]*registry.Entry{}
	after := map[schema.GroupKind]*registry.Entry{}
	for _, entry := range previous {
		before[entry.RootGVK.GroupKind()] = entry
	}
	for _, entry := range c.registry.Entries() {
		after[entry.RootGVK.GroupKind()] = entry
	}
	changed := map[schema.GroupKind]bool{}
	for groupKind, entry := range before {
		if next := after[groupKind]; next == nil || entry.Catalog != next.Catalog || !reflect.DeepEqual(entry.Karta, next.Karta) {
			changed[groupKind] = true
		}
	}
	for groupKind := range after {
		if before[groupKind] == nil {
			changed[groupKind] = true
		}
	}
	c.reconcileWatchers()
	for groupKind := range changed {
		entry := after[groupKind]
		for _, record := range c.store.WorkloadsOfKind(groupKind.Group, groupKind.Kind) {
			if entry == nil || entry.RootGVK.Version != record.Ref.Version {
				c.discardObsoleteWorkload(owner.Ref{UID: record.UID, Namespace: record.Ref.Namespace, Name: record.Ref.Name, GVK: schema.GroupVersionKind{Group: record.Ref.Group, Version: record.Ref.Version, Kind: record.Ref.Kind}})
			}
		}
		c.rebuildGroupKind(groupKind)
	}
}

func (c *Controller) onWorkloadEvent(obj any) {
	c.processWorkload(obj, false)
}

func (c *Controller) onWorkloadUpdate(oldObj, newObj any) {
	old, oldOK := asUnstructured(oldObj)
	next, newOK := asUnstructured(newObj)
	if oldOK && newOK && old.GetUID() != next.GetUID() {
		c.onWorkloadDelete(old)
	}
	c.onWorkloadEvent(newObj)
}

func (c *Controller) processWorkload(obj any, forceReattribute bool) {
	u, ok := asUnstructured(obj)
	if !ok {
		return
	}

	gvk := u.GroupVersionKind()
	var changed, reattributePods bool
	var drained []string
	var missingGVK schema.GroupVersionKind
	updated := func() bool {
		// Version aliases share a lifetime, so all served versions use one stripe.
		lock := c.publicationLock(u.GroupVersionKind(), u.GetNamespace(), u.GetName())
		lock.Lock()
		defer lock.Unlock()
		entry, selected := c.registry.EntryFor(gvk.GroupKind())
		_, deleted := c.deletedWorkloads.Load(u.GetUID())
		if deleted || !selected || entry.RootGVK != gvk {
			return false
		}
		c.mu.Lock()
		watched := c.watchers[gvk]
		c.mu.Unlock()
		if watched == nil || !watched.isRoot {
			return false
		}
		cached := watched.informer.GetStore()
		item, exists, err := cached.GetByKey(u.GetNamespace() + "/" + u.GetName())
		current, valid := asUnstructured(item)
		if err != nil || !exists || !valid || current.GetUID() != u.GetUID() || current.GroupVersionKind() != gvk {
			return false
		}
		u = current

		ref := store.WorkloadRef{
			Namespace: u.GetNamespace(),
			Name:      u.GetName(),
			Group:     gvk.Group,
			Version:   gvk.Version,
			Kind:      gvk.Kind,
		}

		previous, hadPrevious := c.store.Workload(u.GetUID())

		record, evaluationErr := state.Build(context.Background(), entry, u, u.GetUID(), ref)
		record.KartaSource = "cluster"
		if entry.Catalog {
			record.KartaSource = "catalog"
		}
		// Definition mutation and watcher teardown share this lock with the final
		// publication check. Evaluation above stays outside the global lock.
		c.mu.Lock()
		defer c.mu.Unlock()
		selectedEntry, selected := c.registry.EntryFor(gvk.GroupKind())
		if !selected || selectedEntry.RootGVK != gvk || c.watchers[gvk] != watched ||
			(selectedEntry != entry && (selectedEntry.Catalog != entry.Catalog || !reflect.DeepEqual(selectedEntry.Karta, entry.Karta))) {
			return false
		}
		item, exists, err = cached.GetByKey(u.GetNamespace() + "/" + u.GetName())
		current, valid = asUnstructured(item)
		if err != nil || !exists || !valid || current.GetUID() != u.GetUID() ||
			current.GroupVersionKind() != gvk || current.GetResourceVersion() != u.GetResourceVersion() {
			return false
		}
		changed, drained = c.index.UpsertTrackedObject(u, gvk)
		result := c.index.Resolve(u.GetOwnerReferences(), c.registry.IsRoot, c.isIgnored, &owner.Ref{UID: u.GetUID(), Namespace: u.GetNamespace(), Name: u.GetName(), GVK: gvk})
		if result.Outcome == owner.OutcomeFound && result.Root.UID == u.GetUID() {
			record.OwnerResolution = result.Resolution
		}
		if result.Outcome == owner.OutcomeMissing || result.Outcome == owner.OutcomeDepthExceeded {
			record.OwnerFailure = store.ReasonUnresolvedOwner
		}
		record.Generation = u.GetGeneration()
		record.EvaluationFailed = evaluationErr != nil
		if evaluationErr != nil {
			c.logger.Warn("workload state evaluation failed", "workload", ref.Namespace+"/"+ref.Name, "kind", ref.Kind, "error", evaluationErr)
		}
		c.store.UpsertWorkload(record)
		if result.Outcome == owner.OutcomeMissing || result.Outcome == owner.OutcomeDepthExceeded {
			c.store.InvalidateWorkload(record.UID, store.ReasonUnresolvedOwner)
			c.store.InvalidateNamespace(record.Ref.Namespace, store.ReasonUnresolvedOwner)
			missingGVK = result.MissingRef.GVK
			c.pendingWorkloads.Store(record.UID, owner.Ref{UID: record.UID, Namespace: ref.Namespace, Name: ref.Name, GVK: gvk})
		} else {
			c.pendingWorkloads.Delete(record.UID)
		}

		reattributePods = forceReattribute || (hadPrevious && (previous.OwnerResolution != record.OwnerResolution || instanceSetChanged(previous.Components, record.Components)))
		return true
	}()
	if !updated {
		return
	}
	c.observeOwner(missingGVK)

	for _, podKey := range drained {
		c.attributePodByKey(podKey)
	}

	if changed || forceReattribute {
		c.reattributeDescendants(u.GetUID())
	}
	if reattributePods {
		for _, podRecord := range c.store.PodsOfWorkload(u.GetUID()) {
			c.attributePodByKey(podRecord.Namespace + "/" + podRecord.Name)
		}
	}
}

func (c *Controller) onWorkloadDelete(obj any) {
	u, ok := asUnstructured(obj)
	if !ok {
		if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
			if inner, innerOK := asUnstructured(tombstone.Obj); innerOK {
				u = inner
				ok = true
			}
		}
	}
	if !ok {
		return
	}

	lock := c.publicationLock(u.GroupVersionKind(), u.GetNamespace(), u.GetName())
	lock.Lock()
	c.rememberOwnerDeletion(owner.Ref{UID: u.GetUID(), Namespace: u.GetNamespace(), Name: u.GetName(), GVK: u.GroupVersionKind()})
	c.store.DeleteWorkload(u.GetUID())
	c.pendingWorkloads.Delete(u.GetUID())
	c.index.DeleteObject(u.GetUID())
	lock.Unlock()
	c.reattributeDescendants(u.GetUID())
}

func (c *Controller) onChildEvent(obj any, watchedKinds ...schema.GroupVersionKind) {
	objectMeta, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return
	}

	gvk := objectMeta.GroupVersionKind()
	if len(watchedKinds) > 0 {
		gvk = watchedKinds[0]
	}
	lock := c.publicationLock(gvk, objectMeta.Namespace, objectMeta.Name)
	lock.Lock()
	if _, deleted := c.deletedWorkloads.Load(objectMeta.UID); deleted {
		lock.Unlock()
		return
	}
	c.mu.Lock()
	watched := c.watchers[gvk]
	c.mu.Unlock()
	if watched == nil {
		lock.Unlock()
		return
	}
	item, exists, err := watched.informer.GetStore().GetByKey(objectMeta.Namespace + "/" + objectMeta.Name)
	current, valid := item.(*metav1.PartialObjectMetadata)
	if err != nil || !exists || !valid || current.UID != objectMeta.UID {
		lock.Unlock()
		return
	}
	c.mu.Lock()
	if c.watchers[gvk] != watched {
		c.mu.Unlock()
		lock.Unlock()
		return
	}
	item, exists, err = watched.informer.GetStore().GetByKey(objectMeta.Namespace + "/" + objectMeta.Name)
	current, valid = item.(*metav1.PartialObjectMetadata)
	if err != nil || !exists || !valid || current.UID != objectMeta.UID {
		c.mu.Unlock()
		lock.Unlock()
		return
	}
	objectMeta = current
	changed, drained := c.index.UpsertTrackedObject(objectMeta, gvk)
	c.mu.Unlock()
	lock.Unlock()
	if changed {
		c.reattributeDescendants(objectMeta.UID)
	}
	for _, podKey := range drained {
		c.attributePodByKey(podKey)
	}
}

func (c *Controller) onChildUpdate(oldObj, newObj any, gvk schema.GroupVersionKind) {
	old, oldOK := oldObj.(*metav1.PartialObjectMetadata)
	next, newOK := newObj.(*metav1.PartialObjectMetadata)
	if oldOK && newOK && old.UID != next.UID {
		c.onChildDelete(old, gvk)
	}
	c.onChildEvent(newObj, gvk)
}

func (c *Controller) onChildDelete(obj any, watchedKinds ...schema.GroupVersionKind) {
	objectMeta, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
			objectMeta, ok = tombstone.Obj.(*metav1.PartialObjectMetadata)
		}
	}
	if !ok {
		return
	}
	gvk := objectMeta.GroupVersionKind()
	if len(watchedKinds) > 0 {
		gvk = watchedKinds[0]
	}
	lock := c.publicationLock(gvk, objectMeta.Namespace, objectMeta.Name)
	lock.Lock()
	c.rememberOwnerDeletion(owner.Ref{UID: objectMeta.UID, Namespace: objectMeta.Namespace, Name: objectMeta.Name, GVK: gvk})
	c.store.DeleteWorkload(objectMeta.UID)
	c.pendingWorkloads.Delete(objectMeta.UID)
	c.index.DeleteObject(objectMeta.UID)
	lock.Unlock()
	c.reattributeDescendants(objectMeta.UID)
}

func (c *Controller) onPodEvent(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	c.attributePod(pod)
}

// onPodUpdate recomputes attribution when a selector input may have changed.
func (c *Controller) onPodUpdate(oldObj, newObj any) {
	oldPod, oldOK := oldObj.(*corev1.Pod)
	newPod, newOK := newObj.(*corev1.Pod)
	if !oldOK || !newOK {
		return
	}

	if oldPod.UID != newPod.UID {
		c.onPodDelete(oldPod)
		c.attributePod(newPod)
		return
	}

	identityChanged := !reflect.DeepEqual(oldPod.Labels, newPod.Labels) ||
		!reflect.DeepEqual(oldPod.Annotations, newPod.Annotations) ||
		!reflect.DeepEqual(oldPod.OwnerReferences, newPod.OwnerReferences) ||
		oldPod.Spec.NodeName != newPod.Spec.NodeName ||
		oldPod.Status.Phase != newPod.Status.Phase ||
		(c.options.FullPodCache && oldPod.ResourceVersion != newPod.ResourceVersion)

	if identityChanged {
		c.attributePod(newPod)
		return
	}

	if _, ok := c.store.Pod(newPod.UID); ok {
		return
	}

	c.attributePod(newPod)
}

func (c *Controller) onPodDelete(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
			pod, ok = tombstone.Obj.(*corev1.Pod)
		}
	}
	if !ok {
		return
	}

	podKey := pod.Namespace + "/" + pod.Name
	lock := &c.podLocks[crc32.ChecksumIEEE([]byte(podKey))%uint32(len(c.podLocks))]
	lock.Lock()
	c.rememberOwnerDeletion(owner.Ref{UID: pod.UID, Namespace: pod.Namespace, Name: pod.Name, GVK: podGVK})
	c.store.DeletePod(pod.UID)
	c.store.DeleteWorkload(pod.UID)
	c.pendingWorkloads.Delete(pod.UID)
	c.index.DeleteObject(pod.UID)
	item, exists, _ := c.podInformer.GetStore().GetByKey(podKey)
	current, ok := item.(*corev1.Pod)
	if !exists || !ok || current.UID == pod.UID {
		c.index.ForgetPending(podKey)
	}
	lock.Unlock()
	c.reattributeDescendants(pod.UID)
}

// retryPendingPods closes the gap between an unsuccessful owner lookup and
// parking the Pod: the owner event can arrive between those operations.
func (c *Controller) retryPendingPods() {
	for _, key := range c.index.DrainPendingPods() {
		c.attributePodByKey(key)
	}
}

func (c *Controller) attributePodByKey(podKey string) {
	if c.podInformer == nil {
		return
	}
	item, exists, err := c.podInformer.GetStore().GetByKey(podKey)
	if err != nil || !exists {
		return
	}
	pod, ok := item.(*corev1.Pod)
	if !ok {
		return
	}
	c.attributePod(pod)
}

// attributePod walks the pod's owner chain to a Karta-described root and
// stores the attribution. The pod registers its own owner edge first: pods
// can be middle owners (a LeaderWorkerSet worker StatefulSet is owned by
// its leader pod), and other pods may be parked waiting for this one.
func (c *Controller) attributePod(pod *corev1.Pod) {
	podKey := pod.Namespace + "/" + pod.Name
	changed, waiting := func() (bool, []string) {
		// Fixed stripes bound synchronization memory. Descendant callbacks run
		// after unlocking so hash collisions cannot create recursive deadlocks.
		lock := &c.podLocks[crc32.ChecksumIEEE([]byte(podKey))%uint32(len(c.podLocks))]
		lock.Lock()
		defer lock.Unlock()
		item, exists, err := c.podInformer.GetStore().GetByKey(podKey)
		current, ok := item.(*corev1.Pod)
		_, deleted := c.deletedWorkloads.Load(pod.UID)
		if deleted || err != nil || !exists || !ok || current.UID != pod.UID {
			return false, nil
		}
		pod = current
		c.index.ForgetPending(podKey)
		changed, waiting := c.index.UpsertTrackedObject(pod, podGVK)
		c.attributeTrackedPod(pod, podKey)
		return changed, waiting
	}()
	if changed {
		c.reattributeDescendants(pod.UID)
	}
	for _, waitingKey := range waiting {
		if waitingKey != podKey {
			c.attributePodByKey(waitingKey)
		}
	}
}

func (c *Controller) attributeTrackedPod(pod *corev1.Pod, podKey string) {
	var candidate *owner.Ref
	if c.registry.IsRoot(podGVK.GroupKind()) {
		candidate = &owner.Ref{UID: pod.UID, Namespace: pod.Namespace, Name: pod.Name, GVK: podGVK}
	}
	result := c.index.Resolve(pod.OwnerReferences, c.registry.IsRoot, c.isIgnored, candidate, pod.Namespace)
	switch result.Outcome {
	case owner.OutcomeFound:
	case owner.OutcomeMissing:
		c.observeOwner(result.MissingRef.GVK)
		c.store.InvalidateNamespace(pod.Namespace, store.ReasonUnresolvedOwner)
		c.store.DeletePod(pod.UID)
		c.index.MarkPending(result.Missing, podKey)
		return
	case owner.OutcomeDepthExceeded:
		c.store.InvalidateNamespace(pod.Namespace, store.ReasonUnresolvedOwner)
		c.store.DeletePod(pod.UID)
		return
	default:
		c.store.DeletePod(pod.UID)
		return
	}

	groupKind := result.Root.GVK.GroupKind()
	entry, ok := c.registry.EntryFor(groupKind)
	if !ok {
		c.store.DeletePod(pod.UID)
		return
	}

	workloadStore := c.rootWatcherStore(groupKind)
	if workloadStore == nil {
		c.store.DeletePod(pod.UID)
		c.index.MarkPending(result.Root.UID, podKey)
		return
	}

	item, exists, err := workloadStore.GetByKey(pod.Namespace + "/" + result.Root.Name)
	if err != nil || !exists {
		c.store.DeletePod(pod.UID)
		c.index.MarkPending(result.Root.UID, podKey)
		return
	}
	workload, ok := item.(*unstructured.Unstructured)
	if !ok || workload.GetUID() != result.Root.UID {
		c.store.DeletePod(pod.UID)
		c.index.MarkPending(result.Root.UID, podKey)
		return
	}

	instanceIDs := map[string][]string{}
	if record, ok := c.store.Workload(workload.GetUID()); ok {
		for _, componentState := range record.Components {
			instanceIDs[componentState.Component] = append(instanceIDs[componentState.Component], componentState.Instance)
		}
	} else {
		computed, err := attribute.InstanceIDs(context.Background(), entry, workload)
		if err != nil {
			c.logger.Warn("component instance evaluation failed", "workload", workload.GetName(), "error", err)
			c.store.InvalidateWorkload(workload.GetUID(), store.ReasonEvaluationFailed)
		} else {
			instanceIDs = computed
		}
	}

	attribution := attribute.Attribute(context.Background(), pod, entry, instanceIDs)
	if attribution.Reason != "" {
		c.logger.Warn("pod attribution failed", "namespace", pod.Namespace, "pod", pod.Name, "reason", attribution.Reason)
	}

	workloadGVK := workload.GroupVersionKind()
	c.store.UpsertPod(store.PodRecord{
		OwnerResolution: result.Resolution,
		UID:             pod.UID,
		Namespace:       pod.Namespace,
		Name:            pod.Name,
		WorkloadUID:     workload.GetUID(),
		Workload: store.WorkloadRef{
			Namespace: workload.GetNamespace(),
			Name:      workload.GetName(),
			Group:     workloadGVK.Group,
			Version:   workloadGVK.Version,
			Kind:      workloadGVK.Kind,
		},
		Component: attribution.Component,
		Instance:  attribution.Instance,
		Replica:   attribution.Replica,
		Reason:    attribution.Reason,
		Phase:     pod.Status.Phase,
	})
}

func instanceSetChanged(previous, current []store.ComponentState) bool {
	if len(previous) != len(current) {
		return true
	}
	seen := make(map[string]struct{}, len(previous))
	for _, componentState := range previous {
		seen[componentState.Component+"\x00"+componentState.Instance] = struct{}{}
	}
	for _, componentState := range current {
		if _, ok := seen[componentState.Component+"\x00"+componentState.Instance]; !ok {
			return true
		}
	}
	return false
}

func asUnstructured(obj any) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok
}

// Re-evaluate only cached descendants of the changed owner edge.
func (c *Controller) reattributeDescendants(uid types.UID) {
	for _, ref := range c.index.Descendants(uid) {
		if ref.GVK == podGVK {
			c.attributePodByKey(ref.Namespace + "/" + ref.Name)
			continue
		}
		cached := c.rootWatcherStore(ref.GVK.GroupKind())
		if cached == nil {
			continue
		}
		obj, exists, err := cached.GetByKey(ref.Namespace + "/" + ref.Name)
		if err == nil && exists {
			c.processWorkload(obj, false)
		}
	}
}

// A parent event can race the child failure publication, just as it can race
// Pod parking. Retry unresolved candidates without scanning healthy workloads.
func (c *Controller) retryPendingWorkloads() {
	c.retireObservedWorkloadDeletions()
	c.pendingWorkloads.Range(func(key, value any) bool {
		ref := value.(owner.Ref)
		entry, selected := c.registry.EntryFor(ref.GVK.GroupKind())
		if !selected || entry.RootGVK != ref.GVK {
			c.discardObsoleteWorkload(ref)
			return true
		}
		cached := c.rootWatcherStore(ref.GVK.GroupKind())
		if cached == nil {
			return true
		}
		item, exists, err := cached.GetByKey(ref.Namespace + "/" + ref.Name)
		if err != nil || !exists {
			c.pendingWorkloads.CompareAndDelete(key, ref)
			return true
		}
		obj, ok := item.(*unstructured.Unstructured)
		if !ok || obj.GetUID() != ref.UID {
			c.pendingWorkloads.CompareAndDelete(key, ref)
			return true
		}
		result := c.index.Resolve(obj.GetOwnerReferences(), c.registry.IsRoot, c.isIgnored, &ref)
		if result.Outcome != owner.OutcomeMissing && result.Outcome != owner.OutcomeDepthExceeded {
			c.processWorkload(obj, false)
		}
		return true
	})
}

// Definition cleanup uses the publication stripe so an already-running old
// selection cannot restore its record after reconciliation has removed it.
func (c *Controller) discardObsoleteWorkload(ref owner.Ref) {
	lock := c.publicationLock(ref.GVK, ref.Namespace, ref.Name)
	lock.Lock()
	entry, selected := c.registry.EntryFor(ref.GVK.GroupKind())
	if selected && entry.RootGVK == ref.GVK {
		lock.Unlock()
		return
	}
	c.pendingWorkloads.CompareAndDelete(ref.UID, ref)
	removed := false
	if current, exists := c.store.Workload(ref.UID); exists && (!selected || current.Ref.Version == ref.GVK.Version) {
		c.store.DeleteWorkload(ref.UID)
		removed = true
	}
	lock.Unlock()
	if removed {
		c.reattributeDescendants(ref.UID)
	}
}

// Markers exist only while an alias informer can still return the deleted UID.
// A synced absence or replacement proves that its later callbacks cannot restore
// that lifetime. Cleanup scans outstanding deletions, not all live workloads.
func (c *Controller) retireObservedWorkloadDeletions() {
	c.deletedWorkloads.Range(func(key, value any) bool {
		ref := value.(owner.Ref)
		lock := c.publicationLock(ref.GVK, ref.Namespace, ref.Name)
		lock.Lock()
		defer lock.Unlock()
		c.mu.Lock()
		var observers []cache.SharedIndexInformer
		for gvk, candidate := range c.watchers {
			if gvk.GroupKind() == ref.GVK.GroupKind() {
				observers = append(observers, candidate.informer)
			}
		}
		c.mu.Unlock()
		if ref.GVK == podGVK {
			observers = append(observers, c.podInformer)
		}
		for _, informer := range observers {
			if !informer.HasSynced() {
				return true
			}
			item, exists, err := informer.GetStore().GetByKey(ref.Namespace + "/" + ref.Name)
			current, valid := item.(metav1.Object)
			if err != nil || (exists && (!valid || current.GetUID() == ref.UID)) {
				return true
			}
		}
		c.deletedWorkloads.CompareAndDelete(key, value)
		return true
	})
}

// Called while the object's publication stripe is held. Ordinary single-version
// deletion needs no marker because informer callbacks are ordered.
func (c *Controller) rememberOwnerDeletion(ref owner.Ref) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for gvk := range c.watchers {
		if (ref.GVK == podGVK && gvk == podGVK) || (gvk.GroupKind() == ref.GVK.GroupKind() && gvk != ref.GVK) {
			c.deletedWorkloads.Store(ref.UID, ref)
			return
		}
	}
}

func (c *Controller) publicationLock(gvk schema.GroupVersionKind, namespace, name string) *sync.Mutex {
	if gvk == podGVK {
		key := namespace + "/" + name
		return &c.podLocks[crc32.ChecksumIEEE([]byte(key))%uint32(len(c.podLocks))]
	}
	key := gvk.GroupKind().String() + "/" + namespace + "/" + name
	return &c.workloadLocks[crc32.ChecksumIEEE([]byte(key))%uint32(len(c.workloadLocks))]
}
