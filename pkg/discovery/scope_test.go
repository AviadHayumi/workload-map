// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog/kartas"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

type sdkPodScope interface{ Pods() ([]*corev1.Pod, error) }
type sdkComponentScope interface {
	Component(string) (*discovery.Component, error)
}
type sdkReplicaScope interface {
	Replica(string) (*discovery.Replica, error)
}

func sdkAssertScope(t *testing.T, scope sdkPodScope, want ...string) {
	t.Helper()
	pods, err := scope.Pods()
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, pods, want...)
}

func sdkChild(t *testing.T, scope sdkComponentScope, name string) *discovery.Component {
	t.Helper()
	result, err := scope.Component(name)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sdkInstance(t *testing.T, scope *discovery.Component, name string) *discovery.Instance {
	t.Helper()
	result, err := scope.Instance(name)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sdkReplica(t *testing.T, scope sdkReplicaScope, key string) *discovery.Replica {
	t.Helper()
	result, err := scope.Replica(key)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sdkJobSetFixture(t *testing.T, extra ...*corev1.Pod) (*discovery.Workload, *sdkReader) {
	t.Helper()
	jobsetGVK := schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}
	jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	root := sdkObject(jobsetGVK, "team", "jobset", "jobset", nil)
	root.Object["spec"] = map[string]any{"replicatedJobs": []any{map[string]any{"name": "prefill"}, map[string]any{"name": "decode"}, map[string]any{"name": "idle"}}}
	prefill0 := sdkObject(jobGVK, "team", "prefill-0", "prefill-0", root)
	prefill1 := sdkObject(jobGVK, "team", "prefill-1", "prefill-1", root)
	decode0 := sdkObject(jobGVK, "team", "decode-0", "decode-0", root)
	pods := []*corev1.Pod{
		sdkPod("prefill-0-a", prefill0, map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "prefill", "jobset.sigs.k8s.io/job-index": "0"}),
		sdkPod("prefill-0-b", prefill0, map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "prefill", "jobset.sigs.k8s.io/job-index": "0"}),
		sdkPod("prefill-1", prefill1, map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "prefill", "jobset.sigs.k8s.io/job-index": "1"}),
		sdkPod("decode-0", decode0, map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "decode", "jobset.sigs.k8s.io/job-index": "0"}),
	}
	for _, pod := range extra {
		pod.OwnerReferences = []metav1.OwnerReference{sdkOwner(root)}
	}
	pods = append(pods, extra...)
	reader := sdkNewReader(t, []*unstructured.Unstructured{root, prefill0, prefill1, decode0}, pods...)
	workload, err := sdkResolver(t, reader).Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	return workload, reader
}

func TestJobSetInstanceAndReplicaScopes(t *testing.T) {
	workload, reader := sdkJobSetFixture(t)
	before := len(reader.calls)
	jobs := sdkChild(t, workload, "replicatedjob")
	sdkAssertScope(t, jobs, "uid-prefill-0-a", "uid-prefill-0-b", "uid-prefill-1", "uid-decode-0")
	prefill := sdkInstance(t, jobs, "prefill")
	sdkAssertScope(t, prefill, "uid-prefill-0-a", "uid-prefill-0-b", "uid-prefill-1")
	sdkAssertScope(t, sdkReplica(t, prefill, "0"), "uid-prefill-0-a", "uid-prefill-0-b")
	sdkAssertScope(t, sdkReplica(t, jobs, "0"), "uid-prefill-0-a", "uid-prefill-0-b", "uid-decode-0")
	sdkAssertScope(t, sdkReplica(t, prefill, "missing"))
	sdkAssertScope(t, sdkInstance(t, jobs, "idle"))
	if _, err := jobs.Instance("not-declared"); !errors.Is(err, discovery.ErrInstanceNotFound) {
		t.Fatalf("unknown instance = %v", err)
	}
	if _, err := workload.Component("missing"); !errors.Is(err, discovery.ErrComponentNotFound) {
		t.Fatalf("unknown component = %v", err)
	}
	if _, err := sdkChild(t, workload, "jobset").Replica("0"); !errors.Is(err, discovery.ErrNoReplicaAxis) {
		t.Fatalf("absent replica axis = %v", err)
	}
	if len(reader.calls) != before {
		t.Fatal("scope navigation issued reader requests")
	}
	members, err := prefill.Pods()
	if err != nil {
		t.Fatal(err)
	}
	members[0].Labels["jobset.sigs.k8s.io/replicatedjob-name"] = "mutated"
	sdkAssertScope(t, prefill, "uid-prefill-0-a", "uid-prefill-0-b", "uid-prefill-1")
}

