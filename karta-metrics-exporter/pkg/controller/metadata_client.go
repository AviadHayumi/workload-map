// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (r *Reader) getMetadata(ctx context.Context, resource schema.GroupVersionResource, namespace, name string) (*metav1.PartialObjectMetadata, error) {
	object, err := r.clients.Metadata.Resource(resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if !apierrors.IsNotAcceptable(err) && !apierrors.IsUnsupportedMediaType(err) {
		return object, err
	}
	full, err := r.clients.Dynamic.Resource(resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return objectMetadata(full)
}

func (r *Reader) listMetadata(ctx context.Context, resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	objects, err := r.clients.Metadata.Resource(resource).Namespace(namespace).List(ctx, options)
	if !apierrors.IsNotAcceptable(err) && !apierrors.IsUnsupportedMediaType(err) {
		return objects, err
	}
	full, err := r.clients.Dynamic.Resource(resource).Namespace(namespace).List(ctx, options)
	if err != nil {
		return nil, err
	}
	result := &metav1.PartialObjectMetadataList{ListMeta: metav1.ListMeta{ResourceVersion: full.GetResourceVersion(), Continue: full.GetContinue(), RemainingItemCount: full.GetRemainingItemCount()}, Items: make([]metav1.PartialObjectMetadata, 0, len(full.Items))}
	for i := range full.Items {
		metadata, err := objectMetadata(&full.Items[i])
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, *metadata)
	}
	return result, nil
}

func objectMetadata(object *unstructured.Unstructured) (*metav1.PartialObjectMetadata, error) {
	result := &metav1.PartialObjectMetadata{}
	if err := kruntime.DefaultUnstructuredConverter.FromUnstructured(object.Object, result); err != nil {
		return nil, err
	}
	return result, nil
}
