// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"fmt"
	"hash/crc32"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
)

type blockedRootStore struct {
	cache.Store
	reached chan struct{}
	release chan struct{}
}
type blockedRootInformer struct {
	cache.SharedIndexInformer
	store cache.Store
}

func (s blockedRootStore) GetByKey(key string) (any, bool, error) {
	close(s.reached)
	<-s.release
	return s.Store.GetByKey(key)
}
func (i blockedRootInformer) GetStore() cache.Store { return i.store }

func TestPodDeletionSerializesWithInFlightAttribution(t *testing.T) {
	c, _, job := fixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "worker", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	watcher := c.watchers[job.GroupVersionKind()]
	reached, release, attributed, deleted := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	watcher.informer = blockedRootInformer{SharedIndexInformer: watcher.informer, store: blockedRootStore{Store: watcher.informer.GetStore(), reached: reached, release: release}}
	go func() { c.attributePodByKey("test/worker"); close(attributed) }()
	<-reached
	if err := c.podInformer.GetStore().Delete(pod); err != nil {
		t.Fatal(err)
	}
	go func() { c.onPodDelete(pod); close(deleted) }()
	close(release)
	<-attributed
	<-deleted
	if _, exists := c.store.Pod(pod.UID); exists {
		t.Fatal("in-flight attribution survived Pod deletion")
	}
	c.attributePod(pod)
	if _, exists := c.store.Pod(pod.UID); exists {
		t.Fatal("stale event resurrected deleted Pod mapping")
	}
}

func TestOldPodEventCannotReplaceNewLifetimeOrPendingWork(t *testing.T) {
	c, client, job := fixture(t)
	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "old", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
	if err := c.podInformer.GetStore().Add(old); err != nil {
		t.Fatal(err)
	}
	c.attributePod(old)
	replacement := old.DeepCopy()
	replacement.UID = "new"
	replacement.OwnerReferences = jobOwner("future", "future")
	if err := c.podInformer.GetStore().Update(replacement); err != nil {
		t.Fatal(err)
	}
	c.attributePod(replacement)
	c.onPodDelete(old)
	c.attributePod(old)
	if _, exists := c.store.Pod(old.UID); exists {
		t.Fatal("old UID resurrected")
	}
	if c.index.PendingCount() != 1 {
		t.Fatal("old delete/event lost new lifetime pending work")
	}
	future := job.DeepCopy()
	future.SetName("future")
	future.SetUID("future")
	putJob(t, c, client, future)
	if actual, ok := c.store.Pod(replacement.UID); !ok || actual.WorkloadUID != future.GetUID() {
		t.Fatalf("replacement failed recovery: %+v", actual)
	}
	c.attributePod(old)
	if actual, ok := c.store.Pod(replacement.UID); !ok || actual.WorkloadUID != future.GetUID() {
		t.Fatal("old event replaced current lifetime")
	}
}

func TestPodStripeCollisionDoesNotDeadlockDescendantRetry(t *testing.T) {
	c, _, job := fixture(t)
	parentName := "parent"
	slot := crc32.ChecksumIEEE([]byte("test/"+parentName)) % uint32(len(c.podLocks))
	childName := ""
	for n := 0; n < 10000; n++ {
		candidate := fmt.Sprintf("child-%d", n)
		if crc32.ChecksumIEEE([]byte("test/"+candidate))%uint32(len(c.podLocks)) == slot {
			childName = candidate
			break
		}
	}
	if childName == "" {
		t.Fatal("could not construct stripe collision")
	}
	parent := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: parentName, Namespace: "test", UID: types.UID(parentName), OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
	child := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: childName, Namespace: "test", UID: types.UID(childName), OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: parentName, UID: parent.UID, Controller: ptr.To(true)}}}}
	for _, pod := range []*corev1.Pod{parent, child} {
		if err := c.podInformer.GetStore().Add(pod); err != nil {
			t.Fatal(err)
		}
	}
	c.attributePod(child)
	done := make(chan struct{})
	go func() { c.attributePod(parent); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stripe collision deadlocked descendant retry")
	}
	if actual, ok := c.store.Pod(child.UID); !ok || actual.WorkloadUID != job.GetUID() {
		t.Fatalf("descendant not recovered: %+v", actual)
	}
}
