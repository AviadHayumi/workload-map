// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"reflect"
	"testing"

	kartacel "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestProgramsValidSnapshot(t *testing.T) {
	p, err := newPrograms()
	if err != nil {
		t.Fatal(err)
	}
	o := testWorkload()
	o.SetAnnotations(map[string]string{annotationStorage: "1Gi", annotationPool: "general", annotationService: "80"})
	input, err := kartacel.NewInput(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.service.Evaluate(context.Background(), input); err != nil {
		t.Fatalf("direct service evaluation: %v", err)
	}
	got, path, err := p.plan(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	want := &plan{Storage: &storageIntent{Size: "1Gi"}, Placement: &placementIntent{Pool: "general"}, Service: &serviceIntent{Port: 80, TargetPort: 8080}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(path, []string{"spec", "template"}) {
		t.Fatalf("plan = %+v, path = %v", got, path)
	}
}

func TestDecodeStorage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      map[string]any
		want    *storageIntent
		invalid bool
	}{
		{name: "null result", invalid: true},
		{name: "disabled", in: map[string]any{}},
		{name: "canonical", in: map[string]any{"size": "1024Mi"}, want: &storageIntent{Size: "1Gi"}},
		{name: "upper bound", in: map[string]any{"size": "10Gi"}, want: &storageIntent{Size: "10Gi"}},
		{name: "extra key", in: map[string]any{"size": "1Gi", "other": true}, invalid: true},
		{name: "missing key", in: map[string]any{"other": "1Gi"}, invalid: true},
		{name: "null", in: map[string]any{"size": nil}, invalid: true},
		{name: "number", in: map[string]any{"size": int64(1)}, invalid: true},
		{name: "empty", in: map[string]any{"size": ""}, invalid: true},
		{name: "malformed", in: map[string]any{"size": "one Gi"}, invalid: true},
		{name: "zero", in: map[string]any{"size": "0"}, invalid: true},
		{name: "negative", in: map[string]any{"size": "-1Gi"}, invalid: true},
		{name: "too large", in: map[string]any{"size": "11Gi"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeStorage(tc.in)
			if (err != nil) != tc.invalid {
				t.Fatalf("decodeStorage() error = %v, invalid = %v", err, tc.invalid)
			}
			if !tc.invalid && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDecodePlacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      map[string]any
		want    *placementIntent
		invalid bool
	}{
		{name: "null result", invalid: true},
		{name: "disabled", in: map[string]any{}},
		{name: "valid", in: map[string]any{"pool": "cpu-a"}, want: &placementIntent{Pool: "cpu-a"}},
		{name: "extra key", in: map[string]any{"pool": "cpu", "extra": "x"}, invalid: true},
		{name: "missing key", in: map[string]any{"other": "cpu"}, invalid: true},
		{name: "empty", in: map[string]any{"pool": ""}, invalid: true},
		{name: "null", in: map[string]any{"pool": nil}, invalid: true},
		{name: "number", in: map[string]any{"pool": int64(1)}, invalid: true},
		{name: "slash", in: map[string]any{"pool": "pool/cpu"}, invalid: true},
		{name: "spaces", in: map[string]any{"pool": " cpu "}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodePlacement(tc.in)
			if (err != nil) != tc.invalid {
				t.Fatalf("decodePlacement() error = %v, invalid = %v", err, tc.invalid)
			}
			if !tc.invalid && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDecodeService(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      map[string]any
		want    *serviceIntent
		invalid bool
	}{
		{name: "null result", invalid: true},
		{name: "disabled", in: map[string]any{}},
		{name: "valid", in: map[string]any{"port": int64(80), "targetPort": int64(8080)}, want: &serviceIntent{Port: 80, TargetPort: 8080}},
		{name: "bounds", in: map[string]any{"port": int64(1), "targetPort": int64(65535)}, want: &serviceIntent{Port: 1, TargetPort: 65535}},
		{name: "extra key", in: map[string]any{"port": int64(80), "targetPort": int64(8080), "extra": true}, invalid: true},
		{name: "missing target", in: map[string]any{"port": int64(80)}, invalid: true},
		{name: "null target", in: map[string]any{"port": int64(80), "targetPort": nil}, invalid: true},
		{name: "float port", in: map[string]any{"port": float64(80), "targetPort": int64(8080)}, invalid: true},
		{name: "float target", in: map[string]any{"port": int64(80), "targetPort": float64(8080)}, invalid: true},
		{name: "string port", in: map[string]any{"port": "80", "targetPort": int64(8080)}, invalid: true},
		{name: "zero", in: map[string]any{"port": int64(0), "targetPort": int64(8080)}, invalid: true},
		{name: "negative target", in: map[string]any{"port": int64(80), "targetPort": int64(-1)}, invalid: true},
		{name: "too large", in: map[string]any{"port": int64(65536), "targetPort": int64(8080)}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeService(tc.in)
			if (err != nil) != tc.invalid {
				t.Fatalf("decodeService() error = %v, invalid = %v", err, tc.invalid)
			}
			if !tc.invalid && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestProgramsRejectInvalidInputBeforeEffects(t *testing.T) {
	p, err := newPrograms()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		mutate      func(*unstructured.Unstructured)
	}{
		{name: "empty storage", annotations: map[string]string{annotationStorage: ""}},
		{name: "empty pool", annotations: map[string]string{annotationPool: ""}},
		{name: "empty service", annotations: map[string]string{annotationService: ""}},
		{name: "expression is data", annotations: map[string]string{annotationService: "1 + 79"}},
		{name: "missing container port", annotations: map[string]string{annotationService: "80"}, mutate: func(o *unstructured.Unstructured) {
			unstructured.RemoveNestedField(o.Object, "spec", "template", "spec", "containers")
		}},
		{name: "unsupported job", mutate: func(o *unstructured.Unstructured) { o.SetAPIVersion("batch/v1"); o.SetKind("Job") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testWorkload()
			o.SetAnnotations(tc.annotations)
			if tc.mutate != nil {
				tc.mutate(o)
			}
			before := o.DeepCopy()
			if _, _, err := p.plan(context.Background(), o); err == nil {
				t.Fatal("invalid input accepted")
			}
			if !reflect.DeepEqual(o, before) {
				t.Fatal("planning mutated the input")
			}
		})
	}
}

func TestProgramsCronJobPathAndCancellation(t *testing.T) {
	p, err := newPrograms()
	if err != nil {
		t.Fatal(err)
	}
	o := testWorkload()
	o.SetAPIVersion("batch/v1")
	o.SetKind("CronJob")
	o.SetAnnotations(map[string]string{annotationPool: "general", annotationService: "80"})
	template, _, err := unstructured.NestedMap(o.Object, "spec", "template")
	if err != nil {
		t.Fatal(err)
	}
	unstructured.RemoveNestedField(o.Object, "spec", "template")
	if err := unstructured.SetNestedMap(o.Object, template, "spec", "jobTemplate", "spec", "template"); err != nil {
		t.Fatal(err)
	}
	got, path, err := p.plan(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service == nil || got.Service.TargetPort != 8080 || !reflect.DeepEqual(path, []string{"spec", "jobTemplate", "spec", "template"}) {
		t.Fatalf("CronJob plan = %+v, path = %v", got, path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := p.plan(ctx, o); err == nil {
		t.Fatal("canceled planning succeeded")
	}
}

func TestInputHashOnlyTracksDeclaredInputs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*unstructured.Unstructured)
		changes bool
	}{
		{name: "status", mutate: func(o *unstructured.Unstructured) { o.Object["status"] = map[string]any{"readyReplicas": int64(2)} }},
		{name: "result", mutate: func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[annotationResult] = `{}`
			o.SetAnnotations(a)
		}},
		{name: "unrelated annotation", mutate: func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a["example.test/user"] = "keep"
			o.SetAnnotations(a)
		}},
		{name: "unrelated spec", mutate: func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, int64(3), "spec", "replicas")
		}},
		{name: "input value", changes: true, mutate: func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[annotationPool] = "other"
			o.SetAnnotations(a)
		}},
		{name: "input presence", changes: true, mutate: func(o *unstructured.Unstructured) {
			a := o.GetAnnotations()
			a[annotationStorage] = ""
			o.SetAnnotations(a)
		}},
		{name: "target port", changes: true, mutate: func(o *unstructured.Unstructured) {
			containers, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
			containers[0].(map[string]any)["ports"].([]any)[0].(map[string]any)["containerPort"] = int64(9090)
			_ = unstructured.SetNestedSlice(o.Object, containers, "spec", "template", "spec", "containers")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testWorkload()
			o.SetAnnotations(map[string]string{annotationPool: "general"})
			before := inputHash(o, "")
			tc.mutate(o)
			if got := inputHash(o, ""); (got != before) != tc.changes {
				t.Fatalf("hash changed = %v, want %v", got != before, tc.changes)
			}
		})
	}
	o := testWorkload()
	if inputHash(o, "") == inputHash(o, "named-class") {
		t.Fatal("storage class excluded from input hash")
	}
}
