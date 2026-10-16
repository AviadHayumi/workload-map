// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
)

func TestAttributionCacheStructuralRootPaths(t *testing.T) {
	for _, expression := range []string{`.spec.ids[]`, `.spec.replicatedJobs[].name`, `.spec.workerGroupSpecs[].groupName`, `.spec.rayClusterConfig.workerGroupSpecs[].groupName`, `.spec.groups[0].members[].id`, `.spec.groups[][].name`, `.[]`, `.spec.ids`} {
		if !structuralAttributionPath(expression, true) {
			t.Errorf("structural root path bypassed: %s", expression)
		}
	}
	for _, expression := range []string{`now`, `env`, `$ENV`, `.spec.ids[] | now`, `.spec.ids[] | .name`, `.spec.ids[]?`, `.spec.ids[.key]`, `.spec.ids["a\(now)"]`, `.spec.ids[] |= now`, `.spec.ids[] as $id | $id`, `(.spec.ids // [])[]`, `.spec.ids[:2][]`, `.spec.ids[] | error("x")`, `.spec.ids[] | tostring`, `def ids: .spec.ids[]; ids`, `.spec.ids[now]`, `.spec.ids[] | input`} {
		if structuralAttributionPath(expression, true) {
			t.Errorf("unsupported root expression cached: %s", expression)
		}
	}
	if staticAttributionPath(`.metadata.labels[]`) {
		t.Fatal("root iteration accidentally broadened Pod selector eligibility")
	}
}

func TestAttributionCacheCatalogEligibility(t *testing.T) {
	expected := map[string]bool{
		"apps-deployment-v1": true, "apps-statefulset-v1": true, "batch-job-v1": true, "batch-cronjob-v1": true, "core-pod-v1": true,
		"jobset-x-k8s-io-jobset-v1alpha2": true, "kubeflow-org-pytorchjob-v1": true, "kubeflow-org-mpijob-v2beta1": true,
		"ray-io-raycluster-v1": true, "ray-io-rayservice-v1": true, "ray-io-rayjob-v1": false,
		"leaderworkerset-x-k8s-io-leaderworkerset-v1": true, "serving-kserve-io-inferenceservice-v1beta1": true, "serving-knative-dev-service-v1": true,
		"apps-nvidia-com-nimservice-v1alpha1": true, "apps-nvidia-com-nimcache-v1alpha1": true, "milvus-io-milvus-v1beta1": true,
		"nvidia-com-dynamographdeployment-v1alpha1": false, "nvidia-com-dynamographdeployment-v1beta1": false, "grove-io-podcliqueset-v1alpha1": false,
	}
	definitions := catalog.List()
	if len(definitions) != len(expected) {
		t.Fatalf("catalog coverage changed: %d definitions, %d expected", len(definitions), len(expected))
	}
	eligible := 0
	for _, definition := range definitions {
		slug, err := catalog.Slug(definition)
		if err != nil {
			t.Fatal(err)
		}
		want, exists := expected[slug]
		if !exists {
			t.Fatalf("unreviewed catalog definition %s", slug)
		}
		got := cacheableAttribution(definition)
		if got != want {
			t.Errorf("%s eligibility=%v, want %v", slug, got, want)
		}
		if got {
			eligible++
		}
		t.Logf("%s eligible=%v", slug, got)
	}
	if eligible != 16 {
		t.Fatalf("eligible=%d, want16", eligible)
	}
}

