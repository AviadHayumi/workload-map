// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type discoveryTestScope struct{ name meta.RESTScopeName }

func (s *discoveryTestScope) Name() meta.RESTScopeName { return s.name }

func TestMappingRejectsMalformedReaderValues(t *testing.T) {
	var typedNil *discoveryTestScope
	for name, mapping := range map[string]*meta.RESTMapping{
		"nil":                    nil,
		"typed nil scope":        {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Scope: typedNil},
		"wrong resource version": {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v2", Resource: "pods"}, Scope: meta.RESTScopeNamespace},
		"subresource":            {GroupVersionKind: podGVK, Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods/status"}, Scope: meta.RESTScopeNamespace},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := copyMapping(mapping); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("mapping error=%v", err)
			}
		})
	}
}
