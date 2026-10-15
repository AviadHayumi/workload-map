// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package attribution

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

func preparedFor(t *testing.T, k *v1alpha1.Karta, root *unstructured.Unstructured) *Prepared {
	t.Helper()
	plan, err := Compile(k)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(context.Background(), plan, root)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func evaluateFor(t *testing.T, pod *corev1.Pod, prepared *Prepared) Membership {
	t.Helper()
	result, err := Evaluate(context.Background(), pod, prepared)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func observedPod(labels map[string]string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-uid", Name: "pod", Labels: labels}}
}

func rootObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.org/v1", "kind": "Workload",
		"spec": map[string]any{
			"roots": []any{"outer"}, "groups": []any{"group-a", "group-b"}, "leaves": []any{"leaf-a"},
		},
	}}
}

func nestedDefinition() *v1alpha1.Karta {
	return &v1alpha1.Karta{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{
		RootComponent: v1alpha1.ComponentDefinition{
			Name: "root", Kind: &v1alpha1.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Workload"},
			StatusDefinition: &v1alpha1.StatusDefinition{},
			InstanceIdPath:   ptr.To(".spec.roots[]"),
			PodSelector: &v1alpha1.PodSelector{
				ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{IdPath: ".metadata.labels.root"},
				ReplicaSelector:           &v1alpha1.ReplicaSelector{KeyPath: ".metadata.labels.outer"},
			},
		},
		ChildComponents: []v1alpha1.ComponentDefinition{
			{Name: "group", OwnerRef: ptr.To("root"), InstanceIdPath: ptr.To(".spec.groups[]"),
				PodSelector: &v1alpha1.PodSelector{
					ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{IdPath: ".metadata.labels.group"},
					ReplicaSelector:           &v1alpha1.ReplicaSelector{KeyPath: ".metadata.labels.middle"},
				},
			},
			{Name: "leaf", OwnerRef: ptr.To("group"), InstanceIdPath: ptr.To(".spec.leaves[]"),
				SpecDefinition: &v1alpha1.SpecDefinition{PodSpecPath: ptr.To(".spec.template.spec")},
				PodSelector: &v1alpha1.PodSelector{
					ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{IdPath: ".metadata.labels.leaf"},
					ReplicaSelector:           &v1alpha1.ReplicaSelector{KeyPath: ".metadata.labels.inner"},
				},
			},
		},
	}}}
}

func nestedPod() *corev1.Pod {
	return observedPod(map[string]string{"root": "outer", "outer": "3", "group": "group-b", "middle": "5", "leaf": "leaf-a", "inner": "7"})
}

