// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func TestSchedulingGroupsJobSet(t *testing.T) {
	dataset, root := jobSetDataset()
	observed := groupingObservation(t, dataset, root)
	groups, err := workloadinterface.SchedulingGroups(t.Context(), observed)
	if err != nil {
		t.Fatal(err)
	}
	want := []workloadinterface.SchedulingGroup{
		{Name: "job", Keys: []string{"evaluation"}, Pods: []*corev1.Pod{dataset.Pods[2]}},
		{Name: "job", Keys: []string{"training"}, Pods: []*corev1.Pod{dataset.Pods[0], dataset.Pods[1]}},
	}
	if diff := cmp.Diff(want, groups); diff != "" {
		t.Fatalf("SchedulingGroups() mismatch (-want +got):\n%s", diff)
	}
	groups[0].Keys[0] = "changed"
	groups[0].Pods[0].Labels["changed"] = "true"
	groups[0].Pods[0].Status.Phase = corev1.PodFailed
	again, err := workloadinterface.SchedulingGroups(t.Context(), observed)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, again); diff != "" {
		t.Fatalf("returned groups modified the captured workload (-want +got):\n%s", diff)
	}
}

func TestSchedulingGroupsReuseCapturedMembership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dataset, root := jobSetDataset()
		definition, err := catalog.Get(root.GroupVersionKind())
		if err != nil {
			t.Fatal(err)
		}
		selector := definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector
		selector.IdPath = fmt.Sprintf(`if now < %d then %s else "undeclared" end`, time.Now().Unix()+1, selector.IdPath)
		observed := groupingObservation(t, dataset, root, definition)
		if issues := observed.MembershipIssues(); len(issues) != 0 {
			t.Fatalf("initial membership issues = %v, want none", issues)
		}
		time.Sleep(2 * time.Second)
		groups, err := workloadinterface.SchedulingGroups(t.Context(), observed)
		if err != nil {
			t.Fatal(err)
		}
		want := []workloadinterface.SchedulingGroup{
			{Name: "job", Keys: []string{"evaluation"}, Pods: []*corev1.Pod{dataset.Pods[2]}},
			{Name: "job", Keys: []string{"training"}, Pods: []*corev1.Pod{dataset.Pods[0], dataset.Pods[1]}},
		}
		if diff := cmp.Diff(want, groups); diff != "" {
			t.Fatalf("captured grouping mismatch (-want +got):\n%s", diff)
		}
		current := groupingObservation(t, dataset, root, definition)
		groups, err = workloadinterface.SchedulingGroups(t.Context(), current)
		if !errors.Is(err, discovery.ErrIncompleteAttribution) || groups != nil {
			t.Fatalf("new observation grouping = %v, %v, want no groups and incomplete attribution", groups, err)
		}
	})
}

func TestSchedulingGroupsCombinePyTorchMembers(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kubeflow.org/v1", "kind": "PyTorchJob",
		"metadata": map[string]any{"name": "training", "namespace": "example", "uid": "pytorch-uid", "resourceVersion": "1"},
	}}
	master := ownedPod("training-master-0", root, corev1.PodRunning)
	worker := ownedPod("training-worker-0", root, corev1.PodRunning)
	master.Labels = map[string]string{"training.kubeflow.org/replica-type": "master", "training.kubeflow.org/job-name": "training"}
	worker.Labels = map[string]string{"training.kubeflow.org/replica-type": "worker", "training.kubeflow.org/job-name": "training"}
	worker.Spec.Containers = []corev1.Container{{Name: "trainer", Image: "example/trainer:v1", Args: []string{"--epochs=3"}}}
	worker.Status.PodIP = "192.0.2.10"
	dataset := jobDataset(root, []*corev1.Pod{worker, master})
	dataset.Mappings[root.GroupVersionKind()].Resource.Resource = "pytorchjobs"
	observed := groupingObservation(t, dataset, root)
	groups, err := workloadinterface.SchedulingGroups(t.Context(), observed)
	if err != nil {
		t.Fatal(err)
	}
	want := []workloadinterface.SchedulingGroup{
		{Name: "job", Keys: []string{"training"}, Pods: []*corev1.Pod{master, worker}},
	}
	if diff := cmp.Diff(want, groups); diff != "" {
		t.Fatalf("member components should share one gang with full Pods (-want +got):\n%s", diff)
	}
}

func TestSchedulingGroupsPreserveKeyBoundaries(t *testing.T) {
	dataset, root := jobSetDataset()
	definition, err := catalog.Get(root.GroupVersionKind())
	if err != nil {
		t.Fatal(err)
	}
	definition.Spec.Instructions.GangScheduling.PodGroups[0].Members[0].GroupByKeyPaths = []string{
		`.metadata.annotations["example.org/first"]`, `.metadata.annotations["example.org/second"]`,
	}
	dataset.Pods[0].Annotations = map[string]string{"example.org/first": "a|b", "example.org/second": "c"}
	dataset.Pods[1].Annotations = map[string]string{"example.org/first": "a", "example.org/second": "b|c"}
	dataset.Pods[2].Annotations = map[string]string{"example.org/first": "a", "example.org/second": "b|c"}
	observed := groupingObservation(t, dataset, root, definition)
	groups, err := workloadinterface.SchedulingGroups(t.Context(), observed)
	if err != nil {
		t.Fatal(err)
	}
	want := []workloadinterface.SchedulingGroup{
		{Name: "job", Keys: []string{"a", "b|c"}, Pods: []*corev1.Pod{dataset.Pods[2], dataset.Pods[1]}},
		{Name: "job", Keys: []string{"a|b", "c"}, Pods: []*corev1.Pod{dataset.Pods[0]}},
	}
	if diff := cmp.Diff(want, groups); diff != "" {
		t.Fatalf("grouping keys collided (-want +got):\n%s", diff)
	}
}

