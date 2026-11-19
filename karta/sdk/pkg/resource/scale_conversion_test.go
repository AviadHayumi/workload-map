// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

func TestScaleAnnotationConversionAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]any
		minimum     int32
		maximum     *int32
		wantError   bool
	}{
		{name: "numeric strings", annotations: map[string]any{"autoscaling.knative.dev/min-scale": "2", "autoscaling.knative.dev/max-scale": "3"}, minimum: 2, maximum: func() *int32 { value := int32(3); return &value }()},
		{name: "missing annotations", annotations: map[string]any{}, minimum: 1},
		{name: "malformed numeric string", annotations: map[string]any{"autoscaling.knative.dev/min-scale": "abc"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "serving.knative.dev/v1", "kind": "Service", "metadata": map[string]any{"name": "inference"}, "spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": tc.annotations}}}}}
			factory := NewComponentFactoryFromObject(kartas.KnativeServing(), object)
			component, err := factory.GetComponent("revision")
			if err != nil {
				t.Fatal(err)
			}
			scales, err := component.GetScale(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("scale error=%v", err)
			}
			if tc.wantError {
				return
			}
			scale := scales[""]
			if scale.MinReplicas == nil || *scale.MinReplicas != tc.minimum {
				t.Fatalf("min replicas=%v", scale.MinReplicas)
			}
			if (scale.MaxReplicas == nil) != (tc.maximum == nil) || (scale.MaxReplicas != nil && *scale.MaxReplicas != *tc.maximum) {
				t.Fatalf("max replicas=%v", scale.MaxReplicas)
			}
		})
	}
}
