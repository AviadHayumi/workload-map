// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package membership

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

type memoryReader struct {
	objects []*unstructured.Unstructured
	fail    bool
	calls   int
}

func (r *memoryReader) Get(_ context.Context, gvk schema.GroupVersionKind, ns, name string) (*unstructured.Unstructured, error) {
	r.calls++
	if r.fail {
		return nil, errors.New("denied")
	}
	for _, o := range r.objects {
		if o.GroupVersionKind() == gvk && o.GetNamespace() == ns && o.GetName() == name {
			return o.DeepCopy(), nil
		}
	}
	return nil, references.ErrNotFound
}
func (r *memoryReader) List(_ context.Context, gvk schema.GroupVersionKind, q references.ListQuery) ([]unstructured.Unstructured, error) {
	r.calls++
	if r.fail {
		return nil, errors.New("denied")
	}
	out := []unstructured.Unstructured{}
	for _, o := range r.objects {
		if o.GroupVersionKind() == gvk && o.GetNamespace() == q.Namespace && q.Selector.Matches(labels.Set(o.GetLabels())) {
			out = append(out, *o.DeepCopy())
		}
	}
	return out, nil
}
func object(kind, name string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{}}
	o.SetAPIVersion("example.org/v1")
	o.SetKind(kind)
	o.SetName(name)
	o.SetNamespace("test")
	o.SetUID(types.UID(name))
	return o
}
func owner(o *unstructured.Unstructured, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: o.GetAPIVersion(), Kind: o.GetKind(), Name: o.GetName(), UID: o.GetUID(), Controller: ptr.To(controller)}
}
func pod(name string, o *unstructured.Unstructured) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name), OwnerReferences: []metav1.OwnerReference{owner(o, true)}}}
}
func definition() *api.Karta {
	return &api.Karta{Spec: api.KartaSpec{StructureDefinition: api.StructureDefinition{
		RootComponent:   api.ComponentDefinition{Name: "root", Kind: &api.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Root"}, StatusDefinition: &api.StatusDefinition{}, SpecDefinition: &api.SpecDefinition{PodSpec: &api.ValueAccessor{Read: &api.ReadAccessor{Expression: "{}"}}}, PodSelector: &api.PodSelector{ComponentTypeSelector: &api.ComponentTypeSelector{Read: api.ReadAccessor{Expression: "object.metadata.?labels[?'native'].orValue(null)"}}}},
		References:      []api.Reference{{Name: "dependency", APIVersion: "example.org/v1", Kind: "Target", Lookup: &api.LookupReference{NameExpression: "'target'"}}},
		ChildComponents: []api.ComponentDefinition{{Name: "dependency", OwnerRef: ptr.To("root"), PodSelector: &api.PodSelector{FromReferences: []api.PodReference{{Name: "dependency"}}, ComponentTypeSelector: &api.ComponentTypeSelector{Read: api.ReadAccessor{Expression: "'yes'"}, Value: ptr.To("yes")}}}},
	}}}
}
func fixture() (Input, Snapshot, *memoryReader) {
	root := object("Root", "root")
	target := object("Target", "target")
	intermediate := object("ReplicaSet", "rs")
	intermediate.SetOwnerReferences([]metav1.OwnerReference{owner(target, true)})
	pods := []corev1.Pod{pod("p1", intermediate), pod("p2", target)}
	input := Input{Workloads: []WorkloadInput{{root, definition()}}, Pods: pods, ScopeComplete: true}
	snapshot := Snapshot{References: map[types.UID]references.ResolvedReferences{"root": {"dependency": references.NewLookupValue(target)}}, Owners: []*unstructured.Unstructured{intermediate, target}, Complete: true}
	return input, snapshot, &memoryReader{objects: []*unstructured.Unstructured{intermediate, target}}
}
func TestReaderAndSnapshotMatch(t *testing.T) {
	input, snapshot, reader := fixture()
	live, err := Resolve(t.Context(), input, WithReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, manual) {
		t.Fatalf("modes differ: live=%+v manual=%+v", live, manual)
	}
	if !live.Complete || len(live.Pods[ComponentKey{"root", "dependency"}]) != 2 || !live.Targets["target"].Foldable {
		t.Fatalf("unexpected result: %+v", live)
	}
	// Results own their copies.
	live.Pods[ComponentKey{"root", "dependency"}][0].Name = "changed"
	if input.Pods[0].Name == "changed" {
		t.Fatal("result aliases input")
	}
}
func TestMembershipFailureRetainsNative(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Input, *Snapshot)
	}{
		{"scope incomplete", func(i *Input, _ *Snapshot) { i.ScopeComplete = false }},
		{"snapshot incomplete", func(_ *Input, s *Snapshot) { s.Complete = false }},
		{"missing owner", func(_ *Input, s *Snapshot) { s.Owners = s.Owners[1:] }},
		{"stale owner UID", func(_ *Input, s *Snapshot) { s.Owners[0].SetUID("recreated") }},
		{"wrong target namespace", func(_ *Input, s *Snapshot) { s.References["root"]["dependency"].Object.SetNamespace("other") }},
		{"wrong target name", func(_ *Input, s *Snapshot) { s.References["root"]["dependency"].Object.SetName("other") }},
		{"missing target UID", func(_ *Input, s *Snapshot) { s.References["root"]["dependency"].Object.SetUID("") }},
		{"target predicate error", func(i *Input, _ *Snapshot) {
			i.Workloads[0].Definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.FromReferences[0].MatchExpression = "target.spec.unknown"
		}},
		{"selector error", func(i *Input, _ *Snapshot) {
			i.Workloads[0].Definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentTypeSelector.Read.Expression = "object.spec.unknown"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input, snapshot, _ := fixture()
			native := pod("native", input.Workloads[0].Object)
			native.Labels = map[string]string{"native": "yes"}
			input.Pods = append(input.Pods, native)
			tc.change(&input, &snapshot)
			result, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
			if err != nil {
				t.Fatal(err)
			}
			if result.Complete || len(result.Targets) != 0 || len(result.Pods[ComponentKey{"root", "dependency"}]) != 0 || len(result.Pods[ComponentKey{"root", "root"}]) != 1 {
				t.Fatalf("lost native fallback or published incomplete extras: %+v", result)
			}
		})
	}
}
func TestDisabledAndInactiveReferences(t *testing.T) {
	input, snapshot, reader := fixture()
	reader.fail = true
	// Disabled references need no target lookup. Owner evidence is supplied separately.
	result, err := Resolve(t.Context(), input, WithSnapshot(snapshot), WithReferencesEnabled(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) > 0 {
		t.Fatal("disabled references produced targets")
	}
	input.Pods = nil
	input.Workloads[0].Definition.Spec.StructureDefinition.References[0].MatchExpression = "false"
	input.Workloads[0].Definition.Spec.StructureDefinition.References[0].Lookup.NameExpression = "object.spec.missing"
	result, err = Resolve(t.Context(), input, WithReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 0 || !result.Complete || len(result.Targets) > 0 {
		t.Fatalf("inactive reference evaluated or fetched: %+v calls=%d", result, reader.calls)
	}
}
func TestFinalNonControllerEdgeOnly(t *testing.T) {
	input, snapshot, _ := fixture()
	snapshot.Owners[0].SetOwnerReferences([]metav1.OwnerReference{owner(snapshot.Owners[1], false)})
	result, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pods[ComponentKey{"root", "dependency"}]) != 2 {
		t.Fatalf("Strimzi final edge not followed: %+v", result)
	}
	// A non-controller intermediate is not traversed.
	input.Pods[0].OwnerReferences[0].Controller = ptr.To(false)
	result, err = Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pods[ComponentKey{"root", "dependency"}]) != 1 {
		t.Fatalf("arbitrary non-controller graph traversed: %+v", result)
	}
}
func TestConflictsRejectWholeTargetButPreserveIndependentTarget(t *testing.T) {
	input, snapshot, _ := fixture()
	second := object("Root", "second")
	input.Workloads = append(input.Workloads, WorkloadInput{second, definition()})
	snapshot.References["second"] = snapshot.References["root"]
	independent := object("Target", "independent")
	input.Pods = append(input.Pods, pod("p3", independent))
	rootDef := input.Workloads[0].Definition
	rootDef.Spec.StructureDefinition.References = append(rootDef.Spec.StructureDefinition.References, api.Reference{Name: "independent", APIVersion: "example.org/v1", Kind: "Target", Lookup: &api.LookupReference{NameExpression: "'independent'"}})
	rootDef.Spec.StructureDefinition.ChildComponents[0].PodSelector.FromReferences = append(rootDef.Spec.StructureDefinition.ChildComponents[0].PodSelector.FromReferences, api.PodReference{Name: "independent"})
	snapshot.References["root"] = references.ResolvedReferences{"dependency": snapshot.References["root"]["dependency"], "independent": references.NewLookupValue(independent)}
	result, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Targets["target"]; ok {
		t.Fatal("conflicted target accepted")
	}
	if !result.Targets["independent"].Foldable || len(result.Pods[ComponentKey{"root", "dependency"}]) != 1 {
		t.Fatalf("independent target lost: %+v", result)
	}
}
func TestEmptyPodsCanFoldButUnmatchedDescendantCannot(t *testing.T) {
	input, snapshot, _ := fixture()
	input.Pods = nil
	result, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Targets["target"].Foldable {
		t.Fatal("successfully empty target must be foldable")
	}
	input.Pods = []corev1.Pod{pod("unmatched", snapshot.Owners[1])}
	input.Workloads[0].Definition.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentTypeSelector.Read.Expression = "null"
	result, err = Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if result.Targets["target"].Foldable {
		t.Fatal("unaccounted descendant must prevent folding")
	}
}
func TestValueOnlyReferenceFailureDoesNotDisableMembership(t *testing.T) {
	input, snapshot, _ := fixture()
	input.Workloads[0].Definition.Spec.StructureDefinition.References = append(input.Workloads[0].Definition.Spec.StructureDefinition.References, api.Reference{Name: "valueOnly", APIVersion: "example.org/v1", Kind: "Runtime", Lookup: &api.LookupReference{NameExpression: "object.spec.missing"}})
	result, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Targets) != 1 {
		t.Fatalf("value-only reference affected membership: %+v", result)
	}
}
func TestCancellationAndConflictingSources(t *testing.T) {
	input, snapshot, reader := fixture()
	if _, err := Resolve(t.Context(), input, WithReader(reader), WithSnapshot(snapshot)); err == nil {
		t.Fatal("both sources accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Resolve(ctx, input, WithSnapshot(snapshot)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
func TestNamespaceFailureIsolation(t *testing.T) {
	a, sa, _ := fixture()
	a.ScopeComplete = false
	b, sb, _ := fixture()
	for _, w := range b.Workloads {
		w.Object.SetNamespace("other")
	}
	for i := range b.Pods {
		b.Pods[i].Namespace = "other"
	}
	for _, o := range sb.Owners {
		o.SetNamespace("other")
	}
	sb.References["root"]["dependency"].Object.SetNamespace("other")
	failed, err := Resolve(t.Context(), a, WithSnapshot(sa))
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := Resolve(t.Context(), b, WithSnapshot(sb))
	if err != nil {
		t.Fatal(err)
	}
	if failed.Complete || !healthy.Complete || len(healthy.Targets) != 1 {
		t.Fatal("namespace observations interfered")
	}
}

func TestReferenceOnlyComponentKeepsNativeSelectorlessPods(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		input, snapshot, _ := fixture()
		input.Workloads[0].Definition.Spec.StructureDefinition.RootComponent.PodSelector = nil
		input.Pods = append(input.Pods, pod("native", input.Workloads[0].Object))
		result, err := Resolve(t.Context(), input, WithSnapshot(snapshot), WithReferencesEnabled(enabled))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Pods[ComponentKey{"root", "root"}]) != 1 {
			t.Fatalf("enabled=%v lost native selectorless Pod", enabled)
		}
		for _, p := range result.Pods[ComponentKey{"root", "dependency"}] {
			if p.Name == "native" {
				t.Fatal("reference-only component claimed native Pod")
			}
		}
	}
}
func TestZeroPodTargetCannotCrossIndependentNativeOwner(t *testing.T) {
	input, snapshot, _ := fixture()
	input.Pods = nil
	independent := object("Root", "independent")
	def := definition()
	def.Spec.StructureDefinition.References = nil
	def.Spec.StructureDefinition.ChildComponents = nil
	input.Workloads = append(input.Workloads, WorkloadInput{independent, def})
	target := snapshot.References["root"]["dependency"].Object
	target.SetOwnerReferences([]metav1.OwnerReference{owner(independent, true)})
	result, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := result.Targets[target.GetUID()]; exists {
		t.Fatal("zero-Pod target crossed independent workload boundary")
	}
}
