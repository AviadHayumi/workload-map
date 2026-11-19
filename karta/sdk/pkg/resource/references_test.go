// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

type factoryReader struct {
	calls int
	err   error
	found bool
}

func (r *factoryReader) Get(context.Context, schema.GroupVersionKind, string, string) (*unstructured.Unstructured, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	if !r.found {
		return nil, references.ErrNotFound
	}
	return &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"image": "runtime-image"}}}, nil
}
func (*factoryReader) List(context.Context, schema.GroupVersionKind, references.ListQuery) ([]unstructured.Unstructured, error) {
	return []unstructured.Unstructured{}, nil
}

func referenceReadFixture(source string) (*api.Karta, *unstructured.Unstructured) {
	return &api.Karta{Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{
		RootComponent: api.ComponentDefinition{Name: "job", SpecDefinition: &api.SpecDefinition{FragmentedPodSpecDefinition: &api.FragmentedPodSpecDefinition{Image: &api.ValueAccessor{Read: &api.ReadAccessor{Expression: source}}}}},
		References:    []api.Reference{{Name: "runtime", APIVersion: "example.io/v1", Kind: "Runtime", Lookup: &api.LookupReference{NameExpression: `"runtime"`}}},
	}}}, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "TrainJob", "metadata": map[string]any{"name": "job"}, "spec": map[string]any{"image": "override"}}}
}

func extractFactoryImage(t *testing.T, f *ComponentFactory) (string, error) {
	t.Helper()
	root, err := f.GetRootComponent()
	if err != nil {
		return "", err
	}
	values, err := root.GetFragmentedPodSpec(context.Background())
	if err != nil {
		return "", err
	}
	return values[""].Image, nil
}

func TestFactoryReferenceDependencyCachingAndFailures(t *testing.T) {
	definition, workload := referenceReadFixture("object.spec.image")
	reader := &factoryReader{found: true}
	factory := NewComponentFactoryFromObject(definition, workload, WithReferenceReader(reader))
	if got, err := extractFactoryImage(t, factory); err != nil || got != "override" || reader.calls != 0 {
		t.Fatalf("independent read=%q err=%v calls=%d", got, err, reader.calls)
	}
	definition.Spec.StructureDefinition.RootComponent.SpecDefinition.FragmentedPodSpecDefinition.Image.Read.Expression = `has(object.spec.image) ? object.spec.image : references.runtime.spec.image`
	reader.err = errors.New("forbidden")
	factory = NewComponentFactoryFromObject(definition, workload, WithReferenceReader(reader))
	if _, err := extractFactoryImage(t, factory); err == nil {
		t.Fatal("untaken reference branch hid a fetch error")
	}
	reader.err = nil
	for range 2 {
		if got, err := extractFactoryImage(t, factory); err != nil || got != "override" {
			t.Fatalf("retry=%q err=%v", got, err)
		}
	}
	if reader.calls != 2 {
		t.Fatalf("successful resolution not cached or failure cached: calls=%d", reader.calls)
	}
}

func TestFactoryDisabledReferencesOverrideSuppliedValues(t *testing.T) {
	definition, workload := referenceReadFixture(`"runtime" in references ? references.runtime.spec.image : object.spec.image`)
	supplied := references.ResolvedReferences{"runtime": references.NewLookupValue(&unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"image": "supplied"}}})}
	factory := NewComponentFactoryFromObject(definition, workload, WithReferences(supplied), WithReferencesEnabled(false))
	if got, err := extractFactoryImage(t, factory); err != nil || got != "override" {
		t.Fatalf("disabled read=%q err=%v", got, err)
	}
	factory = NewComponentFactoryFromObject(definition, workload)
	if _, err := extractFactoryImage(t, factory); !errors.Is(err, expression.ErrReferencesNotSupported) {
		t.Fatalf("missing source error=%v", err)
	}
	reader := &factoryReader{err: errors.New("must not read")}
	factory = NewComponentFactoryFromObject(definition, workload, WithReferenceReader(reader), WithReferencesEnabled(false))
	if got, err := extractFactoryImage(t, factory); err != nil || got != "override" || reader.calls != 0 {
		t.Fatalf("disabled reader=%q err=%v calls=%d", got, err, reader.calls)
	}
}

