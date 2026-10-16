// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/jq"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

type definitionProviderFunc func(context.Context, schema.GroupVersionKind) (*Definition, error)

func (f definitionProviderFunc) Definition(ctx context.Context, gvk schema.GroupVersionKind) (*Definition, error) {
	return f(ctx, gvk)
}

type definitionDirectReader struct {
	Reader
	lists int
}

func (r *definitionDirectReader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	r.lists++
	return r.Reader.ListPods(ctx, namespace)
}

type definitionCapturedReader struct{ *Index }

func TestDefinitionRegistryPublication(t *testing.T) {
	input := kartas.Deployment()
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	registry, err := NewDefinitionRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.Definition(t.Context(), gvk)
	if err != nil || first == nil || first.GVK() != gvk {
		t.Fatalf("initial definition = %v, %v", first, err)
	}
	if err := registry.Replace(input.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	unchanged, err := registry.Definition(t.Context(), gvk)
	if err != nil || unchanged != first {
		t.Fatalf("unchanged selection did not reuse compiled handle: %v", err)
	}
	input.Name = "caller-mutated"
	copy := first.Karta()
	copy.Spec.StructureDefinition.RootComponent.Kind.Version = "caller-mutated"
	if first.Karta().Name == input.Name || first.GVK() != gvk || first.Karta().Spec.StructureDefinition.RootComponent.Kind.Version != "v1" {
		t.Fatal("compiled definition retained mutable caller state")
	}

	for name, definitions := range map[string][]*v1alpha1.Karta{
		"nil":                 {nil},
		"duplicate":           {kartas.Deployment(), kartas.Deployment()},
		"invalid after valid": {kartas.StatefulSet(), nil},
		"missing root": {{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{
			RootComponent: v1alpha1.ComponentDefinition{Name: "root"},
		}}}},
		"invalid structure": {{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{
			RootComponent: v1alpha1.ComponentDefinition{Kind: &v1alpha1.GroupVersionKind{Version: "v1", Kind: "Invalid"}},
		}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := registry.Replace(definitions...); err == nil {
				t.Fatal("invalid replacement accepted")
			}
			current, err := registry.Definition(t.Context(), gvk)
			if err != nil || current != first {
				t.Fatalf("failed publication changed last valid definition: %v", err)
			}
		})
	}

	version := kartas.Deployment()
	version.Spec.StructureDefinition.RootComponent.Kind.Version = "v2"
	if err := registry.Replace(version); err != nil {
		t.Fatal(err)
	}
	if old, err := registry.Definition(t.Context(), gvk); err != nil || old != nil {
		t.Fatalf("old GVK survived replacement: %v, %v", old, err)
	}
	newGVK := gvk
	newGVK.Version = "v2"
	if current, err := registry.Definition(t.Context(), newGVK); err != nil || current == nil {
		t.Fatalf("new GVK absent: %v, %v", current, err)
	}
	if first.Karta().Spec.StructureDefinition.RootComponent.Kind.Version != "v1" {
		t.Fatal("publication modified retained handle")
	}
	if err := registry.Replace(); err != nil {
		t.Fatal(err)
	}
	if current, err := registry.Definition(t.Context(), newGVK); err != nil || current != nil {
		t.Fatalf("empty selection did not remove definition: %v, %v", current, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := registry.Definition(ctx, gvk); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	var nilRegistry *DefinitionRegistry
	if err := nilRegistry.Replace(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil replacement = %v", err)
	}
	if _, err := nilRegistry.Definition(t.Context(), gvk); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil lookup = %v", err)
	}
	var empty DefinitionRegistry
	if definition, err := empty.Definition(t.Context(), gvk); err != nil || definition != nil {
		t.Fatalf("zero registry = %v, %v", definition, err)
	}
}

func TestDefinitionProviderDefaultsAndSelection(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	deploymentGVK := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	// Existing Definitions remains a catalog override, not a selected-only list.
	legacy, err := New(f.index, Options{Definitions: []*v1alpha1.Karta{f.definition}})
	if err != nil {
		t.Fatal(err)
	}
	if plan, _, err := legacy.plan(t.Context(), deploymentGVK); err != nil || plan == nil {
		t.Fatalf("default catalog lost: %v, %v", plan, err)
	}
	if plan, _, err := legacy.plan(t.Context(), indexBundleGVK); err != nil || plan == nil {
		t.Fatalf("custom definition lost: %v, %v", plan, err)
	}
	registry, err := NewDefinitionRegistry(f.definition)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := New(f.index, Options{DefinitionProvider: registry})
	if err != nil {
		t.Fatal(err)
	}
	if plan, _, err := selected.plan(t.Context(), deploymentGVK); err != nil || plan != nil {
		t.Fatalf("selected registry unexpectedly fell back to catalog: %v, %v", plan, err)
	}
	version := indexBundleGVK
	version.Version = "another-version"
	if plan, _, err := selected.plan(t.Context(), version); err != nil || plan != nil {
		t.Fatalf("definition matched a different version: %v, %v", plan, err)
	}
	if _, err := New(f.index, Options{Definitions: []*v1alpha1.Karta{f.definition}, DefinitionProvider: registry}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ambiguous options accepted: %v", err)
	}
	if _, err := New(f.index, Options{DefinitionProvider: (*DefinitionRegistry)(nil)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("typed nil provider accepted: %v", err)
	}
	if _, err := CompileDefinition(nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil compile = %v", err)
	}
}

func TestDefinitionProviderAcrossReaders(t *testing.T) {
	for _, mode := range []string{"index", "capture", "direct"} {
		t.Run(mode, func(t *testing.T) {
			f := attributionFixtureFor(t, 1)
			var reader Reader = f.index
			switch mode {
			case "capture":
				reader = definitionCapturedReader{f.index}
			case "direct":
				reader = &definitionDirectReader{Reader: f.index}
			}
			compiled, err := CompileDefinition(f.definition)
			if err != nil {
				t.Fatal(err)
			}
			wrong, err := CompileDefinition(kartas.Deployment())
			if err != nil {
				t.Fatal(err)
			}
			notReady := errors.New("definition source has not synchronized")
			for _, test := range []struct {
				name       string
				definition *Definition
				failure    error
				wantErr    error
			}{
				{name: "known", definition: compiled},
				{name: "absent"},
				{name: "not ready", failure: notReady, wantErr: notReady},
				{name: "wrong GVK", definition: wrong, wantErr: ErrInvalidInput},
				{name: "zero definition", definition: &Definition{}, wantErr: ErrInvalidInput},
			} {
				t.Run(test.name, func(t *testing.T) {
					calls := 0
					if direct, ok := reader.(*definitionDirectReader); ok {
						direct.lists = 0
					}
					provider := definitionProviderFunc(func(ctx context.Context, gvk schema.GroupVersionKind) (*Definition, error) {
						calls++
						if gvk != indexBundleGVK {
							t.Fatalf("provider GVK = %s", gvk)
						}
						return test.definition, test.failure
					})
					resolver, err := New(reader, Options{DefinitionProvider: provider})
					if err != nil {
						t.Fatal(err)
					}
					workload, err := resolver.WorkloadForPod(t.Context(), podReference(f.pods[0]))
					if !errors.Is(err, test.wantErr) {
						t.Fatalf("observation error = %v, want %v", err, test.wantErr)
					}
					if calls != 1 {
						t.Fatalf("provider called %d times, want once", calls)
					}
					if test.wantErr != nil {
						if direct, ok := reader.(*definitionDirectReader); ok && direct.lists != 0 {
							t.Fatal("definition failure did not prevent full Pod collection")
						}
						if workload != nil {
							t.Fatal("provider failure returned a generic success")
						}
						return
					}
					if workload.Generic() != (test.definition == nil) || len(workload.Pods()) != 1 {
						t.Fatal("definition selection changed workload membership")
					}
				})
			}
		})
	}
}

func TestLiveDefinitionChangesInvalidateAttributionCache(t *testing.T) {
	f := attributionFixtureFor(t, 2)
	registry, err := NewDefinitionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	f.resolver, err = New(f.index, Options{DefinitionProvider: registry, Cache: &CacheOptions{MaxWorkloads: 2, MaxPods: 4}})
	if err != nil {
		t.Fatal(err)
	}
	generic := attributionQuery(t, f)
	if !generic.Generic() {
		t.Fatal("empty registry was not generic")
	}
	if err := registry.Replace(f.definition); err != nil {
		t.Fatal(err)
	}
	known := attributionQuery(t, f)
	if known.Generic() || known.PodMemberships()[f.pods[0].UID].Component != "worker" {
		t.Fatal("new definition did not replace cached generic attribution")
	}
	if err := registry.Replace(f.definition.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	resynced := attributionQuery(t, f)
	if resynced.prepared != known.prepared {
		t.Fatal("unchanged definition resync discarded cached attribution")
	}
	updated := f.definition.DeepCopy()
	updated.Spec.StructureDefinition.ChildComponents[0].Name = "trainer"
	if err := registry.Replace(updated); err != nil {
		t.Fatal(err)
	}
	changed := attributionQuery(t, f)
	if changed.prepared == known.prepared || changed.PodMemberships()[f.pods[0].UID].Component != "trainer" {
		t.Fatal("definition update reused stale attribution")
	}
	invalid := updated.DeepCopy()
	invalid.Spec.StructureDefinition.RootComponent.Name = ""
	if err := registry.Replace(invalid); err == nil {
		t.Fatal("invalid definition accepted")
	}
	if after := attributionQuery(t, f); after.prepared != changed.prepared {
		t.Fatal("failed update changed active definition or cache entry")
	}
	if err := registry.Replace(); err != nil {
		t.Fatal(err)
	}
	if removed := attributionQuery(t, f); !removed.Generic() || len(removed.Pods()) != 2 {
		t.Fatal("removal retained definition or lost Pods")
	}
	if !generic.Generic() || known.PodMemberships()[f.pods[0].UID].Component != "worker" || changed.PodMemberships()[f.pods[0].UID].Component != "trainer" {
		t.Fatal("later publication changed prior observations")
	}
	for i := 0; i < 20; i++ {
		revision := f.definition.DeepCopy()
		revision.Name = fmt.Sprintf("revision-%d", i)
		if err := registry.Replace(revision); err != nil {
			t.Fatal(err)
		}
		attributionQuery(t, f)
	}
	if len(f.resolver.cache.entries) > 2 || f.resolver.cache.pods > 4 {
		t.Fatal("definition revisions escaped configured cache bounds")
	}
}

func TestDefinitionRegistryConcurrentPublicationAndQueries(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	registry, err := NewDefinitionRegistry(f.definition)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New(f.index, Options{DefinitionProvider: registry, Cache: &CacheOptions{MaxWorkloads: 2, MaxPods: 2}})
	if err != nil {
		t.Fatal(err)
	}
	changed := f.definition.DeepCopy()
	changed.Spec.StructureDefinition.ChildComponents[0].Name = "trainer"
	var workers sync.WaitGroup
	defer workers.Wait()
	for i := 0; i < 8; i++ {
		workers.Go(func() {
			for j := 0; j < 100; j++ {
				workload, err := resolver.Workload(t.Context(), reference(f.root))
				if err != nil {
					t.Error(err)
					return
				}
				membership := workload.PodMemberships()[f.pods[0].UID]
				want := "pods"
				if definition := workload.Definition(); definition != nil {
					want = definition.Spec.StructureDefinition.ChildComponents[0].Name
				}
				if membership.Component != want {
					t.Errorf("inconsistent published definition: component %s, definition %s", membership.Component, want)
					return
				}
				if len(workload.Pods()) != 1 {
					t.Error("definition update lost Pod membership")
					return
				}
			}
		})
	}
	for i := 0; i < 100; i++ {
		var selection []*v1alpha1.Karta
		switch i % 3 {
		case 1:
			selection = []*v1alpha1.Karta{f.definition}
		case 2:
			selection = []*v1alpha1.Karta{changed}
		}
		if err := registry.Replace(selection...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDefinitionProviderDoesNotAffectOwnershipAndHonorsCancellation(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	provider := definitionProviderFunc(func(context.Context, schema.GroupVersionKind) (*Definition, error) {
		calls++
		cancel()
		return nil, nil
	})
	resolver, err := New(f.index, Options{DefinitionProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	path, err := resolver.OwnershipPath(ctx, podReference(f.pods[0]))
	if err != nil || len(path) != 2 || calls != 0 {
		t.Fatalf("ownership depends on provider: path %v, calls %d, error %v", path, calls, err)
	}
	workload, err := resolver.Workload(ctx, reference(f.root))
	if !errors.Is(err, context.Canceled) || workload != nil || calls != 1 {
		t.Fatalf("provider cancellation returned success: workload %v, calls %d, error %v", workload, calls, err)
	}
}

func TestDefinitionRegistryErrorSources(t *testing.T) {
	first := kartas.Deployment()
	first.Name = "first-definition"
	second := kartas.Deployment()
	second.Namespace = "selected"
	second.Name = "second-definition"
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	_, err := NewDefinitionRegistry(first, second)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate lost invalid-input class: %v", err)
	}
	for _, want := range []string{gvk.String(), `"first-definition"`, `"selected/second-definition"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("duplicate diagnostic %q omits %q", err, want)
		}
	}
	invalid := second.DeepCopy()
	invalid.Spec.StructureDefinition.RootComponent.InstanceIdPath = ptr.To("..")
	_, err = CompileDefinition(invalid)
	if !errors.Is(err, jq.ErrRecursiveDescent) {
		t.Fatalf("compilation lost wrapped validation class: %v", err)
	}
	for _, want := range []string{gvk.String(), `"selected/second-definition"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("compile diagnostic %q omits %q", err, want)
		}
	}
	invalid.Spec.StructureDefinition.RootComponent.Kind = nil
	_, err = CompileDefinition(invalid)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), `"selected/second-definition"`) {
		t.Fatalf("missing-GVK diagnostic lost class or source: %v", err)
	}
	invalid.Name, invalid.Namespace = "", ""
	_, err = CompileDefinition(invalid)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), `"<unnamed>"`) {
		t.Fatalf("unnamed diagnostic lost class or source: %v", err)
	}
}

func TestDefinitionRegistryInformerObjectEquality(t *testing.T) {
	f := attributionFixtureFor(t, 1)
	input := f.definition.DeepCopy()
	input.TypeMeta = metav1.TypeMeta{}
	input.UID = "definition-uid"
	input.ResourceVersion = "opaque-version-a"
	input.CreationTimestamp = metav1.NewTime(time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC))
	input.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "definition-controller", Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "run.ai/v1alpha1", Time: input.CreationTimestamp.DeepCopy(), FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:spec":{}}`)}}}
	input.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Validated", Message: "valid definition", LastTransitionTime: input.CreationTimestamp}}
	registry, err := NewDefinitionRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.Definition(t.Context(), indexBundleGVK)
	if err != nil || first == nil {
		t.Fatalf("initial informer definition = %v, %v", first, err)
	}
	if !reflect.DeepEqual(first.Karta(), input) {
		t.Fatal("compilation normalized informer metadata or status")
	}
	if err := registry.Replace(input.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	unchanged, err := registry.Definition(t.Context(), indexBundleGVK)
	if err != nil || unchanged != first {
		t.Fatalf("identical informer object did not reuse its handle: %v", err)
	}
	resolver, err := New(f.index, Options{DefinitionProvider: registry, Cache: &CacheOptions{MaxWorkloads: 2, MaxPods: 2}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := resolver.Snapshot(t.Context(), resource.NewComponentFactoryFromObject(input.DeepCopy(), f.root.DeepCopy()))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*v1alpha1.Karta)
	}{
		{"resourceVersion", func(k *v1alpha1.Karta) { k.ResourceVersion = "opaque-version-b" }},
		{"labels", func(k *v1alpha1.Karta) { k.Labels = map[string]string{"example.org/revision": "next"} }},
		{"managedFields", func(k *v1alpha1.Karta) { k.ManagedFields[0].Manager = "another-controller" }},
		{"status", func(k *v1alpha1.Karta) { k.Status.Conditions[0].Message = "validation refreshed" }},
		{"TypeMeta", func(k *v1alpha1.Karta) { k.TypeMeta = metav1.TypeMeta{APIVersion: "run.ai/v1alpha1", Kind: "Karta"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			current, err := registry.Definition(t.Context(), indexBundleGVK)
			if err != nil {
				t.Fatal(err)
			}
			previous := current.Karta()
			updated := previous.DeepCopy()
			test.edit(updated)
			if err := registry.Replace(updated); err != nil {
				t.Fatal(err)
			}
			next, err := registry.Definition(t.Context(), indexBundleGVK)
			if err != nil || next == current || !reflect.DeepEqual(next.Karta(), updated) {
				t.Fatalf("whole-object change was normalized or reused: %v", err)
			}
			if _, err := resolver.Snapshot(t.Context(), resource.NewComponentFactoryFromObject(previous, f.root.DeepCopy())); !errors.Is(err, ErrStaleInput) {
				t.Fatalf("old full definition no longer rejected as stale: %v", err)
			}
			observed, err := resolver.Snapshot(t.Context(), resource.NewComponentFactoryFromObject(updated.DeepCopy(), f.root.DeepCopy()))
			if err != nil || !reflect.DeepEqual(observed.Definition(), updated) {
				t.Fatalf("updated full definition no longer matches Snapshot: %v", err)
			}
			if !reflect.DeepEqual(observed.PodMemberships(), original.PodMemberships()) || !reflect.DeepEqual(original.Definition(), input) {
				t.Fatal("metadata update changed attribution or the retained original definition")
			}
		})
	}
}
