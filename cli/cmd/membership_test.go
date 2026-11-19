// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/cli/pkg/definitions"
	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

var stackGVK = schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Stack"}

func membershipCluster(t *testing.T, secondParent, withPod bool) (lookup, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	stack := &api.Karta{ObjectMeta: metav1.ObjectMeta{Name: "stack"}, Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{
		RootComponent:   api.ComponentDefinition{Name: "stack", Kind: &api.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Stack"}, StatusDefinition: &api.StatusDefinition{PhaseDefinition: &api.PhaseDefinition{Read: api.ReadAccessor{Expression: `object.?status.?phase.orValue("")`}}, StatusMappings: api.StatusMappings{Running: []api.StatusMatcher{{ByPhase: "Running"}}, Failed: []api.StatusMatcher{{ByPhase: "Failed"}}}}},
		References:      []api.Reference{{Name: "dependency", APIVersion: "apps/v1", Kind: "Deployment", MatchExpression: `object.?spec.?enabled.orValue(true)`, List: &api.ListReference{MatchLabels: map[string]api.LabelValue{"app": {Value: ptr.To("shared")}}}}},
		ChildComponents: []api.ComponentDefinition{{Name: "dependency", OwnerRef: ptr.To("stack"), PodSelector: &api.PodSelector{FromReferences: []api.PodReference{{Name: "dependency"}}, ComponentTypeSelector: &api.ComponentTypeSelector{Read: api.ReadAccessor{Expression: `"dependency"`}, Value: ptr.To("dependency")}}}},
	}}}
	first := object(stackGVK, "parent", nil)
	first.SetLabels(map[string]string{"show": "yes"})
	first.Object["status"] = map[string]any{"phase": "Running"}
	dep := object(appsDeploymentGVK, "dependency", nil)
	dep.SetLabels(map[string]string{"app": "shared", "show": "yes"})
	dep.Object["spec"] = map[string]any{"replicas": int64(0), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "example:v1"}}}}}
	dep.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable"}}}
	objects := []runtime.Object{first, dep}
	if secondParent {
		second := object(stackGVK, "hidden-parent", nil)
		second.SetLabels(map[string]string{"show": "no"})
		second.Object["status"] = map[string]any{"phase": "Failed"}
		objects = append(objects, second)
	}
	if withPod {
		objects = append(objects, object(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, "dependency-pod", controlledBy(appsDeploymentGVK, "dependency")))
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "example.com", Version: "v1", Resource: "stacks"}: "StackList", {Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList", {Version: "v1", Resource: "pods"}: "PodList",
	}, objects...)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{stackGVK.GroupVersion(), appsDeploymentGVK.GroupVersion(), {Version: "v1"}})
	for _, gvk := range []schema.GroupVersionKind{stackGVK, appsDeploymentGVK, {Version: "v1", Kind: "Pod"}} {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	return lookup{namespace: "ml-team", dyn: client, metadata: fakeMetadata(t, objects...), mapper: mapper, resolver: definitions.New([]*api.Karta{stack, kartas.Deployment()}, nil)}, client
}

func TestReferenceFoldingCollectsHiddenClaimants(t *testing.T) {
	for _, tc := range []struct {
		name     string
		conflict bool
		selector string
		phases   []string
		want     []string
	}{
		{name: "accepted target", want: []string{"parent"}},
		{name: "label filtered claimant still conflicts", conflict: true, selector: "show=yes", want: []string{"dependency", "parent"}},
		{name: "phase filtered claimant still conflicts", conflict: true, phases: []string{"Running"}, want: []string{"dependency", "parent"}},
		{name: "parent filtered from display keeps target", selector: "app=shared", want: []string{"dependency"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			look, _ := membershipCluster(t, tc.conflict, true)
			views, _, err := collectAllTypes(context.Background(), look, &getOptions{allTypes: true, selector: tc.selector, phases: tc.phases})
			if err != nil {
				t.Fatal(err)
			}
			sortViews(views, sortByName)
			names := []string{}
			for _, view := range views {
				names = append(names, view.Name)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("rows=%v want=%v", names, tc.want)
			}
		})
	}
}

