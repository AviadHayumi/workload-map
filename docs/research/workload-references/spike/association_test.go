// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package association

import (
	"encoding/json"
	"strings"
	"testing"
)

func testPod() Object {
	return Object{APIVersion: "v1", Kind: "Pod", Metadata: Metadata{
		Namespace: "team-a", Name: "worker", UID: "pod-one",
		Labels:      map[string]string{"example.org/group": "training"},
		Annotations: map[string]string{"example.org/group-name": "training"},
	}}
}

func testRule() Rule {
	return Rule{Relation: "group", Source: Field{From: "label", Key: "example.org/group"},
		TargetSet: "groups", MatchAttribute: "name"}
}

func testTarget(id, namespace string) Target {
	return Target{Namespace: namespace, ID: id, Attributes: map[string]string{"name": "training"}}
}

func evaluateOne(t *testing.T, capture Capture, rule Rule) Result {
	t.Helper()
	results, err := Evaluate(capture, []Rule{rule})
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%+v, error=%v", results, err)
	}
	return results[0]
}

func TestTargetCardinalityAndNamespaceIsolation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		targets    []Target
		status     Status
		candidates int
	}{
		{"zero", nil, Absent, 0},
		{"one", []Target{testTarget("group-a", "team-a")}, Resolved, 1},
		{"multiple", []Target{testTarget("group-a", "team-a"), testTarget("group-b", "team-a")}, Ambiguous, 2},
		{"another namespace", []Target{testTarget("group-a", "team-b")}, Absent, 0},
		{"same name in two namespaces", []Target{testTarget("group-a", "team-a"), testTarget("group-a", "team-b")}, Resolved, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := evaluateOne(t, Capture{Pods: []Object{testPod()}, TargetSets: map[string]TargetSet{
				"groups": {Complete: true, Targets: tc.targets},
			}}, testRule())
			if result.Status != tc.status || result.Candidates != tc.candidates || (result.Target != nil) != (tc.status == Resolved) {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestFailureDoesNotBecomeAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		sets map[string]TargetSet
	}{
		{"not supplied", nil},
		{"partial list", map[string]TargetSet{"groups": {Complete: false}}},
		{"denied", map[string]TargetSet{"groups": {Complete: true, Error: "permission denied"}}},
		{"fetch failed", map[string]TargetSet{"groups": {Error: "read unavailable"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := evaluateOne(t, Capture{Pods: []Object{testPod()}, TargetSets: tc.sets}, testRule())
			if result.Status != Failed || result.Error == "" || result.Target != nil {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestMetadataAssociationDoesNotInventAnObject(t *testing.T) {
	for _, source := range []Field{{From: "label", Key: "example.org/group"}, {From: "annotation", Key: "example.org/group-name"}} {
		result := evaluateOne(t, Capture{Pods: []Object{testPod()}}, Rule{Relation: "logical-group", Source: source})
		if result.Status != ObservedMetadata || result.Value != "training" || result.Target != nil || result.Pod.Metadata.UID != "pod-one" {
			t.Fatalf("result=%+v", result)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"workload_uid", "owner_resolution", "ownerReferences"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("invented ownership: %s", encoded)
			}
		}
	}
}

func TestPodCreateDeleteRecreateAndMetadataChange(t *testing.T) {
	rule := Rule{Relation: "logical-group", Source: Field{From: "label", Key: "example.org/group"}}
	pod := testPod()
	first := evaluateOne(t, Capture{Pods: []Object{pod}}, rule)
	deleted, err := Evaluate(Capture{}, []Rule{rule})
	if err != nil || len(deleted) != 0 {
		t.Fatalf("deleted=%+v, error=%v", deleted, err)
	}
	pod.Metadata.UID = "pod-two"
	pod.Metadata.Labels[rule.Source.Key] = "another-group"
	recreated := evaluateOne(t, Capture{Pods: []Object{pod}}, rule)
	if first.Pod.Metadata.UID == recreated.Pod.Metadata.UID || recreated.Value != "another-group" || first.Value != "training" {
		t.Fatalf("old=%+v, recreated=%+v", first, recreated)
	}
	delete(pod.Metadata.Labels, rule.Source.Key)
	if result := evaluateOne(t, Capture{Pods: []Object{pod}}, rule); result.Status != Absent || result.Value != "" {
		t.Fatalf("removed label retained a relation: %+v", result)
	}
}

func TestKubernetesTargetDeleteRecreate(t *testing.T) {
	target := testTarget("training", "team-a")
	target.Kubernetes = &Object{APIVersion: "example.org/v1", Kind: "Group", Metadata: Metadata{
		Namespace: "team-a", Name: "training", UID: "group-one",
	}}
	capture := Capture{Pods: []Object{testPod()}, TargetSets: map[string]TargetSet{"groups": {Complete: true, Targets: []Target{target}}}}
	first := evaluateOne(t, capture, testRule())
	capture.TargetSets["groups"] = TargetSet{Complete: true}
	if deleted := evaluateOne(t, capture, testRule()); deleted.Status != Absent || deleted.Target != nil {
		t.Fatalf("deleted=%+v", deleted)
	}
	replacement := *target.Kubernetes
	replacement.Metadata.UID = "group-two"
	target.Kubernetes = &replacement
	capture.TargetSets["groups"] = TargetSet{Complete: true, Targets: []Target{target}}
	recreated := evaluateOne(t, capture, testRule())
	if first.Target.Kubernetes.Metadata.UID != "group-one" || recreated.Target.Kubernetes.Metadata.UID != "group-two" {
		t.Fatalf("old=%+v, recreated=%+v", first, recreated)
	}
}

func TestSharedPodDoesNotIdentifyOneConcurrentJob(t *testing.T) {
	capture := Capture{Pods: []Object{testPod()}, TargetSets: map[string]TargetSet{"jobs": {
		Complete: true, Targets: []Target{testTarget("job-1", "team-a"), testTarget("job-2", "team-a")},
	}}}
	rule := testRule()
	rule.Relation, rule.TargetSet = "exclusive-job", "jobs"
	result := evaluateOne(t, capture, rule)
	if result.Status != Ambiguous || result.Candidates != 2 || result.Target != nil {
		t.Fatalf("result=%+v", result)
	}
	rule.TargetSet, rule.MatchAttribute, rule.Relation = "", "", "cluster"
	if cluster := evaluateOne(t, capture, rule); cluster.Status != ObservedMetadata {
		t.Fatalf("cluster=%+v", cluster)
	}
}

func TestMissingIdentityIsRejected(t *testing.T) {
	pod := testPod()
	pod.Metadata.UID = ""
	if _, err := Evaluate(Capture{Pods: []Object{pod}}, []Rule{testRule()}); err == nil {
		t.Fatal("missing Pod UID accepted")
	}
	target := testTarget("training", "team-a")
	target.Kubernetes = &Object{APIVersion: "example.org/v1", Kind: "Group", Metadata: Metadata{Namespace: "team-a", Name: "training"}}
	result := evaluateOne(t, Capture{Pods: []Object{testPod()}, TargetSets: map[string]TargetSet{"groups": {Complete: true, Targets: []Target{target}}}}, testRule())
	if result.Status != Failed || result.Target != nil {
		t.Fatalf("target missing UID accepted: %+v", result)
	}
}

func TestInvalidConfigurationFails(t *testing.T) {
	for _, rules := range [][]Rule{
		{{Relation: "", Source: Field{From: "label", Key: "a"}}},
		{{Relation: "x", Source: Field{From: "spec", Key: "a"}}},
		{{Relation: "x", Source: Field{From: "label", Key: "a"}, TargetSet: "groups"}},
		{testRule(), testRule()},
	} {
		if _, err := Evaluate(Capture{}, rules); err == nil {
			t.Fatalf("invalid rules accepted: %+v", rules)
		}
	}
}
