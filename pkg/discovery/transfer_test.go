// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type sdkTransferReader struct {
	*sdkReader
	transfers     int
	fallbackLists int
	transferError error
	nilList       bool
}

type sdkArrayTransferReader struct {
	*sdkReader
	shape                string
	arrayStart, arrayEnd uintptr
	containerAddress     uintptr
}

func (r *sdkTransferReader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	r.fallbackLists++
	return r.sdkReader.ListPods(ctx, namespace)
}

func (r *sdkTransferReader) ListPodsForTransfer(ctx context.Context, namespace string) (*corev1.PodList, error) {
	r.transfers++
	if r.transferError != nil {
		return nil, r.transferError
	}
	if r.nilList {
		return nil, nil
	}
	// sdkReader allocates a new list and deep-copies each Pod independently.
	// No transferred list or Pod is retained by this reader.
	return r.sdkReader.ListPods(ctx, namespace)
}

func TestPodTransferDispatchAndResultIsolation(t *testing.T) {
	for _, coverage := range []string{"all_selected", "subset"} {
		t.Run(coverage, func(t *testing.T) {
			ctx := context.Background()
			root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
			other := sdkObject(sdkBundleGVK, "team", "other", "other", nil)
			sharedLabels := map[string]string{"snapshot": "original"}
			first := sdkPod("first", root, sharedLabels)
			second := sdkPod("second", root, sharedLabels)
			second.Spec.Containers = first.Spec.Containers
			first.Status.Phase = corev1.PodRunning
			second.Status.Phase = corev1.PodSucceeded
			pods := []*corev1.Pod{first, second}
			if coverage == "subset" {
				pods = append(pods, sdkPod("unrelated", other, nil))
			}
			want := make([]*corev1.Pod, len(pods))
			for i, pod := range pods {
				want[i] = pod.DeepCopy()
			}
			reader := &sdkTransferReader{sdkReader: sdkNewReader(t, []*unstructured.Unstructured{root, other}, pods...)}
			firstDataset, err := discovery.Collect(ctx, reader, "team")
			if err != nil {
				t.Fatal(err)
			}
			secondDataset, err := discovery.Collect(ctx, reader, "team")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(firstDataset.Pods, want) || firstDataset.PodResourceVersion != "list-rv" {
				t.Fatal("transferred dataset lost Pod data or list resourceVersion")
			}
			firstDataset.Pods[0].Labels["snapshot"] = "changed dataset"
			firstDataset.Pods[0].Spec.Containers[0].Image = "ghcr.io/example/changed:v2"
			firstDataset.Pods[0].Status.Phase = corev1.PodFailed
			firstDataset.Pods[0].OwnerReferences[0].Name = "changed dataset"
			if !reflect.DeepEqual(firstDataset.Pods[1:], want[1:]) || !reflect.DeepEqual(secondDataset.Pods, want) || !reflect.DeepEqual(reader.pods, want) {
				t.Fatal("dataset mutation reached another Pod, another result, or reader inputs")
			}

			resolver := sdkResolver(t, reader)
			firstWorkload, err := resolver.WorkloadForPod(ctx, sdkPodRef(first))
			if err != nil {
				t.Fatal(err)
			}
			secondWorkload, err := resolver.Workload(ctx, sdkRef(root))
			if err != nil {
				t.Fatal(err)
			}
			component, err := firstWorkload.Component("pods")
			if err != nil {
				t.Fatal(err)
			}
			queried, err := component.Pods()
			if err != nil || !reflect.DeepEqual(queried, want[:2]) {
				t.Fatalf("component Pods = %+v, %v, want both selected Pods", queried, err)
			}
			queried[0].Labels["snapshot"] = "changed query"
			queried[0].Spec.Containers[0].Image = "ghcr.io/example/changed:v3"
			queried[0].Status.Phase = corev1.PodFailed
			queried[0].OwnerReferences[0].Name = "changed query"
			firstWorkload.Pods()[0].Labels["snapshot"] = "changed accessor"
			reader.pods[0].Labels["snapshot"] = "changed input"
			reader.pods[0].Spec.Containers[0].Image = "ghcr.io/example/changed:v4"
			reader.pods[0].Status.Phase = corev1.PodFailed
			freshQuery, err := component.Pods()
			if err != nil || !reflect.DeepEqual(freshQuery, want[:2]) || !reflect.DeepEqual(firstWorkload.Pods(), want[:2]) || !reflect.DeepEqual(secondWorkload.Pods(), want[:2]) || !reflect.DeepEqual(secondDataset.Pods, want) {
				t.Fatal("reader input or public accessor mutation reached a retained observation")
			}
			if reader.transfers != 4 || reader.fallbackLists != 0 {
				t.Fatalf("transfer calls = %d, fallback calls = %d, want 4, 0", reader.transfers, reader.fallbackLists)
			}
		})
	}
}

