// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

func TestPodFiltersRequireBooleansAndAllMustMatch(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default", Labels: map[string]string{"app": "training"}}}
	for _, tc := range []struct {
		name      string
		filters   []string
		want      bool
		wantError bool
	}{
		{name: "none", want: true}, {name: "true", filters: []string{`object.metadata.labels.app == "training"`}, want: true},
		{name: "all true", filters: []string{`object.metadata.labels.app == "training"`, `object.metadata.namespace == "default"`}, want: true},
		{name: "one false", filters: []string{"true", "false"}},
		{name: "missing optional", filters: []string{`object.metadata.labels[?"missing"].orValue("") == "x"`}},
		{name: "string", filters: []string{`object.metadata.name`}, wantError: true},
		{name: "list", filters: []string{`[true]`}, wantError: true},
		{name: "null", filters: []string{`null`}, wantError: true},
		{name: "invalid", filters: []string{`object..metadata`}, wantError: true},
		{name: "workload variable unavailable", filters: []string{`variables.name == "worker"`}, wantError: true},
		{name: "reference unavailable", filters: []string{`references.pods.size() > 0`}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewPodQuerier(pod).PassesFilters(context.Background(), tc.filters)
			if (err != nil) != tc.wantError || got != tc.want {
				t.Fatalf("result=%t err=%v", got, err)
			}
		})
	}
}

func TestPodGroupingExpressionsKeepOrderAndRejectMissingValues(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default", Labels: map[string]string{"app": "training", "role": "worker"}}}
	for _, tc := range []struct {
		name        string
		expressions []string
		want        []string
		wantError   bool
	}{
		{name: "none", want: []string{}},
		{name: "single", expressions: []string{"object.metadata.labels.app"}, want: []string{"training"}},
		{name: "ordered", expressions: []string{"object.metadata.labels.role", "object.metadata.labels.app", "object.metadata.namespace"}, want: []string{"worker", "training", "default"}},
		{name: "missing", expressions: []string{`object.metadata.labels[?"missing"].orValue(null)`}, wantError: true},
		{name: "multiple", expressions: []string{`object.metadata.labels.map(k,k)`}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewPodQuerier(pod).ExtractGroupKeysFor(context.Background(), api.PodGroupMemberDefinition{GroupByExpressions: tc.expressions})
			if (err != nil) != tc.wantError || (!tc.wantError && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("keys=%v err=%v", got, err)
			}
		})
	}
}
