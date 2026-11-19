// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package catalog_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
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
func (r *optionalPodReader) List(context.Context, schema.GroupVersionKind, references.ListQuery) ([]unstructured.Unstructured, error) {
	r.calls++
	return r.pods, r.err
}

func TestDeploymentOptionalPodHealth(t *testing.T) {
	definition := kartas.Deployment()
	definition.Spec.StructureDefinition.References = []api.Reference{{Name: "pods", APIVersion: "v1", Kind: "Pod", List: &api.ListReference{MatchLabels: map[string]api.LabelValue{"app": {Value: ptr.To("example")}}}}}
	definition.Spec.StructureDefinition.RootComponent.StatusDefinition = &api.StatusDefinition{StatusMappings: api.StatusMappings{Running: []api.StatusMatcher{{ByExpression: &api.ExpressionMatcher{Expression: `"pods" in references ? size(references.pods) > 0 && references.pods.all(p, p.status.phase == "Running") : object.status.readyReplicas > 0`}}}}}
	if err := api.NewKartaValidator(definition).Validate(); err != nil {
		t.Fatal(err)
	}
	workload := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "example", "namespace": "test"}, "status": map[string]any{"readyReplicas": int64(1)}}}
	pending := unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "example-0", "namespace": "test"}, "status": map[string]any{"phase": "Pending"}}}
	supplied := references.ResolvedReferences{"pods": references.NewListValue([]unstructured.Unstructured{pending})}
	for _, tc := range []struct {
		name        string
		enabled     bool
		manual      bool
		wantRunning bool
	}{{"live Pods", true, false, false}, {"supplied Pods", true, true, false}, {"disabled live fallback", false, false, true}, {"disabled supplied fallback", false, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &optionalPodReader{pods: []unstructured.Unstructured{pending}}
			options := []resource.FactoryOption{resource.WithReferencesEnabled(tc.enabled)}
			if tc.manual {
				options = append(options, resource.WithReferences(supplied))
			} else {
				options = append(options, resource.WithReferenceReader(reader))
			}
			factory := resource.NewComponentFactoryFromObject(definition, workload, options...)
			root, err := factory.GetRootComponent()
			if err != nil {
				t.Fatal(err)
			}
			status, err := root.GetStatus(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(status.MatchedStatuses, api.RunningStatus) != tc.wantRunning {
				t.Fatalf("unexpected status %v", status.MatchedStatuses)
			}
			if !tc.enabled && reader.calls != 0 {
				t.Fatal("disabled references made a client call")
			}
			if workload.Object["status"].(map[string]any)["readyReplicas"] != int64(1) {
				t.Fatal("computed status mutated workload")
			}
		})
	}
	denied := &optionalPodReader{err: errors.New("forbidden")}
	root, err := resource.NewComponentFactoryFromObject(definition, workload, resource.WithReferenceReader(denied)).GetRootComponent()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.GetStatus(t.Context()); err == nil {
		t.Fatal("failed Pod read silently fell back")
	}
}