func TestCatalogMembership(t *testing.T) {
	tests := []struct {
		name       string
		definition *v1alpha1.Karta
		root       *unstructured.Unstructured
		pod        *corev1.Pod
		expected   []Coordinate
	}{
		{
			name: "JobSet", definition: kartas.Jobset(),
			root:     &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"replicatedJobs": []any{map[string]any{"name": "prefill"}, map[string]any{"name": "decode"}}}}},
			pod:      observedPod(map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "decode", "jobset.sigs.k8s.io/job-index": "2"}),
			expected: []Coordinate{{Component: "jobset", Instance: ptr.To("")}, {Component: "replicatedjob", Instance: ptr.To("decode"), Replica: ptr.To("2")}},
		},
		{
			name: "LWS leader", definition: kartas.LWS(), root: rootObject(),
			pod:      observedPod(map[string]string{"leaderworkerset.sigs.k8s.io/worker-index": "0", "leaderworkerset.sigs.k8s.io/group-index": "2"}),
			expected: []Coordinate{{Component: "leaderworkerset", Instance: ptr.To("")}, {Component: "group", Instance: ptr.To(""), Replica: ptr.To("2")}, {Component: "leader", Instance: ptr.To("")}},
		},
		{
			name: "LWS worker", definition: kartas.LWS(), root: rootObject(),
			pod:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "worker", Labels: map[string]string{"leaderworkerset.sigs.k8s.io/worker-index": "2", "leaderworkerset.sigs.k8s.io/group-index": "1"}, Annotations: map[string]string{"leaderworkerset.sigs.k8s.io/leader-name": "leader"}}},
			expected: []Coordinate{{Component: "leaderworkerset", Instance: ptr.To("")}, {Component: "group", Instance: ptr.To(""), Replica: ptr.To("1")}, {Component: "worker", Instance: ptr.To("")}},
		},
		{
			name: "Dynamo grouped", definition: kartas.Dynamo(),
			root:     &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"services": map[string]any{"decode": map[string]any{}}}}},
			pod:      observedPod(map[string]string{"nvidia.com/dynamo-component": "decode", "grove.io/podcliquescalinggroup-replica-index": "4"}),
			expected: []Coordinate{{Component: "dynamographdeployment", Instance: ptr.To("")}, {Component: "service", Instance: ptr.To("decode"), Replica: ptr.To("4")}},
		},
		{
			name: "Dynamo deployment opts out", definition: kartas.Dynamo(),
			root:     &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"services": map[string]any{"decode": map[string]any{}}}}},
			pod:      &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod", Labels: map[string]string{"nvidia.com/dynamo-component": "decode"}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Controller: ptr.To(true)}}}},
			expected: []Coordinate{{Component: "dynamographdeployment", Instance: ptr.To("")}, {Component: "service", Instance: ptr.To("decode")}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared := preparedFor(t, test.definition, test.root)
			got := evaluateFor(t, test.pod, prepared)
			if len(got.Issues) != 0 {
				t.Fatalf("unexpected issues: %+v", got.Issues)
			}
			if !reflect.DeepEqual(got.Path, test.expected) {
				t.Fatalf("path = %#v, want %#v", got.Path, test.expected)
			}
			if got.Component != test.expected[len(test.expected)-1].Component {
				t.Fatalf("component = %q", got.Component)
			}
		})
	}
}

func TestNestedAxesAndPartialFailures(t *testing.T) {
	prepared := preparedFor(t, nestedDefinition(), rootObject())
	expected := []Coordinate{
		{Component: "root", Instance: ptr.To("outer"), Replica: ptr.To("3")},
		{Component: "group", Instance: ptr.To("group-b"), Replica: ptr.To("5")},
		{Component: "leaf", Instance: ptr.To("leaf-a"), Replica: ptr.To("7")},
	}
	got := evaluateFor(t, nestedPod(), prepared)
	if !reflect.DeepEqual(got.Path, expected) || len(got.Issues) != 0 {
		t.Fatalf("membership = %+v", got)
	}

	for _, test := range []struct {
		name, field, value, component, axis string
		index                               int
	}{
		{name: "missing parent instance", field: "group", component: "group", axis: "instance", index: 1},
		{name: "unknown parent instance", field: "group", value: "absent", component: "group", axis: "instance", index: 1},
		{name: "missing parent replica", field: "middle", component: "group", axis: "replica", index: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := nestedPod()
			if test.value == "" {
				delete(pod.Labels, test.field)
			} else {
				pod.Labels[test.field] = test.value
			}
			got := evaluateFor(t, pod, prepared)
			if got.Component != "leaf" || len(got.Path) != 3 || len(got.Issues) != 1 {
				t.Fatalf("membership = %+v", got)
			}
			issue := got.Issues[0]
			if issue.PodUID != pod.UID || issue.Component != test.component || issue.Axis != test.axis || issue.Cause == nil {
				t.Fatalf("issue = %+v", issue)
			}
			want := slices.Clone(expected)
			if test.axis == "instance" {
				want[test.index].Instance = nil
			} else {
				want[test.index].Replica = nil
			}
			if !reflect.DeepEqual(got.Path, want) {
				t.Fatalf("known coordinates were lost: %+v", got.Path)
			}
			if test.value != "" && !errors.As(issue.Cause, new(resource.InstanceNotFoundError)) {
				t.Fatalf("unknown instance cause = %v", issue.Cause)
			}
		})
	}
}

