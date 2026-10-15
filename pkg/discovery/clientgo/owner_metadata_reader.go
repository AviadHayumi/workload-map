// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package clientgo

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type ownerMetadataReader struct {
	*reader
	metadataClient metadata.Interface
}

var (
	_ discovery.OwnerMetadataReader = (*ownerMetadataReader)(nil)
	_ discovery.PodTransferReader   = (*ownerMetadataReader)(nil)
)

// NewReaderWithOwnerMetadata opts into request-provenance identity for owner
// metadata. The caller owns both clients and must configure them for the same
// Kubernetes authority with a consistent exact-version mapper. Construction
// performs no I/O or background work. Full-object and Pod reads use client.
//
// The metadata client may decode an ordinary JSON response from the same GET,
// discarding its original GVK. This path therefore cannot independently verify
// that original GVK. Request or decode errors never trigger a dynamic GET.
func NewReaderWithOwnerMetadata(client dynamic.Interface, metadataClient metadata.Interface, mapper meta.RESTMapper) (discovery.OwnerMetadataReader, error) {
	if isNil(client) || isNil(metadataClient) || isNil(mapper) {
		return nil, fmt.Errorf("dynamic client, metadata client and REST mapper are required: %w", discovery.ErrNoClient)
	}
	return &ownerMetadataReader{reader: &reader{client: client, mapper: mapper}, metadataClient: metadataClient}, nil
}

func (r *ownerMetadataReader) GetOwnerMetadata(ctx context.Context, ref discovery.ObjectRef) (*metav1.PartialObjectMetadata, error) {
	if ref.Name == "" || ref.UID == "" {
		return nil, fmt.Errorf("%w: object name and UID are required", discovery.ErrInvalidInput)
	}
	mapping, err := r.Mapping(ctx, ref.GVK)
	if err != nil {
		return nil, err
	}
	resource := r.metadataClient.Resource(mapping.Resource)
	var scoped metadata.ResourceInterface = resource
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if ref.Namespace == "" {
			return nil, fmt.Errorf("%w: namespace required for %s", ownership.ErrScope, ref.GVK)
		}
		scoped = resource.Namespace(ref.Namespace)
	} else {
		ref.Namespace = ""
	}
	object, err := scoped.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s %s/%s: %w", ref.GVK, ref.Namespace, ref.Name, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("get %s: %w", ref.Name, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%w: nil object for %s", discovery.ErrInvalidInput, ref.Name)
	}
	actual := discovery.ObjectRef{GVK: ref.GVK, Namespace: object.Namespace, Name: object.Name, UID: object.UID}
	if actual.Namespace != ref.Namespace || actual.Name != ref.Name {
		return nil, fmt.Errorf("%w: returned coordinates disagree for %s", discovery.ErrStaleInput, ref.Name)
	}
	if actual.UID != ref.UID {
		return nil, &ownership.Error{Kind: ownership.ErrUIDMismatch, Expected: ref, Actual: actual}
	}
	result := &metav1.PartialObjectMetadata{
		TypeMeta: object.TypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Name: object.Name, Namespace: object.Namespace, UID: object.UID, ResourceVersion: object.ResourceVersion,
		},
	}
	if object.OwnerReferences != nil {
		result.OwnerReferences = make([]metav1.OwnerReference, len(object.OwnerReferences))
		for i := range object.OwnerReferences {
			object.OwnerReferences[i].DeepCopyInto(&result.OwnerReferences[i])
		}
	}
	return result, nil
}
