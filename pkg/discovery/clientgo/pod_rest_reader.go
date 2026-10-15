// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package clientgo

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	kjson "sigs.k8s.io/json"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

type podRESTReader struct {
	discovery.Reader
	pods rest.Interface
}

type ownerMetadataPodRESTReader struct {
	*podRESTReader
	metadataReader discovery.OwnerMetadataReader
}

var (
	_ discovery.PodTransferReader   = (*podRESTReader)(nil)
	_ discovery.PodTransferReader   = (*ownerMetadataPodRESTReader)(nil)
	_ discovery.OwnerMetadataReader = (*ownerMetadataPodRESTReader)(nil)
)

// WithPodRESTClient replaces base's Pod listing with JSON reads through pods.
// Both clients are caller-owned and must address the same Kubernetes authority.
// pods must use core v1 routing, such as CoreV1().RESTClient(). Construction
// performs no I/O or background work. IndexedReader bases are rejected; use an
// index directly for coherent cached queries. Existing constructors remain unchanged.
//
// The returned reader always implements discovery.PodTransferReader and preserves
// discovery.OwnerMetadataReader when base provides it. Object reads and mappings
// are delegated unchanged. List errors never fall back to base.ListPods.
//
// Lists must have an observed v1/PodList envelope. Empty item TypeMeta is filled
// from that envelope; partial or conflicting item TypeMeta is rejected. Duplicate
// decoded fields and incomplete owner identities, including non-controllers, are
// rejected. Unknown fields remain allowed. This validation is intentionally
// stricter than NewReader's dynamic Pod decoding.
func WithPodRESTClient(base discovery.Reader, pods rest.Interface) (discovery.Reader, error) {
	if isNil(base) || isNil(pods) {
		return nil, fmt.Errorf("base reader and core REST client are required: %w", discovery.ErrNoClient)
	}
	if _, ok := base.(discovery.IndexedReader); ok {
		return nil, fmt.Errorf("%w: Pod REST decoration requires a direct reader", discovery.ErrInvalidInput)
	}
	if pods.APIVersion() != corev1.SchemeGroupVersion {
		return nil, fmt.Errorf("%w: core REST client must use v1", discovery.ErrInvalidInput)
	}
	result := &podRESTReader{Reader: base, pods: pods}
	if metadataReader, ok := base.(discovery.OwnerMetadataReader); ok {
		return &ownerMetadataPodRESTReader{podRESTReader: result, metadataReader: metadataReader}, nil
	}
	return result, nil
}

func (r *ownerMetadataPodRESTReader) GetOwnerMetadata(ctx context.Context, ref discovery.ObjectRef) (*metav1.PartialObjectMetadata, error) {
	return r.metadataReader.GetOwnerMetadata(ctx, ref)
}

func (r *podRESTReader) ListPodsForTransfer(ctx context.Context, namespace string) (*corev1.PodList, error) {
	return r.ListPods(ctx, namespace)
}

