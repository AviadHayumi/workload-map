// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

func ExampleSelectDefinitions() {
	ctx := context.Background()
	gvk := schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}
	builtIn, err := catalog.Get(gvk)
	if err != nil {
		panic(err)
	}
	catalogSelection := []*v1alpha1.Karta{builtIn}
	registry, err := discovery.NewDefinitionRegistry(catalogSelection...)
	if err != nil {
		panic(err)
	}
	custom := builtIn.DeepCopy()
	custom.Name = "cluster-jobset"
	custom.ResourceVersion = "opaque-revision"

	// Watch callbacks enqueue a definition reconciliation. Its worker reads the
	// complete current source after synchronization, then publishes this merge.
	for _, current := range []struct {
		name    string
		watched []*v1alpha1.Karta
	}{
		{"added", []*v1alpha1.Karta{custom}},
		{"unchanged", []*v1alpha1.Karta{custom.DeepCopy()}},
		{"deleted", nil},
	} {
		before, err := registry.Definition(ctx, gvk)
		if err != nil {
			panic(err)
		}
		selected, err := workloadinterface.SelectDefinitions(catalogSelection, current.watched)
		if err != nil {
			panic(err) // Reject ambiguous sources; do not publish a partial merge.
		}
		if err := registry.Replace(selected...); err != nil {
			panic(err)
		}
		after, err := registry.Definition(ctx, gvk)
		if err != nil {
			panic(err)
		}
		// Compare handles across the old/new GVK union in a real consumer.
		// A nil handle also participates, covering additions and removals.
		fmt.Printf("%s: definition=%s requeue=%t\n", current.name, after.Karta().Name, before != after)
	}
	// Output:
	// added: definition=cluster-jobset requeue=true
	// unchanged: definition=cluster-jobset requeue=false
	// deleted: definition=jobset-x-k8s-io-jobset-v1alpha2 requeue=true
}

func TestSelectDefinitionsOrderAndIsolation(t *testing.T) {
	job := kartas.BatchJob()
	deployment := kartas.Deployment()
	pod := kartas.Pod()
	watched := job.DeepCopy()
	watched.Name = "custom-job"
	watched.ResourceVersion = "opaque-revision"
	watched.Labels = map[string]string{"source": "watched"}
	watched.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Observed"}}
	catalogInput := []*v1alpha1.Karta{job, deployment}
	watchInput := []*v1alpha1.Karta{watched, pod}
	beforeCatalog := []*v1alpha1.Karta{job.DeepCopy(), deployment.DeepCopy()}
	beforeWatch := []*v1alpha1.Karta{watched.DeepCopy(), pod.DeepCopy()}
	selected, err := workloadinterface.SelectDefinitions(catalogInput, watchInput)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(beforeCatalog, catalogInput); diff != "" {
		t.Errorf("catalog source mutated (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(beforeWatch, watchInput); diff != "" {
		t.Errorf("watched source mutated (-want +got):\n%s", diff)
	}
	if len(selected) != 3 {
		t.Fatalf("selected %d entries, want exactly 3", len(selected))
	}
	var keys []string
	for _, definition := range selected {
		keys = append(keys, catalog.RootKey(definition).String())
		if catalog.RootKey(definition) == catalog.RootKey(job) {
			if diff := cmp.Diff(watched, definition); diff != "" {
				t.Fatalf("override lost fields (-want +got):\n%s", diff)
			}
			definition.Labels["source"] = "caller-mutated"
			definition.ResourceVersion = "caller-mutated"
			definition.Status.Conditions[0].Reason = "caller-mutated"
		}
	}
	if !slices.IsSorted(keys) {
		t.Errorf("unsorted GVKs: %v", keys)
	}
	if watched.Labels["source"] != "watched" || watched.ResourceVersion != "opaque-revision" || watched.Status.Conditions[0].Reason != "Observed" {
		t.Fatal("returned selection aliases the watched source")
	}
	forward, err := workloadinterface.SelectDefinitions(catalogInput, watchInput)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(catalogInput)
	slices.Reverse(watchInput)
	reversed, err := workloadinterface.SelectDefinitions(catalogInput, watchInput)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(forward, reversed); diff != "" {
		t.Errorf("source ordering changed selection (-want +got):\n%s", diff)
	}
}

func TestSelectDefinitionsRejectsAmbiguousOrInvalidSources(t *testing.T) {
	first := kartas.BatchJob()
	first.Name, first.Namespace, first.UID = "first", "team-one", "first-uid"
	second := first.DeepCopy()
	second.Name, second.Namespace, second.UID = "second", "team-two", "second-uid"
	missingVersion := first.DeepCopy()
	missingVersion.Spec.StructureDefinition.RootComponent.Kind.Version = ""
	missingKind := first.DeepCopy()
	missingKind.Spec.StructureDefinition.RootComponent.Kind.Kind = ""
	missingRoot := first.DeepCopy()
	missingRoot.Spec.StructureDefinition.RootComponent.Kind = nil
	for _, tc := range []struct {
		name       string
		catalog    []*v1alpha1.Karta
		watched    []*v1alpha1.Karta
		duplicates bool
	}{
		{name: "duplicate catalog", catalog: []*v1alpha1.Karta{first, second}, duplicates: true},
		{name: "duplicate watched", catalog: []*v1alpha1.Karta{first}, watched: []*v1alpha1.Karta{first, second}, duplicates: true},
		{name: "nil catalog", catalog: []*v1alpha1.Karta{nil}},
		{name: "nil watched", watched: []*v1alpha1.Karta{nil}},
		{name: "missing version", watched: []*v1alpha1.Karta{missingVersion}},
		{name: "missing kind", watched: []*v1alpha1.Karta{missingKind}},
		{name: "missing root", catalog: []*v1alpha1.Karta{missingRoot}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := workloadinterface.SelectDefinitions(tc.catalog, tc.watched)
			if !errors.Is(err, discovery.ErrInvalidInput) || selected != nil {
				t.Fatalf("selection=%v, error=%v; want no partial selection and ErrInvalidInput", selected, err)
			}
			if tc.duplicates {
				for _, identity := range []string{"first", "second", "team-one", "team-two", "first-uid", "second-uid"} {
					if !strings.Contains(err.Error(), identity) {
						t.Errorf("conflict omitted identity %q: %v", identity, err)
					}
				}
			}
		})
	}
}

