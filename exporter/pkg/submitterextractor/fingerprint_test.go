// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package submitterextractor

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestFingerprintFieldBoundaries(t *testing.T) {
	obj := &metav1.ObjectMeta{
		Name: "one", Namespace: "ns",
		Labels:      map[string]string{"a": "bc", "ab": "c"},
		Annotations: map[string]string{"user": "alice"},
	}
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	before := fingerprint(obj, gvk)
	for name, change := range map[string]func(*metav1.ObjectMeta){
		"value_boundaries": func(o *metav1.ObjectMeta) { o.Labels = map[string]string{"a": "b", "ab": "cc"} },
		"map_boundaries": func(o *metav1.ObjectMeta) {
			o.Labels["user"] = o.Annotations["user"]
			o.Annotations = nil
		},
		"resource_boundaries": func(o *metav1.ObjectMeta) { o.Namespace, o.Name = "n", "sone" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := obj.DeepCopy()
			change(copy)
			if fingerprint(copy, gvk) == before {
				t.Fatal("fingerprint ignored field boundaries")
			}
		})
	}
	copy := obj.DeepCopy()
	copy.Labels = map[string]string{"ab": "c", "a": "bc"}
	copy.ResourceVersion = "new"
	copy.Generation = 99
	copy.UID = "replacement"
	if fingerprint(copy, gvk) != before {
		t.Fatal("non-CEL metadata or map order changed fingerprint")
	}
	if fingerprint(obj, schema.GroupVersionKind{Version: "v2", Kind: "Pod"}) == before {
		t.Fatal("fingerprint ignored version input")
	}
	empty := &metav1.ObjectMeta{}
	withMaps := &metav1.ObjectMeta{Labels: map[string]string{}, Annotations: map[string]string{}}
	if fingerprint(empty, gvk) != fingerprint(withMaps, gvk) {
		t.Fatal("nil and empty maps evaluate identically but hash differently")
	}
}
