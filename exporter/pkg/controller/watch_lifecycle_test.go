// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metadatafake "k8s.io/client-go/metadata/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
)

func TestRemovingPodRootKeepsDedicatedPodOwnerEdges(t *testing.T) {
	c, _, job := fixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "test", UID: "child", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
	c.index.UpsertTrackedObject(pod, podGVK)
	c.watchers[podGVK] = &watcher{gvk: podGVK, isRoot: true, informer: c.podInformer, stop: make(chan struct{})}
	c.reconcileWatchers()
	if c.watchers[podGVK] != nil {
		t.Fatal("unselected explicit Pod root watcher remained")
	}
	if !c.index.HasOwnerKind(job.GroupVersionKind()) || len(c.index.Descendants(job.GetUID())) != 1 {
		t.Fatal("removing explicit Pod root discarded the dedicated Pod observer's edges")
	}
}

func TestRemovedDefinitionRetainsOnlyNeededOwnerWatch(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		referenced bool
		required   bool
	}{
		{name: "unused"},
		{name: "referenced", referenced: true},
		{name: "required", required: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c, _, job := fixture(t)
			gvk := job.GroupVersionKind()
			c.watchers[gvk].stop = make(chan struct{})
			t.Cleanup(c.stopAllWatchers)
			c.metadataClient.(*metadatafake.FakeMetadataClient).PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &metav1.List{}, nil
			})
			if scenario.referenced {
				c.index.UpsertObject("dependent", jobOwner(job.GetName(), job.GetUID()))
			}
			if scenario.required {
				c.options.RequiredKinds = []schema.GroupVersionKind{gvk}
			}
			previous := c.registry.Entries()
			entry, _ := c.registry.EntryFor(gvk.GroupKind())
			c.registry.Remove(entry.Karta.Name)
			c.reconcileDefinitions(previous)
			c.mu.Lock()
			remaining := c.watchers[gvk]
			c.mu.Unlock()
			want := scenario.referenced || scenario.required
			if (remaining != nil) != want || (remaining != nil && remaining.isRoot) {
				t.Fatalf("remaining watcher=%v, want metadata=%v", remaining, want)
			}
			if !want {
				c.verifyHealth(context.Background())
				for _, row := range c.store.Snapshot().Health {
					if row.Ref.Group == gvk.Group && row.Ref.Kind == gvk.Kind {
						t.Fatal("removed definition retained obsolete API health")
					}
				}
			}
			if scenario.referenced {
				c.index.DeleteObject("dependent")
				c.reconcileWatchers()
				c.mu.Lock()
				remaining = c.watchers[gvk]
				c.mu.Unlock()
				if remaining != nil {
					t.Fatal("last owner edge removal retained discovered watcher")
				}
				// A reference arriving during teardown must restore observation
				// even if its event resolved the old cached owner before closure.
				c.index.UpsertObject("new-dependent", jobOwner(job.GetName(), job.GetUID()))
				c.reconcileWatchers()
				c.mu.Lock()
				remaining = c.watchers[gvk]
				c.mu.Unlock()
				if remaining == nil || remaining.isRoot {
					t.Fatal("new owner edge did not restore its metadata watcher")
				}
			}
		})
	}
}

func TestPrunedAliasPreservesOnlySameLifetimeInSurvivingCache(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		present bool
		sameUID bool
		deleted bool
	}{
		{name: "same_lifetime", present: true, sameUID: true},
		{name: "deleted_lifetime_in_lagging_alias", present: true, sameUID: true, deleted: true},
		{name: "replacement_uid", present: true},
		{name: "absent"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c, _, job := fixture(t)
			first := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}
			second := first
			second.Version = "v2"
			obj := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "middle", Namespace: "test", UID: "middle", OwnerReferences: jobOwner(job.GetName(), job.GetUID())}}
			for _, gvk := range []schema.GroupVersionKind{second, first} {
				cacheChild(t, c, obj, gvk)
				c.watchers[gvk].stop = make(chan struct{})
				c.discoveredOwners[gvk] = struct{}{}
				c.onChildEvent(obj, gvk)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "pod", OwnerReferences: jobOwner(obj.Name, obj.UID)}}
			pod.OwnerReferences[0].APIVersion = second.GroupVersion().String()
			pod.OwnerReferences[0].Kind = second.Kind
			if err := c.podInformer.GetStore().Add(pod); err != nil {
				t.Fatal(err)
			}
			c.attributePod(pod)
			if _, ok := c.store.Pod(pod.UID); !ok {
				t.Fatal("initial attribution missing")
			}
			if !scenario.present {
				if err := c.watchers[second].informer.GetStore().Delete(obj); err != nil {
					t.Fatal(err)
				}
			} else if !scenario.sameUID {
				replacement := obj.DeepCopy()
				replacement.UID = "replacement"
				cacheChild(t, c, replacement, second)
			}
			if scenario.deleted {
				// A deletion can set its barrier before removing the index node.
				c.deletedWorkloads.Store(obj.UID, owner.Ref{UID: obj.UID, Namespace: obj.Namespace, Name: obj.Name, GVK: first})
			}
			wantPreserved := scenario.sameUID && !scenario.deleted
			c.index.MarkPending(obj.UID, pod.Namespace+"/"+pod.Name)
			c.store.DeletePod(pod.UID)
			c.reconcileWatchers()
			if c.watchers[first] != nil || c.watchers[second] == nil {
				t.Fatal("unexpected alias retention")
			}
			if wantPreserved {
				if _, restored := c.store.Pod(pod.UID); !restored {
					t.Fatal("surviving alias did not retry its pending Pod")
				}
			}
			c.attributePod(pod)
			if actual, ok := c.store.Pod(pod.UID); ok != wantPreserved || (ok && actual.WorkloadUID != job.GetUID()) {
				t.Fatalf("alias preservation: mapping=%+v present=%v, want same lifetime=%v", actual, ok, wantPreserved)
			}
			if wantPreserved && c.index.PendingCount() != 0 {
				t.Fatal("restored alias lost pending Pod retry")
			}
		})
	}
}
