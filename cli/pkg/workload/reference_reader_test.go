// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workload

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/cli/pkg/definitions"
	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

func TestReferenceReaderPagination(t *testing.T) {
	for _, fail := range []bool{false, true} {
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList"})
		mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "apps", Version: "v1"}})
		mapper.Add(deploymentGVK, meta.RESTScopeNamespace)
		calls := 0
		client.PrependReactor("list", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
			calls++
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			if action.GetNamespace() != "ml-team" || options.LabelSelector != "app=example" {
				t.Fatalf("wrong scope or selector: %#v", action)
			}
			if calls == 2 {
				if options.Continue != "next" {
					t.Fatalf("continue=%s", options.Continue)
				}
				if fail {
					return true, nil, errors.New("second page failed")
				}
			}
			result := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{Object: map[string]any{"metadata": map[string]any{"name": "page", "labels": map[string]any{"app": "example"}}}}}}
			if calls == 1 {
				result.SetContinue("next")
			}
			return true, result, nil
		})
		reader := NewReferenceReader(client, mapper)
		result, err := reader.List(context.Background(), deploymentGVK, references.ListQuery{Namespace: "ml-team", Selector: labels.SelectorFromSet(labels.Set{"app": "example"})})
		if fail {
			if err == nil || len(result) != 0 {
				t.Fatalf("partial result escaped: %v %v", result, err)
			}
		} else if err != nil || len(result) != 2 || result[0].GroupVersionKind() != deploymentGVK {
			t.Fatalf("result=%v error=%v", result, err)
		}
		if calls != 2 {
			t.Fatalf("calls=%d", calls)
		}
	}
}

func TestReferenceReaderScopes(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "trainer.kubeflow.org", Version: "v1alpha1", Kind: "ClusterTrainingRuntime"}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "metadata": map[string]any{"name": "torch"}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), object)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion(), deploymentGVK.GroupVersion()})
	mapper.Add(gvk, meta.RESTScopeRoot)
	mapper.Add(deploymentGVK, meta.RESTScopeNamespace)
	reader := NewReferenceReader(client, mapper)
	if _, err := reader.Get(context.Background(), gvk, "ml-team", "torch"); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetNamespace() != "" {
			t.Fatalf("cluster reference used namespace %q", action.GetNamespace())
		}
	}
	if _, err := reader.Get(context.Background(), deploymentGVK, "", "example"); err == nil {
		t.Fatal("accepted namespaced reference without namespace")
	}
}

func TestDescribeComponentPodsRemainIsolated(t *testing.T) {
	selector := func(name string) *api.PodSelector {
		return &api.PodSelector{FromReferences: []api.PodReference{{Name: "targets"}}, ComponentTypeSelector: &api.ComponentTypeSelector{Read: api.ReadAccessor{Expression: `"` + name + `"`}, Value: ptr.To(name)}}
	}
	def := &api.Karta{Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{RootComponent: api.ComponentDefinition{Name: "stack", StatusDefinition: &api.StatusDefinition{}, Kind: &api.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Stack"}}, References: []api.Reference{{Name: "targets", APIVersion: "apps/v1", Kind: "Deployment", List: &api.ListReference{MatchLabels: map[string]api.LabelValue{"app": {Value: ptr.To("example")}}}}}, ChildComponents: []api.ComponentDefinition{{Name: "left", OwnerRef: ptr.To("stack"), PodSelector: selector("left")}, {Name: "right", OwnerRef: ptr.To("stack"), PodSelector: selector("right")}}}}}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Stack", "metadata": map[string]any{"name": "sample", "namespace": "ml-team"}}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "left-pod"}}
	unrelated := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}}
	view, err := ResolveDescribe(context.Background(), object, definitions.Definition{Karta: def}, []corev1.Pod{pod, unrelated}, WithComponentPods(map[string][]corev1.Pod{"left": {pod}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Components) != 2 {
		t.Fatalf("components=%#v", view.Components)
	}
	for _, component := range view.Components {
		want := int32(0)
		if component.Name == "left" {
			want = 1
		}
		if component.Replicas.Current != want {
			t.Fatalf("component %s has %d Pods", component.Name, component.Replicas.Current)
		}
	}
}

func TestDescribeAssignedPodsRetainParentInstanceScope(t *testing.T) {
	def := &api.Karta{Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{
		RootComponent: api.ComponentDefinition{Name: "root", Kind: &api.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Stack"}, StatusDefinition: &api.StatusDefinition{}},
		ChildComponents: []api.ComponentDefinition{
			{Name: "group", OwnerRef: ptr.To("root"), InstanceIds: &api.ValueAccessor{Read: &api.ReadAccessor{Expression: `["a", "b"]`}}, PodSelector: &api.PodSelector{ComponentInstanceSelector: &api.ComponentInstanceSelector{Read: api.ReadAccessor{Expression: `object.metadata.labels.group`}}}},
			{Name: "worker", OwnerRef: ptr.To("group"), SpecDefinition: &api.SpecDefinition{PodSpec: &api.ValueAccessor{Read: &api.ReadAccessor{Expression: `{"containers": [{"name":"main"}]}`}}}},
		},
	}}}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Stack"}}
	pods := []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"group": "a"}}}, {ObjectMeta: metav1.ObjectMeta{Name: "b", Labels: map[string]string{"group": "b"}}}}
	view, err := ResolveDescribe(context.Background(), object, definitions.Definition{Karta: def}, pods, WithComponentPods(map[string][]corev1.Pod{"worker": pods}))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range view.Components[0].Children {
		if len(group.Children) != 1 || group.Children[0].Replicas.Current != 1 {
			t.Fatalf("instance %s received another instance's Pods: %#v", group.Name, group)
		}
	}
}

func TestDescribeNativeFallbackIgnoresReferenceOnlyComponents(t *testing.T) {
	definition := &api.Karta{Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{
		RootComponent: api.ComponentDefinition{
			Name:             "stack",
			Kind:             &api.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Stack"},
			StatusDefinition: &api.StatusDefinition{},
			SpecDefinition:   &api.SpecDefinition{PodSpec: &api.ValueAccessor{Read: &api.ReadAccessor{Expression: `object.spec.template`}}},
		},
		References: []api.Reference{{Name: "dependency", APIVersion: "apps/v1", Kind: "Deployment", Lookup: &api.LookupReference{NameExpression: `"dependency"`}}},
		ChildComponents: []api.ComponentDefinition{{Name: "dependency", OwnerRef: ptr.To("stack"), PodSelector: &api.PodSelector{
			FromReferences:        []api.PodReference{{Name: "dependency"}},
			ComponentTypeSelector: &api.ComponentTypeSelector{Read: api.ReadAccessor{Expression: `"dependency"`}, Value: ptr.To("dependency")},
		}}},
	}}}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Stack",
		"metadata": map[string]any{"name": "sample", "namespace": "ml-team"},
		"spec":     map[string]any{"template": map[string]any{"containers": []any{map[string]any{"name": "native"}}}},
	}}
	native := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "native"}}
	view, err := ResolveDescribe(context.Background(), object, definitions.Definition{Karta: definition}, []corev1.Pod{native})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Components) != 2 {
		t.Fatalf("components=%#v", view.Components)
	}
	for _, component := range view.Components {
		want := int32(0)
		if component.Name == "stack" {
			want = 1
		}
		if component.Replicas.Current != want {
			t.Fatalf("component %s has %d Pods, want %d", component.Name, component.Replicas.Current, want)
		}
	}
}
