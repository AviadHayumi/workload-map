// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

func TestCollectedPodCoordinatesRetainExactGVK(t *testing.T) {
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "example.org", Version: "v1", Kind: "Pod"},
		{Version: "v2", Kind: "Pod"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
	} {
		t.Run(gvk.String(), func(t *testing.T) {
			root := sdkObject(gvk, "team", "shared", "root-uid", nil)
			pod := sdkPod("shared", root, nil)
			reader := sdkNewReader(t, []*unstructured.Unstructured{root}, pod)
			dataset, err := discovery.Collect(t.Context(), reader, "team")
			if err != nil {
				t.Fatal(err)
			}
			if len(dataset.Objects) != 1 || !reflect.DeepEqual(dataset.Objects[0], root) || len(dataset.Pods) != 1 || !reflect.DeepEqual(dataset.Pods[0], pod) {
				t.Fatal("same-name Pod replaced a different GVK or changed its complete body")
			}
			failure := errors.New("owner read failed")
			reader.getErrors[root.GetUID()] = failure
			if _, err := discovery.Collect(t.Context(), reader, "team"); !errors.Is(err, failure) {
				t.Fatalf("foreign owner read failure was hidden: %v", err)
			}
		})
	}
}

func TestCollectedPodCoordinatesRetainNamespace(t *testing.T) {
	a, b := sdkPod("shared", nil, nil), sdkPod("shared", nil, nil)
	b.Namespace, b.UID = "other", "other-uid"
	reader := sdkNewReader(t, nil, a, b)
	dataset, err := discovery.Collect(t.Context(), reader, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Pods) != 2 {
		t.Fatalf("same-name Pods across namespaces collapsed: %d", len(dataset.Pods))
	}
	byNamespace := make(map[string]string)
	for _, pod := range dataset.Pods {
		byNamespace[pod.Namespace] = string(pod.UID)
	}
	if byNamespace[a.Namespace] != string(a.UID) || byNamespace[b.Namespace] != string(b.UID) {
		t.Fatal("namespace lookup returned a different Pod lifetime")
	}
}
