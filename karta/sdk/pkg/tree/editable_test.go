// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package tree_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/tree"
)

func editableFixture() (*api.Karta, *unstructured.Unstructured) {
	definition := &api.Karta{Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{
		RootComponent: api.ComponentDefinition{Name: "deployment", Kind: &api.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, StatusDefinition: &api.StatusDefinition{}},
		ChildComponents: []api.ComponentDefinition{{Name: "worker", OwnerRef: ptr.To("deployment"), Kind: &api.GroupVersionKind{Version: "v1", Kind: "Pod"}, SpecDefinition: &api.SpecDefinition{
			PodSpec:  &api.ValueAccessor{Read: &api.ReadAccessor{Expression: "object.spec.template.spec"}, Write: &api.WriteAccessor{Path: ptr.To("/spec/template/spec")}},
			Metadata: &api.ValueAccessor{Write: &api.WriteAccessor{Path: ptr.To("/metadata/labels")}},
		}}},
	}}}
	workload := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "demo", "namespace": "team-a", "uid": "root-uid", "labels": map[string]any{"keep": "yes", "drop": "yes"}},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"schedulerName": "old", "vendor": map[string]any{"unknown": "retain"}, "containers": []any{map[string]any{"name": "trainer", "image": "old", "unknown": "retain"}}}}},
	}}
	return definition, workload
}

func openFixture(t *testing.T, definition *api.Karta, workload *unstructured.Unstructured, opts ...resource.FactoryOption) tree.Editable {
	t.Helper()
	editor, err := tree.Open(context.Background(), definition, workload, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return editor
}

func objectOf(t *testing.T, editor tree.Editable) *unstructured.Unstructured {
	t.Helper()
	value, err := editor.GetResource()
	if err != nil {
		t.Fatal(err)
	}
	return value.(*unstructured.Unstructured)
}

func TestRawMergePreservesUnknownFieldsAndOwnsSnapshots(t *testing.T) {
	definition, workload := editableFixture()
	original := workload.DeepCopy()
	editor := openFixture(t, definition, workload)
	patch := map[string]any{"schedulerName": "new"}
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.PodSpec, Value: patch}, tree.Write{Component: "worker", Field: tree.Metadata, Value: map[string]any{"drop": nil, "new": "yes"}}); err != nil {
		t.Fatal(err)
	}
	updated := objectOf(t, editor)
	spec, _, _ := unstructured.NestedMap(updated.Object, "spec", "template", "spec")
	if spec["schedulerName"] != "new" || spec["vendor"].(map[string]any)["unknown"] != "retain" {
		t.Fatalf("partial patch lost data: %#v", spec)
	}
	labels := updated.GetLabels()
	if labels["keep"] != "yes" || labels["new"] != "yes" || labels["drop"] != "" {
		t.Fatalf("merge labels: %#v", labels)
	}
	if !reflect.DeepEqual(workload, original) {
		t.Fatal("editor modified input object")
	}
	patch["schedulerName"] = "caller-changed"
	updated.Object["spec"] = nil
	snapshot := editor.Snapshot()
	snapshot.Children[0].Instances[0].ExtractedInstance.PodSpec.SchedulerName = "caller-changed"
	if editor.Snapshot().Children[0].Instances[0].ExtractedInstance.PodSpec.SchedulerName != "new" {
		t.Fatal("snapshot is not detached")
	}
	if _, ok := objectOf(t, editor).Object["spec"].(map[string]any); !ok {
		t.Fatal("returned object is not detached")
	}
}

func TestArraysReplaceAndScalarsReplace(t *testing.T) {
	definition, workload := editableFixture()
	editor := openFixture(t, definition, workload)
	patch := map[string]any{"containers": []any{map[string]any{"name": "new", "image": "new"}}, "schedulerName": "changed"}
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.PodSpec, Value: patch}); err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(objectOf(t, editor).Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 || containers[0].(map[string]any)["name"] != "new" || containers[0].(map[string]any)["unknown"] != nil {
		t.Fatalf("array was merged: %#v", containers)
	}
}

