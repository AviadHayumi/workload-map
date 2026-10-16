// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build live_discovery

package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	sdkclient "github.com/dsx-ai-factory/workload-map/pkg/discovery/clientgo"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

func liveRef(object *unstructured.Unstructured) sdk.ObjectRef {
	return sdk.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
}
func liveOwner(object *unstructured.Unstructured) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Name: object.GetName(), UID: object.GetUID(), Controller: ptr.To(true)}}
}
func waitLive(t *testing.T, ctx context.Context, check func() (bool, error)) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		done, err := check()
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

// This test creates only uniquely named test namespaces and Pods. It never
// changes an existing node, operator, or workload. Explicit opt-in is required.
func TestLiveOwnershipChurn(t *testing.T) {
	if os.Getenv("KARTA_DISCOVERY_MUTATION_TESTS") != "true" {
		t.Skip("set KARTA_DISCOVERY_MUTATION_TESTS=true for isolated namespace mutation tests")
	}
	ctx, config, client, mapper, reader := liveClient(t)
	nsResource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	namespace := fmt.Sprintf("sdk-discovery-churn-%d", time.Now().UnixNano())
	if _, err := nsResource.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pods := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace)
	roots := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(namespace)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		list, err := pods.List(cleanup, metav1.ListOptions{})
		if err == nil {
			for _, pod := range list.Items {
				pod.SetFinalizers(nil)
				if _, err := pods.Update(cleanup, &pod, metav1.UpdateOptions{}); err != nil && !apierrors.IsNotFound(err) {
					t.Errorf("remove test finalizer: %v", err)
				}
			}
		}
		if err := nsResource.Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete test namespace: %v", err)
		}
	})
	createRoot := func(name string) *unstructured.Unstructured {
		object, err := roots.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": name}, "data": map[string]any{"fixture": "discovery"}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return object
	}
	rootA, rootB := createRoot("root-a"), createRoot("root-b")
	createPod := func(name string, root *unstructured.Unstructured) *unstructured.Unstructured {
		object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name}, "spec": map[string]any{"nodeSelector": map[string]any{"discovery.example.com/unschedulable": "true"}, "containers": []any{map[string]any{"name": "work", "image": "busybox:1.37"}}}}}
		object.SetOwnerReferences(liveOwner(root))
		object.SetFinalizers([]string{"discovery.example.com/test-hold"})
		created, err := pods.Create(ctx, object, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	podA, podB := createPod("pod-a", rootA), createPod("pod-b", rootA)
	direct, err := sdk.New(reader, sdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The scheduler may update a new unscheduled Pod between the initial GET
	// and complete LIST. Establish a consistent observation before mutating owners.
	initialContext, cancelInitial := context.WithTimeout(ctx, 10*time.Second)
	defer cancelInitial()
	var before *sdk.Workload
	waitLive(t, initialContext, func() (bool, error) {
		var err error
		before, err = direct.WorkloadForPod(initialContext, liveRef(podA))
		if errors.Is(err, sdk.ErrStaleInput) {
			if before != nil {
				return false, fmt.Errorf("stale acquisition returned a workload: %w", err)
			}
			return false, nil
		}
		return err == nil, err
	})
	originalUIDs := []string{string(podA.GetUID()), string(podB.GetUID())}
	assertPods(t, before.Pods(), originalUIDs)
	data, err := sdk.Collect(ctx, reader, namespace, liveRef(rootA), liveRef(rootB))
	if err != nil {
		t.Fatal(err)
	}
	index := sdk.NewIndex()
	generation, err := index.Replace(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	indexed, err := sdk.New(index, sdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := sdk.New(index, sdk.Options{Cache: &sdk.CacheOptions{MaxWorkloads: 10, MaxPods: 100}})
	if err != nil {
		t.Fatal(err)
	}
	cachedBefore, err := cached.WorkloadForPod(ctx, liveRef(podA))
	if err != nil {
		t.Fatal(err)
	}
	assertPods(t, cachedBefore.Pods(), originalUIDs)
	for range 2 {
		empty, err := cached.Workload(ctx, liveRef(rootB))
		if err != nil {
			t.Fatal(err)
		}
		assertPods(t, empty.Pods(), nil)
	}

	// Move one existing lifetime to the other root and publish its current API object.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := pods.Get(ctx, podA.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		current.SetOwnerReferences(liveOwner(rootB))
		podA, err = pods.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, err = index.Apply(ctx, generation, sdk.Changes{Upserts: []*unstructured.Unstructured{podA}})
	if err != nil {
		t.Fatal(err)
	}
	for _, resolver := range []*sdk.Resolver{direct, indexed, cached} {
		a, err := resolver.Workload(ctx, liveRef(rootA))
		if err != nil {
			t.Fatal(err)
		}
		assertPods(t, a.Pods(), []string{string(podB.GetUID())})
		b, err := resolver.WorkloadForPod(ctx, liveRef(podA))
		if err != nil {
			t.Fatal(err)
		}
		if b.Root().UID != rootB.GetUID() {
			t.Fatal("reparent retained old root")
		}
		assertPods(t, b.Pods(), []string{string(podA.GetUID())})
	}
	assertPods(t, before.Pods(), originalUIDs)
	assertPods(t, cachedBefore.Pods(), originalUIDs)
	t.Log("reparent: direct, indexed and cached membership updated; earlier observations unchanged")

	// A terminating Pod remains part of the observed workload until it disappears.
	if err := pods.Delete(ctx, podA.GetName(), metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))}); err != nil {
		t.Fatal(err)
	}
	podA, err = pods.Get(ctx, podA.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if podA.GetDeletionTimestamp() == nil {
		t.Fatal("test Pod did not enter termination")
	}
	generation, err = index.Apply(ctx, generation, sdk.Changes{Upserts: []*unstructured.Unstructured{podA}})
	if err != nil {
		t.Fatal(err)
	}
	for _, resolver := range []*sdk.Resolver{indexed, cached} {
		terminating, err := resolver.Workload(ctx, liveRef(rootB))
		if err != nil {
			t.Fatal(err)
		}
		assertPods(t, terminating.Pods(), []string{string(podA.GetUID())})
		if terminating.Pods()[0].DeletionTimestamp == nil {
			t.Fatal("updated Pod body lost its deletion timestamp")
		}
	}
	t.Log("terminating and unscheduled Pods remain visible")

	// An authenticated caller lacking GET permission receives Forbidden, never
	// an empty workload or generic fallback caused by the failed owner read.
	restrictedConfig := rest.CopyConfig(config)
	restrictedConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + namespace + ":unprivileged", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"}}
	restrictedClient, err := dynamic.NewForConfig(restrictedConfig)
	if err != nil {
		t.Fatal(err)
	}
	restrictedReader, err := sdkclient.NewReader(restrictedClient, mapper)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := sdk.New(restrictedReader, sdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if workload, err := restricted.Workload(ctx, liveRef(rootB)); workload != nil || !apierrors.IsForbidden(err) {
		t.Fatalf("expected Forbidden and no result; got %v / %v", workload, err)
	}
	t.Log("RBAC denial preserves Forbidden and returns no partial workload")

	// Recreate an owner name while the old Pod lifetime remains held by its finalizer.
	if err := roots.Delete(ctx, rootA.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitLive(t, ctx, func() (bool, error) {
		_, err := roots.Get(ctx, rootA.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	replacement := createRoot(rootA.GetName())
	if replacement.GetUID() == rootA.GetUID() {
		t.Fatal("owner UID was reused")
	}
	if workload, err := direct.WorkloadForPod(ctx, liveRef(podB)); workload != nil || !errors.Is(err, ownership.ErrUIDMismatch) {
		t.Fatalf("expected old owner UID mismatch, got %v / %v", workload, err)
	}
	generation, err = index.Apply(ctx, generation, sdk.Changes{Deletes: []sdk.ObjectRef{liveRef(rootA)}, Upserts: []*unstructured.Unstructured{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	for _, resolver := range []*sdk.Resolver{indexed, cached} {
		if workload, err := resolver.WorkloadForPod(ctx, liveRef(podB)); workload != nil || !errors.Is(err, ownership.ErrUIDMismatch) {
			t.Fatalf("indexed old owner UID mismatch: %v / %v", workload, err)
		}
	}
	if _, err := index.Apply(ctx, generation, sdk.Changes{Upserts: []*unstructured.Unstructured{rootA}}); !errors.Is(err, sdk.ErrRetiredUID) {
		t.Fatalf("late old owner must not resurrect: %v", err)
	}
	index.Invalidate(fmt.Errorf("test watch continuity lost"))
	for _, resolver := range []*sdk.Resolver{indexed, cached} {
		if workload, err := resolver.Workload(ctx, liveRef(rootB)); workload != nil || !errors.Is(err, sdk.ErrNotSynced) {
			t.Fatalf("invalidated index: %v / %v", workload, err)
		}
	}
	t.Log("name reuse rejects stale UID; late resurrection and invalidated reads fail explicitly")
}