func (r *podRESTReader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	result := &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}}
	options := metav1.ListOptions{Limit: podPageSize}
	seenTokens := make(map[string]bool)
	seenUIDs := make(map[types.UID]bool)
	seenCoordinates := make(map[types.NamespacedName]bool)
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list Pods: %w", err)
		}
		response := r.pods.Get().NamespaceIfScoped(namespace, namespace != "").Resource("pods").
			VersionedParams(&options, metav1.ParameterCodec).SetHeader("Accept", "application/json").Do(ctx)
		// Error enriches non-2xx responses with Kubernetes Status details.
		if err := response.Error(); err != nil {
			return nil, fmt.Errorf("list Pods in %q: %w", namespace, err)
		}
		body, err := response.Raw()
		if err != nil {
			return nil, fmt.Errorf("list Pods in %q: %w", namespace, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list Pods: %w", err)
		}
		// Do reads the response into owned bytes. Fresh typed decoding owns the
		// nested data; no caller-supplied typed object is borrowed or retained.
		page := &corev1.PodList{}
		strictErrors, err := kjson.UnmarshalStrict(body, page, kjson.DisallowDuplicateFields)
		if err != nil {
			return nil, fmt.Errorf("%w: decode Pod page: %w", discovery.ErrInvalidInput, err)
		}
		if len(strictErrors) != 0 {
			return nil, fmt.Errorf("%w: duplicate Pod page fields: %v", discovery.ErrInvalidInput, strictErrors)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("list Pods: %w", err)
		}
		if page.APIVersion == "v1" && page.Kind == "Status" {
			status := &metav1.Status{}
			strictErrors, err := kjson.UnmarshalStrict(body, status, kjson.DisallowDuplicateFields)
			if err != nil || len(strictErrors) != 0 {
				return nil, fmt.Errorf("%w: invalid Status response", discovery.ErrInvalidInput)
			}
			if status.Status == metav1.StatusFailure {
				return nil, fmt.Errorf("list Pods in %q: %w", namespace, apierrors.FromObject(status))
			}
		}
		if page.APIVersion != "v1" || page.Kind != "PodList" {
			return nil, fmt.Errorf("%w: Pod page must have v1/PodList envelope", discovery.ErrInvalidInput)
		}
		if !first && page.ResourceVersion != result.ResourceVersion {
			return nil, fmt.Errorf("%w: Pod resource version changed between pages", discovery.ErrIncompleteOwnership)
		}
		if (options.Continue != "" || page.Continue != "") && page.ResourceVersion == "" {
			return nil, fmt.Errorf("%w: paginated Pod list has no resource version", discovery.ErrIncompleteOwnership)
		}
		if page.Continue == "" && page.RemainingItemCount != nil && *page.RemainingItemCount > 0 {
			return nil, fmt.Errorf("%w: Pod page declares unreturned items", discovery.ErrIncompleteOwnership)
		}
		if first {
			result.ResourceVersion = page.ResourceVersion
			first = false
		}
		for i := range page.Items {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("list Pods: %w", err)
			}
			pod := &page.Items[i]
			if pod.APIVersion == "" && pod.Kind == "" {
				pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
			} else if pod.APIVersion != "v1" || pod.Kind != "Pod" {
				return nil, fmt.Errorf("%w: Pod item has conflicting GVK", discovery.ErrInvalidInput)
			}
			if pod.Name == "" || pod.Namespace == "" || pod.UID == "" {
				return nil, fmt.Errorf("%w: incomplete Pod identity", discovery.ErrInvalidInput)
			}
			if namespace != "" && pod.Namespace != namespace {
				return nil, fmt.Errorf("%w: Pod outside requested namespace", discovery.ErrInsufficientScope)
			}
			if seenUIDs[pod.UID] {
				return nil, fmt.Errorf("%w: duplicate Pod UID %s", discovery.ErrInvalidInput, pod.UID)
			}
			coordinates := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
			if seenCoordinates[coordinates] {
				return nil, fmt.Errorf("%w: duplicate Pod coordinates", discovery.ErrInvalidInput)
			}
			for j, owner := range pod.OwnerReferences {
				gv, err := schema.ParseGroupVersion(owner.APIVersion)
				if err != nil || gv.Version == "" || owner.Kind == "" || owner.Name == "" || owner.UID == "" {
					return nil, fmt.Errorf("%w: Pod ownerReferences[%d] has incomplete or invalid identity", discovery.ErrInvalidInput, j)
				}
			}
			seenUIDs[pod.UID], seenCoordinates[coordinates] = true, true
		}
		result.Items = append(result.Items, page.Items...)
		next := page.Continue
		if next == "" {
			return result, nil
		}
		if seenTokens[next] {
			return nil, fmt.Errorf("%w: repeated Pod continuation token", discovery.ErrIncompleteOwnership)
		}
		seenTokens[next] = true
		options.Continue = next
	}
}
