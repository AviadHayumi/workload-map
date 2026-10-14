// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"fmt"
	"hash/crc32"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
)

type capturedRootStore struct {
	cache.Store
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func (s *capturedRootStore) GetByKey(key string) (any, bool, error) {
	item, exists, err := s.Store.GetByKey(key)
	s.once.Do(func() {
		close(s.reached)
		<-s.release
	})
	return item, exists, err
}

func TestStaleWorkloadRetryCannotResurrectDeletedRoot(t *testing.T) {
	c, _, job := fixture(t)
	cached := c.rootWatcherStore(job.GroupVersionKind().GroupKind())
	stale, exists, err := cached.GetByKey(job.GetNamespace() + "/" + job.GetName())
	if err != nil || !exists {
		t.Fatalf("cached root: exists=%v err=%v", exists, err)
	}
	if err := cached.Delete(job); err != nil {
		t.Fatal(err)
	}
	c.onWorkloadDelete(job)
	// A retry can already hold this pointer when the deletion handler finishes.
	c.processWorkload(stale, false)
	if _, exists := c.store.Workload(job.GetUID()); exists {
		t.Fatal("stale retry resurrected deleted zero-Pod root")
	}
}

func TestWorkloadDeletionSerializesWithInFlightPublication(t *testing.T) {
	c, _, job := fixture(t)
	watcher := c.watchers[job.GroupVersionKind()]
	cached := watcher.informer.GetStore()
	reached, release, published, deleted := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	watcher.informer = blockedRootInformer{SharedIndexInformer: watcher.informer, store: &capturedRootStore{Store: cached, reached: reached, release: release}}
	go func() { c.processWorkload(job, false); close(published) }()
	<-reached
	if err := cached.Delete(job); err != nil {
		t.Fatal(err)
	}
	go func() { c.onWorkloadDelete(job); close(deleted) }()
	close(release)
	<-published
	<-deleted
	if _, exists := c.store.Workload(job.GetUID()); exists {
		t.Fatal("in-flight publication survived workload deletion")
	}
	if _, pending := c.pendingWorkloads.Load(job.GetUID()); pending {
		t.Fatal("deleted workload retained pending retry")
	}
}

func TestOldWorkloadRetryCannotReplaceNewLifetime(t *testing.T) {
	c, client, old := fixture(t)
	replacement := old.DeepCopy()
	replacement.SetUID("replacement")
	putJob(t, c, client, replacement)
	c.onWorkloadDelete(old)
	c.processWorkload(old, true)
	if _, exists := c.store.Workload(old.GetUID()); exists {
		t.Fatal("stale retry resurrected old workload lifetime")
	}
	if current, exists := c.store.Workload(replacement.GetUID()); !exists || current.OwnerResolution == "" {
		t.Fatal("old retry/delete removed current lifetime")
	}
}

func TestWorkloadStripeCollisionDoesNotDeadlockDescendantRetry(t *testing.T) {
	c, client, parent := fixture(t)
	prefix := parent.GroupVersionKind().GroupKind().String() + "/" + parent.GetNamespace() + "/"
	slot := crc32.ChecksumIEEE([]byte(prefix+parent.GetName())) % uint32(len(c.workloadLocks))
	childName := ""
	for n := 0; n < 10000; n++ {
		candidate := fmt.Sprintf("child-%d", n)
		if crc32.ChecksumIEEE([]byte(prefix+candidate))%uint32(len(c.workloadLocks)) == slot {
			childName = candidate
			break
		}
	}
	if childName == "" {
		t.Fatal("could not construct stripe collision")
	}
	child := parent.DeepCopy()
	child.SetName(childName)
	child.SetUID(types.UID(childName))
	child.SetOwnerReferences(jobOwner(parent.GetName(), parent.GetUID()))
	putJob(t, c, client, child)
	c.index.DeleteObject(parent.GetUID())
	done := make(chan struct{})
	go func() { c.processWorkload(parent, true); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stripe collision deadlocked descendant retry")
	}
	if record, exists := c.store.Workload(child.GetUID()); !exists || record.OwnerResolution != "" || record.OwnerFailure != "" {
		t.Fatalf("child failed to resolve higher root: %+v", record)
	}
}

func TestDefinitionRemovalSerializesWithInFlightWorkload(t *testing.T) {
	c, _, job := fixture(t)
	watcher := c.watchers[job.GroupVersionKind()]
	reached, release, published, discarded := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	watcher.informer = blockedRootInformer{SharedIndexInformer: watcher.informer, store: &capturedRootStore{Store: watcher.informer.GetStore(), reached: reached, release: release}}
	go func() { c.processWorkload(job, false); close(published) }()
	<-reached
	entry, _ := c.registry.EntryFor(job.GroupVersionKind().GroupKind())
	c.registry.Remove(entry.Karta.Name)
	ref := owner.Ref{UID: job.GetUID(), Namespace: job.GetNamespace(), Name: job.GetName(), GVK: job.GroupVersionKind()}
	go func() { c.discardObsoleteWorkload(ref); close(discarded) }()
	close(release)
	<-published
	<-discarded
	if _, exists := c.store.Workload(job.GetUID()); exists {
		t.Fatal("old definition publication survived removal")
	}
}

func TestObsoleteCleanupRechecksRestoredDefinition(t *testing.T) {
	c, _, job := fixture(t)
	ref := owner.Ref{UID: job.GetUID(), Namespace: job.GetNamespace(), Name: job.GetName(), GVK: job.GroupVersionKind()}
	// A retry may have observed a missing definition before it was restored.
	c.pendingWorkloads.Store(job.GetUID(), ref)
	c.discardObsoleteWorkload(ref)
	if _, exists := c.store.Workload(job.GetUID()); !exists {
		t.Fatal("stale cleanup removed the selected workload")
	}
	if _, exists := c.pendingWorkloads.Load(job.GetUID()); !exists {
		t.Fatal("stale cleanup removed current pending work")
	}
}

func TestRelistUIDReplacementRemovesOldLifetimes(t *testing.T) {
	t.Run("workload", func(t *testing.T) {
		c, _, old := fixture(t)
		next := old.DeepCopy()
		next.SetUID("replacement")
		if err := c.rootWatcherStore(old.GroupVersionKind().GroupKind()).Update(next); err != nil {
			t.Fatal(err)
		}
		c.onWorkloadUpdate(old, next)
		if _, exists := c.store.Workload(old.GetUID()); exists {
			t.Fatal("relist retained old workload UID")
		}
		if _, exists := c.store.Workload(next.GetUID()); !exists {
			t.Fatal("relist omitted replacement workload")
		}
	})
	t.Run("pod", func(t *testing.T) {
		c, _, job := fixture(t)
		old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "old", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
		if err := c.podInformer.GetStore().Add(old); err != nil {
			t.Fatal(err)
		}
		c.attributePod(old)
		next := old.DeepCopy()
		next.UID = "replacement"
		if err := c.podInformer.GetStore().Update(next); err != nil {
			t.Fatal(err)
		}
		c.onPodUpdate(old, next)
		if _, exists := c.store.Pod(old.UID); exists {
			t.Fatal("relist retained old Pod UID")
		}
		if _, exists := c.store.Pod(next.UID); !exists {
			t.Fatal("relist omitted replacement Pod")
		}
	})
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("intermediate_alias_%v", alias), func(t *testing.T) {
			c, _, job := fixture(t)
			gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}
			old := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "middle", Namespace: "test", UID: "old", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
			cacheChild(t, c, old, gvk)
			c.onChildEvent(old, gvk)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "pod", OwnerReferences: jobOwner(old.Name, old.UID)}}
			pod.OwnerReferences[0].APIVersion = "apps/v1"
			pod.OwnerReferences[0].Kind = "ReplicaSet"
			if err := c.podInformer.GetStore().Add(pod); err != nil {
				t.Fatal(err)
			}
			c.attributePod(pod)
			if _, exists := c.store.Pod(pod.UID); !exists {
				t.Fatal("initial chain not attributed")
			}
			next := old.DeepCopy()
			next.UID = "replacement"
			if alias {
				aliasGVK := gvk
				aliasGVK.Version = "v2"
				informer := syncedInformer{cache.NewSharedIndexInformer(nil, &metav1.PartialObjectMetadata{}, 0, cache.Indexers{})}
				if err := informer.GetStore().Add(old); err != nil {
					t.Fatal(err)
				}
				c.watchers[aliasGVK] = &watcher{gvk: aliasGVK, informer: informer}
				if err := c.watchers[gvk].informer.GetStore().Delete(old); err != nil {
					t.Fatal(err)
				}
				c.onChildDelete(old, gvk)
				c.onChildEvent(old, aliasGVK)
				if _, exists := c.store.Pod(pod.UID); exists {
					t.Fatal("lagging alias restored deleted owner chain")
				}
				c.retireObservedWorkloadDeletions()
				if _, marked := c.deletedWorkloads.Load(old.UID); !marked {
					t.Fatal("lagging metadata alias lost marker")
				}
				if err := informer.GetStore().Update(next); err != nil {
					t.Fatal(err)
				}
				c.onChildUpdate(old, next, aliasGVK)
				c.retireObservedWorkloadDeletions()
				if _, marked := c.deletedWorkloads.Load(old.UID); marked {
					t.Fatal("replacement metadata UID retained old marker")
				}
				c.onChildEvent(old, aliasGVK)
			} else {
				cacheChild(t, c, next, gvk)
				c.onChildUpdate(old, next, gvk)
			}
			if _, exists := c.store.Pod(pod.UID); exists {
				t.Fatal("relist retained mapping through deleted owner UID")
			}
			if c.index.PendingCount() != 1 {
				t.Fatal("orphaned descendant not waiting for correct owner UID")
			}
		})
	}
}