func TestSelectDefinitionsRegistryLifecycle(t *testing.T) {
	ctx := t.Context()
	catalogDefinition := kartas.Jobset()
	custom := catalogDefinition.DeepCopy()
	custom.Name = "custom-training"
	custom.Spec.StructureDefinition.RootComponent.Kind = &v1alpha1.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Training"}
	moved := custom.DeepCopy()
	moved.Spec.StructureDefinition.RootComponent.Kind.Version = "v2"
	override := catalogDefinition.DeepCopy()
	override.Name = "cluster-jobset"
	registry, err := discovery.NewDefinitionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	dataset, root := jobSetDataset()
	catalogSDK, err := karta.NewDiscoverer(&datasetReader{dataset: dataset}, discovery.Options{DefinitionProvider: registry})
	if err != nil {
		t.Fatal(err)
	}
	customObjects := make([]*unstructured.Unstructured, 0, 2)
	customSDKs := make([]karta.Discoverer, 0, 2)
	for _, definition := range []*v1alpha1.Karta{custom, moved} {
		object := root.DeepCopy()
		object.SetGroupVersionKind(catalog.RootKey(definition))
		sdk, err := karta.NewDiscoverer(&datasetReader{dataset: jobDataset(object, nil)}, discovery.Options{DefinitionProvider: registry})
		if err != nil {
			t.Fatal(err)
		}
		customObjects = append(customObjects, object)
		customSDKs = append(customSDKs, sdk)
	}
	var catalogHandle *discovery.Definition
	for _, stage := range []struct {
		name        string
		watched     []*v1alpha1.Karta
		catalogName string
		generic     [2]bool
	}{
		{"catalog only", nil, catalogDefinition.Name, [2]bool{true, true}},
		{"override and custom", []*v1alpha1.Karta{override, custom}, override.Name, [2]bool{false, true}},
		{"delete override", []*v1alpha1.Karta{custom}, catalogDefinition.Name, [2]bool{false, true}},
		{"custom GVK changes", []*v1alpha1.Karta{moved}, catalogDefinition.Name, [2]bool{true, false}},
		{"delete custom", nil, catalogDefinition.Name, [2]bool{true, true}},
	} {
		t.Run(stage.name, func(t *testing.T) {
			selection, err := workloadinterface.SelectDefinitions([]*v1alpha1.Karta{catalogDefinition}, stage.watched)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Replace(selection...); err != nil {
				t.Fatal(err)
			}
			observed, err := catalogSDK.WorkloadForPod(ctx, discovery.PodReference(dataset.Pods[0]))
			if err != nil {
				t.Fatal(err)
			}
			if observed.Generic() || observed.Definition().Name != stage.catalogName || len(observed.Pods()) != 3 {
				t.Fatal("selected catalog entry or complete Pod set was lost")
			}
			for i, sdk := range customSDKs {
				observed, err := sdk.Workload(ctx, objectRef(customObjects[i]))
				if err != nil {
					t.Fatal(err)
				}
				if observed.Generic() != stage.generic[i] {
					t.Errorf("%s generic=%t, want %t", customObjects[i].GroupVersionKind(), observed.Generic(), stage.generic[i])
				}
			}
			current, err := registry.Definition(ctx, catalog.RootKey(catalogDefinition))
			if err != nil {
				t.Fatal(err)
			}
			if (stage.name == "custom GVK changes" || stage.name == "delete custom") && current != catalogHandle {
				t.Fatal("unrelated custom update changed the selected catalog handle")
			}
			catalogHandle = current
		})
	}
}
