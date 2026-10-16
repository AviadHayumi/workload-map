// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/cached/memory"
	discoveryfake "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/restmapper"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/registry"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
)

func TestOwnerVerifierClusterScope(t *testing.T) {
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "Node"}
	for _, tc := range []struct {
		name                            string
		cached, replacement             bool
		childNamespace, parentNamespace string
		wantError                       bool
	}{
		{"cached cluster owner", true, false, "test", "", false},
		{"fetched cluster owner", false, false, "test", "", false},
		{"fetched replacement UID", false, true, "test", "", true},
		{"cross namespace", true, false, "test", "other", true},
		{"cluster cannot own namespaced parent", true, false, "", "test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := fixture(t)
			discovery := kubefake.NewClientset().Discovery().(*discoveryfake.FakeDiscovery)
			discovery.Resources = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "nodes", Kind: "Node", Namespaced: false}}}}
			c.mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discovery))
			parent := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: tc.parentNamespace, UID: "parent"}}
			if tc.replacement {
				parent.UID = "replacement"
			}
			objects := map[types.UID]metav1.Object{}
			if tc.cached {
				objects["parent"] = parent
			}
			c.metadataClient.(*metadatafake.FakeMetadataClient).PrependReactor("get", "nodes", func(action ktesting.Action) (bool, runtime.Object, error) {
				if action.GetNamespace() != "" {
					t.Errorf("cluster owner GET used namespace %q", action.GetNamespace())
				}
				return true, parent, nil
			})
			verifier := ownerVerifier{controller: c, objects: objects, entries: map[schema.GroupKind]*registry.Entry{gvk.GroupKind(): {}}, failures: map[types.UID]error{}, cacheMismatches: map[types.UID]bool{}}
			child := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: tc.childNamespace, UID: "child", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "owner", UID: "parent", Controller: ptr.To(true)}}}}
			uid, resolution, err := verifier.root(context.Background(), child, "child")
			if (err != nil) != tc.wantError {
				t.Fatalf("root error = %v, want error %v", err, tc.wantError)
			}
			if !tc.wantError && (uid != "parent" || resolution != "resolved") {
				t.Fatalf("root=%s resolution=%s", uid, resolution)
			}
		})
	}
}

func TestProcessClusterScopedWorkload(t *testing.T) {
	c, _, job := fixture(t)
	definition, err := catalog.Get(job.GroupVersionKind())
	if err != nil {
		t.Fatal(err)
	}
	definition.Name = "cluster-job"
	definition.Spec.StructureDefinition.RootComponent.Kind.Kind = "ClusterJob"
	c.registry.Set(definition)
	root := job.DeepCopy()
	root.SetKind("ClusterJob")
	root.SetNamespace("")
	root.SetName("cluster-job")
	root.SetUID("cluster-job")
	gvk := root.GroupVersionKind()
	informer := syncedInformer{cache.NewSharedIndexInformer(&cache.ListWatch{}, &unstructured.Unstructured{}, 0, cache.Indexers{})}
	if err := informer.GetStore().Add(root); err != nil {
		t.Fatal(err)
	}
	c.watchers[gvk] = &watcher{gvk: gvk, isRoot: true, informer: informer}
	c.processWorkload(root, false)
	record, exists := c.store.Workload(root.GetUID())
	if !exists || record.Ref.Namespace != "" || record.Ref.Name != root.GetName() {
		t.Fatalf("cluster root missing or incorrect: %+v, exists=%v", record, exists)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "test", UID: "worker", OwnerReferences: []metav1.OwnerReference{{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: root.GetName(), UID: root.GetUID(), Controller: ptr.To(true)}}}}
	if err := c.podInformer.GetStore().Add(pod); err != nil {
		t.Fatal(err)
	}
	c.attributePod(pod)
	attributed, exists := c.store.Pod(pod.UID)
	if !exists || attributed.WorkloadUID != root.GetUID() || attributed.Workload.Namespace != "" {
		t.Fatalf("Pod not attributed to cluster root: %+v, exists=%v", attributed, exists)
	}

	child := root.DeepCopy()
	child.SetName("child")
	child.SetUID("child")
	child.SetOwnerReferences(pod.OwnerReferences)
	if err := informer.GetStore().Add(child); err != nil {
		t.Fatal(err)
	}
	c.processWorkload(child, false)
	child.SetGeneration(2)
	if err := informer.GetStore().Update(child); err != nil {
		t.Fatal(err)
	}
	c.reattributeDescendants(root.GetUID())
	updated, exists := c.store.Workload(child.GetUID())
	if !exists || updated.Generation != 2 {
		t.Fatalf("cluster descendant not refreshed: %+v", updated)
	}

	root.SetGeneration(3)
	if err := informer.GetStore().Update(root); err != nil {
		t.Fatal(err)
	}
	ref := owner.Ref{UID: root.GetUID(), Name: root.GetName(), GVK: gvk}
	c.pendingWorkloads.Store(root.GetUID(), ref)
	c.retryPendingWorkloads()
	updated, _ = c.store.Workload(root.GetUID())
	if updated.Generation != 3 {
		t.Fatal("cluster pending root not retried")
	}

	c.deletedWorkloads.Store(root.GetUID(), ref)
	c.retireObservedWorkloadDeletions()
	if _, exists := c.deletedWorkloads.Load(root.GetUID()); !exists {
		t.Fatal("live cluster cache entry lost deletion fence")
	}
	if err := informer.GetStore().Delete(root); err != nil {
		t.Fatal(err)
	}
	c.retireObservedWorkloadDeletions()
	if _, exists := c.deletedWorkloads.Load(root.GetUID()); exists {
		t.Fatal("absent cluster cache entry retained deletion fence")
	}

}
