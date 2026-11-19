// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package references

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

type fakeReader struct {
	gets, lists     int
	object          *unstructured.Unstructured
	objects         []unstructured.Unstructured
	err             error
	namespace, name string
	query           ListQuery
}

func (r *fakeReader) Get(_ context.Context, _ schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	r.gets++
	r.namespace, r.name = namespace, name
	return r.object, r.err
}
func (r *fakeReader) List(_ context.Context, _ schema.GroupVersionKind, q ListQuery) ([]unstructured.Unstructured, error) {
	r.lists++
	r.query = q
	return r.objects, r.err
}
func workload() any {
	return map[string]any{"metadata": map[string]any{"namespace": "team-a"}, "spec": map[string]any{"runtime": "torch", "enabled": true}}
}
func runtime(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "ClusterTrainingRuntime", "metadata": map[string]any{"name": name}, "spec": map[string]any{"count": int64(9007199254740993)}}}
}
func lookup() api.Reference {
	return api.Reference{Name: "runtime", APIVersion: "trainer.kubeflow.org/v1alpha1", Kind: "ClusterTrainingRuntime", Lookup: &api.LookupReference{NameExpression: "object.spec.runtime"}}
}
func list() api.Reference {
	return api.Reference{Name: "pods", APIVersion: "v1", Kind: "Pod", List: &api.ListReference{MatchLabels: map[string]api.LabelValue{"app": {Expression: ptr.To("object.spec.runtime")}}, MatchExpressions: []api.LabelSelectorRequirement{{Key: "role", Operator: api.LabelSelectorOpIn, Values: []api.LabelValue{{Value: ptr.To("worker")}}}}}}
}
func TestLiveLookupAndMissing(t *testing.T) {
	ctx := context.Background()
	r := &fakeReader{object: runtime("torch")}
	got, err := ResolveDefinitions(ctx, r, []api.Reference{lookup()}, workload())
	if err != nil {
		t.Fatal(err)
	}
	if r.gets != 1 || r.namespace != "team-a" || r.name != "torch" {
		t.Fatalf("bad Get: %+v", r)
	}
	r.object.Object["spec"].(map[string]any)["count"] = int64(0)
	bindings, err := got.Bindings()
	if err != nil {
		t.Fatal(err)
	}
	if bindings["runtime"].(map[string]any)["spec"].(map[string]any)["count"] != int64(9007199254740993) {
		t.Fatal("reader result not copied or integer corrupted")
	}
	r.err = ErrNotFound
	got, err = ResolveDefinitions(ctx, r, []api.Reference{lookup()}, workload())
	if err != nil {
		t.Fatal(err)
	}
	bindings, _ = got.Bindings()
	if _, ok := bindings["runtime"]; ok {
		t.Fatal("missing lookup bound")
	}
	r.err = errors.New("DO_NOT_LEAK")
	_, err = ResolveDefinitions(ctx, r, []api.Reference{lookup()}, workload())
	if err == nil || strings.Contains(err.Error(), "DO_NOT_LEAK") || !errors.Is(err, r.err) {
		t.Fatalf("reader error: %v", err)
	}
	r.err = apierrors.NewForbidden(schema.GroupResource{Resource: "runtimes"}, "private-name", errors.New("private server detail"))
	_, err = ResolveDefinitions(ctx, r, []api.Reference{lookup()}, workload())
	if !apierrors.IsForbidden(err) || err.Error() != `reference "runtime" lookup failed: forbidden` {
		t.Fatalf("expected a redacted permission error: %v", err)
	}
}
func TestListsAreSortedAndEmptyIsBound(t *testing.T) {
	r := &fakeReader{objects: []unstructured.Unstructured{{Object: map[string]any{"metadata": map[string]any{"name": "z", "namespace": "team-a"}}}, {Object: map[string]any{"metadata": map[string]any{"name": "a", "namespace": "team-a"}}}}}
	got, err := ResolveDefinitions(context.Background(), r, []api.Reference{list()}, workload())
	if err != nil {
		t.Fatal(err)
	}
	if r.query.Namespace != "team-a" || r.query.Selector.String() != "app=torch,role in (worker)" {
		t.Fatalf("wrong list query: %+v", r.query)
	}
	if got["pods"].List[0].GetName() != "a" {
		t.Fatal("list not sorted")
	}
	r.objects = nil
	got, err = ResolveDefinitions(context.Background(), r, []api.Reference{list()}, workload())
	if err != nil {
		t.Fatal(err)
	}
	bindings, _ := got.Bindings()
	if !reflect.DeepEqual(bindings["pods"], []any{}) {
		t.Fatalf("empty list missing: %#v", bindings)
	}
}
func TestEligibilityBeforeExpressionsAndReader(t *testing.T) {
	d := lookup()
	d.MatchExpression = "false"
	d.Lookup.NameExpression = "object.missing"
	r := &fakeReader{err: errors.New("must not run")}
	got, err := ResolveDefinitions(context.Background(), r, []api.Reference{d}, workload())
	if err != nil || r.gets != 0 || len(got) != 0 {
		t.Fatalf("inactive resolution: %v %+v", err, r)
	}
	got, err = ResolveSupplied(context.Background(), []api.Reference{d}, workload(), ResolvedReferences{"runtime": {Object: &unstructured.Unstructured{Object: map[string]any{"kind": "Wrong"}}}})
	if err != nil || len(got) != 0 {
		t.Fatalf("inactive supplied validation: %v %v", got, err)
	}
	d.MatchExpression = "true"
	if _, err = ResolveDefinitions(context.Background(), r, []api.Reference{d}, workload()); err == nil || r.gets != 0 {
		t.Fatalf("bad lookup called reader: %v", err)
	}
	d.Lookup.NameExpression = "references.runtime.metadata.name"
	if _, err = Plan(context.Background(), d, workload()); err == nil {
		t.Fatal("reference recipe could read references")
	}
}
func TestSuppliedCopiesShapesNamesKindsAndNamespaces(t *testing.T) {
	ctx := context.Background()
	d := lookup()
	obj := runtime("torch")
	source := ResolvedReferences{"runtime": NewLookupValue(obj)}
	obj.SetName("changed")
	got, err := ResolveSupplied(ctx, []api.Reference{d}, workload(), source)
	if err != nil {
		t.Fatal(err)
	}
	source["runtime"].Object.SetName("changed-again")
	if got["runtime"].Object.GetName() != "torch" {
		t.Fatal("supplied mutation leaked")
	}
	tests := []ResolvedReferences{
		{"unknown": NewLookupValue(runtime("torch"))},
		{"runtime": {Object: runtime("torch"), List: []unstructured.Unstructured{}}},
		{"runtime": NewListValue(nil)},
		{"runtime": NewLookupValue(&unstructured.Unstructured{Object: map[string]any{"kind": "Other"}})},
	}
	for _, bad := range tests {
		if _, err := ResolveSupplied(ctx, []api.Reference{d}, workload(), bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	obj = runtime("torch")
	obj.SetNamespace("other")
	if _, err := ResolveSupplied(ctx, []api.Reference{d}, workload(), ResolvedReferences{"runtime": NewLookupValue(obj)}); err == nil {
		t.Fatal("cross namespace accepted")
	}
	if _, err := ResolvedReferences(map[string]ReferenceValue{"x": {Object: runtime("torch"), List: []unstructured.Unstructured{}}}).Bindings(); err == nil {
		t.Fatal("ambiguous binding accepted")
	}
}
func TestQueryEquality(t *testing.T) {
	a, err := Plan(context.Background(), list(), workload())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Plan(context.Background(), list(), workload())
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equal(b) {
		t.Fatal("same selector differs")
	}
	b.Namespace = "elsewhere"
	if a.Equal(b) {
		t.Fatal("different namespace equal")
	}
	a = Query{}
	b = Query{Name: "ignored"}
	if !a.Equal(b) {
		t.Fatal("inactive recipes should compare equal")
	}
}

func TestObservationCacheCopiesSuccessAndRetriesErrors(t *testing.T) {
	source := &fakeReader{object: runtime("torch"), err: errors.New("retry")}
	reader := NewCachingReader(source)
	ctx := context.Background()
	gvk := schema.GroupVersionKind{Group: "trainer.kubeflow.org", Version: "v1alpha1", Kind: "ClusterTrainingRuntime"}
	if _, err := reader.Get(ctx, gvk, "", "torch"); err == nil {
		t.Fatal("expected error")
	}
	source.err = nil
	first, err := reader.Get(ctx, gvk, "", "torch")
	if err != nil {
		t.Fatal(err)
	}
	first.SetName("changed")
	second, err := reader.Get(ctx, gvk, "", "torch")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetName() != "torch" || source.gets != 2 {
		t.Fatalf("copy/cache failure: %s, %d", second.GetName(), source.gets)
	}
	for range 2 {
		items, err := reader.List(ctx, gvk, ListQuery{})
		if err != nil || items == nil {
			t.Fatalf("empty list %v %v", items, err)
		}
	}
	if source.lists != 1 {
		t.Fatal("empty list was not cached")
	}
	if NewCachingReader(nil) != nil {
		t.Fatal("nil reader wrapped")
	}
}

func TestDecodeSnapshotPreservesIntegersAndEmptyLists(t *testing.T) {
	got, err := DecodeSnapshot([]byte("runtime:\n  object:\n    apiVersion: example.com/v1\n    kind: Runtime\n    spec:\n      count: 9007199254740993\npods:\n  list: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	count, found, err := unstructured.NestedInt64(got["runtime"].Object.Object, "spec", "count")
	if err != nil || !found || count != 9007199254740993 || got["pods"].List == nil {
		t.Fatalf("count=%d found=%v pods=%v err=%v", count, found, got["pods"], err)
	}
	if _, err := DecodeSnapshot([]byte("pods: {items: []}")); err == nil {
		t.Fatal("accepted a misspelled list field")
	}
}
