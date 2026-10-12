// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
)

func TestPendingPodRecoversAfterOwnerEventPasses(t *testing.T) {
	c, _, job := fixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "worker", Namespace: "test", UID: "pod-a",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "example", UID: job.GetUID(), Controller: ptr.To(true)}},
	}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	c.index.DeleteObject(job.GetUID())
	result := c.index.RootFor(pod.OwnerReferences, func(gk schema.GroupKind) bool { return c.registry.IsRoot(gk) })
	// The owner event arrives after lookup but before this Pod is parked.
	c.processWorkload(job, false)
	c.index.MarkPending(result.Missing, "test/worker")
	if _, ok := c.store.Pod(pod.UID); ok {
		t.Fatal("Pod unexpectedly attributed before retry")
	}
	c.retryPendingPods()
	record, ok := c.store.Pod(pod.UID)
	if !ok || record.WorkloadUID != job.GetUID() || record.Component != "job" || record.Reason != "" {
		t.Fatalf("retry did not restore complete attribution: %+v, found=%t", record, ok)
	}
	if c.index.PendingCount() != 0 {
		t.Fatal("recovered Pod still parked")
	}
}

func TestPendingPodRemainsPendingUntilOwnerArrives(t *testing.T) {
	c, _, job := fixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "worker", Namespace: "test", UID: "pod-a",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "example", UID: job.GetUID(), Controller: ptr.To(true)}},
	}}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	c.index.DeleteObject(job.GetUID())
	c.attributePod(pod)
	for range 3 {
		c.retryPendingPods()
		if c.index.PendingCount() != 1 {
			t.Fatal("retry lost the pending Pod or duplicated it")
		}
	}
	c.processWorkload(job, false)
	if _, ok := c.store.Pod(pod.UID); !ok || c.index.PendingCount() != 0 {
		t.Fatal("owner arrival did not recover Pod")
	}
}
