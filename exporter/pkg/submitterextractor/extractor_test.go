// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package submitterextractor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/interpreter"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const annotationExpression = "'user' in annotations ? annotations['user'] : ''"

func TestEvaluateMetadata(t *testing.T) {
	for _, tc := range []struct {
		name       string
		expression string
		object     metav1.Object
		gvk        schema.GroupVersionKind
		want       Submitter
	}{
		{
			name: "annotation_precedes_label",
			expression: `("karta.run.ai/submitted-by" in annotations && annotations["karta.run.ai/submitted-by"] != "")
				? annotations["karta.run.ai/submitted-by"]
				: ("example.com/submitted-by" in labels ? labels["example.com/submitted-by"] : "")`,
			object: &metav1.ObjectMeta{
				Annotations: map[string]string{"karta.run.ai/submitted-by": "alice"},
				Labels:      map[string]string{"example.com/submitted-by": "bob"},
			},
			want: Submitter{Identity: "alice"},
		},
		{
			name: "empty_annotation_uses_label",
			expression: `("user" in annotations && annotations["user"] != "")
				? annotations["user"] : ("user" in labels ? labels["user"] : "")`,
			object: &metav1.ObjectMeta{
				Annotations: map[string]string{"user": ""},
				Labels:      map[string]string{"user": "bob"},
			},
			want: Submitter{Identity: "bob"},
		},
		{name: "absent_maps", expression: annotationExpression, object: &metav1.ObjectMeta{}},
		{
			name:       "resource_fields",
			expression: "resource.namespace + '/' + resource.name + '/' + resource.group + '/' + resource.version + '/' + resource.kind",
			object:     &metav1.ObjectMeta{Namespace: "tenant", Name: "training"},
			gvk:        schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
			want:       Submitter{Identity: "tenant/training/batch/v1/Job"},
		},
		{
			name:       "full_pod_is_metadata_only_input",
			expression: annotationExpression,
			object: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"user": "alice"}},
				Spec:       corev1.PodSpec{NodeName: "node"},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			},
			want: Submitter{Identity: "alice"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extractor, err := New(tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			got, err := extractor.Evaluate(t.Context(), tc.object, tc.gvk)
			if err != nil || got != tc.want {
				t.Fatalf("Evaluate() = %+v, %v; want %+v, nil", got, err, tc.want)
			}
		})
	}
}

func TestRejectInvalidExpression(t *testing.T) {
	for name, expression := range map[string]string{
		"empty":          "",
		"syntax":         "annotations['user'] +",
		"number_result":  "42",
		"boolean_result": "true",
		"dynamic_result": "dyn('alice')",
		"long":           strings.Repeat("x", maxExpressionBytes+1),
		"spec":           "spec.nodeName",
		"status":         "status.phase",
		"object":         "object.metadata.name",
		"api":            "get('Pod', 'tenant', 'training')",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(expression); err == nil {
				t.Fatal("New() accepted invalid expression")
			}
		})
	}
}

