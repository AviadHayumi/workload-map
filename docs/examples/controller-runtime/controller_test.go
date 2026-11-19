// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

func TestTemplateLabelPatchPreservesUnknownFields(t *testing.T) {
	definition := &api.Karta{Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{
		RootComponent:   api.ComponentDefinition{Name: "root", Kind: &api.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, StatusDefinition: &api.StatusDefinition{}},
		ChildComponents: []api.ComponentDefinition{{Name: "pod", OwnerRef: ptr.To("root"), Kind: &api.GroupVersionKind{Version: "v1", Kind: "Pod"}, SpecDefinition: &api.SpecDefinition{PodTemplateSpec: &api.ValueAccessor{Read: &api.ReadAccessor{Expression: "object.spec.template"}, Write: &api.WriteAccessor{Path: ptr.To("/spec/template")}}}}},
	}}}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "team-a"}, "spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"existing": "keep"}, "unknownMetadata": "keep"},
		"spec":     map[string]any{"unknownSpec": "keep", "containers": []any{map[string]any{"name": "worker", "image": "example:v1", "unknownContainer": "keep"}}},
	}}}}
	original := object.DeepCopy()
	factory := resource.NewComponentFactoryFromObject(definition, object)
	components, err := allComponents(factory)
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := injectPodTemplateLabels(context.Background(), factory, components, nil)
	if err != nil || !changed {
		t.Fatalf("label patch: changed=%v err=%v", changed, err)
	}
	if !reflect.DeepEqual(original.Object, object.Object) {
		t.Fatal("input object mutated")
	}
	labels, _, err := unstructured.NestedStringMap(updated.Object, "spec", "template", "metadata", "labels")
	if err != nil {
		t.Fatal(err)
	}
	if labels["existing"] != "keep" || labels[managedByLabelKey] != managedByLabelValue {
		t.Fatalf("labels: %v", labels)
	}
	for _, path := range [][]string{{"spec", "template", "metadata", "unknownMetadata"}, {"spec", "template", "spec", "unknownSpec"}} {
		value, _, _ := unstructured.NestedString(updated.Object, path...)
		if value != "keep" {
			t.Fatalf("unknown field lost: %v", path)
		}
	}
	containers, _, _ := unstructured.NestedSlice(updated.Object, "spec", "template", "spec", "containers")
	if containers[0].(map[string]any)["unknownContainer"] != "keep" {
		t.Fatal("container replaced")
	}
	next := resource.NewComponentFactoryFromObject(definition, updated)
	components, err = allComponents(next)
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err = injectPodTemplateLabels(context.Background(), next, components, nil)
	if err != nil || changed {
		t.Fatalf("not idempotent: changed=%v err=%v", changed, err)
	}
}