func TestStrictProducerSelectors(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*v1alpha1.Karta)
		want   string
		issues []string
	}{
		{name: "sole explicit mismatch", modify: func(k *v1alpha1.Karta) {
			k.Spec.StructureDefinition.ChildComponents[1].PodSelector.ComponentTypeSelector = &v1alpha1.ComponentTypeSelector{KeyPath: ".metadata.labels.role", Value: ptr.To("worker")}
		}, issues: []string{""}},
		{name: "sole selectorless implicit", modify: func(*v1alpha1.Karta) {}, want: "leaf"},
		{name: "most specific ancestor overlap", modify: func(k *v1alpha1.Karta) {
			k.Spec.StructureDefinition.RootComponent.SpecDefinition = &v1alpha1.SpecDefinition{PodSpecPath: ptr.To(".spec")}
			k.Spec.StructureDefinition.RootComponent.PodSelector.ComponentTypeSelector = &v1alpha1.ComponentTypeSelector{KeyPath: ".metadata.labels.root"}
			k.Spec.StructureDefinition.ChildComponents[1].PodSelector.ComponentTypeSelector = &v1alpha1.ComponentTypeSelector{KeyPath: ".metadata.labels.leaf"}
		}, want: "leaf"},
		{name: "unrelated overlap", modify: func(k *v1alpha1.Karta) {
			leaf := &k.Spec.StructureDefinition.ChildComponents[1]
			leaf.PodSelector.ComponentTypeSelector = &v1alpha1.ComponentTypeSelector{KeyPath: ".metadata.labels.leaf"}
			other := leaf.DeepCopy()
			other.Name, other.OwnerRef = "other", ptr.To("root")
			k.Spec.StructureDefinition.ChildComponents = append(k.Spec.StructureDefinition.ChildComponents, *other)
		}, issues: []string{"leaf", "other"}},
		{name: "multiple selectorless are not implicit", modify: func(k *v1alpha1.Karta) {
			other := k.Spec.StructureDefinition.ChildComponents[1].DeepCopy()
			other.Name, other.OwnerRef = "other", ptr.To("root")
			k.Spec.StructureDefinition.ChildComponents = append(k.Spec.StructureDefinition.ChildComponents, *other)
		}, issues: []string{""}},
		{name: "selector error retains competing uncertainty", modify: func(k *v1alpha1.Karta) {
			leaf := &k.Spec.StructureDefinition.ChildComponents[1]
			leaf.PodSelector.ComponentTypeSelector = &v1alpha1.ComponentTypeSelector{KeyPath: ".metadata.labels.leaf"}
			other := leaf.DeepCopy()
			other.Name, other.OwnerRef = "other", ptr.To("root")
			other.PodSelector.ComponentTypeSelector.KeyPath = `error("selector failed")`
			k.Spec.StructureDefinition.ChildComponents = append(k.Spec.StructureDefinition.ChildComponents, *other)
		}, issues: []string{"other", "leaf"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			k := nestedDefinition()
			test.modify(k)
			got := evaluateFor(t, nestedPod(), preparedFor(t, k, rootObject()))
			if got.Component != test.want {
				t.Fatalf("component = %q, want %q", got.Component, test.want)
			}
			var components []string
			for _, issue := range got.Issues {
				if issue.Axis != "component" {
					t.Fatalf("issue = %+v", issue)
				}
				components = append(components, issue.Component)
			}
			if !reflect.DeepEqual(components, test.issues) {
				t.Fatalf("issues = %v, want %v", components, test.issues)
			}
		})
	}
}