func cacheChild(t *testing.T, c *Controller, obj *metav1.PartialObjectMetadata, gvk schema.GroupVersionKind) {
	t.Helper()
	if c.watchers[gvk] == nil {
		informer := syncedInformer{cache.NewSharedIndexInformer(nil, &metav1.PartialObjectMetadata{}, 0, cache.Indexers{})}
		c.watchers[gvk] = &watcher{gvk: gvk, informer: informer}
	}
	if err := c.watchers[gvk].informer.GetStore().Update(obj); err != nil {
		t.Fatal(err)
	}
}

func TestPodRootDeletionAcrossDuplicateObservers(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		t.Run(fmt.Sprintf("dedicated_%v", dedicated), func(t *testing.T) {
			c, _, _ := fixture(t)
			definition, err := catalog.Get(podGVK)
			if err != nil {
				t.Fatal(err)
			}
			c.registry.Set(definition)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: "test", UID: "bare"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
			if err != nil {
				t.Fatal(err)
			}
			obj := &unstructured.Unstructured{Object: raw}
			obj.SetGroupVersionKind(podGVK)
			informer := syncedInformer{cache.NewSharedIndexInformer(nil, &unstructured.Unstructured{}, 0, cache.Indexers{})}
			if err := informer.GetStore().Add(obj); err != nil {
				t.Fatal(err)
			}
			c.watchers[podGVK] = &watcher{gvk: podGVK, isRoot: true, informer: informer}
			if err := c.podInformer.GetStore().Add(pod); err != nil {
				t.Fatal(err)
			}
			c.processWorkload(obj, true)
			c.attributePod(pod)
			if _, exists := c.store.Workload(pod.UID); !exists {
				t.Fatal("initial Pod root missing")
			}
			if dedicated {
				if err := c.podInformer.GetStore().Delete(pod); err != nil {
					t.Fatal(err)
				}
				c.onPodDelete(pod)
			} else {
				if err := informer.GetStore().Delete(obj); err != nil {
					t.Fatal(err)
				}
				c.onWorkloadDelete(obj)
			}
			c.processWorkload(obj, true)
			c.attributePod(pod)
			if _, exists := c.store.Workload(pod.UID); exists {
				t.Fatal("lagging observer resurrected Pod root")
			}
			if _, exists := c.store.Pod(pod.UID); exists {
				t.Fatal("lagging observer resurrected Pod mapping")
			}
			c.retireObservedWorkloadDeletions()
			if _, marked := c.deletedWorkloads.Load(pod.UID); !marked {
				t.Fatal("lagging duplicate observer lost marker")
			}
			if err := informer.GetStore().Delete(obj); err != nil {
				t.Fatal(err)
			}
			if err := c.podInformer.GetStore().Delete(pod); err != nil {
				t.Fatal(err)
			}
			c.retireObservedWorkloadDeletions()
			if _, marked := c.deletedWorkloads.Load(pod.UID); marked {
				t.Fatal("converged duplicate observers retained marker")
			}
			c.processWorkload(obj, true)
			c.attributePod(pod)
			if _, exists := c.store.Workload(pod.UID); exists {
				t.Fatal("queued stale callback resurrected Pod root")
			}
		})
	}
}