func TestMutationFailuresRollBackObjectAndTree(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		patch      any
		duplicate  bool
	}{
		{name: "top-level null", path: "/spec/template/spec", patch: nil},
		{name: "typed nil map", path: "/spec/template/spec", patch: map[string]any(nil)},
		{name: "scalar parent", path: "/spec/template/spec/schedulerName/child", patch: "bad"},
		{name: "null parent", path: "/spec/null/child", patch: "bad"},
		{name: "missing array", path: "/spec/missing/0/child", patch: "bad"},
		{name: "out of range", path: "/spec/template/spec/containers/4/image", patch: "bad"},
		{name: "invalid pointer", path: "/spec/~2bad", patch: "bad"},
		{name: "root scalar", path: "", patch: "bad"},
		{name: "identity", path: "/metadata/name", patch: "different"},
		{name: "uid", path: "/metadata/uid", patch: "different"},
		{name: "extraction type", path: "/spec/template/spec", patch: "not a pod spec"},
		{name: "duplicate", path: "/spec/template/spec", patch: map[string]any{"schedulerName": "bad"}, duplicate: true},
		{name: "overlap", path: "/spec/template", patch: map[string]any{"other": "bad"}, duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, workload := editableFixture()
			workload.Object["spec"].(map[string]any)["null"] = nil
			definition.Spec.StructureDefinition.ChildComponents[0].SpecDefinition.Metadata.Write.Path = ptr.To(tc.path)
			if tc.name == "invalid pointer" {
				definition.Spec.StructureDefinition.ChildComponents[0].SpecDefinition.Metadata.Write = &api.WriteAccessor{PathExpression: `"/spec/~2bad"`}
			}
			editor := openFixture(t, definition, workload)
			before := objectOf(t, editor)
			snapshot := editor.Snapshot()
			writes := []tree.Write{{Component: "worker", Field: tree.Metadata, Value: tc.patch}}
			if tc.duplicate {
				writes = append(writes, tree.Write{Component: "worker", Field: tree.PodSpec, Value: map[string]any{"schedulerName": "another"}})
			}
			if err := editor.Mutate(context.Background(), writes...); err == nil {
				t.Fatal("expected rejection")
			}
			if !reflect.DeepEqual(before, objectOf(t, editor)) || !reflect.DeepEqual(snapshot, editor.Snapshot()) {
				t.Fatal("failed batch published a partial mutation")
			}
		})
	}
}

func TestMissingMapParentsAndEscapedPointers(t *testing.T) {
	definition, workload := editableFixture()
	definition.Spec.StructureDefinition.ChildComponents[0].SpecDefinition.Metadata.Write.Path = ptr.To("/spec/new/annotations/a~1b~0c")
	editor := openFixture(t, definition, workload)
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.Metadata, Value: "created"}); err != nil {
		t.Fatal(err)
	}
	value, _, _ := unstructured.NestedString(objectOf(t, editor).Object, "spec", "new", "annotations", "a/b~c")
	if value != "created" {
		t.Fatalf("value = %q", value)
	}
}

func TestAllDestinationsResolveOnStartingObject(t *testing.T) {
	definition, workload := editableFixture()
	spec := workload.Object["spec"].(map[string]any)
	spec["selected"] = "a"
	spec["items"] = map[string]any{"a": map[string]any{"schedulerName": "a"}, "b": map[string]any{"schedulerName": "b"}}
	child := &definition.Spec.StructureDefinition.ChildComponents[0]
	child.SpecDefinition.Metadata.Write.Path = ptr.To("/spec/selected")
	child.SpecDefinition.PodSpec.Read.Expression = "object.spec.items[object.spec.selected]"
	child.SpecDefinition.PodSpec.Write = &api.WriteAccessor{MatchExpression: `object.spec.selected == "a"`, PathExpression: `"/spec/items/" + object.spec.selected`}
	editor := openFixture(t, definition, workload)
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.Metadata, Value: "b"}, tree.Write{Component: "worker", Field: tree.PodSpec, Value: map[string]any{"schedulerName": "changed"}}); err != nil {
		t.Fatal(err)
	}
	result := objectOf(t, editor)
	a, _, _ := unstructured.NestedString(result.Object, "spec", "items", "a", "schedulerName")
	b, _, _ := unstructured.NestedString(result.Object, "spec", "items", "b", "schedulerName")
	if a != "changed" || b != "b" {
		t.Fatalf("destinations were not frozen: a=%q b=%q", a, b)
	}
}