func TestAttributionCacheCatalogInstances(t *testing.T) {
	cases := []struct {
		gvk                schema.GroupVersionKind
		component, idField string
		path               []string
		instanceLabel      string
		ray                bool
	}{
		{schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}, "replicatedjob", "name", []string{"spec", "replicatedJobs"}, "jobset.sigs.k8s.io/replicatedjob-name", false},
		{schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayCluster"}, "worker", "groupName", []string{"spec", "workerGroupSpecs"}, "ray.io/group", true},
		{schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayService"}, "worker", "groupName", []string{"spec", "rayClusterConfig", "workerGroupSpecs"}, "ray.io/group", true},
	}
	for _, tc := range cases {
		t.Run(tc.gvk.Kind, func(t *testing.T) {
			root := indexTestObject("operator", "team", tc.gvk, nil)
			declarations := []any{map[string]any{tc.idField: "alpha"}, map[string]any{tc.idField: "beta"}, map[string]any{tc.idField: "empty"}}
			if err := unstructured.SetNestedSlice(root.Object, declarations, tc.path...); err != nil {
				t.Fatal(err)
			}
			objects := []*unstructured.Unstructured{root}
			owner := root
			mappings := map[schema.GroupVersionKind]*meta.RESTMapping{tc.gvk: indexTestMapping(tc.gvk, meta.RESTScopeNamespace), podGVK: indexTestMapping(podGVK, meta.RESTScopeNamespace)}
			if tc.gvk.Kind != "RayCluster" {
				childGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
				if tc.ray {
					childGVK = schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayCluster"}
				}
				owner = indexTestObject("intermediate", "team", childGVK, root)
				objects = append(objects, owner)
				mappings[childGVK] = indexTestMapping(childGVK, meta.RESTScopeNamespace)
			}
			var pods []*corev1.Pod
			for _, id := range []string{"alpha", "beta"} {
				pod := indexTestPod(id, "team", owner)
				pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
				pod.Labels = map[string]string{tc.instanceLabel: id}
				if tc.ray {
					pod.Labels["ray.io/node-type"] = "worker"
				} else {
					pod.Labels["jobset.sigs.k8s.io/job-index"] = "0"
				}
				pod.Spec.Containers = []corev1.Container{{Name: "worker", Image: "ghcr.io/example/worker:v1", Args: []string{"--fixture"}}}
				pods = append(pods, pod)
			}
			index := NewIndex()
			generation, err := index.Replace(t.Context(), Dataset{Namespace: "team", Objects: objects, Pods: pods, Mappings: mappings})
			if err != nil {
				t.Fatal(err)
			}
			on, err := New(index, Options{Cache: &CacheOptions{MaxWorkloads: 4, MaxPods: 8}})
			if err != nil {
				t.Fatal(err)
			}
			off, err := New(index, Options{})
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := off.WorkloadForPod(t.Context(), podReference(pods[0]))
			if err != nil {
				t.Fatal(err)
			}
			first, err := on.WorkloadForPod(t.Context(), podReference(pods[0]))
			if err != nil {
				t.Fatal(err)
			}
			hit, err := on.Workload(t.Context(), reference(root))
			if err != nil {
				t.Fatal(err)
			}
			if first.prepared != hit.prepared || len(on.cache.entries) != 1 || len(hit.issues) != 0 {
				t.Fatal("catalog definition did not produce successful cache hit")
			}
			if !reflect.DeepEqual(hit.Object(), baseline.Object()) || !reflect.DeepEqual(hit.Pods(), baseline.Pods()) || !reflect.DeepEqual(hit.memberships, baseline.memberships) {
				t.Fatal("cache changed catalog full output")
			}
			ids, err := hit.prepared.InstanceIDs(tc.component)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids, []string{"alpha", "beta", "empty"}) {
				t.Fatalf("declared IDs=%v", ids)
			}
			ids[0] = "caller mutation"
			component, err := hit.Component(tc.component)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"alpha", "beta", "empty"} {
				instance, err := component.Instance(id)
				if err != nil {
					t.Fatal(err)
				}
				members, err := instance.Pods()
				if err != nil {
					t.Fatal(err)
				}
				if id == "empty" {
					if len(members) != 0 {
						t.Fatal("empty declared instance gained Pods")
					}
					continue
				}
				if len(members) != 1 || members[0].Name != id {
					t.Fatalf("instance %s Pods=%v", id, members)
				}
				members[0].Spec.Containers[0].Args[0] = "caller mutation"
			}
			if _, err := component.Instance("absent"); !errors.Is(err, ErrInstanceNotFound) {
				t.Fatalf("undeclared instance accepted: %v", err)
			}
			copiedRoot := hit.Object()
			if err := unstructured.SetNestedSlice(copiedRoot.Object, []any{}, tc.path...); err != nil {
				t.Fatal(err)
			}
			again, err := on.Workload(t.Context(), reference(root))
			if err != nil {
				t.Fatal(err)
			}
			if again.prepared != hit.prepared || !reflect.DeepEqual(again.Pods(), pods) {
				t.Fatal("public result mutation changed cached attribution")
			}
			// Replace the declaration without changing resourceVersion. Old Pod membership must become unresolved.
			changed := root.DeepCopy()
			declarations[0] = map[string]any{tc.idField: "gamma"}
			if err := unstructured.SetNestedSlice(changed.Object, declarations, tc.path...); err != nil {
				t.Fatal(err)
			}
			generation, err = index.Apply(t.Context(), generation, Changes{Upserts: []*unstructured.Unstructured{changed}})
			if err != nil {
				t.Fatal(err)
			}
			stale, err := on.Workload(t.Context(), reference(changed))
			if err != nil {
				t.Fatal(err)
			}
			if stale.prepared == hit.prepared || len(stale.issues) == 0 {
				t.Fatal("root declaration edit replayed prior membership")
			}
			revised := pods[0].DeepCopy()
			revised.Labels[tc.instanceLabel] = "gamma"
			object, err := podObject(revised)
			if err != nil {
				t.Fatal(err)
			}
			generation, err = index.Apply(t.Context(), generation, Changes{Upserts: []*unstructured.Unstructured{object}})
			if err != nil {
				t.Fatal(err)
			}
			repaired, err := on.Workload(t.Context(), reference(changed))
			if err != nil {
				t.Fatal(err)
			}
			if len(repaired.issues) != 0 {
				t.Fatalf("repaired label failed: %v", repaired.issues)
			}
			unrelated := indexTestObject("unrelated", "team", tc.gvk, nil)
			generation, err = index.Apply(t.Context(), generation, Changes{Upserts: []*unstructured.Unstructured{unrelated}})
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := on.Workload(t.Context(), reference(changed))
			if err != nil {
				t.Fatal(err)
			}
			if fresh.prepared != repaired.prepared || fresh.Observation().Generation != generation || fresh.Observation() == repaired.Observation() {
				t.Fatal("unrelated update invalidated derived state or froze observation")
			}
		})
	}
}

