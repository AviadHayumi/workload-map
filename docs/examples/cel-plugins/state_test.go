// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestStateStrictSchema(t *testing.T) {
	o := testWorkload()
	valid := `{"version":1,"workloadUID":"` + string(o.GetUID()) + `"}`
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{name: "minimal", raw: valid, valid: true},
		{name: "pending", raw: strings.TrimSuffix(valid, "}") + `,"storage":{"size":"1Gi","storageClass":""}}`, valid: true},
		{name: "empty", raw: ""},
		{name: "null", raw: "null"},
		{name: "missing version", raw: `{"workloadUID":"` + string(o.GetUID()) + `"}`},
		{name: "wrong version", raw: strings.Replace(valid, `"version":1`, `"version":2`, 1)},
		{name: "missing UID", raw: `{"version":1}`},
		{name: "wrong UID", raw: `{"version":1,"workloadUID":"another-parent"}`},
		{name: "unknown field", raw: strings.TrimSuffix(valid, "}") + `,"extra":true}`},
		{name: "nested unknown", raw: strings.TrimSuffix(valid, "}") + `,"storage":{"size":"1Gi","storageClass":"","extra":true}}`},
		{name: "trailing document", raw: valid + ` {}`},
		{name: "too large", raw: valid + strings.Repeat(" ", 4096)},
		{name: "invalid saved pool", raw: strings.TrimSuffix(valid, "}") + `,"pool":"bad/value"}`},
		{name: "noncanonical size", raw: strings.TrimSuffix(valid, "}") + `,"storage":{"size":"1024Mi","storageClass":""}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := o.DeepCopy()
			obj.SetAnnotations(map[string]string{annotationState: tc.raw})
			before := obj.DeepCopy()
			_, err := readState(obj)
			if (err == nil) != tc.valid {
				t.Fatalf("readState() error = %v, valid = %v", err, tc.valid)
			}
			if !reflect.DeepEqual(before, obj) {
				t.Fatal("state decoder mutated parent")
			}
		})
	}
}

func TestTemplateEditsPreserveRawFields(t *testing.T) {
	for _, path := range [][]string{{"spec", "template"}, {"spec", "jobTemplate", "spec", "template"}} {
		t.Run(strings.Join(path, "."), func(t *testing.T) {
			o := testWorkload()
			template, _, err := unstructured.NestedMap(o.Object, "spec", "template")
			if err != nil {
				t.Fatal(err)
			}
			template["futureField"] = map[string]any{"largeInteger": int64(9007199254740993), "nested": []any{map[string]any{"value": "keep"}}}
			spec := template["spec"].(map[string]any)
			spec["nodeSelector"] = map[string]any{"example.test/user": "kept"}
			spec["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "user-claim"}}}
			spec["containers"].([]any)[0].(map[string]any)["volumeMounts"] = []any{map[string]any{"name": "data", "mountPath": "/data"}}
			unstructured.RemoveNestedField(o.Object, "spec", "template")
			if err := unstructured.SetNestedMap(o.Object, template, path...); err != nil {
				t.Fatal(err)
			}
			before := o.DeepCopy()
			previous := state{Version: 1, WorkloadUID: o.GetUID()}
			next := previous
			desired := &plan{Placement: &placementIntent{Pool: "general"}, Service: &serviceIntent{Port: 80, TargetPort: 8080}}
			if err := editTemplate(o, path, previous, desired, &next); err != nil {
				t.Fatal(err)
			}
			pool, _, _ := unstructured.NestedString(o.Object, append(append([]string{}, path...), "spec", "nodeSelector", poolKey)...)
			if pool != "general" || next.Pool != "general" || !next.Service {
				t.Fatalf("ownership not acquired: pool=%q state=%+v", pool, next)
			}
			last := next
			if err := editTemplate(o, path, last, &plan{}, &next); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, o) {
				beforeJSON, _ := json.Marshal(before.Object)
				afterJSON, _ := json.Marshal(o.Object)
				t.Fatalf("raw fields changed during edit/removal:\nbefore %s\nafter  %s", beforeJSON, afterJSON)
			}
			if next.Pool != "" || next.Service {
				t.Fatalf("ownership not removed: %+v", next)
			}
		})
	}
}

func TestTemplateReservedFieldProvenance(t *testing.T) {
	for _, field := range []string{"pool", "service"} {
		for _, mode := range []string{"acquire equal", "acquire different", "owned missing", "owned different", "remove missing", "remove different"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				o := testWorkload()
				path := []string{"spec", "template"}
				previous := state{Version: 1, WorkloadUID: o.GetUID()}
				desired := &plan{}
				var keyPath []string
				var expected string
				if field == "pool" {
					keyPath = []string{"spec", "template", "spec", "nodeSelector", poolKey}
					expected = "general"
					desired.Placement = &placementIntent{Pool: expected}
				} else {
					keyPath = []string{"spec", "template", "metadata", "labels", workloadUIDKey}
					expected = string(o.GetUID())
					desired.Service = &serviceIntent{Port: 80, TargetPort: 8080}
				}
				owned := !strings.HasPrefix(mode, "acquire")
				if owned {
					if field == "pool" {
						previous.Pool = expected
					} else {
						previous.Service = true
					}
				}
				if strings.HasPrefix(mode, "remove") {
					desired = &plan{}
				}
				if !strings.HasSuffix(mode, "missing") {
					value := "foreign"
					if strings.HasSuffix(mode, "equal") {
						value = expected
					}
					if err := unstructured.SetNestedField(o.Object, value, keyPath...); err != nil {
						t.Fatal(err)
					}
				}
				before, foundBefore, _ := unstructured.NestedFieldCopy(o.Object, keyPath...)
				next := previous
				if err := editTemplate(o, path, previous, desired, &next); err == nil {
					t.Fatal("reserved field conflict was accepted")
				}
				after, foundAfter, _ := unstructured.NestedFieldCopy(o.Object, keyPath...)
				if foundBefore != foundAfter || !reflect.DeepEqual(before, after) {
					t.Fatal("conflicting writer's field changed")
				}
			})
		}
	}
}