func TestParseConfiguration(t *testing.T) {
	for name, data := range map[string]string{
		"missing":         `{}`,
		"null":            `null`,
		"unknown_field":   `{"submittedByExpression":"'alice'","typo":true}`,
		"legacy_rules":    `{"rules":[]}`,
		"multiple":        `{"submittedByExpression":"'alice'"} {}`,
		"trailing":        `{"submittedByExpression":"'alice'"} !`,
		"wrong_type":      `{"submittedByExpression":42}`,
		"size":            strings.Repeat(" ", maxConfigBytes+1),
		"non_object":      `[]`,
		"invalid_program": `{"submittedByExpression":"true"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("Parse() accepted invalid configuration")
			}
		})
	}
	extractor, err := Parse([]byte(`{"submittedByExpression":"'alice'"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := extractor.Evaluate(t.Context(), &metav1.ObjectMeta{}, schema.GroupVersionKind{})
	if err != nil || got != (Submitter{Identity: "alice"}) {
		t.Fatalf("Evaluate() = %+v, %v; want alice, nil", got, err)
	}
}

func TestOutputLimits(t *testing.T) {
	extractor, err := New(annotationExpression)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		value    string
		wantFail bool
	}{
		{name: "empty"},
		{name: "unicode", value: "\u00e9lise"},
		{name: "maximum", value: strings.Repeat("a", maxOutputBytes)},
		{name: "too_long", value: strings.Repeat("a", maxOutputBytes+1), wantFail: true},
		{name: "utf8_byte_limit", value: strings.Repeat("\u00e9", maxOutputBytes/2+1), wantFail: true},
		{name: "invalid_utf8", value: string([]byte{0xff}), wantFail: true},
		{name: "newline", value: "alice\nadmin", wantFail: true},
		{name: "control", value: "alice\u007f", wantFail: true},
		{name: "leading_space", value: " alice", wantFail: true},
		{name: "trailing_space", value: "alice ", wantFail: true},
		{name: "unicode_space", value: "\u00a0alice", wantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractor.Evaluate(t.Context(), &metav1.ObjectMeta{
				Annotations: map[string]string{"user": tc.value},
			}, schema.GroupVersionKind{})
			if (err != nil) != tc.wantFail {
				t.Fatalf("Evaluate() error = %v, want failure %t", err, tc.wantFail)
			}
			want := Submitter{Identity: tc.value}
			if tc.wantFail {
				want = Submitter{}
			}
			if got != want {
				t.Fatalf("Evaluate() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestEvaluationErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		expression string
		object     metav1.Object
	}{
		{name: "missing_key", expression: "annotations['user']", object: &metav1.ObjectMeta{}},
		{name: "unexposed_resource_field", expression: "resource['uid']", object: &metav1.ObjectMeta{}},
		{name: "nil_object", expression: "'alice'"},
		{name: "nil_pod_pointer", expression: "'alice'", object: (*corev1.Pod)(nil)},
		{name: "nil_metadata_pointer", expression: "'alice'", object: (*metav1.ObjectMeta)(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extractor, err := New(tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			got, err := extractor.Evaluate(t.Context(), tc.object, schema.GroupVersionKind{})
			if err == nil || got != (Submitter{}) {
				t.Fatalf("Evaluate() = %+v, %v; want empty result and error", got, err)
			}
			if len(extractor.(*Extractor).cache) != 0 {
				t.Fatal("failed evaluation populated cache")
			}
		})
	}
}

func TestCostLimit(t *testing.T) {
	extractor, err := New("labels.all(k, labels.all(j, k == j || k != j)) ? 'alice' : ''")
	if err != nil {
		t.Fatal(err)
	}
	obj := &metav1.ObjectMeta{Labels: map[string]string{}}
	for n := range 200 {
		obj.Labels[fmt.Sprint(n)] = "value"
	}
	got, err := extractor.Evaluate(t.Context(), obj, schema.GroupVersionKind{})
	var canceled interpreter.EvalCancelledError
	if !errors.As(err, &canceled) || canceled.Cause != interpreter.CostLimitExceeded || got != (Submitter{}) {
		t.Fatalf("Evaluate() = %+v, %v; want cost limit error", got, err)
	}
}

func TestCancellation(t *testing.T) {
	extractor, err := New("'alice'")
	if err != nil {
		t.Fatal(err)
	}
	obj := &metav1.ObjectMeta{Name: "pod"}
	for _, warmCache := range []bool{false, true} {
		if warmCache {
			if _, err := extractor.Evaluate(t.Context(), obj, schema.GroupVersionKind{}); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		got, err := extractor.Evaluate(ctx, obj, schema.GroupVersionKind{})
		if !errors.Is(err, context.Canceled) || got != (Submitter{}) {
			t.Fatalf("Evaluate(canceled, warm=%t) = %+v, %v; want context.Canceled", warmCache, got, err)
		}
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := extractor.Evaluate(ctx, obj, schema.GroupVersionKind{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Evaluate(expired) error = %v, want context.DeadlineExceeded", err)
	}
}

func TestCanceledEvaluationIsNotCached(t *testing.T) {
	value, err := New("'alice'")
	if err != nil {
		t.Fatal(err)
	}
	extractor := value.(*Extractor)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	program := &countingProgram{Program: extractor.program, afterEval: cancel}
	extractor.program = program
	got, err := extractor.Evaluate(ctx, &metav1.ObjectMeta{}, schema.GroupVersionKind{})
	if !errors.Is(err, context.Canceled) || got != (Submitter{}) || len(extractor.cache) != 0 {
		t.Fatalf("Evaluate() = %+v, %v, cached=%d; want canceled and uncached", got, err, len(extractor.cache))
	}
	program.afterEval = nil
	got, err = extractor.Evaluate(t.Context(), &metav1.ObjectMeta{}, schema.GroupVersionKind{})
	if err != nil || got.Identity != "alice" || program.calls.Load() != 2 {
		t.Fatalf("Evaluate() = %+v, %v, calls=%d; want alice and fresh evaluation", got, err, program.calls.Load())
	}
}

func TestCacheTracksEveryCELInput(t *testing.T) {
	value, err := New(annotationExpression)
	if err != nil {
		t.Fatal(err)
	}
	extractor := value.(*Extractor)
	program := &countingProgram{Program: extractor.program}
	extractor.program = program
	obj := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "tenant", Name: "pod", UID: "first", ResourceVersion: "1",
		Labels: map[string]string{"app": "worker"}, Annotations: map[string]string{"user": "alice"},
	}}
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	if _, err := extractor.Evaluate(t.Context(), obj, gvk); err != nil {
		t.Fatal(err)
	}
	copy := obj.DeepCopy()
	copy.ResourceVersion = "2"
	copy.Generation = 3
	copy.Status.Phase = corev1.PodRunning
	copy.Spec.NodeName = "node"
	copy.UID = "replacement"
	if _, err := extractor.Evaluate(t.Context(), copy, gvk); err != nil {
		t.Fatal(err)
	}
	if program.calls.Load() != 1 {
		t.Fatal("non-CEL inputs forced reevaluation")
	}
	for _, mutate := range []func(*corev1.Pod, *schema.GroupVersionKind){
		func(p *corev1.Pod, _ *schema.GroupVersionKind) { p.Namespace = "other" },
		func(p *corev1.Pod, _ *schema.GroupVersionKind) { p.Name = "other" },
		func(p *corev1.Pod, _ *schema.GroupVersionKind) { p.Labels["app"] = "other" },
		func(p *corev1.Pod, _ *schema.GroupVersionKind) { p.Annotations["user"] = "bob" },
		func(_ *corev1.Pod, g *schema.GroupVersionKind) { g.Group = "other" },
		func(_ *corev1.Pod, g *schema.GroupVersionKind) { g.Version = "v2" },
		func(_ *corev1.Pod, g *schema.GroupVersionKind) { g.Kind = "Job" },
	} {
		copy, kind := obj.DeepCopy(), gvk
		mutate(copy, &kind)
		before := program.calls.Load()
		got, err := extractor.Evaluate(t.Context(), copy, kind)
		if err != nil || got.Identity != copy.Annotations["user"] || program.calls.Load() != before+1 {
			t.Fatalf("Evaluate(changed input) = %+v, %v, calls=%d; want %q and new evaluation", got, err, program.calls.Load(), copy.Annotations["user"])
		}
	}
}

func TestCacheBoundAndRecency(t *testing.T) {
	value, err := New("resource.name")
	if err != nil {
		t.Fatal(err)
	}
	extractor := value.(*Extractor)
	for n := range maxCachedResults {
		if _, err := extractor.Evaluate(t.Context(), &metav1.ObjectMeta{Name: fmt.Sprint(n)}, schema.GroupVersionKind{}); err != nil {
			t.Fatal(err)
		}
	}
	first := &metav1.ObjectMeta{Name: "0"}
	if _, err := extractor.Evaluate(t.Context(), first, schema.GroupVersionKind{}); err != nil {
		t.Fatal(err)
	}
	if _, err := extractor.Evaluate(t.Context(), &metav1.ObjectMeta{Name: "new"}, schema.GroupVersionKind{}); err != nil {
		t.Fatal(err)
	}
	if len(extractor.cache) != maxCachedResults || extractor.recent.Len() != maxCachedResults {
		t.Fatalf("cache size = %d, list size = %d; want %d", len(extractor.cache), extractor.recent.Len(), maxCachedResults)
	}
	if _, ok := extractor.cache[fingerprint(first, schema.GroupVersionKind{})]; !ok {
		t.Fatal("cache evicted most recently used input")
	}
	if _, ok := extractor.cache[fingerprint(&metav1.ObjectMeta{Name: "1"}, schema.GroupVersionKind{})]; ok {
		t.Fatal("cache retained least recently used input")
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	extractor, err := New(annotationExpression)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for n := range 32 {
		workers.Go(func() {
			obj := &metav1.ObjectMeta{Annotations: map[string]string{"user": fmt.Sprint(n)}}
			for range 50 {
				got, err := extractor.Evaluate(t.Context(), obj, schema.GroupVersionKind{})
				if err != nil || got.Identity != obj.Annotations["user"] {
					t.Errorf("Evaluate() = %+v, %v; want %s", got, err, obj.Annotations["user"])
					return
				}
			}
		})
	}
	workers.Wait()
}

func BenchmarkEvaluateCached(b *testing.B) {
	extractor, err := New(annotationExpression)
	if err != nil {
		b.Fatal(err)
	}
	obj := &metav1.ObjectMeta{Name: "pod", Annotations: map[string]string{"user": "alice"}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := extractor.Evaluate(b.Context(), obj, schema.GroupVersionKind{Version: "v1", Kind: "Pod"}); err != nil {
			b.Fatal(err)
		}
	}
}

type countingProgram struct {
	cel.Program
	calls     atomic.Int64
	afterEval func()
}

func (p *countingProgram) ContextEval(ctx context.Context, input any) (ref.Val, *cel.EvalDetails, error) {
	p.calls.Add(1)
	value, details, err := p.Program.ContextEval(ctx, input)
	if p.afterEval != nil {
		p.afterEval()
	}
	return value, details, err
}