func TestPodTransferRejectsInvalidObservationsWithoutFallback(t *testing.T) {
	readFailure := errors.New("transfer read failed")
	for _, test := range []struct {
		name  string
		setup func(*sdkTransferReader, *unstructured.Unstructured)
		want  error
	}{
		{name: "read_error", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) { reader.transferError = readFailure }, want: readFailure},
		{name: "nil_list", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) { reader.nilList = true }, want: discovery.ErrIncompleteOwnership},
		{name: "partial_list", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) {
			reader.mutateList = func(list *corev1.PodList) { list.Continue = "next-page" }
		}, want: discovery.ErrIncompleteOwnership},
		{name: "duplicate_uid", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) {
			reader.mutateList = func(list *corev1.PodList) { list.Items[1].UID = list.Items[0].UID }
		}, want: discovery.ErrInvalidInput},
		{name: "duplicate_coordinates", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) {
			reader.mutateList = func(list *corev1.PodList) { list.Items[1].Name = list.Items[0].Name }
		}, want: discovery.ErrInvalidInput},
		{name: "namespace", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) {
			reader.mutateList = func(list *corev1.PodList) { list.Items[0].Namespace = "elsewhere" }
		}, want: discovery.ErrInsufficientScope},
		{name: "pod_scope", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) {
			reader.mappings[sdkPodGVK].Scope = meta.RESTScopeRoot
		}, want: ownership.ErrScope},
		{name: "missing_uid", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) {
			reader.mutateList = func(list *corev1.PodList) { list.Items[0].UID = "" }
		}, want: discovery.ErrInvalidInput},
		{name: "missing_owner", setup: func(reader *sdkTransferReader, _ *unstructured.Unstructured) {
			missing := sdkObject(sdkBundleGVK, "team", "missing", "missing", nil)
			reader.pods[1].OwnerReferences = []metav1.OwnerReference{sdkOwner(missing)}
		}, want: ownership.ErrMissing},
		{name: "unlisted_pod_owner", setup: func(reader *sdkTransferReader, root *unstructured.Unstructured) {
			missing := sdkObject(sdkPodGVK, "team", "missing", "missing", root)
			reader.addObject(missing)
			reader.pods[1].OwnerReferences = []metav1.OwnerReference{sdkOwner(missing)}
		}, want: discovery.ErrStaleInput},
		{name: "alias_resource_version", setup: func(reader *sdkTransferReader, root *unstructured.Unstructured) {
			alias := root.DeepCopy()
			alias.SetAPIVersion(sdkBundleGVK.Group + "/v2")
			alias.SetResourceVersion("2")
			reader.addObject(alias)
			reader.pods[1].OwnerReferences = []metav1.OwnerReference{sdkOwner(alias)}
		}, want: discovery.ErrStaleInput},
		{name: "alias_owners", setup: func(reader *sdkTransferReader, root *unstructured.Unstructured) {
			alias := root.DeepCopy()
			alias.SetAPIVersion(sdkBundleGVK.Group + "/v2")
			alias.SetOwnerReferences([]metav1.OwnerReference{sdkOwner(sdkObject(sdkBundleGVK, "team", "other", "other", nil))})
			reader.addObject(alias)
			reader.pods[1].OwnerReferences = []metav1.OwnerReference{sdkOwner(alias)}
		}, want: discovery.ErrStaleInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, acquisition := range []string{"collect", "direct"} {
				t.Run(acquisition, func(t *testing.T) {
					root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
					reader := &sdkTransferReader{sdkReader: sdkNewReader(t, []*unstructured.Unstructured{root}, sdkPod("first", root, nil), sdkPod("second", root, nil))}
					test.setup(reader, root)
					var err error
					if acquisition == "collect" {
						var dataset discovery.Dataset
						dataset, err = discovery.Collect(context.Background(), reader, "team")
						if !reflect.DeepEqual(dataset, discovery.Dataset{}) {
							t.Fatal("Collect returned a partial dataset")
						}
					} else {
						var workload *discovery.Workload
						workload, err = sdkResolver(t, reader).Workload(context.Background(), sdkRef(root))
						if workload != nil {
							t.Fatal("Workload returned a partial observation")
						}
					}
					if !errors.Is(err, test.want) {
						t.Fatalf("%s error = %v, want %v", acquisition, err, test.want)
					}
					if reader.transfers != 1 || reader.fallbackLists != 0 {
						t.Fatalf("transfer calls = %d, fallback calls = %d, want 1, 0", reader.transfers, reader.fallbackLists)
					}
				})
			}
		})
	}
}