func TestAttributionCacheRootPreparationFailures(t *testing.T) {
	overLimit := make([]any, 1001)
	for i := range overLimit {
		overLimit[i] = fmt.Sprint(i)
	}
	cases := []struct {
		name    string
		value   any
		missing bool
	}{
		{"missing", nil, true}, {"null", nil, false}, {"scalar", "alpha", false}, {"nonstring", []any{float64(1)}, false}, {"empty-string", []any{""}, false}, {"mixed", []any{"alpha", false}, false}, {"null-element", []any{nil}, false}, {"result-limit", overLimit, false},
	}
	for _, count := range []int{0, 1} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/pods%d", tc.name, count), func(t *testing.T) {
				f := attributionFixtureFor(t, count)
				definition := f.definition.DeepCopy()
				definition.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = ptr.To(`.spec.ids[]`)
				definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector = &v1alpha1.ComponentInstanceSelector{IdPath: `.metadata.labels.instance`}
				if !tc.missing {
					if err := unstructured.SetNestedField(f.root.Object, tc.value, "spec", "ids"); err != nil {
						t.Fatal(err)
					}
				}
				for _, pod := range f.pods {
					pod.Labels["instance"] = "alpha"
				}
				var err error
				f.generation, err = f.index.Replace(t.Context(), f.dataset)
				if err != nil {
					t.Fatal(err)
				}
				f.resolver, err = New(f.index, Options{Definitions: []*v1alpha1.Karta{definition}, Cache: &CacheOptions{MaxWorkloads: 4, MaxPods: 8}})
				if err != nil {
					t.Fatal(err)
				}
				first := attributionQuery(t, f)
				second := attributionQuery(t, f)
				if first.prepared == second.prepared || len(f.resolver.cache.entries) != 0 {
					t.Fatal("preparation failure was cached")
				}
				if _, err := second.prepared.InstanceIDs("worker"); err == nil {
					t.Fatal("fixture did not fail preparation")
				}
				if len(second.Pods()) != count {
					t.Fatal("preparation error dropped ownership-confirmed Pods")
				}
				if count == 0 && len(second.issues) != 0 {
					t.Fatal("expected zero-Pod failure hidden from membership issues")
				}
				if err := unstructured.SetNestedSlice(f.root.Object, []any{"alpha"}, "spec", "ids"); err != nil {
					t.Fatal(err)
				}
				attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{f.root}})
				repaired := attributionQuery(t, f)
				hit := attributionQuery(t, f)
				if repaired.prepared != hit.prepared || len(f.resolver.cache.entries) != 1 {
					t.Fatal("successful preparation did not recover after failure")
				}
			})
		}
	}
}