func TestReadMatchSkipsExpressionAndWriteHasIndependentMatch(t *testing.T) {
	definition, workload := editableFixture()
	via := definition.Spec.StructureDefinition.ChildComponents[0].SpecDefinition.PodSpec
	via.Read = &api.ReadAccessor{MatchExpression: "false", Expression: "object.missing.path"}
	factory := resource.NewComponentFactoryFromObject(definition, workload)
	component, err := factory.GetComponent("worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := component.GetPodSpec(context.Background()); !errors.Is(err, resource.ErrValueNotFound) {
		t.Fatalf("read error=%v", err)
	}
	editor := openFixture(t, definition, workload)
	if editor.Snapshot().Children[0].Instances[0].ExtractedInstance.PodSpec != nil {
		t.Fatal("whole read did not omit unmatched field")
	}
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.PodSpec, Value: map[string]any{"schedulerName": "allowed"}}); err != nil {
		t.Fatal(err)
	}
	via.Write.MatchExpression = "false"
	editor = openFixture(t, definition, workload)
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.PodSpec, Value: map[string]any{}}); err == nil {
		t.Fatal("false write match accepted")
	}
}

func TestKServeModelSelectionAndUnknownFieldPreservation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		predictor map[string]any
		canWrite  bool
	}{
		{name: "none", predictor: map[string]any{}},
		{name: "single", predictor: map[string]any{"model": map[string]any{"storageUri": "s3://example/model", "image": "old", "vendor": "keep"}}, canWrite: true},
		{name: "ambiguous", predictor: map[string]any{"model": map[string]any{"storageUri": "s3://example/a"}, "tensorflow": map[string]any{"storageUri": "s3://example/b"}}},
		{name: "escaped key", predictor: map[string]any{"a/b~c": map[string]any{"storageUri": "s3://example/model", "image": "old", "vendor": "keep"}}, canWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workload := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "serving.kserve.io/v1beta1", "kind": "InferenceService", "metadata": map[string]any{"name": "example"}, "spec": map[string]any{"predictor": tc.predictor}}}
			if tc.name == "ambiguous" {
				factory := resource.NewComponentFactoryFromObject(kartas.KServe(), workload)
				component, err := factory.GetComponent("predictor")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = component.GetFragmentedPodSpec(context.Background()); err == nil {
					t.Fatal("ambiguous read accepted")
				}
				via := component.Definition().SpecDefinition.FragmentedPodSpecDefinition.Container
				if _, err = factory.ResolveWriteTarget(context.Background(), via, "", 0); err == nil {
					t.Fatal("ambiguous write accepted")
				}
				return
			}
			editor := openFixture(t, kartas.KServe(), workload)
			before := objectOf(t, editor)
			err := editor.Mutate(context.Background(), tree.Write{Component: "predictor", Field: tree.Container, Value: map[string]any{"image": "new"}})
			if tc.canWrite {
				if err != nil {
					t.Fatal(err)
				}
				for key := range tc.predictor {
					model, _, _ := unstructured.NestedMap(objectOf(t, editor).Object, "spec", "predictor", key)
					if model["image"] != "new" || model["vendor"] != "keep" {
						t.Fatalf("model=%#v", model)
					}
				}
			} else if err == nil || !reflect.DeepEqual(before, objectOf(t, editor)) {
				t.Fatalf("unsafe selection accepted or changed object: %v", err)
			}
		})
	}
}

func TestInstanceValidationAndMutationRollback(t *testing.T) {
	for _, ids := range []string{`null`, `"a"`, `[1]`, `[""]`, `["a","a"]`} {
		definition, workload := editableFixture()
		definition.Spec.StructureDefinition.ChildComponents[0].PodSelector = &api.PodSelector{ComponentInstanceSelector: &api.ComponentInstanceSelector{Read: api.ReadAccessor{Expression: `object.metadata.name`}}}
		definition.Spec.StructureDefinition.ChildComponents[0].InstanceIds = &api.ValueAccessor{Read: &api.ReadAccessor{Expression: ids}}
		if _, err := tree.Open(context.Background(), definition, workload); err == nil {
			t.Fatalf("accepted malformed IDs %s", ids)
		}
	}
	definition, workload := editableFixture()
	child := &definition.Spec.StructureDefinition.ChildComponents[0]
	workload.Object["spec"].(map[string]any)["ids"] = []any{"a", "b"}
	child.PodSelector = &api.PodSelector{ComponentInstanceSelector: &api.ComponentInstanceSelector{Read: api.ReadAccessor{Expression: `object.metadata.name`}}}
	child.InstanceIds = &api.ValueAccessor{Read: &api.ReadAccessor{Expression: "object.spec.ids"}}
	child.SpecDefinition.PodSpec.Read.Expression = "object.spec.ids.map(id, object.spec.template.spec)"
	child.SpecDefinition.Metadata.Write.Path = ptr.To("/spec/ids")
	editor := openFixture(t, definition, workload)
	before := objectOf(t, editor)
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Instance: "a", Field: tree.Metadata, Value: []any{"a", "a"}}); err == nil {
		t.Fatal("accepted duplicate instance IDs after patch")
	}
	if !reflect.DeepEqual(before, objectOf(t, editor)) {
		t.Fatal("instance extraction failure changed object")
	}
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Instance: "missing", Field: tree.Metadata, Value: []any{}}); err == nil {
		t.Fatal("accepted missing target instance")
	}
}

