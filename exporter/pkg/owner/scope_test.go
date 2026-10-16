// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package owner

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestResolveOwnerScope(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Owner"}
	for _, tc := range []struct {
		name, childNamespace, parentNamespace, grandparentNamespace string
		found                                                       bool
	}{
		{"namespaced to cluster", "team", "", "", true},
		{"same namespace", "team", "team", "team", true},
		{"cross namespace", "team", "other", "other", false},
		{"cluster to namespaced", "", "team", "team", false},
		{"cluster intermediate to namespaced", "team", "", "team", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := New()
			grandparent := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "grandparent", Namespace: tc.grandparentNamespace, UID: "grandparent"}}
			parent := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: tc.parentNamespace, UID: "parent", OwnerReferences: []metav1.OwnerReference{{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: "grandparent", UID: "grandparent", Controller: ptr.To(true)}}}}
			index.UpsertTrackedObject(grandparent, gvk)
			index.UpsertTrackedObject(parent, gvk)
			result := index.Resolve([]metav1.OwnerReference{{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: "parent", UID: "parent", Controller: ptr.To(true)}}, func(gk schema.GroupKind) bool { return gk == gvk.GroupKind() }, nil, nil, tc.childNamespace)
			if (result.Outcome == OutcomeFound) != tc.found {
				t.Fatalf("outcome = %v, want found %v", result.Outcome, tc.found)
			}
			if tc.found && (result.Root.UID != types.UID("grandparent") || result.Root.Namespace != tc.grandparentNamespace) {
				t.Fatalf("wrong root: %+v", result.Root)
			}
		})
	}
}
