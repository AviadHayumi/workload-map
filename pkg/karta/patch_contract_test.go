// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package karta_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
	"github.com/dsx-ai-factory/workload-map/pkg/karta/kartatest"
)

func TestNilUpdatesReturnEmptyPatch(t *testing.T) {
	for name, update := range map[string]karta.PodTemplateUpdate{
		"nil": nil, "pod_patch": (*karta.PodPatch)(nil), "raw_patch": (*karta.Patch)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			definition := templateDefinition()
			workload, err := karta.New(definition, templateWorkload())
			if err != nil {
				t.Fatal(err)
			}
			for _, implementation := range []karta.Workload{workload, kartatest.NewFromKarta(definition)} {
				if err := implementation.UpdatePodTemplate(t.Context(), "root", update); !errors.Is(err, karta.ErrEmptyPatch) {
					t.Fatalf("UpdatePodTemplate(%T) = %v, want ErrEmptyPatch", update, err)
				}
			}
			if err := karta.ValidatePodTemplateUpdate(definition.Spec.StructureDefinition.RootComponent, "root", update); !errors.Is(err, karta.ErrEmptyPatch) {
				t.Fatalf("ValidatePodTemplateUpdate(%T) = %v, want ErrEmptyPatch", update, err)
			}
		})
	}
}

func TestTargetedUpdatesPreserveAbsentFields(t *testing.T) {
	for _, objectIteration := range []bool{false, true} {
		for _, missingParent := range []bool{false, true} {
			definition := fragmentedWorkersDefinition()
			fragmented := definition.Spec.StructureDefinition.RootComponent.SpecDefinition.FragmentedPodSpecDefinition
			fragmented.SchedulerNamePath = ptr.To(".spec.workers[].template.spec.schedulerName")
			fragmented.LabelsPath = ptr.To(".spec.workers[].template.metadata.labels")
			selected := map[string]any{"name": "a", "template": map[string]any{"spec": map[string]any{"schedulerName": "old"}}}
			excluded := map[string]any{"name": "b"}
			if !missingParent {
				excluded["template"] = map[string]any{"spec": map[string]any{}, "metadata": map[string]any{}}
			}
			object := fragmentedWorkersWorkload()
			var workers any = []any{selected, excluded}
			if objectIteration {
				workers = map[string]any{"selected": selected, "excluded": excluded}
			}
			object.Object["spec"].(map[string]any)["workers"] = workers
			workload, err := karta.New(definition, object)
			if err != nil {
				t.Fatal(err)
			}
			if err := workload.UpdatePodTemplate(t.Context(), "root", karta.PodPatch{
				SchedulerName: ptr.To("custom"), Labels: map[string]string{"team": "ml"},
			}, karta.WithInstances("a")); err != nil {
				t.Fatalf("UpdatePodTemplate(map=%v, missingParent=%v) = %v", objectIteration, missingParent, err)
			}
			updated, err := workload.Object()
			if err != nil {
				t.Fatal(err)
			}
			gotWorkers := updated.(*unstructured.Unstructured).Object["spec"].(map[string]any)["workers"]
			var gotExcluded any
			var gotSelected map[string]any
			if objectIteration {
				gotExcluded = gotWorkers.(map[string]any)["excluded"]
				gotSelected = gotWorkers.(map[string]any)["selected"].(map[string]any)
			} else {
				gotExcluded = gotWorkers.([]any)[1]
				gotSelected = gotWorkers.([]any)[0].(map[string]any)
			}
			if diff := cmp.Diff(excluded, gotExcluded); diff != "" {
				t.Fatalf("excluded instance changed (-before +after):\n%s", diff)
			}
			got, _, err := unstructured.NestedString(gotSelected, "template", "spec", "schedulerName")
			if err != nil || got != "custom" {
				t.Fatalf("selected scheduler = %q, %v; want custom", got, err)
			}
		}
	}
}