func TestMetadataAliasDeletionRetiresSelectedRoot(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement_%v", replacement), func(t *testing.T) {
			c, _, job := fixture(t)
			alias := job.GroupVersionKind()
			alias.Version = "v2"
			obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: job.GetName(), Namespace: job.GetNamespace(), UID: job.GetUID()}}
			cacheChild(t, c, obj, alias)
			c.onChildEvent(obj, alias)
			c.pendingWorkloads.Store(job.GetUID(), owner.Ref{UID: job.GetUID(), Namespace: job.GetNamespace(), Name: job.GetName(), GVK: job.GroupVersionKind()})
			if replacement {
				next := obj.DeepCopy()
				next.UID = "replacement"
				cacheChild(t, c, next, alias)
				c.onChildUpdate(obj, next, alias)
			} else {
				if err := c.watchers[alias].informer.GetStore().Delete(obj); err != nil {
					t.Fatal(err)
				}
				c.onChildDelete(obj, alias)
			}
			if _, exists := c.store.Workload(job.GetUID()); exists {
				t.Fatal("metadata alias deletion retained selected root")
			}
			if _, exists := c.pendingWorkloads.Load(job.GetUID()); exists {
				t.Fatal("metadata alias deletion retained pending workload")
			}
			c.processWorkload(job, true)
			if _, exists := c.store.Workload(job.GetUID()); exists {
				t.Fatal("lagging full observer resurrected deleted UID")
			}
		})
	}
}

