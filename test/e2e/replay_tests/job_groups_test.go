// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package replay

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/instructions"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/membership"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRecordedJobSchedulingGroups(t *testing.T) {
	data, err := os.ReadFile("testdata/grouping/jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Jobs []unstructured.Unstructured
		Pods []corev1.Pod
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatal(err)
	}
	def := kartas.BatchJob()
	input := membership.Input{ScopeComplete: true, Pods: frame.Pods}
	for i := range frame.Jobs {
		input.Workloads = append(input.Workloads, membership.WorkloadInput{Object: &frame.Jobs[i], Definition: def})
	}
	resolved, err := membership.Resolve(t.Context(), input, membership.WithSnapshot(membership.Snapshot{Complete: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Complete || len(resolved.Diagnostics) != 0 {
		t.Fatalf("incomplete grouping: %v", resolved.Diagnostics)
	}
	summary, err := instructions.NewStructureSummary(def)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"train": 2, "evaluate": 1}
	if len(frame.Jobs) != len(want) || len(frame.Pods) != 3 {
		t.Fatal("expected both Jobs and all three Pods")
	}
	for _, job := range frame.Jobs {
		name := job.GetName()
		count, ok := want[name]
		if !ok {
			t.Fatalf("unexpected Job %q", name)
		}
		delete(want, name)
		factory := resource.NewComponentFactoryFromObject(def, &job)
		pods := resolved.Pods[membership.ComponentKey{Workload: job.GetUID(), Component: "job"}]
		if len(pods) != count {
			t.Fatalf("%s owns %d Pods, want %d", name, len(pods), count)
		}
		for _, pod := range pods {
			query := factory.NewPodQuerier(&pod)
			component, err := instructions.InferPodComponent(t.Context(), query, summary)
			if err != nil {
				t.Fatal(err)
			}
			group, err := instructions.GetPodGroupingEffectiveComponent(t.Context(), query, component, summary)
			if err != nil || group == nil || group.PodGroupName != "job" {
				t.Fatalf("group for %s: %v, %v", pod.Name, group, err)
			}
			keys, err := query.ExtractGroupKeysFor(t.Context(), *group.MemberDefinition)
			if err != nil || !slices.Equal(keys, []string{name}) {
				t.Fatalf("%s keys %v, want [%s]: %v", pod.Name, keys, name, err)
			}
			required, err := instructions.CalculateSubtreeScale(t.Context(), group.EffectiveComponent, nil, factory, summary)
			if err != nil || int(required) != count {
				t.Fatalf("%s requires %d Pods, want %d: %v", name, required, count, err)
			}
		}
	}
}
