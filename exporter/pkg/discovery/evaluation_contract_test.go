// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"slices"
	"testing"

	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

func TestStatusAndMappingDoNotEvaluateScaleExpressions(t *testing.T) {
	definition := kartas.Jobset()
	unused := `error("scale is not requested")`
	definition.Spec.StructureDefinition.RootComponent.ScaleDefinition = &v1alpha1.ScaleDefinition{ReplicasPath: &unused}
	definition.Spec.StructureDefinition.ChildComponents[0].ScaleDefinition.ReplicasPath = ptr.To(unused)
	d, workload, pod, _ := evaluationFixture(t, definition, jobsetBody(),
		map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "2"}, nil)
	batch := d.NewEvaluationBatch()
	status, err := workload.Status(t.Context(), batch)
	if err != nil || !status.Defined || !slices.Contains(status.Matched, v1alpha1.RunningStatus) {
		t.Fatalf("status evaluated an unused scale expression: %+v, %v", status, err)
	}
	mapping, err := workload.PodMapping(t.Context(), pod, batch)
	if err != nil || mapping.Component != "replicatedjob" || mapping.Instance != "trainer" || mapping.Group != "2" {
		t.Fatalf("mapping evaluated an unused scale expression: %+v, %v", mapping, err)
	}
}

func TestStatusMatchesSuspendedCondition(t *testing.T) {
	body := map[string]any{"status": map[string]any{
		"conditions":           []any{map[string]any{"type": "Suspended", "status": "True"}},
		"replicatedJobsStatus": []any{},
	}}
	d, workload, _, _ := evaluationFixture(t, kartas.Jobset(), body, nil, nil)
	status, err := workload.Status(t.Context(), d.NewEvaluationBatch())
	if err != nil || !status.Defined || !slices.Equal(status.Matched, []v1alpha1.ResourceStatus{v1alpha1.SuspendedStatus}) || !d.IsCurrent(status.State()) {
		t.Fatalf("condition status = %+v, err = %v", status, err)
	}
}