func TestPreparationFailuresAreScopedAndDesiredStateIsLazy(t *testing.T) {
	k := nestedDefinition()
	group := &k.Spec.StructureDefinition.ChildComponents[0]
	group.InstanceIdPath = ptr.To(`error("instances failed")`)
	k.Spec.StructureDefinition.RootComponent.StatusDefinition.PhaseDefinition = &v1alpha1.PhaseDefinition{Path: `error("status failed")`}
	k.Spec.StructureDefinition.ChildComponents[1].SpecDefinition.PodSpecPath = ptr.To(`error("spec failed")`)
	k.Spec.StructureDefinition.ChildComponents[1].ScaleDefinition = &v1alpha1.ScaleDefinition{ReplicasPath: ptr.To(`error("scale failed")`)}
	prepared := preparedFor(t, k, rootObject())
	if _, err := prepared.InstanceIDs("group"); err == nil {
		t.Fatal("expected scoped preparation error")
	}
	ids, err := prepared.InstanceIDs("leaf")
	if err != nil || !reflect.DeepEqual(ids, []string{"leaf-a"}) {
		t.Fatalf("leaf IDs = %v, %v", ids, err)
	}
	got := evaluateFor(t, nestedPod(), prepared)
	if got.Component != "leaf" || len(got.Issues) != 1 || got.Issues[0].Component != "group" || got.Issues[0].Axis != "instance" {
		t.Fatalf("membership = %+v", got)
	}
	if got.Path[0].Instance == nil || got.Path[1].Replica == nil || got.Path[2].Instance == nil {
		t.Fatalf("known coordinates lost: %+v", got.Path)
	}

	// A failing declaration on an unrelated component does not affect this Pod.
	unrelated := group.DeepCopy()
	unrelated.Name = "unrelated"
	k.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = ptr.To(".spec.groups[]")
	k.Spec.StructureDefinition.ChildComponents = append(k.Spec.StructureDefinition.ChildComponents, *unrelated)
	got = evaluateFor(t, nestedPod(), preparedFor(t, k, rootObject()))
	if len(got.Issues) != 0 {
		t.Fatalf("unrelated preparation failure affected membership: %+v", got.Issues)
	}
}

func TestReplicaEmptyAndErrors(t *testing.T) {
	for _, expression := range []string{"empty", "null", `""`, `"a", "b"`, `error("replica failed")`} {
		t.Run(expression, func(t *testing.T) {
			k := nestedDefinition()
			k.Spec.StructureDefinition.ChildComponents[0].PodSelector.ReplicaSelector.KeyPath = expression
			got := evaluateFor(t, nestedPod(), preparedFor(t, k, rootObject()))
			if got.Path[1].Replica != nil || got.Path[0].Replica == nil || got.Path[2].Replica == nil {
				t.Fatalf("replica coordinates = %+v", got.Path)
			}
			if expression == "empty" {
				if len(got.Issues) != 0 {
					t.Fatalf("empty must opt out: %+v", got.Issues)
				}
			} else if len(got.Issues) != 1 || got.Issues[0].Axis != "replica" || got.Issues[0].Component != "group" {
				t.Fatalf("issues = %+v", got.Issues)
			}
		})
	}
}

func TestInputAndOutputMutationIsolation(t *testing.T) {
	k, root := nestedDefinition(), rootObject()
	prepared := preparedFor(t, k, root)
	k.Spec.StructureDefinition.ChildComponents[1].PodSelector.ComponentInstanceSelector.IdPath = `"wrong"`
	root.Object["spec"].(map[string]any)["leaves"] = []any{"wrong"}
	copy := prepared.Plan().Definition()
	copy.Spec.StructureDefinition.ChildComponents[1].Name = "wrong"
	ids, err := prepared.InstanceIDs("leaf")
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = "wrong"
	pod := nestedPod()
	first := evaluateFor(t, pod, prepared)
	*first.Path[0].Instance = "changed"
	first.Path[1].Component = "changed"
	pod.Labels["group"] = "absent"
	failed := evaluateFor(t, pod, prepared)
	failed.Issues[0].Component = "changed"
	failed.Issues[0].Cause = errors.New("changed")
	got := evaluateFor(t, nestedPod(), prepared)
	if len(got.Issues) != 0 || got.Path[0].Instance == nil || *got.Path[0].Instance != "outer" || got.Path[1].Component != "group" {
		t.Fatalf("mutations escaped: %+v", got)
	}
	again, err := prepared.InstanceIDs("leaf")
	if err != nil || !reflect.DeepEqual(again, []string{"leaf-a"}) {
		t.Fatalf("IDs mutated: %v, %v", again, err)
	}
}