func TestReferenceFoldingZeroPodsAndFailure(t *testing.T) {
	t.Run("zero Pods remains a complete accepted target", func(t *testing.T) {
		look, _ := membershipCluster(t, false, false)
		views, warnings, err := collectAllTypes(context.Background(), look, &getOptions{allTypes: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 0 || len(views) != 1 || views[0].Name != "parent" {
			t.Fatalf("rows=%v warnings=%v", views, warnings)
		}
	})
	t.Run("failed Pod collection retains native rows", func(t *testing.T) {
		look, client := membershipCluster(t, false, true)
		client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("unavailable") })
		views, warnings, err := collectAllTypes(context.Background(), look, &getOptions{allTypes: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 2 || len(warnings) == 0 {
			t.Fatalf("rows=%v warnings=%v", views, warnings)
		}
	})
	t.Run("inactive reference retains native rows", func(t *testing.T) {
		look, client := membershipCluster(t, false, true)
		gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "stacks"}
		parent, err := client.Resource(gvr).Namespace("ml-team").Get(context.Background(), "parent", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		parent.Object["spec"] = map[string]any{"enabled": false}
		if _, err = client.Resource(gvr).Namespace("ml-team").Update(context.Background(), parent, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		views, _, err := collectAllTypes(context.Background(), look, &getOptions{allTypes: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(views) != 2 {
			t.Fatalf("rows=%v", views)
		}
	})
}

func TestTypedGetKeepsReferenceTargets(t *testing.T) {
	look, _ := membershipCluster(t, false, true)
	def, err := look.resolver.Resolve(appsDeploymentGVK)
	if err != nil {
		t.Fatal(err)
	}
	views, _, _, err := collect(context.Background(), look.dyn, look.mapper, def, "ml-team", &getOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Name != "dependency" {
		t.Fatalf("rows=%v", views)
	}
}

func TestAllNamespacesMembershipDoesNotMixClaimants(t *testing.T) {
	look, client := membershipCluster(t, false, true)
	for _, gvr := range []schema.GroupVersionResource{{Group: "example.com", Version: "v1", Resource: "stacks"}, {Group: "apps", Version: "v1", Resource: "deployments"}, {Version: "v1", Resource: "pods"}} {
		objects, err := client.Resource(gvr).Namespace("ml-team").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range objects.Items {
			object.SetNamespace("other-team")
			object.SetUID(types.UID("other-team/" + object.GetName()))
			owners := object.GetOwnerReferences()
			for i := range owners {
				owners[i].UID = types.UID("other-team/" + owners[i].Name)
			}
			object.SetOwnerReferences(owners)
			if err := client.Tracker().Add(&object); err != nil {
				t.Fatal(err)
			}
		}
	}
	look.namespace = ""
	views, warnings, err := collectAllTypes(context.Background(), look, &getOptions{allTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || len(warnings) != 0 {
		t.Fatalf("rows=%v warnings=%v", views, warnings)
	}
	for _, view := range views {
		if view.Name != "parent" {
			t.Fatalf("target was not folded in %s", view.Namespace)
		}
	}
}

func TestReferenceSourceFailureKeepsTargetRows(t *testing.T) {
	look, client := membershipCluster(t, false, true)
	calls := 0
	client.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls > 1 {
			return true, nil, errors.New("reference source unavailable")
		}
		return false, nil, nil
	})
	views, warnings, err := collectAllTypes(context.Background(), look, &getOptions{allTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || len(warnings) == 0 {
		t.Fatalf("rows=%v warnings=%v", views, warnings)
	}
}

func TestDescribeCollectsCompetingParents(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		look, client := membershipCluster(t, conflict, true)
		var err error
		look.definition, err = look.resolver.Resolve(stackGVK)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := client.Resource(schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "stacks"}).Namespace("ml-team").Get(context.Background(), "parent", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		assigned, _, err := describeComponentMembership(context.Background(), look, parent)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if conflict {
			want = 0
		}
		if len(assigned["dependency"]) != want {
			t.Fatalf("conflict=%t assigned=%v", conflict, assigned)
		}
	}
}

func TestDisabledReferenceFoldingKeepsBothWorkloads(t *testing.T) {
	look, client := membershipCluster(t, false, true)
	views, _, err := collectAllTypes(t.Context(), look, &getOptions{allTypes: true, noReferences: true})
	if err != nil || len(views) != 2 {
		t.Fatalf("rows=%v, want both workloads: %v", views, err)
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "pods" {
			t.Fatalf("disabled membership issued %s Pods", action.GetVerb())
		}
	}
}