func TestLWSAncestorReplicaIncludesLogicalDescendants(t *testing.T) {
	lwsGVK := schema.GroupVersionKind{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet"}
	root := sdkObject(lwsGVK, "team", "serve", "lws", nil)
	pods := []*corev1.Pod{}
	for _, name := range []string{"leader-0", "worker-0-a", "worker-0-b", "leader-1", "worker-1"} {
		group, worker := "0", "1"
		if name == "leader-1" || name == "worker-1" {
			group = "1"
		}
		if name == "leader-0" || name == "leader-1" {
			worker = "0"
		}
		pod := sdkPod(name, root, map[string]string{"leaderworkerset.sigs.k8s.io/group-index": group, "leaderworkerset.sigs.k8s.io/worker-index": worker})
		if worker != "0" {
			pod.Annotations = map[string]string{"leaderworkerset.sigs.k8s.io/leader-name": "leader-" + group}
		}
		pods = append(pods, pod)
	}
	w, err := sdkResolver(t, sdkNewReader(t, []*unstructured.Unstructured{root}, pods...)).Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	groups := sdkChild(t, w, "group")
	sdkAssertScope(t, groups, "uid-leader-0", "uid-worker-0-a", "uid-worker-0-b", "uid-leader-1", "uid-worker-1")
	group0 := sdkReplica(t, groups, "0")
	sdkAssertScope(t, sdkChild(t, group0, "leader"), "uid-leader-0")
	sdkAssertScope(t, sdkChild(t, group0, "worker"), "uid-worker-0-a", "uid-worker-0-b")
	// Binding an inherited axis cannot discard the existing ancestor constraint.
	worker0 := sdkChild(t, group0, "worker")
	if _, err := worker0.Replica("1"); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("conflicting inherited replica binding = %v", err)
	}
	sdkAssertScope(t, worker0, "uid-worker-0-a", "uid-worker-0-b")
	sdkAssertScope(t, sdkReplica(t, worker0, "0"), "uid-worker-0-a", "uid-worker-0-b")
	sdkAssertScope(t, sdkReplica(t, sdkChild(t, w, "worker"), "1"), "uid-worker-1")
	sdkAssertScope(t, sdkChild(t, sdkReplica(t, sdkInstance(t, groups, ""), "1"), "leader"), "uid-leader-1")
	if _, err := group0.Component("leaderworkerset"); !errors.Is(err, discovery.ErrComponentNotFound) {
		t.Fatalf("scope escaped to ancestor: %v", err)
	}
	if _, err := group0.Component("group"); !errors.Is(err, discovery.ErrComponentNotFound) {
		t.Fatalf("self accepted as child: %v", err)
	}
}

func sdkNestedDefinition() *v1alpha1.Karta {
	return &v1alpha1.Karta{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{
		RootComponent: v1alpha1.ComponentDefinition{Name: "outer", Kind: &v1alpha1.GroupVersionKind{Group: sdkBundleGVK.Group, Version: sdkBundleGVK.Version, Kind: sdkBundleGVK.Kind},
			StatusDefinition: &v1alpha1.StatusDefinition{}, InstanceIdPath: ptr.To(".spec.outers[]"),
			PodSelector: &v1alpha1.PodSelector{ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{IdPath: ".metadata.labels.outer"}, ReplicaSelector: &v1alpha1.ReplicaSelector{KeyPath: ".metadata.labels.outerReplica"}},
		},
		ChildComponents: []v1alpha1.ComponentDefinition{
			{Name: "middle", OwnerRef: ptr.To("outer"), InstanceIdPath: ptr.To(".spec.middles[]"), PodSelector: &v1alpha1.PodSelector{ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{IdPath: ".metadata.labels.middle"}, ReplicaSelector: &v1alpha1.ReplicaSelector{KeyPath: ".metadata.labels.middleReplica"}}},
			{Name: "leaf", OwnerRef: ptr.To("middle"), InstanceIdPath: ptr.To(".spec.leaves[]"), SpecDefinition: &v1alpha1.SpecDefinition{PodSpecPath: ptr.To(".spec.template.spec")}, PodSelector: &v1alpha1.PodSelector{ComponentInstanceSelector: &v1alpha1.ComponentInstanceSelector{IdPath: ".metadata.labels.leaf"}, ReplicaSelector: &v1alpha1.ReplicaSelector{KeyPath: ".metadata.labels.leafReplica"}}},
		},
	}}}
}

