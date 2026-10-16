// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package identity

import (
	"fmt"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

func rule() Rule {
	return Rule{Name: "test", MatchExpression: "resource['namespace'] == 'test'", SubmittedByExpression: "'user' in annotations ? annotations['user'] : ''", IdentitySourceExpression: "'tenant-a'", SubmitterTypeExpression: "'unknown'"}
}

func TestPlatformRulesFromChartExample(t *testing.T) {
	data, err := os.ReadFile("../../examples/submitter-attribution/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Exporter struct {
			Attribution Config `json:"attribution"`
		} `json:"exporter"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	e, err := New(values.Exporter.Attribution)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, namespace, kind, want, status string
		labels, annotations                 map[string]string
	}{
		{name: "runai root", namespace: "runai-attribution-lab", kind: "TrainingWorkload", annotations: map[string]string{"run.ai/submitted-by": "alice@example.test"}, want: "alice@example.test", status: Partial},
		{name: "runai pod needs workload inheritance", namespace: "runai-attribution-lab", kind: "Pod", annotations: map[string]string{"user": "alice@example.test"}, status: Unknown},
		{name: "generic user outside scope", namespace: "other", kind: "Pod", annotations: map[string]string{"user": "alice@example.test"}, status: Unknown},
		{name: "osmo short identity", namespace: "osmo-submitter-lab", kind: "Pod", labels: map[string]string{"osmo.workflow_id": "workflow-1", "osmo.submitted_by": "alice"}, want: "alice", status: Partial},
		{name: "osmo missing identity", namespace: "osmo-submitter-lab", kind: "Pod", labels: map[string]string{"osmo.workflow_id": "workflow-1"}, status: Unknown},
		{name: "nvcf account is not a submitter", namespace: "nvcf-backend", kind: "Pod", labels: map[string]string{"nca-id": "account", "function-id": "function"}, status: Unknown},
		{name: "canonical precedes lossy osmo", namespace: "osmo-submitter-lab", kind: "Pod", labels: map[string]string{"osmo.workflow_id": "workflow-1", "osmo.submitted_by": "alice"}, annotations: map[string]string{"workload.dgxc.nvidia.com/submitter-id": "alice@example.test", "workload.dgxc.nvidia.com/identity-source": "tenant"}, want: "alice@example.test", status: Partial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := e.Evaluate(t.Context(), &metav1.ObjectMeta{Namespace: tc.namespace, Labels: tc.labels, Annotations: tc.annotations}, schema.GroupVersionKind{Kind: tc.kind})
			if got.Submitter != tc.want || got.Status != tc.status {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestMetadataExtraction(t *testing.T) {
	for _, tc := range []struct{ name, user, namespace, status string }{
		{"full", "alice@example.test", "test", Complete},
		{"missing", "", "test", Unknown},
		{"outside scope", "alice", "other", Unknown},
		{"too long", strings.Repeat("a", 513), "test", EvaluationError},
		{"control character", "alice\nadmin", "test", EvaluationError},
		{"whitespace", " alice", "test", EvaluationError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := New(Config{Rules: []Rule{rule()}})
			if err != nil {
				t.Fatal(err)
			}
			obj := &metav1.ObjectMeta{Namespace: tc.namespace, Annotations: map[string]string{"user": tc.user}}
			result := e.Evaluate(t.Context(), obj, schema.GroupVersionKind{Version: "v1", Kind: "Pod"})
			if result.Status != tc.status {
				t.Fatalf("got %+v", result)
			}
		})
	}
}

func TestTenKeysAndConditionalMapping(t *testing.T) {
	r := rule()
	r.SubmittedByExpression = "''"
	for i := 9; i >= 0; i-- {
		r.SubmittedByExpression = fmt.Sprintf("'key%d' in annotations ? annotations['key%d'] : (%s)", i, i, r.SubmittedByExpression)
	}
	e, err := New(Config{Rules: []Rule{r}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		obj := &metav1.ObjectMeta{Namespace: "test", Annotations: map[string]string{fmt.Sprintf("key%d", i): "alice"}}
		if got := e.Evaluate(t.Context(), obj, schema.GroupVersionKind{}); got.Submitter != "alice" {
			t.Fatalf("key %d: %+v", i, got)
		}
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, expr := range []string{"", "42", "missingVariable", "annotations['user'] +", strings.Repeat("x", 8193)} {
		r := rule()
		r.SubmittedByExpression = expr
		if _, err := New(Config{Rules: []Rule{r}}); err == nil {
			t.Fatalf("accepted %q", expr)
		}
	}
	for _, data := range []string{`{"rules":[],"typo":true}`, `{"rules":[]} {}`, `{}`} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatal("accepted invalid JSON configuration")
		}
	}
	if _, err := New(Config{Rules: []Rule{rule(), rule()}}); err == nil {
		t.Fatal("accepted duplicate rule name")
	}
}

func TestBoundedEvaluationAndAbsentMaps(t *testing.T) {
	r := rule()
	r.SubmittedByExpression = "annotations['missing']"
	e, err := New(Config{Rules: []Rule{r}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Evaluate(t.Context(), &metav1.ObjectMeta{Namespace: "test"}, schema.GroupVersionKind{}); got.Status != EvaluationError {
		t.Fatalf("missing key: %+v", got)
	}
	r.MatchExpression = "labels.all(k, labels.all(j, k == j || k != j))"
	e, err = New(Config{Rules: []Rule{r}})
	if err != nil {
		t.Fatal(err)
	}
	obj := &metav1.ObjectMeta{Namespace: "test", Labels: map[string]string{}}
	for i := 0; i < 200; i++ {
		obj.Labels[fmt.Sprint(i)] = "value"
	}
	if got := e.Evaluate(t.Context(), obj, schema.GroupVersionKind{}); got.Status != EvaluationError {
		t.Fatalf("cost limit: %+v", got)
	}
}

func TestConflictAndPartialIdentity(t *testing.T) {
	a := Result{Submitter: "alice", Source: "tenant-a", Type: Unknown, Status: Complete, Rule: "a"}
	b := a
	b.Source = "tenant-b"
	if got := Merge(a, b); got.Status != Conflict || got.Submitter != "" {
		t.Fatalf("cross tenant merge: %+v", got)
	}
	b = a
	b.Status = Partial
	if got := Merge(a, b); got.Status != Partial {
		t.Fatalf("partial promoted: %+v", got)
	}
	b = a
	b.Type = "service"
	a.Type = "human"
	if got := Merge(a, b); got.Status != Conflict {
		t.Fatalf("type conflict: %+v", got)
	}
	r := rule()
	r.Partial = true
	e, err := New(Config{Rules: []Rule{r}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Evaluate(t.Context(), &metav1.ObjectMeta{Namespace: "test", Annotations: map[string]string{"user": "alice"}}, schema.GroupVersionKind{}); got.Status != Partial {
		t.Fatalf("partial rule: %+v", got)
	}
}

func BenchmarkEvaluate(b *testing.B) {
	e, err := New(Config{Rules: []Rule{rule()}})
	if err != nil {
		b.Fatal(err)
	}
	obj := &metav1.ObjectMeta{Namespace: "test", Annotations: map[string]string{"user": "alice@example.test"}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		e.Evaluate(b.Context(), obj, schema.GroupVersionKind{Version: "v1", Kind: "Pod"})
	}
}

func TestFingerprintTracksOnlyCELInputs(t *testing.T) {
	obj := &metav1.ObjectMeta{Name: "one", Namespace: "ns", Labels: map[string]string{"a": "bc", "ab": "c"}, Annotations: map[string]string{"user": "alice"}}
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	before := Fingerprint(obj, gvk)
	copy := obj.DeepCopy()
	copy.ResourceVersion = "new"
	copy.Generation = 99
	if Fingerprint(copy, gvk) != before {
		t.Fatal("non-CEL metadata invalidates result")
	}
	copy.Annotations["user"] = "bob"
	if Fingerprint(copy, gvk) == before {
		t.Fatal("annotation change ignored")
	}
	copy = obj.DeepCopy()
	copy.Labels = map[string]string{"a": "b", "ab": "cc"}
	if Fingerprint(copy, gvk) == before {
		t.Fatal("field boundaries collide")
	}
	if Fingerprint(obj, schema.GroupVersionKind{Version: "v2", Kind: "Pod"}) == before {
		t.Fatal("version input ignored")
	}
}

func TestComplexExpressionFromChartExample(t *testing.T) {
	data, err := os.ReadFile("../../examples/submitter-attribution/values-complex.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		Exporter struct {
			Attribution Config `json:"attribution"`
		} `json:"exporter"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	e, err := New(values.Exporter.Attribution)
	if err != nil {
		t.Fatal(err)
	}
	keys := [][]string{
		{"workload.dgxc.nvidia.com/submitter-id", "run.ai/submitted-by", "workflow.example.com/submitter", "platform.example.com/username", "platform.example.com/created-by"},
		{"identity.example.com/submitted-by", "osmo.submitted_by", "workflow.example.com/submitter", "platform.example.com/username", "platform.example.com/created-by"},
	}
	for side, group := range keys {
		for _, key := range group {
			obj := &metav1.ObjectMeta{Labels: map[string]string{}, Annotations: map[string]string{}}
			if side == 0 {
				obj.Annotations[key] = "alice"
			} else {
				obj.Labels[key] = "alice"
			}
			if got := e.Evaluate(t.Context(), obj, schema.GroupVersionKind{}); got.Submitter != "alice" {
				t.Fatalf("%d/%s: %+v", side, key, got)
			}
		}
	}
	for _, tc := range []struct {
		annotations, labels map[string]string
		want                string
	}{
		{want: ""},
		{annotations: map[string]string{"run.ai/submitted-by": ""}, labels: map[string]string{"osmo.submitted_by": "alice"}, want: "alice"},
		{annotations: map[string]string{"run.ai/submitted-by": "alice"}, labels: map[string]string{"osmo.submitted_by": "bob"}, want: "alice"},
	} {
		if got := e.Evaluate(t.Context(), &metav1.ObjectMeta{Annotations: tc.annotations, Labels: tc.labels}, schema.GroupVersionKind{}); got.Submitter != tc.want {
			t.Fatalf("got %+v, want %q", got, tc.want)
		}
	}
	if _, err := New(Config{SubmittedByExpression: "'alice'", Rules: []Rule{rule()}}); err == nil {
		t.Fatal("accepted ambiguous configurations")
	}
}
