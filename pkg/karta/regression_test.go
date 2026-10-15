// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package karta_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
	"github.com/dsx-ai-factory/workload-map/pkg/karta/kartatest"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

type nilCopyObject struct{ unstructured.Unstructured }

func (*nilCopyObject) DeepCopyObject() runtime.Object {
	return (*unstructured.Unstructured)(nil)
}

type numberObject struct {
	unstructured.Unstructured
	Number any
}

func (o *numberObject) DeepCopyObject() runtime.Object {
	return &numberObject{Unstructured: *o.DeepCopy(), Number: o.Number}
}

func (o *numberObject) MarshalJSON() ([]byte, error) {
	copy := o.DeepCopy()
	copy.Object["number"] = o.Number
	return copy.MarshalJSON()
}

func TestNewRejectsNilObjects(t *testing.T) {
	for name, object := range map[string]resource.KubernetesObject{
		"nil":          nil,
		"unstructured": (*unstructured.Unstructured)(nil),
		"typed":        (*corev1.Pod)(nil),
		"nil_copy":     &nilCopyObject{},
	} {
		t.Run(name, func(t *testing.T) {
			workload, err := karta.New(templateDefinition(), object)
			if err == nil || workload != nil {
				t.Fatalf("New(%T) = %v, %v; want nil workload and error", object, workload, err)
			}
		})
	}
}

func TestSplitRoutesOnlyRequestedFields(t *testing.T) {
	for _, test := range []struct {
		name        string
		specPath    string
		metaPath    string
		field       karta.PodField
		patch       karta.PodPatch
		unsupported karta.PodPatch
		path        []string
		want        any
	}{
		{
			name: "spec", specPath: ".spec.podSpec", metaPath: ".spec.podMetadata | .",
			field: karta.PodFieldSchedulerName, patch: karta.PodPatch{SchedulerName: ptr.To("custom")},
			unsupported: karta.PodPatch{Labels: map[string]string{"team": "ml"}},
			path:        []string{"spec", "podSpec", "schedulerName"}, want: "custom",
		},
		{
			name: "metadata", specPath: ".spec.podSpec | .", metaPath: ".spec.podMetadata",
			field: karta.PodFieldLabels, patch: karta.PodPatch{Labels: map[string]string{"team": "ml"}},
			unsupported: karta.PodPatch{SchedulerName: ptr.To("custom")},
			path:        []string{"spec", "podMetadata", "labels"}, want: map[string]any{"existing": "yes", "team": "ml"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := splitDefinition()
			definition.Spec.StructureDefinition.RootComponent.SpecDefinition.PodSpecPath = &test.specPath
			definition.Spec.StructureDefinition.RootComponent.SpecDefinition.MetadataPath = &test.metaPath
			if fields := karta.WritablePodFields(definition.Spec.StructureDefinition.RootComponent); !slices.Contains(fields, test.field) {
				t.Fatalf("WritablePodFields() = %v, want %s", fields, test.field)
			}
			workload, err := karta.New(definition, podSpecWorkload())
			if err != nil {
				t.Fatal(err)
			}
			if err := workload.UpdatePodTemplate(t.Context(), "root", test.patch); err != nil {
				t.Fatalf("UpdatePodTemplate(%s) = %v", test.field, err)
			}
			before, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := unstructured.NestedFieldNoCopy(before.(*unstructured.Unstructured).Object, test.path...)
			if err != nil || !found {
				t.Fatalf("read updated field = %v, %v", found, err)
			}
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("updated field mismatch (-want +got):\n%s", diff)
			}
			err = workload.UpdatePodTemplate(t.Context(), "root", test.unsupported)
			var unsupported *karta.UnsupportedFieldsError
			if !errors.Is(err, karta.ErrNotSupported) || !errors.As(err, &unsupported) {
				t.Fatalf("unsupported update = %v, want UnsupportedFieldsError", err)
			}
			after, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(before, after); diff != "" {
				t.Fatalf("rejected update changed object (-before +after):\n%s", diff)
			}
		})
	}
}

