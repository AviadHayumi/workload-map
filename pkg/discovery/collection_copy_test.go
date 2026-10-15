// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type collectionCopyReader struct {
	capturedReader
	pods   []*corev1.Pod
	listed *corev1.PodList
	onGet  func(ObjectRef)
}

func (r *collectionCopyReader) Get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	if r.onGet != nil {
		r.onGet(ref)
	}
	object, err := r.capturedReader.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	return object.DeepCopy(), nil
}

func (r *collectionCopyReader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.listed = &corev1.PodList{}
	for _, pod := range r.pods {
		if namespace == "" || pod.Namespace == namespace {
			r.listed.Items = append(r.listed.Items, *pod.DeepCopy())
		}
	}
	return r.listed, nil
}

func TestCollectionCopiesAllPodsBeforeOwnerReads(t *testing.T) {
	for _, acquisition := range []string{"collect", "direct_selected", "direct_all"} {
		t.Run(acquisition, func(t *testing.T) {
			root := indexTestObject("root", "team", indexBundleGVK, nil)
			other := indexTestObject("other", "team", indexBundleGVK, nil)
			child := indexTestObject("child", "team", indexWorkerGVK, root)
			first := indexTestPod("first", "team", child)
			later := indexTestPod("later", "team", root)
			later.Labels = map[string]string{"snapshot": "listed"}
			later.Spec.Containers = []corev1.Container{{Name: "main", Image: "ghcr.io/example/workload:v1"}}
			unrelated := indexTestPod("unrelated", "team", other)
			reader := &collectionCopyReader{
				capturedReader: capturedReader{
					objects: map[objectKey]*unstructured.Unstructured{
						{gvk: indexBundleGVK, namespace: "team", name: "root"}:  root,
						{gvk: indexBundleGVK, namespace: "team", name: "other"}: other,
						{gvk: indexWorkerGVK, namespace: "team", name: "child"}: child,
					},
					mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
						podGVK:         indexTestMapping(podGVK, meta.RESTScopeNamespace),
						indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace),
						indexWorkerGVK: indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace),
					},
				},
				pods: []*corev1.Pod{first, later, unrelated},
			}
			if acquisition == "direct_all" {
				reader.pods = reader.pods[:2]
			}
			mutated := false
			reader.onGet = func(ref ObjectRef) {
				if ref != reference(child) || reader.listed == nil {
					return
				}
				mutated = true
				pod := &reader.listed.Items[1]
				pod.Labels["snapshot"] = "changed"
				pod.Spec.Containers[0].Image = "ghcr.io/example/workload:v2"
				pod.OwnerReferences[0].Name = other.GetName()
				pod.OwnerReferences[0].UID = other.GetUID()
			}
			want := []*corev1.Pod{first.DeepCopy(), later.DeepCopy()}
			var observed []*corev1.Pod
			if acquisition == "collect" {
				dataset, err := Collect(context.Background(), reader, "team")
				if err != nil {
					t.Fatal(err)
				}
				observed = dataset.Pods
				want = append(want, unrelated.DeepCopy())
			} else {
				resolver, err := New(reader, Options{})
				if err != nil {
					t.Fatal(err)
				}
				workload, err := resolver.Workload(context.Background(), reference(root))
				if err != nil {
					t.Fatal(err)
				}
				observed = workload.pods
			}
			if !mutated {
				t.Fatal("owner lookup did not mutate the retained list")
			}
			reader.listed.Items[0].Name = "changed-after-acquisition"
			reader.listed.Items[1].Labels["snapshot"] = "changed-after-acquisition"
			if !reflect.DeepEqual(observed, want) {
				t.Fatalf("%s Pods changed after reader mutations: got %+v, want %+v", acquisition, observed, want)
			}
		})
	}
}

func TestCollectionKeepsEmptyPodSliceShape(t *testing.T) {
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	reader := &collectionCopyReader{capturedReader: capturedReader{
		objects: map[objectKey]*unstructured.Unstructured{{gvk: indexBundleGVK, namespace: "team", name: "root"}: root},
		mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
			podGVK:         indexTestMapping(podGVK, meta.RESTScopeNamespace),
			indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace),
		},
	}}
	dataset, err := Collect(context.Background(), reader, "team", reference(root))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Pods != nil {
		t.Fatalf("empty Collect() Pods = %+v, want nil", dataset.Pods)
	}
	resolver, err := New(reader, Options{})
	if err != nil {
		t.Fatal(err)
	}
	workload, err := resolver.Workload(context.Background(), reference(root))
	if err != nil {
		t.Fatal(err)
	}
	if workload.pods == nil || len(workload.pods) != 0 {
		t.Fatalf("empty Workload() Pods = %+v, want a non-nil empty slice", workload.pods)
	}
}

func TestCollectionRootForPodPreservesValidation(t *testing.T) {
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	reader := &capturedReader{
		objects: map[objectKey]*unstructured.Unstructured{{gvk: indexBundleGVK, namespace: "team", name: "root"}: root},
		mappings: map[schema.GroupVersionKind]*meta.RESTMapping{
			podGVK:         indexTestMapping(podGVK, meta.RESTScopeNamespace),
			indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeNamespace),
		},
	}
	collection := newCollection(reader)
	pod := indexTestPod("pod", "team", root)
	actual, err := collection.rootForPod(context.Background(), pod, 1)
	if err != nil || actual != reference(root) {
		t.Fatalf("rootForPod() = %+v, %v, want root, nil", actual, err)
	}
	if _, err := collection.rootForPod(context.Background(), nil, 1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rootForPod(nil) error = %v, want ErrInvalidInput", err)
	}
	if _, err := collection.rootForPod(context.Background(), pod, 0); !errors.Is(err, ownership.ErrDepthExceeded) {
		t.Fatalf("rootForPod(depth=0) error = %v, want ErrDepthExceeded", err)
	}
	pod.Namespace = ""
	if _, err := collection.rootForPod(context.Background(), pod, 1); !errors.Is(err, ownership.ErrScope) {
		t.Fatalf("rootForPod(no namespace) error = %v, want ErrScope", err)
	}
}