func sdkNestedFixture(t *testing.T, extra ...*corev1.Pod) *discovery.Workload {
	t.Helper()
	root := sdkObject(sdkBundleGVK, "team", "nested", "nested", nil)
	root.Object["spec"] = map[string]any{"outers": []any{"A", "B"}, "middles": []any{"x", "y", "idle"}, "leaves": []any{"left", "right"}}
	rows := []struct{ name, outer, outerReplica, middle, middleReplica, leaf, leafReplica string }{
		{"exact", "A", "0", "x", "0", "left", "0"},
		{"other-outer", "B", "0", "x", "0", "left", "0"},
		{"other-outer-replica", "A", "1", "x", "0", "left", "0"},
		{"other-middle", "A", "0", "y", "0", "left", "0"},
		{"other-middle-replica", "A", "0", "x", "1", "left", "0"},
		{"other-leaf", "A", "0", "x", "0", "right", "0"},
		{"other-leaf-replica", "A", "0", "x", "0", "left", "1"},
	}
	var pods []*corev1.Pod
	for _, row := range rows {
		pods = append(pods, sdkPod(row.name, root, map[string]string{"outer": row.outer, "outerReplica": row.outerReplica, "middle": row.middle, "middleReplica": row.middleReplica, "leaf": row.leaf, "leafReplica": row.leafReplica}))
	}
	for _, pod := range extra {
		pod.OwnerReferences = []metav1.OwnerReference{sdkOwner(root)}
	}
	pods = append(pods, extra...)
	w, err := sdkResolver(t, sdkNewReader(t, []*unstructured.Unstructured{root}, pods...), sdkNestedDefinition()).Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestNestedScopesRetainEveryAncestorBinding(t *testing.T) {
	w := sdkNestedFixture(t)
	outerA := sdkInstance(t, sdkChild(t, w, "outer"), "A")
	outerA0 := sdkReplica(t, outerA, "0")
	middle := sdkChild(t, outerA0, "middle")
	middleX := sdkInstance(t, middle, "x")
	middleX0 := sdkReplica(t, middleX, "0")
	leaf := sdkChild(t, middleX0, "leaf")
	sdkAssertScope(t, leaf, "uid-exact", "uid-other-leaf", "uid-other-leaf-replica")
	left := sdkInstance(t, leaf, "left")
	sdkAssertScope(t, left, "uid-exact", "uid-other-leaf-replica")
	sdkAssertScope(t, sdkReplica(t, left, "0"), "uid-exact")
	sdkAssertScope(t, sdkReplica(t, left, "1"), "uid-other-leaf-replica")
	sdkAssertScope(t, sdkInstance(t, middle, "idle"))
	// Creating a sibling branch must leave the existing scopes unchanged.
	outerB := sdkInstance(t, sdkChild(t, w, "outer"), "B")
	sdkAssertScope(t, sdkChild(t, outerB, "leaf"), "uid-other-outer")
	sdkAssertScope(t, sdkReplica(t, left, "0"), "uid-exact")
	sdkAssertScope(t, middleX0, "uid-exact", "uid-other-leaf", "uid-other-leaf-replica")
}

func TestAxisFailuresOnlyBlockScopesTheyCouldChange(t *testing.T) {
	bad := sdkPod("decode-no-replica", nil, map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "decode"})
	w, _ := sdkJobSetFixture(t, bad)
	jobs := sdkChild(t, w, "replicatedjob")
	sdkAssertScope(t, jobs, "uid-prefill-0-a", "uid-prefill-0-b", "uid-prefill-1", "uid-decode-0", "uid-decode-no-replica")
	prefill := sdkInstance(t, jobs, "prefill")
	sdkAssertScope(t, sdkReplica(t, prefill, "0"), "uid-prefill-0-a", "uid-prefill-0-b")
	decode := sdkInstance(t, jobs, "decode")
	sdkAssertScope(t, decode, "uid-decode-0", "uid-decode-no-replica")
	if pods, err := sdkReplica(t, decode, "0").Pods(); pods != nil || !errors.Is(err, discovery.ErrIncompleteAttribution) {
		t.Fatalf("unresolved replica = %v, %v", pods, err)
	}
	sdkAssertPods(t, w.Pods(), "uid-prefill-0-a", "uid-prefill-0-b", "uid-prefill-1", "uid-decode-0", "uid-decode-no-replica")

	unknown := sdkPod("unknown-instance", nil, map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "undeclared", "jobset.sigs.k8s.io/job-index": "0"})
	w, _ = sdkJobSetFixture(t, unknown)
	jobs = sdkChild(t, w, "replicatedjob")
	sdkAssertScope(t, jobs, "uid-prefill-0-a", "uid-prefill-0-b", "uid-prefill-1", "uid-decode-0", "uid-unknown-instance")
	if pods, err := sdkInstance(t, jobs, "prefill").Pods(); pods != nil || !errors.Is(err, discovery.ErrIncompleteAttribution) {
		t.Fatalf("unresolved instance = %v, %v", pods, err)
	}
	// A known different replica excludes the issue even though its instance is unresolved.
	sdkAssertScope(t, sdkReplica(t, sdkInstance(t, jobs, "prefill"), "1"), "uid-prefill-1")
}

