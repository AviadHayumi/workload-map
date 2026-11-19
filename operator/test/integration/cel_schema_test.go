// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package integration

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// These requests go directly through the generated CRD schema. The envtest
// manager runs reconciliation, so schema failures cannot depend on a webhook.
var _ = Describe("CEL CRD schema (envtest)", func() {
	It("rejects malformed field shapes before storing a definition", func() {
		tests := []struct {
			name   string
			change func(map[string]any, map[string]any)
		}{
			{"empty accessor", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{}}
			}},
			{"accessor with only a pruned field", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"unsupported": true}}
			}},
			{"empty read expression", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"read": map[string]any{"expression": ""}}}
			}},
			{"missing write destination", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"write": map[string]any{"matchExpression": "true"}}}
			}},
			{"two write destinations", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"write": map[string]any{"path": "/spec/replicas", "pathExpression": "'/spec/replicas'"}}}
			}},
			{"bad fixed JSON Pointer", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"write": map[string]any{"path": "/spec/~2"}}}
			}},
			{"writable instance IDs", func(root, structure map[string]any) {
				root["instanceIds"] = map[string]any{"read": map[string]any{"expression": "[]"}, "write": map[string]any{"path": "/spec/ids"}}
			}},
			{"reference without lookup or list", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{})}
			}},
			{"reference with both lookup and list", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"lookup": map[string]any{"nameExpression": "'runtime'"}, "list": map[string]any{"matchLabels": map[string]any{"app": map[string]any{"value": "runtime"}}}})}
			}},
			{"empty list selector", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"list": map[string]any{}})}
			}},
			{"empty label map", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"list": map[string]any{"matchLabels": map[string]any{}}})}
			}},
			{"empty requirements list", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"list": map[string]any{"matchExpressions": []any{}}})}
			}},
			{"empty label value", func(root, structure map[string]any) {
				structure["references"] = []any{schemaLabelReference(map[string]any{})}
			}},
			{"label value with only a pruned field", func(root, structure map[string]any) {
				structure["references"] = []any{schemaLabelReference(map[string]any{"unsupported": "value"})}
			}},
			{"ambiguous label value", func(root, structure map[string]any) {
				structure["references"] = []any{schemaLabelReference(map[string]any{"value": "runtime", "expression": "'runtime'"})}
			}},
			{"empty label expression", func(root, structure map[string]any) {
				structure["references"] = []any{schemaLabelReference(map[string]any{"expression": ""})}
			}},
			{"In without values", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"list": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In"}}}})}
			}},
			{"Exists with values", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"list": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "Exists", "values": []any{map[string]any{"value": "runtime"}}}}}})}
			}},
			{"empty membership list", func(root, structure map[string]any) { root["podSelector"] = map[string]any{"fromReferences": []any{}} }},
			{"membership without component selector", func(root, structure map[string]any) {
				root["podSelector"] = map[string]any{"fromReferences": []any{map[string]any{"name": "runtime"}}}
			}},
			{"duplicate membership names", func(root, structure map[string]any) {
				root["podSelector"] = map[string]any{"componentTypeSelector": map[string]any{"read": map[string]any{"expression": "true"}}, "fromReferences": []any{map[string]any{"name": "runtime"}, map[string]any{"name": "runtime"}}}
			}},
			{"empty suspend actions", func(root, structure map[string]any) {
				root["suspendDefinition"] = map[string]any{"suspendActions": []any{}, "resumeActions": []any{map[string]any{"write": map[string]any{"path": "/spec/suspend"}, "value": "false"}}}
			}},
		}
		for i, tc := range tests {
			By(tc.name)
			object, root, structure := schemaKarta(fmt.Sprintf("cel-invalid-%d", i))
			tc.change(root, structure)
			err := k8sClient.Create(testCtx, object)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s: expected schema rejection, got %v", tc.name, err)
		}
	})
	It("accepts independent read/write accessors and each label selector form", func() {
		tests := []struct {
			name   string
			change func(map[string]any, map[string]any)
		}{
			{"read only", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"read": map[string]any{"expression": "object.spec.replicas"}}}
			}},
			{"write only root path", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"write": map[string]any{"path": ""}}}
			}},
			{"read and conditional write", func(root, structure map[string]any) {
				root["scaleDefinition"] = map[string]any{"replicas": map[string]any{"read": map[string]any{"expression": "object.spec.replicas", "matchExpression": "true"}, "write": map[string]any{"pathExpression": "'/spec/replicas'", "matchExpression": "true"}}}
			}},
			{"empty literal label", func(root, structure map[string]any) {
				structure["references"] = []any{schemaLabelReference(map[string]any{"value": ""})}
			}},
			{"expression label", func(root, structure map[string]any) {
				structure["references"] = []any{schemaLabelReference(map[string]any{"expression": "object.metadata.name"})}
			}},
			{"Exists without values", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"list": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "Exists"}}}})}
			}},
			{"In with expression values", func(root, structure map[string]any) {
				structure["references"] = []any{schemaReference(map[string]any{"list": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{map[string]any{"expression": "object.metadata.name"}}}}}})}
			}},
		}
		for i, tc := range tests {
			By(tc.name)
			object, root, structure := schemaKarta(fmt.Sprintf("cel-valid-%d", i))
			tc.change(root, structure)
			Expect(k8sClient.Create(testCtx, object)).To(Succeed(), tc.name)
			Expect(k8sClient.Delete(testCtx, object)).To(Succeed())
		}
	})
})

func schemaKarta(name string) (*unstructured.Unstructured, map[string]any, map[string]any) {
	root := map[string]any{"name": "root", "kind": map[string]any{"group": "apps", "version": "v1", "kind": "Deployment"}, "statusDefinition": map[string]any{"statusMappings": map[string]any{}}}
	structure := map[string]any{"rootComponent": root}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "run.ai/v1alpha2", "kind": "Karta", "metadata": map[string]any{"name": name}, "spec": map[string]any{"structureDefinition": structure}}}, root, structure
}
func schemaReference(recipe map[string]any) map[string]any {
	result := map[string]any{"name": "runtime", "apiVersion": "v1", "kind": "Pod"}
	for key, value := range recipe {
		result[key] = value
	}
	return result
}
func schemaLabelReference(value map[string]any) map[string]any {
	return schemaReference(map[string]any{"list": map[string]any{"matchLabels": map[string]any{"app": value}}})
}
