// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type discoveryTestScope struct{ name meta.RESTScopeName }

func (s *discoveryTestScope) Name() meta.RESTScopeName { return s.name }

func TestMappingRejectsMalformedReaderValues(t *testing.T) {
	var typedNil *discoveryTestScope
	for name, mapping := range map[string]*meta.RESTMapping{
		"nil":                    nil,
		"nil scope":              {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}},
		"missing kind version":   {GroupVersionKind: schema.GroupVersionKind{Kind: "Pod"}, Resource: schema.GroupVersionResource{Resource: "pods"}, Scope: meta.RESTScopeNamespace},
		"missing kind":           {GroupVersionKind: schema.GroupVersionKind{Version: "v1"}, Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Scope: meta.RESTScopeNamespace},
		"missing resource":       {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v1"}, Scope: meta.RESTScopeNamespace},
		"wrong resource group":   {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Group: "other", Version: "v1", Resource: "pods"}, Scope: meta.RESTScopeNamespace},
		"unsupported scope":      {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Scope: &discoveryTestScope{name: "unsupported"}},
		"typed nil scope":        {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Scope: typedNil},
		"wrong resource version": {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v2", Resource: "pods"}, Scope: meta.RESTScopeNamespace},
		"subresource":            {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods/status"}, Scope: meta.RESTScopeNamespace},
	} {
		t.Run(name, func(t *testing.T) {
			_, validationErr := validateMapping(mapping)
			_, copyErr := copyMapping(mapping)
			if !errors.Is(validationErr, ErrInvalidInput) || !errors.Is(copyErr, ErrInvalidInput) || validationErr.Error() != copyErr.Error() {
				t.Fatalf("mapping validation error = %v, copy error = %v, want identical ErrInvalidInput errors", validationErr, copyErr)
			}
			object := indexTestObject("pod", "team", podGVK, nil)
			if _, err := ownershipObject(object, mapping); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ownership mapping error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestMappingValidationAndCopyPreserveScope(t *testing.T) {
	for _, scopeName := range []meta.RESTScopeName{meta.RESTScopeNameNamespace, meta.RESTScopeNameRoot} {
		t.Run(string(scopeName), func(t *testing.T) {
			scope := &discoveryTestScope{name: scopeName}
			mapping := indexTestMapping(indexBundleGVK, scope)
			validated, err := validateMapping(mapping)
			if err != nil || validated.Name() != scopeName {
				t.Fatalf("validated scope = %v, %v, want %s", validated, err, scopeName)
			}
			copied, err := copyMapping(mapping)
			if err != nil {
				t.Fatal(err)
			}
			if copied == mapping || copied.Scope == mapping.Scope || !reflect.DeepEqual(copied.Resource, mapping.Resource) || copied.GroupVersionKind != mapping.GroupVersionKind || copied.Scope != validated {
				t.Fatal("copyMapping did not detach the mapping and canonicalize its scope")
			}
			namespace := ""
			if scopeName == meta.RESTScopeNameNamespace {
				namespace = "team"
			}
			object := indexTestObject("root", namespace, indexBundleGVK, nil)
			metadata, err := ownershipObject(object, mapping)
			if err != nil || metadata.Namespaced != (namespace != "") {
				t.Fatalf("ownership scope = %v, %v, want namespace %q", metadata.Namespaced, err, namespace)
			}
			if namespace == "" {
				object.SetNamespace("team")
			} else {
				object.SetNamespace("")
			}
			if _, err := ownershipObject(object, mapping); !errors.Is(err, ownership.ErrScope) {
				t.Fatalf("mismatched object scope error = %v, want ErrScope", err)
			}
			scope.name = "changed"
			mapping.Resource.Resource = "changed"
			mapping.GroupVersionKind.Kind = "Changed"
			if copied.Scope.Name() != scopeName || validated.Name() != scopeName || copied.Resource.Resource == "changed" || copied.GroupVersionKind.Kind != indexBundleGVK.Kind {
				t.Fatal("source mutation changed the copied mapping or validated scope")
			}
		})
	}
}