func TestPodTransferValidatesPreviouslyAcquiredPod(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*corev1.Pod)
	}{
		{name: "uid", mutate: func(pod *corev1.Pod) { pod.UID = "replacement" }},
		{name: "resource_version", mutate: func(pod *corev1.Pod) { pod.ResourceVersion = "2" }},
		{name: "owners", mutate: func(pod *corev1.Pod) { pod.OwnerReferences = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
			seed := sdkPod("seed", root, nil)
			reader := &sdkTransferReader{sdkReader: sdkNewReader(t, []*unstructured.Unstructured{root}, seed)}
			reader.mutateList = func(list *corev1.PodList) { test.mutate(&list.Items[0]) }
			workload, err := sdkResolver(t, reader).WorkloadForPod(context.Background(), sdkPodRef(seed))
			if workload != nil || !errors.Is(err, discovery.ErrStaleInput) {
				t.Fatalf("WorkloadForPod = %v, %v, want nil, ErrStaleInput", workload, err)
			}
			if reader.transfers != 1 || reader.fallbackLists != 0 {
				t.Fatalf("transfer calls = %d, fallback calls = %d, want 1, 0", reader.transfers, reader.fallbackLists)
			}
		})
	}
}

func TestPodTransferPreservesEmptyShapes(t *testing.T) {
	for _, shape := range []string{"nil_items", "empty_items"} {
		t.Run(shape, func(t *testing.T) {
			root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
			reader := &sdkTransferReader{sdkReader: sdkNewReader(t, []*unstructured.Unstructured{root})}
			if shape == "empty_items" {
				reader.mutateList = func(list *corev1.PodList) { list.Items = []corev1.Pod{} }
			}
			dataset, err := discovery.Collect(context.Background(), reader, "team", sdkRef(root))
			if err != nil || dataset.Pods != nil || len(dataset.Objects) != 1 || dataset.PodResourceVersion != "list-rv" {
				t.Fatalf("empty Collect = %+v, %v, want nil Pods and explicit root", dataset, err)
			}
			workload, err := sdkResolver(t, reader).Workload(context.Background(), sdkRef(root))
			if err != nil {
				t.Fatal(err)
			}
			if pods := workload.Pods(); pods == nil || len(pods) != 0 {
				t.Fatalf("empty workload Pods = %+v, want non-nil empty slice", pods)
			}
			if reader.transfers != 2 || reader.fallbackLists != 0 {
				t.Fatalf("transfer calls = %d, fallback calls = %d, want 2, 0", reader.transfers, reader.fallbackLists)
			}
		})
	}
}

func (r *sdkArrayTransferReader) ListPodsForTransfer(ctx context.Context, namespace string) (*corev1.PodList, error) {
	list, err := r.ListPods(ctx, namespace)
	if err != nil {
		return nil, err
	}
	storage := make([]corev1.Pod, 256)
	r.arrayStart = reflect.ValueOf(&storage[0]).Pointer()
	r.arrayEnd = r.arrayStart + uintptr(len(storage))*reflect.TypeOf(corev1.Pod{}).Size()
	switch r.shape {
	case "oversized":
		storage[0] = list.Items[0]
		list.Items = storage[:1]
	case "clipped_prefix":
		storage[0] = list.Items[0]
		list.Items = storage[:1:1]
	case "clipped_suffix":
		last := len(storage) - 1
		storage[last] = list.Items[0]
		list.Items = storage[last : last+1 : last+1]
	}
	r.containerAddress = reflect.ValueOf(&list.Items[0].Spec.Containers[0]).Pointer()
	return list, nil
}

func TestPodTransferDetachesOversizedBackingArrays(t *testing.T) {
	for _, shape := range []string{"oversized", "clipped_prefix", "clipped_suffix"} {
		t.Run(shape, func(t *testing.T) {
			for _, acquisition := range []string{"collect", "all_selected_workload"} {
				t.Run(acquisition, func(t *testing.T) {
					root := sdkObject(sdkBundleGVK, "team", "root", "root", nil)
					pod := sdkPod("pod", root, map[string]string{"snapshot": "original"})
					reader := &sdkArrayTransferReader{sdkReader: sdkNewReader(t, []*unstructured.Unstructured{root}, pod), shape: shape}
					var retained reflect.Value
					if acquisition == "collect" {
						dataset, err := discovery.Collect(context.Background(), reader, "team")
						if err != nil {
							t.Fatal(err)
						}
						if len(dataset.Pods) != 1 || !reflect.DeepEqual(dataset.Pods[0], pod) {
							t.Fatal("transferred dataset lost Pod data")
						}
						retained = reflect.ValueOf(dataset.Pods[0])
					} else {
						workload, err := sdkResolver(t, reader).Workload(context.Background(), sdkRef(root))
						if err != nil {
							t.Fatal(err)
						}
						if observed := workload.Pods(); len(observed) != 1 || !reflect.DeepEqual(observed[0], pod) {
							t.Fatal("transferred workload lost Pod data")
						}
						// Public accessors copy Pods, so inspect only the retained address.
						retained = reflect.ValueOf(workload).Elem().FieldByName("pods").Index(0)
					}
					address := retained.Pointer()
					if address >= reader.arrayStart && address < reader.arrayEnd {
						t.Fatal("observation retained the transferred backing array and inaccessible Pods")
					}
					container := retained.Elem().FieldByName("Spec").FieldByName("Containers").Index(0).Addr().Pointer()
					if container != reader.containerAddress {
						t.Fatal("compaction deep-copied exclusively owned nested Pod data")
					}
				})
			}
		})
	}
}