func TestAncestorFailureDoesNotDiscardOtherKnownBindings(t *testing.T) {
	bad := sdkPod("bad-middle-B", nil, map[string]string{"outer": "B", "outerReplica": "0", "middleReplica": "0", "leaf": "left", "leafReplica": "0"})
	w := sdkNestedFixture(t, bad)
	outerA := sdkInstance(t, sdkChild(t, w, "outer"), "A")
	middleA := sdkChild(t, outerA, "middle")
	sdkAssertScope(t, sdkInstance(t, middleA, "x"), "uid-exact", "uid-other-outer-replica", "uid-other-middle-replica", "uid-other-leaf", "uid-other-leaf-replica")
	outerB := sdkInstance(t, sdkChild(t, w, "outer"), "B")
	sdkAssertScope(t, outerB, "uid-other-outer", "uid-bad-middle-B")
	if pods, err := sdkInstance(t, sdkChild(t, outerB, "middle"), "x").Pods(); pods != nil || !errors.Is(err, discovery.ErrIncompleteAttribution) {
		t.Fatalf("unresolved ancestor instance = %v, %v", pods, err)
	}
	// The leaf's own replica axis is still proven when the middle instance is not.
	sdkAssertScope(t, sdkReplica(t, sdkChild(t, outerB, "leaf"), "0"), "uid-other-outer", "uid-bad-middle-B")
}

func TestSelectorAmbiguityOnlyAffectsCandidateBranches(t *testing.T) {
	k := &v1alpha1.Karta{Spec: v1alpha1.KartaSpec{StructureDefinition: v1alpha1.StructureDefinition{RootComponent: v1alpha1.ComponentDefinition{
		Name: "root", Kind: &v1alpha1.GroupVersionKind{Group: sdkBundleGVK.Group, Version: sdkBundleGVK.Version, Kind: sdkBundleGVK.Kind}, StatusDefinition: &v1alpha1.StatusDefinition{},
	}}}}
	for _, name := range []string{"a", "b", "c"} {
		k.Spec.StructureDefinition.ChildComponents = append(k.Spec.StructureDefinition.ChildComponents, v1alpha1.ComponentDefinition{Name: name, OwnerRef: ptr.To("root"), SpecDefinition: &v1alpha1.SpecDefinition{PodSpecPath: ptr.To(".spec")}, PodSelector: &v1alpha1.PodSelector{ComponentTypeSelector: &v1alpha1.ComponentTypeSelector{KeyPath: ".metadata.labels." + name}}})
	}
	root := sdkObject(sdkBundleGVK, "team", "ambiguous", "ambiguous", nil)
	ambiguous := sdkPod("ambiguous", root, map[string]string{"a": "true", "b": "true"})
	clean := sdkPod("clean", root, map[string]string{"c": "true"})
	w, err := sdkResolver(t, sdkNewReader(t, []*unstructured.Unstructured{root}, ambiguous, clean), k).Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, w.Pods(), "uid-ambiguous", "uid-clean")
	sdkAssertScope(t, sdkChild(t, w, "c"), "uid-clean")
	for _, name := range []string{"root", "a", "b"} {
		if pods, err := sdkChild(t, w, name).Pods(); pods != nil || !errors.Is(err, discovery.ErrIncompleteAttribution) {
			t.Fatalf("ambiguous %s = %v, %v", name, pods, err)
		}
	}
	issues := w.MembershipIssues()
	if len(issues) != 2 || issues[0].PodUID != ambiguous.UID {
		t.Fatalf("issues = %+v", issues)
	}
	original := issues[0].Cause.Error()
	issues[0].Cause = errors.New("caller replacement")
	issues[0].Component = "caller replacement"
	issues[0].PodUID = "caller replacement"
	fresh := w.MembershipIssues()
	if fresh[0].Cause.Error() != original || fresh[0].Component != "a" || fresh[0].PodUID != ambiguous.UID {
		t.Fatal("diagnostic mutation escaped")
	}
}

