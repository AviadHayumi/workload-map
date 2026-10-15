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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

func TestLiveClusterRoot(t *testing.T) {
	if os.Getenv("KARTA_DISCOVERY_MUTATION_TESTS") != "true" {
		t.Skip("set KARTA_DISCOVERY_MUTATION_TESTS=true for isolated mutation tests")
	}
	ctx, _, client, mapper, reader := liveClient(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	group := "discovery-" + suffix + ".example.com"
	crds := client.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"})
	crdName := "clusterbundles." + group
	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": crdName},
		"spec": map[string]any{"group": group, "scope": "Cluster", "names": map[string]any{"plural": "clusterbundles", "singular": "clusterbundle", "kind": "ClusterBundle"}, "versions": []any{map[string]any{"name": "v1", "served": true, "storage": true, "schema": map[string]any{"openAPIV3Schema": map[string]any{"type": "object"}}}}},
	}}
	if _, err := crds.Create(ctx, crd, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := crds.Delete(cleanup, crdName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete test CRD: %v", err)
		}
	})
	waitLive(t, ctx, func() (bool, error) {
		current, err := crds.Get(ctx, crdName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		raw, found, err := unstructured.NestedFieldNoCopy(current.Object, "status", "conditions")
		if err != nil {
			return false, err
		}
		if !found || raw == nil {
			return false, nil
		}
		conditions, ok := raw.([]any)
		if !ok {
			return false, fmt.Errorf("unexpected CRD conditions type %T", raw)
		}
		for _, condition := range conditions {
			c := condition.(map[string]any)
			if c["type"] == "Established" && c["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	})
	mapper.(meta.ResettableRESTMapper).Reset()
	roots := client.Resource(schema.GroupVersionResource{Group: group, Version: "v1", Resource: "clusterbundles"})
	root, err := roots.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1", "kind": "ClusterBundle", "metadata": map[string]any{"name": "root"}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var podUIDs []string
	namespaces := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	var firstNamespace string
	for _, part := range []string{"a", "b"} {
		namespace := "sdk-cluster-" + suffix + "-" + part
		if firstNamespace == "" {
			firstNamespace = namespace
		}
		if _, err := namespaces.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace}}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := namespaces.Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete test namespace: %v", err)
			}
		})
		pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "member"}, "spec": map[string]any{"nodeSelector": map[string]any{"discovery.example.com/unschedulable": "true"}, "containers": []any{map[string]any{"name": "work", "image": "busybox:1.37"}}}}}
		pod.SetOwnerReferences(liveOwner(root))
		created, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		podUIDs = append(podUIDs, string(created.GetUID()))
	}
	resolver, err := sdk.New(reader, sdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	workload, err := resolver.Workload(ctx, liveRef(root))
	if err != nil {
		t.Fatal(err)
	}
	assertPods(t, workload.Pods(), podUIDs)
	if workload.Observation().Namespace != "" {
		t.Fatal("cluster root should record all-namespace observation")
	}
	restricted, err := sdk.Collect(ctx, reader, firstNamespace)
	if err != nil {
		t.Fatal(err)
	}
	index := sdk.NewIndex()
	if _, err := index.Replace(ctx, restricted); err != nil {
		t.Fatal(err)
	}
	indexed, err := sdk.New(index, sdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := indexed.Workload(ctx, liveRef(root)); result != nil || !errors.Is(err, sdk.ErrInsufficientScope) {
		t.Fatalf("namespace index must reject cluster root: %v / %v", result, err)
	}
	complete, err := sdk.Collect(ctx, reader, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Replace(ctx, complete); err != nil {
		t.Fatal(err)
	}
	workload, err = indexed.Workload(ctx, liveRef(root))
	if err != nil {
		t.Fatal(err)
	}
	assertPods(t, workload.Pods(), podUIDs)
	t.Log("cluster root includes both namespaces; namespace-only index rejects incomplete coverage")
}