func TestAffinityValuesReplaceIncludingEmptyObjects(t *testing.T) {
	for _, field := range []string{"nodeAffinity", "podAffinity"} {
		for _, empty := range []bool{false, true} {
			for _, fragmented := range []bool{false, true} {
				for _, raw := range []bool{false, true} {
					definition, object := templateDefinition(), templateWorkload()
					path := []string{"spec", "template", "spec", "affinity", field}
					if fragmented {
						definition = fragmentedDefinition()
						definition.Spec.StructureDefinition.RootComponent.SpecDefinition.FragmentedPodSpecDefinition = &v1alpha1.FragmentedPodSpecDefinition{
							NodeAffinityPath: ptr.To(".spec.nodeAffinity"), PodAffinityPath: ptr.To(".spec.podAffinity"),
						}
						path = []string{"spec", field}
					}
					old := map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{"topologyKey": "old"}}}
					if field == "nodeAffinity" {
						old = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{}}}}
					}
					if err := unstructured.SetNestedMap(object.Object, old, path...); err != nil {
						t.Fatal(err)
					}
					patch := karta.PodPatch{}
					if field == "nodeAffinity" {
						patch.NodeAffinity = &corev1.NodeAffinity{}
						if !empty {
							patch.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{{Weight: 1, Preference: corev1.NodeSelectorTerm{}}}
						}
					} else {
						patch.PodAffinity = &corev1.PodAffinity{}
						if !empty {
							patch.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.WeightedPodAffinityTerm{{Weight: 1, PodAffinityTerm: corev1.PodAffinityTerm{TopologyKey: "zone"}}}
						}
					}
					var update karta.PodTemplateUpdate = patch
					if raw {
						update = patch.AsPodMergePatch()
					}
					workload, err := karta.New(definition, object)
					if err != nil {
						t.Fatal(err)
					}
					if err := workload.UpdatePodTemplate(t.Context(), "root", update); err != nil {
						t.Fatalf("UpdatePodTemplate(field=%s, empty=%v, fragmented=%v, raw=%v) = %v", field, empty, fragmented, raw, err)
					}
					updated, err := workload.Object()
					if err != nil {
						t.Fatal(err)
					}
					got, found, err := unstructured.NestedMap(updated.(*unstructured.Unstructured).Object, path...)
					if err != nil || !found {
						t.Fatalf("read affinity = %v, %v", found, err)
					}
					if _, retained := got["requiredDuringSchedulingIgnoredDuringExecution"]; retained {
						t.Fatalf("%s retained replaced required affinity: %v", field, got)
					}
					if empty && len(got) != 0 {
						t.Fatalf("empty %s = %v, want empty object", field, got)
					}
					if !empty && got["preferredDuringSchedulingIgnoredDuringExecution"] == nil {
						t.Fatalf("%s lost requested preferred affinity", field)
					}
				}
			}
		}
	}
}

func TestRawMapKeysAreLiteral(t *testing.T) {
	for _, definition := range []*v1alpha1.Karta{templateDefinition(), podSpecDefinition()} {
		object := templateWorkload()
		path := []string{"spec", "template", "spec", "nodeSelector"}
		if definition.Spec.StructureDefinition.RootComponent.SpecDefinition.PodSpecPath != nil {
			object = podSpecWorkload()
			path = []string{"spec", "podSpec", "nodeSelector"}
		}
		workload, err := karta.New(definition, object)
		if err != nil {
			t.Fatal(err)
		}
		keys := map[string]any{"example.com/pool": "gpu", "example.com": "literal", "quote\"key": "quoted", "control\vkey": "control", "": "empty"}
		if err := workload.UpdatePodTemplate(t.Context(), "root", karta.Patch{"spec": map[string]any{"nodeSelector": keys}}); err != nil {
			t.Fatal(err)
		}
		updated, err := workload.Object()
		if err != nil {
			t.Fatal(err)
		}
		got, found, err := unstructured.NestedMap(updated.(*unstructured.Unstructured).Object, path...)
		if err != nil || !found {
			t.Fatalf("read nodeSelector = %v, %v", found, err)
		}
		if diff := cmp.Diff(keys, got); diff != "" {
			t.Fatalf("nodeSelector mismatch (-want +got):\n%s", diff)
		}
	}
}

func TestObjectPreservesKubernetesIntegerGetters(t *testing.T) {
	object := &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "example.com/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Name: "job", Generation: 12, DeletionGracePeriodSeconds: ptr.To(int64(30))},
		Spec:       batchv1.JobSpec{Parallelism: ptr.To(int32(4))},
	}
	workload, err := karta.New(templateDefinition(), object)
	if err != nil {
		t.Fatal(err)
	}
	returned, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	if returned.GetGeneration() != 12 || returned.GetDeletionGracePeriodSeconds() == nil || *returned.GetDeletionGracePeriodSeconds() != 30 {
		t.Fatalf("metadata integer getters = generation %d, grace %v", returned.GetGeneration(), returned.GetDeletionGracePeriodSeconds())
	}
	parallelism, found, err := unstructured.NestedInt64(returned.(*unstructured.Unstructured).Object, "spec", "parallelism")
	if err != nil || !found || parallelism != 4 {
		t.Fatalf("NestedInt64(parallelism) = %d, %v, %v; want 4, true, nil", parallelism, found, err)
	}
	returned.SetGeneration(99)
	if err := unstructured.SetNestedField(returned.(*unstructured.Unstructured).Object, int64(99), "spec", "parallelism"); err != nil {
		t.Fatal(err)
	}
	again, err := workload.Object()
	if err != nil {
		t.Fatal(err)
	}
	if again.GetGeneration() != 12 || object.Generation != 12 {
		t.Fatal("mutating Object output changed retained or input generation")
	}
	parallelism, _, err = unstructured.NestedInt64(again.(*unstructured.Unstructured).Object, "spec", "parallelism")
	if err != nil || parallelism != 4 {
		t.Fatalf("retained parallelism = %d, %v; want 4", parallelism, err)
	}
}
