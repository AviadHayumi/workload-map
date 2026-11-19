// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package ctrlreader

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

func fixture(t *testing.T) (client.Client, meta.RESTMapper) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Pod"), meta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Node"), meta.RESTScopeRoot)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "team-a", Labels: map[string]string{"app": "train"}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a", Labels: map[string]string{"app": "else"}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "team-b", Labels: map[string]string{"app": "train"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-one"}},
	).Build()
	return c, mapper
}
func TestScopeAndSelection(t *testing.T) {
	client, mapper := fixture(t)
	r := New(client, mapper)
	ctx := context.Background()
	podGVK := corev1.SchemeGroupVersion.WithKind("Pod")
	object, err := r.Get(ctx, podGVK, "team-a", "worker")
	if err != nil || object.GetNamespace() != "team-a" {
		t.Fatalf("Pod Get: %v %v", object, err)
	}
	node, err := r.Get(ctx, corev1.SchemeGroupVersion.WithKind("Node"), "ignored", "node-one")
	if err != nil || node.GetNamespace() != "" {
		t.Fatalf("cluster Get: %v %v", node, err)
	}
	if _, err = r.Get(ctx, podGVK, "", "worker"); err == nil {
		t.Fatal("empty namespaced lookup accepted")
	}
	if _, err = r.List(ctx, podGVK, references.ListQuery{}); err == nil {
		t.Fatal("empty namespaced list accepted")
	}
	if _, err = r.Get(ctx, podGVK, "team-a", "missing"); !errors.Is(err, references.ErrNotFound) {
		t.Fatalf("missing lookup: %v", err)
	}
	items, err := r.List(ctx, podGVK, references.ListQuery{Namespace: "team-a", Selector: labels.SelectorFromSet(labels.Set{"app": "train"})})
	if err != nil || len(items) != 1 || items[0].GetName() != "worker" || items[0].GroupVersionKind() != podGVK {
		t.Fatalf("List: %v %v", items, err)
	}
	if _, err = r.Get(ctx, schema.GroupVersionKind{Group: "unknown.test", Version: "v1", Kind: "Unknown"}, "team-a", "x"); err == nil {
		t.Fatal("unknown mapping accepted")
	}
}

type listReader struct {
	client.Reader
	fn func(context.Context, client.ObjectList, ...client.ListOption) error
}

func (r listReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.fn(ctx, list, opts...)
}
func TestListErrorsAndPagination(t *testing.T) {
	c, mapper := fixture(t)
	failure := errors.New("unavailable")
	r := New(listReader{Reader: c, fn: func(context.Context, client.ObjectList, ...client.ListOption) error { return failure }}, mapper)
	if _, err := r.List(context.Background(), corev1.SchemeGroupVersion.WithKind("Pod"), references.ListQuery{Namespace: "team-a"}); !errors.Is(err, failure) {
		t.Fatalf("List error lost: %v", err)
	}
	calls := 0
	r = New(listReader{Reader: c, fn: func(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
		calls++
		options := &client.ListOptions{}
		for _, opt := range opts {
			opt.ApplyToList(options)
		}
		page := list.(*unstructured.UnstructuredList)
		if calls == 1 {
			page.Items = []unstructured.Unstructured{{Object: map[string]any{"metadata": map[string]any{"name": "first"}}}}
			page.SetContinue("page-2")
		} else {
			if options.Raw.Continue != "page-2" {
				t.Fatalf("continuation lost: %+v", options.Raw)
			}
			page.Items = []unstructured.Unstructured{{Object: map[string]any{"metadata": map[string]any{"name": "second"}}}}
		}
		return nil
	}}, mapper)
	items, err := r.List(context.Background(), corev1.SchemeGroupVersion.WithKind("Pod"), references.ListQuery{Namespace: "team-a"})
	if err != nil || len(items) != 2 || calls != 2 {
		t.Fatalf("pagination: %v %v calls=%d", items, err, calls)
	}
}
