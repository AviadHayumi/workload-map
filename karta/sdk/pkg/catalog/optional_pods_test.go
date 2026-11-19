// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package catalog_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

type optionalPodReader struct {
	pods  []unstructured.Unstructured
	calls int
	err   error
}

func (*optionalPodReader) Get(context.Context, schema.GroupVersionKind, string, string) (*unstructured.Unstructured, error) {
	return nil, errors.New("unexpected lookup")
}
func (r *optionalPodReader) List(_ context.Context, gvk schema.GroupVersionKind, q references.ListQuery) ([]unstructured.Unstructured, error) {
	r.calls++
	if gvk != (schema.GroupVersionKind{Version: "v1", Kind: "Pod"}) || q.Namespace != "team-a" || q.Selector == nil || q.Selector.String() != "app=web" {
		return nil, errors.New("expected Pods matching app=web in team-a")
	}
	return r.pods, r.err
}

func TestDeploymentOptionalPodHealth(t *testing.T) {
	data, err := os.ReadFile("../../../../docs/examples/optional-pod-health.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var definition api.Karta
	if err := yaml.UnmarshalStrict(data, &definition); err != nil {
		t.Fatal(err)
	}
	if err := api.NewKartaValidator(&definition).Validate(); err != nil {
		t.Fatal(err)
	}
	workload := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a", Generation: 3},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
		},
	}
	pod := func(name, phase, ready string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": name, "namespace": "team-a", "labels": map[string]any{"app": "web"}},
			"status": map[string]any{"phase": phase, "conditions": []any{
				map[string]any{"type": "Ready", "status": ready},
			}},
		}}
	}
	ready := []unstructured.Unstructured{pod("web-0", "Running", "True"), pod("web-1", "Running", "True")}
	terminating := ready[1].DeepCopy()
	terminating.Object["metadata"].(map[string]any)["deletionTimestamp"] = "2026-01-01T00:00:00Z"
	for _, tc := range []struct {
		name                 string
		pods                 []unstructured.Unstructured
		replicas, updated    int32
		observed             int64
		available            corev1.ConditionStatus
		wantPods, wantStatus bool
	}{
		{"two ready Pods", ready, 2, 2, 3, corev1.ConditionTrue, true, true},
		{"Deployment unavailable", ready, 2, 2, 3, corev1.ConditionFalse, true, false},
		{"waiting for controller", ready, 2, 2, 2, corev1.ConditionTrue, false, false},
		{"incomplete rollout", ready, 2, 1, 3, corev1.ConditionTrue, false, false},
		{"zero replicas", nil, 0, 0, 3, corev1.ConditionTrue, false, false},
		{"no Pods listed", nil, 2, 2, 3, corev1.ConditionTrue, false, true},
		{"one Pod missing", ready[:1], 2, 2, 3, corev1.ConditionTrue, false, true},
		{"pending Pod", []unstructured.Unstructured{ready[0], pod("web-1", "Pending", "False")}, 2, 2, 3, corev1.ConditionTrue, false, true},
		{"running Pod not Ready", []unstructured.Unstructured{ready[0], pod("web-1", "Running", "False")}, 2, 2, 3, corev1.ConditionTrue, false, true},
		{"terminating Ready Pod", []unstructured.Unstructured{ready[0], *terminating}, 2, 2, 3, corev1.ConditionTrue, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := workload.DeepCopy()
			deployment.Spec.Replicas = &tc.replicas
			deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: tc.observed, UpdatedReplicas: tc.updated,
				Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: tc.available}},
			}
			before := deployment.DeepCopy()
			reader := &optionalPodReader{pods: tc.pods}
			live := resource.WithReferenceReader(reader)
			manual := resource.WithReferences(references.ResolvedReferences{"pods": references.NewListValue(tc.pods)})
			disabled := resource.WithReferencesEnabled(false)
			for _, mode := range []struct {
				name        string
				options     []resource.FactoryOption
				wantRunning bool
				wantCalls   int
			}{
				{"live", []resource.FactoryOption{live}, tc.wantPods, 1},
				{"manual", []resource.FactoryOption{manual}, tc.wantPods, 0},
				{"disabled live", []resource.FactoryOption{disabled, live}, tc.wantStatus, 0},
				{"disabled manual", []resource.FactoryOption{disabled, manual}, tc.wantStatus, 0},
			} {
				t.Run(mode.name, func(t *testing.T) {
					reader.calls = 0
					root, err := resource.NewComponentFactoryFromObject(&definition, deployment, mode.options...).GetRootComponent()
					if err != nil {
						t.Fatal(err)
					}
					status, err := root.GetStatus(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if slices.Contains(status.MatchedStatuses, api.RunningStatus) != mode.wantRunning || reader.calls != mode.wantCalls {
						t.Fatalf("statuses %v, list calls %d", status.MatchedStatuses, reader.calls)
					}
				})
			}
			if !reflect.DeepEqual(deployment, before) {
				t.Fatal("computed status mutated the Deployment")
			}
		})
	}
	denied := &optionalPodReader{err: errors.New("forbidden")}
	root, err := resource.NewComponentFactoryFromObject(&definition, workload, resource.WithReferenceReader(denied)).GetRootComponent()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.GetStatus(t.Context()); !errors.Is(err, denied.err) {
		t.Fatalf("expected denied Pod read, got %v", err)
	}
}