func TestAttributionCacheRootEmptyAndOrderedInstances(t *testing.T) {
	for _, tc := range []struct {
		value any
		ids   []string
	}{
		{[]any{}, nil}, {[]any{"beta", "alpha", "beta"}, []string{"beta", "alpha", "beta"}},
		{map[string]any{"z": "beta", "a": "alpha"}, []string{"alpha", "beta"}},
	} {
		ids := tc.ids
		f := attributionFixtureFor(t, 0)
		definition := f.definition.DeepCopy()
		definition.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = ptr.To(`.spec.ids[]`)
		definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector = &v1alpha1.ComponentInstanceSelector{IdPath: `.metadata.labels.instance`}
		if err := unstructured.SetNestedField(f.root.Object, tc.value, "spec", "ids"); err != nil {
			t.Fatal(err)
		}
		attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{f.root}})
		var err error
		f.resolver, err = New(f.index, Options{Definitions: []*v1alpha1.Karta{definition}, Cache: &CacheOptions{MaxWorkloads: 4, MaxPods: 8}})
		if err != nil {
			t.Fatal(err)
		}
		first := attributionQuery(t, f)
		hit := attributionQuery(t, f)
		if first.prepared != hit.prepared {
			t.Fatal("valid zero-Pod declared instance set did not hit")
		}
		got, err := hit.prepared.InstanceIDs("worker")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(ids) {
			t.Fatalf("instance count=%d, want%d", len(got), len(ids))
		}
		for i, id := range ids {
			if got[i] != id {
				t.Fatalf("instance order changed: %v", got)
			}
		}
		for _, expired := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			if expired {
				cancel()
				ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
			} else {
				cancel()
			}
			_, err := f.resolver.Workload(ctx, reference(f.root))
			cancel()
			want := context.Canceled
			if expired {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("caller context ignored: %v", err)
			}
		}
	}
}

func TestAttributionCacheUnsupportedRootEvaluation(t *testing.T) {
	for _, expression := range []string{`.spec.ids[] | .`, `now | tostring`, `env`, `error("root failure")`} {
		t.Run(expression, func(t *testing.T) {
			f := attributionFixtureFor(t, 0)
			definition := f.definition.DeepCopy()
			definition.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = &expression
			definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector = &v1alpha1.ComponentInstanceSelector{IdPath: `.metadata.labels.instance`}
			if err := unstructured.SetNestedSlice(f.root.Object, []any{"alpha"}, "spec", "ids"); err != nil {
				t.Fatal(err)
			}
			attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{f.root}})
			var err error
			f.resolver, err = New(f.index, Options{Definitions: []*v1alpha1.Karta{definition}, Cache: &CacheOptions{MaxWorkloads: 4, MaxPods: 8}})
			if err != nil {
				t.Fatal(err)
			}
			first := attributionQuery(t, f)
			second := attributionQuery(t, f)
			if first.prepared == second.prepared || len(f.resolver.cache.entries) != 0 {
				t.Fatal("unsupported root expression did not use fresh normal evaluation")
			}
		})
	}
}