func TestFakeOwnsDefinitionCapabilitiesAndRecordedInputs(t *testing.T) {
	definition := templateDefinition()
	fake := kartatest.NewFromKarta(definition)
	definition.Spec.StructureDefinition.RootComponent.SpecDefinition.PodTemplateSpecPath = ptr.To(".spec.changed | .")
	infos := fake.Components()
	infos[0].Name = "changed"
	infos[0].PodFields[0] = "changed"
	if diff := cmp.Diff(karta.ComponentInfos(templateDefinition()), fake.Components()); diff != "" {
		t.Fatalf("Components() mismatch (-want +got):\n%s", diff)
	}
	labels := map[string]any{"team": "ml"}
	container := map[string]any{"name": "main", "image": "app:v2"}
	patch := karta.Patch{
		"metadata": map[string]any{"labels": labels},
		"spec":     map[string]any{"containers": []any{container}},
	}
	ids := []string{"worker"}
	if err := fake.UpdatePodTemplate(t.Context(), "root", patch, func(options *karta.UpdateOptions) { options.Instances = ids }); err != nil {
		t.Fatal(err)
	}
	labels["team"] = "changed"
	container["image"] = "changed"
	ids[0] = "changed"
	want := []kartatest.PodUpdate{{
		Component: "root",
		Patch: karta.Patch{
			"metadata": map[string]any{"labels": map[string]any{"team": "ml"}},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "main", "image": "app:v2"}}},
		},
		Instances: []string{"worker"},
	}}
	if diff := cmp.Diff(want, fake.PodUpdates); diff != "" {
		t.Fatalf("recorded updates mismatch (-want +got):\n%s", diff)
	}
}

func TestWorkloadOwnsInputsAndReturnedValues(t *testing.T) {
	definition, object := templateDefinition(), templateWorkload()
	workload, err := karta.New(definition, object)
	if err != nil {
		t.Fatal(err)
	}
	definition.Spec.StructureDefinition.RootComponent.Name = "changed"
	object.SetName("changed")
	returned, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	returned.SetName("changed-output")
	infos := workload.Components()
	infos[0].Name = "changed-output"
	infos[0].PodFields[0] = "changed-output"
	if err := workload.UpdatePodTemplate(t.Context(), "root", karta.PodPatch{SchedulerName: ptr.To("custom")}); err != nil {
		t.Fatal(err)
	}
	current, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	if current.GetName() != "job" {
		t.Fatalf("Object().GetName() = %q, want job", current.GetName())
	}
	if diff := cmp.Diff(karta.ComponentInfos(templateDefinition()), workload.Components()); diff != "" {
		t.Fatalf("Components() mismatch (-want +got):\n%s", diff)
	}
}

func TestIntegerPrecisionBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  any
		reject bool
	}{
		{name: "positive_boundary", value: int64(1 << 53)},
		{name: "negative_boundary", value: int64(-(1 << 53))},
		{name: "unsigned_boundary", value: uint64(1 << 53)},
		{name: "large_signed", value: int64(1<<53 + 1), reject: true},
		{name: "large_negative", value: int64(-(1<<53 + 1)), reject: true},
		{name: "large_unsigned", value: uint64(1 << 63), reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Typed objects can carry unsigned JSON integers; an unstructured
			// object itself only supports Kubernetes JSON value types.
			object := &numberObject{Unstructured: *templateWorkload(), Number: test.value}
			workload, err := karta.New(templateDefinition(), object)
			if test.reject {
				if err == nil || !strings.Contains(err.Error(), "outside the supported range") || workload != nil {
					t.Fatalf("New(%v) = %v, %v; want precision error", test.value, workload, err)
				}
			} else if err != nil {
				t.Fatalf("New(%v) = %v", test.value, err)
			}
			if diff := cmp.Diff(test.value, object.Number); diff != "" {
				t.Fatalf("New changed input (-before +after):\n%s", diff)
			}
		})
	}
}

func TestOversizedPatchAndSuspendValuesAreRejected(t *testing.T) {
	workload, err := karta.New(templateDefinition(), templateWorkload())
	if err != nil {
		t.Fatal(err)
	}
	before, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	patch := karta.Patch{"spec": map[string]any{"terminationGracePeriodSeconds": int64(1<<53 + 1)}}
	if err := workload.UpdatePodTemplate(t.Context(), "root", patch); err == nil || !strings.Contains(err.Error(), "outside the supported range") {
		t.Fatalf("oversized patch = %v, want precision error", err)
	}
	after, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(before, after); diff != "" {
		t.Fatalf("rejected patch changed object (-before +after):\n%s", diff)
	}
	definition := templateDefinition()
	definition.Spec.StructureDefinition.RootComponent.SuspendDefinition = &v1alpha1.SuspendDefinition{
		SuspendActions: []v1alpha1.SuspendAction{{Path: ".spec.counter", Value: "9007199254740993"}},
	}
	if _, err := karta.New(definition, templateWorkload()); err == nil || !strings.Contains(err.Error(), "outside the supported range") {
		t.Fatalf("oversized suspend value = %v, want precision error", err)
	}
}

func TestCanceledUpdateIsAtomic(t *testing.T) {
	workload, err := karta.New(templateDefinition(), templateWorkload())
	if err != nil {
		t.Fatal(err)
	}
	before, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := workload.UpdatePodTemplate(ctx, "root", karta.PodPatch{SchedulerName: ptr.To("custom")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update = %v, want context.Canceled", err)
	}
	after, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(before, after); diff != "" {
		t.Fatalf("canceled update changed object (-before +after):\n%s", diff)
	}
}