func TestDynamoReplicaOptOutDoesNotPoisonGroupedScopes(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"}
	root := sdkObject(gvk, "team", "graph", "graph", nil)
	root.Object["spec"] = map[string]any{"services": map[string]any{"decode": map[string]any{}}}
	rs := sdkObject(sdkReplicaSetGVK, "team", "decode", "decode-rs", root)
	ungrouped := sdkPod("ungrouped", rs, map[string]string{"nvidia.com/dynamo-component": "decode"})
	grouped := sdkPod("grouped", root, map[string]string{"nvidia.com/dynamo-component": "decode", "leaderworkerset.sigs.k8s.io/group-index": "0"})
	w, err := sdkResolver(t, sdkNewReader(t, []*unstructured.Unstructured{root, rs}, ungrouped, grouped), kartas.Dynamo()).Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.MembershipIssues()) != 0 {
		t.Fatalf("jq empty became an issue: %+v", w.MembershipIssues())
	}
	decode := sdkInstance(t, sdkChild(t, w, "service"), "decode")
	sdkAssertScope(t, decode, "uid-ungrouped", "uid-grouped")
	sdkAssertScope(t, sdkReplica(t, decode, "0"), "uid-grouped")
	sdkAssertScope(t, sdkReplica(t, decode, "absent"))
}

func TestPreparationErrorOnlyBlocksItsDeclaredInstanceAxis(t *testing.T) {
	k := sdkNestedDefinition()
	k.Spec.StructureDefinition.ChildComponents[0].InstanceIdPath = ptr.To(`error("middle declarations unavailable")`)
	root := sdkObject(sdkBundleGVK, "team", "prepare-failure", "prepare-failure", nil)
	root.Object["spec"] = map[string]any{"outers": []any{"A"}, "leaves": []any{"left"}}
	pod := sdkPod("prepare-failure", root, map[string]string{"outer": "A", "outerReplica": "0", "middle": "x", "middleReplica": "0", "leaf": "left", "leafReplica": "0"})
	w, err := sdkResolver(t, sdkNewReader(t, []*unstructured.Unstructured{root}, pod), k).Workload(context.Background(), sdkRef(root))
	if err != nil {
		t.Fatal(err)
	}
	sdkAssertPods(t, w.Pods(), "uid-prepare-failure")
	sdkAssertScope(t, sdkChild(t, w, "middle"), "uid-prepare-failure")
	sdkAssertScope(t, sdkChild(t, w, "leaf"), "uid-prepare-failure")
	if _, err := sdkChild(t, w, "middle").Instance("x"); !errors.Is(err, discovery.ErrIncompleteAttribution) {
		t.Fatalf("failed declaration was usable: %v", err)
	}
	outer := sdkReplica(t, sdkInstance(t, sdkChild(t, w, "outer"), "A"), "0")
	sdkAssertScope(t, sdkReplica(t, sdkChild(t, outer, "leaf"), "0"), "uid-prepare-failure")
}
