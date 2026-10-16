// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package karta_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func TestSelectorOnlyContainerUpdatesReturnEmptyPatch(t *testing.T) {
	for name, update := range map[string]karta.PodTemplateUpdate{
		"typed_name_only":   karta.PodPatch{Containers: []karta.ContainerPatch{{Name: "main"}}},
		"typed_empty_entry": karta.PodPatch{Containers: []karta.ContainerPatch{{}}},
		"raw_name_only": karta.Patch{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "main"}},
		}},
		"raw_empty_entry": karta.Patch{"spec": map[string]any{
			"containers": []any{map[string]any{}},
		}},
		"raw_empty_list": karta.Patch{"spec": map[string]any{"containers": []any{}}},
	} {
		t.Run(name, func(t *testing.T) {
			definition, input := templateDefinition(), templateWorkload()
			original := input.DeepCopy()
			workload, err := karta.New(definition, input)
			if err != nil {
				t.Fatal(err)
			}
			before, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			if err := workload.UpdatePodTemplate(t.Context(), "root", update); !errors.Is(err, karta.ErrEmptyPatch) {
				t.Errorf("UpdatePodTemplate() = %v, want ErrEmptyPatch", err)
			}
			after, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(before, after); diff != "" {
				t.Errorf("empty patch changed the workload (-before +after):\n%s", diff)
			}
			if diff := cmp.Diff(original, input); diff != "" {
				t.Errorf("empty patch changed the input (-before +after):\n%s", diff)
			}
			if err := karta.ValidatePodTemplateUpdate(definition.Spec.StructureDefinition.RootComponent, "root", update); !errors.Is(err, karta.ErrEmptyPatch) {
				t.Errorf("ValidatePodTemplateUpdate() = %v, want ErrEmptyPatch", err)
			}
		})
	}
}

func TestContainerSelectorsDoNotDiscardActualUpdates(t *testing.T) {
	for _, test := range []struct {
		name     string
		update   karta.PodTemplateUpdate
		sidecar  bool
		wantPath []string
		want     any
	}{
		{
			name: "typed_scheduler_and_selector",
			update: karta.PodPatch{
				SchedulerName: ptr.To("custom"),
				Containers:    []karta.ContainerPatch{{Name: "main"}},
			},
			wantPath: []string{"spec", "template", "spec", "schedulerName"}, want: "custom",
		},
		{
			name: "raw_scheduler_and_selector",
			update: karta.Patch{"spec": map[string]any{
				"schedulerName": "custom",
				"containers":    []any{map[string]any{"name": "main"}},
			}},
			wantPath: []string{"spec", "template", "spec", "schedulerName"}, want: "custom",
		},
		{
			name: "typed_image_and_selector",
			update: karta.PodPatch{Containers: []karta.ContainerPatch{
				{Name: "main"}, {Name: "sidecar", Image: ptr.To("sidecar:v2")},
			}},
			sidecar: true, wantPath: []string{"spec", "template", "spec", "containers"},
			want: []any{map[string]any{"name": "main", "image": "app:v1"}, map[string]any{"name": "sidecar", "image": "sidecar:v2"}},
		},
		{
			name: "raw_image_and_selector",
			update: karta.Patch{"spec": map[string]any{"containers": []any{
				map[string]any{"name": "main"}, map[string]any{"name": "sidecar", "image": "sidecar:v2"},
			}}},
			sidecar: true, wantPath: []string{"spec", "template", "spec", "containers"},
			want: []any{map[string]any{"name": "main", "image": "app:v1"}, map[string]any{"name": "sidecar", "image": "sidecar:v2"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition, input := templateDefinition(), templateWorkload()
			if test.sidecar {
				containers := []any{map[string]any{"name": "main", "image": "app:v1"}, map[string]any{"name": "sidecar", "image": "sidecar:v1"}}
				if err := unstructured.SetNestedSlice(input.Object, containers, "spec", "template", "spec", "containers"); err != nil {
					t.Fatal(err)
				}
			}
			original := input.DeepCopy()
			want := input.DeepCopy()
			if err := unstructured.SetNestedField(want.Object, test.want, test.wantPath...); err != nil {
				t.Fatal(err)
			}
			workload, err := karta.New(definition, input)
			if err != nil {
				t.Fatal(err)
			}
			if err := karta.ValidatePodTemplateUpdate(definition.Spec.StructureDefinition.RootComponent, "root", test.update); err != nil {
				t.Fatalf("ValidatePodTemplateUpdate() = %v", err)
			}
			if err := workload.UpdatePodTemplate(t.Context(), "root", test.update); err != nil {
				t.Fatalf("UpdatePodTemplate() = %v", err)
			}
			got, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got.(*unstructured.Unstructured)); diff != "" {
				t.Errorf("updated object mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(original, input); diff != "" {
				t.Errorf("update changed the input (-before +after):\n%s", diff)
			}
		})
	}
}

func TestMixedContainerUpdateRetainsSelectorValidation(t *testing.T) {
	for name, update := range map[string]karta.PodTemplateUpdate{
		"typed": karta.PodPatch{SchedulerName: ptr.To("custom"), Containers: []karta.ContainerPatch{{Name: "absent"}}},
		"raw": karta.Patch{"spec": map[string]any{
			"schedulerName": "custom", "containers": []any{map[string]any{"name": "absent"}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			workload, err := karta.New(templateDefinition(), templateWorkload())
			if err != nil {
				t.Fatal(err)
			}
			before, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			if err := workload.UpdatePodTemplate(t.Context(), "root", update); err == nil || errors.Is(err, karta.ErrEmptyPatch) {
				t.Fatalf("unknown container selector returned %v", err)
			}
			after, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(before, after); diff != "" {
				t.Fatalf("invalid selector partially changed the object (-before +after):\n%s", diff)
			}
		})
	}
}
