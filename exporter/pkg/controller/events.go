// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"reflect"

	otelattribute "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/attribute"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/collector"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
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

	c.registry.Set(karta)
	c.reconcileWatchers()
	if rootKind := karta.Spec.StructureDefinition.RootComponent.Kind; rootKind != nil {
		c.rebuildGroupKind(schema.GroupKind{Group: rootKind.Group, Kind: rootKind.Kind})
	}
	c.dropUnservedWorkloads()
	c.markEvent()
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

	c.registry.Remove(u.GetName())
	c.reconcileWatchers()
	c.dropUnservedWorkloads()
	group, _, _ := unstructured.NestedString(u.Object, "spec", "structureDefinition", "rootComponent", "kind", "group")
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "structureDefinition", "rootComponent", "kind", "kind")
	if kind != "" {
		c.rebuildGroupKind(schema.GroupKind{Group: group, Kind: kind})
	}
	c.markEvent()
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

// dropUnservedWorkloads removes workload records whose group and kind no
// longer has a chosen Karta, so their series disappear at the next scrape.
func (c *Controller) dropUnservedWorkloads() {
	for _, record := range c.store.Workloads() {
		groupKind := schema.GroupKind{Group: record.Ref.Group, Kind: record.Ref.Kind}
		if !c.registry.IsRoot(groupKind) {
			c.store.DeleteWorkload(record.UID)
		}
	}
}

func (c *Controller) onWorkloadEvent(obj any) {
	c.processWorkload(obj, false)
	c.markEvent()
}

func (c *Controller) processWorkload(obj any, forceReattribute bool) {
	u, ok := asUnstructured(obj)
	if !ok {
		return
	}

	gvk := u.GroupVersionKind()
	entry, ok := c.registry.EntryFor(gvk.GroupKind())
	if !ok {
		return
	}

	drained := c.index.UpsertObject(u.GetUID(), u.GetOwnerReferences())

	ref := store.WorkloadRef{
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		Group:     gvk.Group,
		Version:   gvk.Version,
		Kind:      gvk.Kind,
	}

	previous, hadPrevious := c.store.Workload(u.GetUID())

	record, err := state.Build(context.Background(), entry, u, u.GetUID(), ref)
	record.Generation = u.GetGeneration()
	record.EvaluationFailed = err != nil
	if err != nil {
		c.attributionErrors.Add(context.Background(), 1, metric.WithAttributes(otelattribute.String(collector.LabelReason, collector.ReasonStatusEval)))
		c.logger.Warn("workload state evaluation failed", "workload", ref.Namespace+"/"+ref.Name, "kind", ref.Kind, "error", err)
	}
	c.store.UpsertWorkload(record)

	for _, podKey := range drained {
		c.attributePodByKey(podKey)
	}

	if forceReattribute || (hadPrevious && instanceSetChanged(previous.Components, record.Components)) {
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

	c.store.DeleteWorkload(u.GetUID())
	c.index.DeleteObject(u.GetUID())
	c.markEvent()
}

func (c *Controller) onChildEvent(obj any) {
	objectMeta, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return
	}

	drained := c.index.UpsertObject(objectMeta.UID, objectMeta.OwnerReferences)
	for _, podKey := range drained {
		c.attributePodByKey(podKey)
	}
	c.markEvent()
}

func (c *Controller) onChildDelete(obj any) {
	objectMeta, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
			objectMeta, ok = tombstone.Obj.(*metav1.PartialObjectMetadata)
		}
	}
	if !ok {
		return
	}
	c.index.DeleteObject(objectMeta.UID)
}

func (c *Controller) onPodEvent(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	c.attributePod(pod)
	c.markEvent()
}

// onPodUpdate recomputes attribution only when identity inputs changed.
// A phase-only change updates the stored phase without re-running selectors.
func (c *Controller) onPodUpdate(oldObj, newObj any) {
	oldPod, oldOK := oldObj.(*corev1.Pod)
	newPod, newOK := newObj.(*corev1.Pod)
	if !oldOK || !newOK {
		return
	}

	identityChanged := !reflect.DeepEqual(oldPod.Labels, newPod.Labels) ||
		!reflect.DeepEqual(oldPod.Annotations, newPod.Annotations) ||
		!reflect.DeepEqual(oldPod.OwnerReferences, newPod.OwnerReferences) ||
		(c.options.FullPodCache && oldPod.ResourceVersion != newPod.ResourceVersion)

	if identityChanged {
		c.attributePod(newPod)
		c.markEvent()
		return
	}

	if record, ok := c.store.Pod(newPod.UID); ok {
		if record.Phase != newPod.Status.Phase {
			record.Phase = newPod.Status.Phase
			c.store.UpsertPod(record)
			c.markEvent()
		}
		return
	}

	c.attributePod(newPod)
	c.markEvent()
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

	c.store.DeletePod(pod.UID)
	c.index.DeleteObject(pod.UID)
	c.index.ForgetPending(pod.Namespace + "/" + pod.Name)
	c.markEvent()
}

// retryPendingPods closes the gap between an unsuccessful owner lookup and
// parking the Pod: the owner event can arrive between those operations.
func (c *Controller) retryPendingPods() {
	for _, key := range c.index.DrainPendingPods() {
		c.attributePodByKey(key)
	}
}

func (c *Controller) attributePodByKey(podKey string) {
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

	waiting := c.index.UpsertObject(pod.UID, pod.OwnerReferences)
	defer func() {
		for _, waitingKey := range waiting {
			if waitingKey != podKey {
				c.attributePodByKey(waitingKey)
			}
		}
	}()

	result := c.index.RootFor(pod.OwnerReferences, c.registry.IsRoot)
	switch result.Outcome {
	case owner.OutcomeFound:
	case owner.OutcomeMissing:
		c.store.DeletePod(pod.UID)
		c.index.MarkPending(result.Missing, podKey)
		return
	case owner.OutcomeDepthExceeded:
		c.attributionErrors.Add(context.Background(), 1, metric.WithAttributes(otelattribute.String(collector.LabelReason, collector.ReasonNoOwner)))
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
		c.index.MarkPending(result.Root.UID, podKey)
		return
	}

	item, exists, err := workloadStore.GetByKey(pod.Namespace + "/" + result.Root.Name)
	if err != nil || !exists {
		c.index.MarkPending(result.Root.UID, podKey)
		return
	}
	workload, ok := item.(*unstructured.Unstructured)
	if !ok || workload.GetUID() != result.Root.UID {
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
			c.attributionErrors.Add(context.Background(), 1, metric.WithAttributes(otelattribute.String(collector.LabelReason, collector.ReasonJQError)))
		} else {
			instanceIDs = computed
		}
	}

	attribution := attribute.Attribute(context.Background(), pod, entry, instanceIDs)
	if attribution.Reason != "" {
		c.attributionErrors.Add(context.Background(), 1, metric.WithAttributes(otelattribute.String(collector.LabelReason, attribution.Reason)))
	}

	workloadGVK := workload.GroupVersionKind()
	c.store.UpsertPod(store.PodRecord{
		UID:         pod.UID,
		Namespace:   pod.Namespace,
		Name:        pod.Name,
		WorkloadUID: workload.GetUID(),
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
