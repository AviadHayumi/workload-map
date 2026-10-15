// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

func TestCollectionOrderingAcrossShuffledLists(t *testing.T) {
	root := sdkObject(sdkBundleGVK, "", "root", "z-root", nil)
	other := sdkObject(sdkBundleGVK, "", "other", "a-unrelated", nil)
	middle := sdkObject(sdkReplicaSetGVK, "team", "middle", "m-owner", root)
	last := sdkPod("z-last", middle, nil)
	first := sdkPod("z-first", root, nil)
	first.Namespace = "alpha"
	beforeLast := sdkPod("a-first", middle, nil)
	unrelated := sdkPod("between", other, nil)
	unrelated.Namespace = "beta"
	pods := []*corev1.Pod{last, first, beforeLast, unrelated}
	objects := []*unstructured.Unstructured{root, other, middle}
	for _, test := range []struct {
		name  string
		order []int
	}{
		{name: "original", order: []int{0, 1, 2, 3}},
		{name: "reversed", order: []int{3, 2, 1, 0}},
		{name: "interleaved", order: []int{2, 0, 3, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			listed := make([]*corev1.Pod, len(test.order))
			for i, index := range test.order {
				listed[i] = pods[index]
			}
			for _, acquisition := range []string{"direct_pod", "direct_root", "collect"} {
				t.Run(acquisition, func(t *testing.T) {
					reader := sdkNewReader(t, objects, listed...)
					want := []types.UID{first.UID, beforeLast.UID, last.UID}
					var observed []*corev1.Pod
					switch acquisition {
					case "collect":
						dataset, err := discovery.Collect(context.Background(), reader, "")
						if err != nil {
							t.Fatal(err)
						}
						observed = dataset.Pods
						want = []types.UID{first.UID, unrelated.UID, beforeLast.UID, last.UID}
						gotObjects := make([]types.UID, len(dataset.Objects))
						for i, object := range dataset.Objects {
							gotObjects[i] = object.GetUID()
						}
						wantObjects := []types.UID{other.GetUID(), middle.GetUID(), root.GetUID()}
						if !slices.Equal(gotObjects, wantObjects) {
							t.Fatalf("Collect() object UIDs = %v, want %v", gotObjects, wantObjects)
						}
					default:
						resolver := sdkResolver(t, reader)
						var workload *discovery.Workload
						var err error
						if acquisition == "direct_pod" {
							workload, err = resolver.WorkloadForPod(context.Background(), sdkPodRef(last))
						} else {
							workload, err = resolver.Workload(context.Background(), sdkRef(root))
						}
						if err != nil {
							t.Fatal(err)
						}
						if workload.Root() != sdkRef(root) {
							t.Fatalf("Root() = %+v, want %+v", workload.Root(), sdkRef(root))
						}
						observed = workload.Pods()
					}
					got := make([]types.UID, len(observed))
					for i, pod := range observed {
						got[i] = pod.UID
					}
					if !slices.Equal(got, want) {
						t.Fatalf("%s Pod UIDs = %v, want %v", acquisition, got, want)
					}
				})
			}
		})
	}
}

func TestCollectionRejectsDuplicateListedPods(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*corev1.PodList)
	}{
		{name: "uid", mutate: func(list *corev1.PodList) { list.Items[1].UID = list.Items[0].UID }},
		{name: "coordinates", mutate: func(list *corev1.PodList) { list.Items[1].Name = list.Items[0].Name }},
		{name: "repeated_entry", mutate: func(list *corev1.PodList) {
			list.Items = append(list.Items, *list.Items[0].DeepCopy())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, acquisition := range []string{"direct", "collect"} {
				t.Run(acquisition, func(t *testing.T) {
					root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
					reader := sdkNewReader(t, []*unstructured.Unstructured{root}, sdkPod("one", root, nil), sdkPod("two", root, nil))
					reader.mutateList = test.mutate
					var err error
					switch acquisition {
					case "direct":
						var workload *discovery.Workload
						workload, err = sdkResolver(t, reader).Workload(context.Background(), sdkRef(root))
						if workload != nil {
							t.Fatal("Workload() returned a partial observation")
						}
					case "collect":
						var dataset discovery.Dataset
						dataset, err = discovery.Collect(context.Background(), reader, "team")
						if len(dataset.Pods) != 0 || len(dataset.Objects) != 0 || len(dataset.Mappings) != 0 {
							t.Fatal("Collect() returned a partial observation")
						}
					}
					if !errors.Is(err, discovery.ErrInvalidInput) {
						t.Fatalf("%s error = %v, want ErrInvalidInput", acquisition, err)
					}
				})
			}
		})
	}
}

func TestDirectCollectionChecksAcquiredPodOwner(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*corev1.Pod)
	}{
		{name: "unchanged"},
		{name: "uid", mutate: func(pod *corev1.Pod) { pod.UID = "replacement" }},
		{name: "resource_version", mutate: func(pod *corev1.Pod) { pod.ResourceVersion = "2" }},
		{name: "owner_reference", mutate: func(pod *corev1.Pod) {
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "child", UID: "uid-child", Controller: ptr.To(true)}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := sdkPod("parent", nil, nil)
			child := sdkPod("child", nil, nil)
			child.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: parent.Name, UID: parent.UID, Controller: ptr.To(true)}}
			reader := sdkNewReader(t, nil, child, parent)
			if test.mutate != nil {
				reader.mutateList = func(list *corev1.PodList) { test.mutate(&list.Items[1]) }
			}
			workload, err := sdkResolver(t, reader).WorkloadForPod(context.Background(), sdkPodRef(child))
			if test.mutate != nil {
				if workload != nil || !errors.Is(err, discovery.ErrStaleInput) {
					t.Fatalf("WorkloadForPod() = %v, %v, want nil, ErrStaleInput", workload, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if workload.Root() != sdkPodRef(parent) {
				t.Fatalf("Root() = %+v, want %+v", workload.Root(), sdkPodRef(parent))
			}
			sdkAssertPods(t, workload.Pods(), string(child.UID), string(parent.UID))
		})
	}
}