func TestFactoryMissingLookupFallbackAndSuppliedEligibility(t *testing.T) {
	definition, workload := referenceReadFixture(`has(object.spec.image) ? object.spec.image : references.runtime.spec.image`)
	factory := NewComponentFactoryFromObject(definition, workload, WithReferenceReader(&factoryReader{}))
	if got, err := extractFactoryImage(t, factory); err != nil || got != "override" {
		t.Fatalf("missing lookup fallback=%q err=%v", got, err)
	}
	delete(workload.Object["spec"].(map[string]any), "image")
	factory = NewComponentFactoryFromObject(definition, workload, WithReferenceReader(&factoryReader{}))
	if _, err := extractFactoryImage(t, factory); err == nil {
		t.Fatal("missing required fallback accepted")
	}
	definition.Spec.StructureDefinition.References[0].MatchExpression = "false"
	definition.Spec.StructureDefinition.References[0].Lookup.NameExpression = "object.missing.path"
	definition.Spec.StructureDefinition.RootComponent.SpecDefinition.FragmentedPodSpecDefinition.Image.Read.Expression = `"runtime" in references ? "bad" : "absent"`
	supplied := references.ResolvedReferences{"runtime": references.NewLookupValue(&unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"image": "supplied"}}})}
	factory = NewComponentFactoryFromObject(definition, workload, WithReferences(supplied))
	if got, err := extractFactoryImage(t, factory); err != nil || got != "absent" {
		t.Fatalf("inactive supplied reference=%q err=%v", got, err)
	}
}

func TestFactoryInputAndReadResultsAreDetached(t *testing.T) {
	definition, workload := referenceReadFixture("object.spec.image")
	factory := NewComponentFactoryFromObject(definition, workload)
	definition.Spec.StructureDefinition.RootComponent.SpecDefinition.FragmentedPodSpecDefinition.Image.Read.Expression = `"changed"`
	workload.Object["spec"].(map[string]any)["image"] = "changed"
	first, err := factory.GetResource()
	if err != nil {
		t.Fatal(err)
	}
	before := first.DeepCopyObject()
	first.(*unstructured.Unstructured).Object["spec"] = nil
	second, err := factory.GetResource()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, second) {
		t.Fatal("resource result aliases factory")
	}
	if got, err := extractFactoryImage(t, factory); err != nil || got != "override" {
		t.Fatalf("snapshot image=%q err=%v", got, err)
	}
}

func TestComponentActionsRefreshFactoryReferences(t *testing.T) {
	definition, workload := referenceReadFixture("references.runtime.spec.image")
	definition.Spec.StructureDefinition.RootComponent.SuspendDefinition = &api.SuspendDefinition{SuspendActions: []api.SuspendAction{{Write: api.WriteAccessor{Path: ptr.To("/spec/suspend")}, Value: "true"}}, ResumeActions: []api.SuspendAction{{Write: api.WriteAccessor{Path: ptr.To("/spec/suspend")}, Value: "false"}}}
	reader := &factoryReader{found: true}
	factory := NewComponentFactoryFromObject(definition, workload, WithReferenceReader(reader))
	first, err := factory.GetRootComponent()
	if err != nil {
		t.Fatal(err)
	}
	second, err := factory.GetRootComponent()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.GetFragmentedPodSpec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = first.Suspend(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = second.GetFragmentedPodSpec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 2 {
		t.Fatalf("cached component reused old references: calls=%d", reader.calls)
	}
	if err = second.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = first.GetFragmentedPodSpec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 3 {
		t.Fatalf("resume reused old references: calls=%d", reader.calls)
	}
}

func TestFactoryEvaluationLimitReachesRunner(t *testing.T) {
	definition, workload := referenceReadFixture(`[1,2,3,4].map(n,n*n).map(n,string(n))[0]`)
	factory := NewComponentFactoryFromObject(definition, workload, WithEvaluationLimits(1, time.Second))
	if _, err := extractFactoryImage(t, factory); err == nil {
		t.Fatal("factory ignored CEL cost limit")
	}
}