type snapshotReader struct {
	names []string
	fail  bool
}

func (r *snapshotReader) Get(_ context.Context, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	r.names = append(r.names, name)
	if r.fail {
		return nil, errors.New("read denied")
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "metadata": map[string]any{"name": name, "namespace": namespace}, "spec": map[string]any{"schedulerName": name}}}, nil
}
func (*snapshotReader) List(context.Context, schema.GroupVersionKind, references.ListQuery) ([]unstructured.Unstructured, error) {
	return nil, nil
}

func referenceFixture() (*api.Karta, *unstructured.Unstructured) {
	definition, workload := editableFixture()
	workload.Object["spec"].(map[string]any)["runtime"] = "first"
	definition.Spec.StructureDefinition.References = []api.Reference{{Name: "runtime", APIVersion: "example.io/v1", Kind: "Runtime", Lookup: &api.LookupReference{NameExpression: "object.spec.runtime"}}}
	child := &definition.Spec.StructureDefinition.ChildComponents[0]
	child.SpecDefinition.PodSpec.Read.Expression = `{"schedulerName": references.runtime.spec.schedulerName}`
	child.SpecDefinition.Metadata.Write.Path = ptr.To("/spec/runtime")
	return definition, workload
}

func TestMutationRefreshesLiveReferencesAndRollsBackReadFailures(t *testing.T) {
	definition, workload := referenceFixture()
	reader := &snapshotReader{}
	editor := openFixture(t, definition, workload, resource.WithReferenceReader(reader))
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.Metadata, Value: "second"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reader.names, []string{"first", "second"}) {
		t.Fatalf("reference reads=%v", reader.names)
	}
	if got := editor.Snapshot().Children[0].Instances[0].ExtractedInstance.PodSpec.SchedulerName; got != "second" {
		t.Fatalf("stale read=%q", got)
	}
	before := objectOf(t, editor)
	reader.fail = true
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.Metadata, Value: "third"}); err == nil {
		t.Fatal("reference error accepted")
	}
	if !reflect.DeepEqual(before, objectOf(t, editor)) {
		t.Fatal("reference failure published workload")
	}
}

func TestSuppliedReferenceRecipeChangesAreRejected(t *testing.T) {
	definition, workload := referenceFixture()
	reader := &snapshotReader{}
	runtime, err := reader.Get(context.Background(), schema.GroupVersionKind{Group: "example.io", Version: "v1", Kind: "Runtime"}, "team-a", "first")
	if err != nil {
		t.Fatal(err)
	}
	supplied := references.ResolvedReferences{"runtime": references.NewLookupValue(runtime)}
	editor := openFixture(t, definition, workload, resource.WithReferences(supplied))
	before := objectOf(t, editor)
	err = editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.Metadata, Value: "second"})
	if err == nil || !strings.Contains(err.Error(), "changes reference") {
		t.Fatalf("recipe change error=%v", err)
	}
	if !reflect.DeepEqual(before, objectOf(t, editor)) {
		t.Fatal("changed supplied recipe published workload")
	}
	runtime.Object["spec"].(map[string]any)["schedulerName"] = "caller-change"
	if err := editor.Mutate(context.Background(), tree.Write{Component: "worker", Field: tree.PodSpec, Value: map[string]any{"schedulerName": "irrelevant"}}); err != nil {
		t.Fatal(err)
	}
	if got := editor.Snapshot().Children[0].Instances[0].ExtractedInstance.PodSpec.SchedulerName; got != "first" {
		t.Fatalf("supplied snapshot changed: %q", got)
	}
}