func TestStoppedMetadataWatcherCannotPublishLateEvent(t *testing.T) {
	c, _, job := fixture(t)
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}
	obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "middle", Namespace: "test", UID: "middle", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
	cacheChild(t, c, obj, gvk)
	watched := c.watchers[gvk]
	watched.stop = make(chan struct{})
	reached, release, published := make(chan struct{}), make(chan struct{}), make(chan struct{})
	watched.informer = blockedRootInformer{SharedIndexInformer: watched.informer, store: &capturedRootStore{Store: watched.informer.GetStore(), reached: reached, release: release}}
	go func() { c.onChildEvent(obj, gvk); close(published) }()
	<-reached
	c.reconcileWatchers()
	close(release)
	<-published
	if c.index.HasOwnerKind(job.GroupVersionKind()) {
		t.Fatal("stopped metadata watcher restored an owner edge")
	}
}

func TestDefinitionRemovalRejectsFirstWorkloadPublication(t *testing.T) {
	c, _, job := fixture(t)
	c.store.DeleteWorkload(job.GetUID())
	c.index.DeleteObject(job.GetUID())
	watched := c.watchers[job.GroupVersionKind()]
	watched.stop = make(chan struct{})
	reached, release, published := make(chan struct{}), make(chan struct{}), make(chan struct{})
	watched.informer = blockedRootInformer{SharedIndexInformer: watched.informer, store: &capturedRootStore{Store: watched.informer.GetStore(), reached: reached, release: release}}
	entry, _ := c.registry.EntryFor(job.GroupVersionKind().GroupKind())
	go func() { c.processWorkload(job, false); close(published) }()
	<-reached
	definition := &unstructured.Unstructured{}
	definition.SetName(entry.Karta.Name)
	c.onKartaDelete(definition)
	close(release)
	<-published
	if _, exists := c.store.Workload(job.GetUID()); exists {
		t.Fatal("first workload publication survived definition removal")
	}
	if _, exists := c.pendingWorkloads.Load(job.GetUID()); exists {
		t.Fatal("first workload publication restored pending retry")
	}
}
