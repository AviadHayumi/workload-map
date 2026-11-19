// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package ctrlreader adapts an application's Kubernetes reader to Karta.
package ctrlreader

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

type reader struct {
	client client.Reader
	mapper meta.RESTMapper
}

// New uses the application's credentials, client and discovery information.
func New(client client.Reader, mapper meta.RESTMapper) references.ResourceReader {
	return &reader{client: client, mapper: mapper}
}
func (r *reader) namespace(gvk schema.GroupVersionKind, namespace string) (string, error) {
	if r.client == nil || r.mapper == nil {
		return "", errors.New("Kubernetes reader and REST mapper are required")
	}
	mapping, err := r.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return "", err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		return "", nil
	}
	if namespace == "" {
		return "", errors.New("namespaced reference requires the workload namespace")
	}
	return namespace, nil
}
func (r *reader) Get(ctx context.Context, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	namespace, err := r.namespace(gvk, namespace)
	if err != nil {
		return nil, err
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gvk)
	if err = r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, object); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, references.ErrNotFound
		}
		return nil, err
	}
	return object, nil
}
func (r *reader) List(ctx context.Context, gvk schema.GroupVersionKind, query references.ListQuery) ([]unstructured.Unstructured, error) {
	namespace, err := r.namespace(gvk, query.Namespace)
	if err != nil {
		return nil, err
	}
	result := []unstructured.Unstructured{}
	continuation := ""
	for {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		options := &client.ListOptions{Namespace: namespace, LabelSelector: query.Selector, Raw: &metav1.ListOptions{Continue: continuation}}
		if err := r.client.List(ctx, list, options); err != nil {
			return nil, err
		}
		for i := range list.Items {
			list.Items[i].SetGroupVersionKind(gvk)
			result = append(result, list.Items[i])
		}
		next := list.GetContinue()
		if next == "" {
			return result, nil
		}
		if next == continuation {
			return nil, errors.New("reference list repeated a continuation token")
		}
		continuation = next
	}
}