func TestSchedulingGroupsPreserveGroupNames(t *testing.T) {
	dataset, root := jobSetDataset()
	definition, err := catalog.Get(root.GroupVersionKind())
	if err != nil {
		t.Fatal(err)
	}
	definition.Spec.Instructions.GangScheduling.PodGroups = []v1alpha1.PodGroupDefinition{
		{Name: "training-gang", Members: []v1alpha1.PodGroupMemberDefinition{{
			ComponentName: "replicatedjob", GroupByKeyPaths: []string{`"shared"`},
			Filters: []string{`.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] == "training"`},
		}}},
		{Name: "evaluation-gang", Members: []v1alpha1.PodGroupMemberDefinition{{
			ComponentName: "replicatedjob", GroupByKeyPaths: []string{`"shared"`},
			Filters: []string{`.metadata.labels["jobset.sigs.k8s.io/replicatedjob-name"] == "evaluation"`},
		}}},
	}
	observed := groupingObservation(t, dataset, root, definition)
	groups, err := workloadinterface.SchedulingGroups(t.Context(), observed)
	if err != nil {
		t.Fatal(err)
	}
	want := []workloadinterface.SchedulingGroup{
		{Name: "evaluation-gang", Keys: []string{"shared"}, Pods: []*corev1.Pod{dataset.Pods[2]}},
		{Name: "training-gang", Keys: []string{"shared"}, Pods: []*corev1.Pod{dataset.Pods[0], dataset.Pods[1]}},
	}
	if diff := cmp.Diff(want, groups); diff != "" {
		t.Fatalf("different named gangs collided (-want +got):\n%s", diff)
	}
}

func TestSchedulingGroupsErrorsAndEmptyResults(t *testing.T) {
	t.Run("zero Pods", func(t *testing.T) {
		dataset, root := jobSetDataset()
		dataset.Pods = nil
		observed := groupingObservation(t, dataset, root)
		groups, err := workloadinterface.SchedulingGroups(t.Context(), observed)
		if err != nil || len(groups) != 0 {
			t.Fatalf("zero Pods returned groups=%v err=%v", groups, err)
		}
	})
	t.Run("generic workload", func(t *testing.T) {
		root := jobObject()
		root.SetAPIVersion("example.org/v1")
		root.SetKind("Training")
		observed := groupingObservation(t, jobDataset(root, nil), root)
		if _, err := workloadinterface.SchedulingGroups(t.Context(), observed); !errors.Is(err, discovery.ErrUnsupported) {
			t.Fatalf("generic workload error = %v", err)
		}
	})
	t.Run("nil workload", func(t *testing.T) {
		if _, err := workloadinterface.SchedulingGroups(t.Context(), nil); !errors.Is(err, discovery.ErrInvalidInput) {
			t.Fatalf("nil workload error = %v", err)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := workloadinterface.SchedulingGroups(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled context error = %v", err)
		}
	})
	for _, name := range []string{"unresolved membership", "invalid group key", "no matching group", "no instructions", "singular podGroup", "owner fallback"} {
		t.Run(name, func(t *testing.T) {
			dataset, root := jobSetDataset()
			definition, err := catalog.Get(root.GroupVersionKind())
			if err != nil {
				t.Fatal(err)
			}
			var wantError error
			switch name {
			case "unresolved membership":
				dataset.Pods[1].Labels["jobset.sigs.k8s.io/replicatedjob-name"] = "undeclared"
				wantError = discovery.ErrIncompleteAttribution
			case "invalid group key":
				definition.Spec.Instructions.GangScheduling.PodGroups[0].Members[0].GroupByKeyPaths = []string{`.metadata.annotations.missing`}
				dataset.Pods[0].Annotations = map[string]string{"missing": "shared"}
				dataset.Pods[2].Annotations = map[string]string{"missing": "shared"}
			case "no matching group":
				definition.Spec.Instructions.GangScheduling.PodGroups[0].Members[0].Filters = []string{"false"}
			case "no instructions":
				definition.Spec.Instructions.GangScheduling = nil
			case "owner fallback":
				definition.Spec.Instructions.GangScheduling.PodGroups[0].Members[0].GroupByKeyPaths = nil
				wantError = discovery.ErrUnsupported
			case "singular podGroup":
				definition.Spec.Instructions.GangScheduling = &v1alpha1.GangSchedulingInstruction{
					PodGroup: &v1alpha1.PodGroupComponentsMapping{Name: "training"},
				}
				wantError = discovery.ErrUnsupported
			}
			observed := groupingObservation(t, dataset, root, definition)
			groups, err := workloadinterface.SchedulingGroups(t.Context(), observed)
			if name == "invalid group key" {
				if err == nil || groups != nil {
					t.Fatalf("invalid group key returned groups=%v err=%v", groups, err)
				}
				return
			}
			if !errors.Is(err, wantError) {
				t.Fatalf("error = %v, want %v", err, wantError)
			}
			if len(groups) != 0 || (err != nil && groups != nil) {
				t.Fatalf("expected no partial groups, got %v", groups)
			}
		})
	}
}

func groupingObservation(t *testing.T, dataset discovery.Dataset, root *unstructured.Unstructured, definitions ...*v1alpha1.Karta) *discovery.Workload {
	t.Helper()
	index := discovery.NewIndex()
	if _, err := index.Replace(t.Context(), dataset); err != nil {
		t.Fatal(err)
	}
	discoverer, err := karta.NewDiscoverer(index, discovery.Options{Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := discoverer.Workload(t.Context(), objectRef(root))
	if err != nil {
		t.Fatal(err)
	}
	return observed
}
