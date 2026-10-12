// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package registry

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/owner"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
)

func TestCatalogOwnershipChains(t *testing.T) {
	knative := []schema.GroupVersionKind{
		{Group: "serving.knative.dev", Version: "v1", Kind: "Service"},
		{Group: "serving.knative.dev", Version: "v1", Kind: "Configuration"},
		{Group: "serving.knative.dev", Version: "v1", Kind: "Revision"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "apps", Version: "v1", Kind: "ReplicaSet"},
	}
	for _, tc := range []struct {
		name  string
		karta *v1alpha1.Karta
		chain []schema.GroupVersionKind
	}{
		{"knative", kartas.KnativeServing(), knative},
		{"kserve serverless", kartas.KServe(), append([]schema.GroupVersionKind{{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"}}, knative...)},
		{"rayjob", kartas.Rayjob(), []schema.GroupVersionKind{{Group: "ray.io", Version: "v1", Kind: "RayJob"}, {Group: "ray.io", Version: "v1", Kind: "RayCluster"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry, err := newEntry(tc.karta)
			if err != nil {
				t.Fatal(err)
			}
			watched := map[schema.GroupVersionKind]bool{entry.RootGVK: true, {Group: "apps", Version: "v1", Kind: "ReplicaSet"}: true}
			for _, kind := range entry.ChildKinds {
				watched[kind] = true
			}
			index := owner.New()
			var refs []metav1.OwnerReference
			for _, kind := range tc.chain {
				uid := types.UID(kind.Group + "/" + kind.Kind)
				if watched[kind] {
					index.UpsertObject(uid, refs)
				}
				refs = []metav1.OwnerReference{{APIVersion: kind.GroupVersion().String(), Kind: kind.Kind, Name: kind.Kind, UID: uid, Controller: ptr.To(true)}}
			}
			got := index.RootFor(refs, func(gk schema.GroupKind) bool { return gk == entry.RootGVK.GroupKind() })
			if got.Outcome != owner.OutcomeFound || got.Root.GVK != entry.RootGVK {
				t.Fatalf("root walk = %+v, want %s", got, entry.RootGVK)
			}
		})
	}
}