func TestCancellation(t *testing.T) {
	plan, err := Compile(nestedDefinition())
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(canceled, plan, rootObject()); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare error = %v", err)
	}
	prepared := preparedFor(t, nestedDefinition(), rootObject())
	if _, err := Evaluate(canceled, nestedPod(), prepared); !errors.Is(err, context.Canceled) {
		t.Fatalf("evaluate error = %v", err)
	}
	if _, err := InstanceIDs(canceled, nestedDefinition(), rootObject(), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("IDs error = %v", err)
	}

	for _, phase := range []string{"prepare", "evaluate"} {
		t.Run(phase, func(t *testing.T) {
			k := nestedDefinition()
			if phase == "prepare" {
				k.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = ptr.To("0 | until(. > 1000000000; . + 1) | tostring")
			} else {
				k.Spec.StructureDefinition.ChildComponents[1].PodSelector.ReplicaSelector.KeyPath = "0 | until(. > 1000000000; . + 1) | tostring"
			}
			plan, err := Compile(k)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if phase == "prepare" {
				_, err = Prepare(ctx, plan, rootObject())
			} else {
				prepared, prepareErr := Prepare(context.Background(), plan, rootObject())
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				_, err = Evaluate(ctx, nestedPod(), prepared)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation became scoped issue: %v", err)
			}
		})
	}
}

func TestCompileValidationAndInstanceModes(t *testing.T) {
	if _, err := Compile(nil); err == nil {
		t.Fatal("nil definition compiled")
	}
	k := nestedDefinition()
	k.Spec.StructureDefinition.ChildComponents[0].PodSelector.ComponentInstanceSelector = nil
	if _, err := Compile(k); err == nil {
		t.Fatal("missing parent instance selector compiled")
	}
	k = nestedDefinition()
	k.Spec.StructureDefinition.ChildComponents[0].OwnerRef = ptr.To("leaf")
	if _, err := Compile(k); err == nil {
		t.Fatal("cyclic components compiled")
	}
	k = nestedDefinition()
	all, err := InstanceIDs(context.Background(), k, rootObject(), true)
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := InstanceIDs(context.Background(), k, rootObject(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || len(leaves) != 1 || !reflect.DeepEqual(leaves["leaf"], []string{"leaf-a"}) {
		t.Fatalf("all = %v, leaves = %v", all, leaves)
	}
	prepared := preparedFor(t, k, rootObject())
	if _, err := prepared.InstanceIDs("missing"); err == nil {
		t.Fatal("unknown component succeeded")
	}
	var root *unstructured.Unstructured
	if _, err := Prepare(context.Background(), prepared.Plan(), root); err == nil {
		t.Fatal("typed-nil root succeeded")
	}
}

func TestEmptyDeclarationsAndAnonymousInstance(t *testing.T) {
	k := nestedDefinition()
	k.Spec.StructureDefinition.ChildComponents[1].InstanceIdPath = ptr.To("empty")
	prepared := preparedFor(t, k, rootObject())
	ids, err := prepared.InstanceIDs("leaf")
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty declaration = %v, %v", ids, err)
	}
	got := evaluateFor(t, nestedPod(), prepared)
	if got.Component != "leaf" || got.Path[2].Instance != nil || len(got.Issues) != 1 || got.Issues[0].Axis != "instance" {
		t.Fatalf("undeclared instance resolved: %+v", got)
	}
	k.Spec.StructureDefinition.ChildComponents[1].InstanceIdPath = nil
	k.Spec.StructureDefinition.ChildComponents[1].PodSelector.ComponentInstanceSelector = nil
	got = evaluateFor(t, nestedPod(), preparedFor(t, k, rootObject()))
	if got.Path[2].Instance == nil || *got.Path[2].Instance != "" || len(got.Issues) != 0 {
		t.Fatalf("anonymous instance lost: %+v", got)
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	prepared := preparedFor(t, nestedDefinition(), rootObject())
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			for range 5 {
				got, err := Evaluate(context.Background(), nestedPod(), prepared)
				if err != nil {
					t.Error(err)
					return
				}
				if got.Component != "leaf" || len(got.Issues) != 0 {
					t.Errorf("membership = %+v", got)
				}
				ids, err := prepared.InstanceIDs("leaf")
				if err != nil {
					t.Error(err)
					return
				}
				ids[0] = "local mutation"
			}
		})
	}
	group.Wait()
}
