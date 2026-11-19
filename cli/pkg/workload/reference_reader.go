// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workload

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

// ReferenceReader resolves SDK references through the CLI's Kubernetes client.
// List returns every page or an error; callers never receive a partial result.
type ReferenceReader struct {
	client dynamic.Interface
	mapper meta.RESTMapper
}

func NewReferenceReader(client dynamic.Interface, mapper meta.RESTMapper) *ReferenceReader {
	return &ReferenceReader{client: client, mapper: mapper}
}

func (r *ReferenceReader) resource(gvk schema.GroupVersionKind, namespace string) (dynamic.ResourceInterface, error) {
	mapping, err := r.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("map reference kind: %w", err)
	}
	resource := r.client.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		return resource, nil
	}
	if namespace == "" {
		return nil, errors.New("namespaced reference requires a workload namespace")
	}
	return resource.Namespace(namespace), nil
}

func (r *ReferenceReader) Get(ctx context.Context, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	client, err := r.resource(gvk, namespace)
	if err != nil {
		return nil, err
	}
	object, err := client.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	object.SetGroupVersionKind(gvk)
	return object, nil
}

func (r *ReferenceReader) List(ctx context.Context, gvk schema.GroupVersionKind, query references.ListQuery) ([]unstructured.Unstructured, error) {
	client, err := r.resource(gvk, query.Namespace)
	if err != nil {
		return nil, err
	}
	options := metav1.ListOptions{Limit: 500}
	if query.Selector != nil {
		options.LabelSelector = query.Selector.String()
	}
	var objects []unstructured.Unstructured
	seen := map[string]bool{}
	for {
		page, err := client.List(ctx, options)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			page.Items[i].SetGroupVersionKind(gvk)
		}
		objects = append(objects, page.Items...)
		options.Continue = page.GetContinue()
		if options.Continue == "" {
			return objects, nil
		}
		if seen[options.Continue] {
			return nil, errors.New("reference list repeated a continuation token")
		}
		seen[options.Continue] = true
	}
}
