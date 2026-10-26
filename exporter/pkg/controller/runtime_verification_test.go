// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
)

func TestRuntimeVerificationMetadataFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		wantFull   bool
		wantErr    bool
		noMetadata bool
	}{
		{name: "metadata", status: http.StatusOK},
		{name: "not_acceptable", status: http.StatusNotAcceptable, wantFull: true},
		{name: "unsupported_media_type", status: http.StatusUnsupportedMediaType, wantFull: true},
		{name: "permission_denied", status: http.StatusForbidden, wantErr: true},
		{name: "server_failure", status: http.StatusInternalServerError, wantErr: true},
		{name: "dynamic_only", wantFull: true, noMetadata: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := runtimeJob("work", "root")
			gvr := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
			dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "JobList"})
			var fullCalls, metadataCalls int
			dynamicClient.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, runtime.Object, error) {
				fullCalls++
				list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*root}}
				list.SetResourceVersion("7")
				return true, list, nil
			})
			controller := &Runtime{dynamic: dynamicClient, options: RuntimeOptions{PageSize: 500, RequestTimeout: time.Second}}
			var metadataErr error
			if !test.noMetadata {
				metadataClient := metadatafake.NewSimpleMetadataClient(runtime.NewScheme())
				if test.status != http.StatusOK {
					metadataErr = apierrors.NewGenericServerResponse(test.status, "list", gvr.GroupResource(), "", "test failure", 0, false)
				}
				metadataClient.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, runtime.Object, error) {
					metadataCalls++
					if metadataErr != nil {
						return true, nil, metadataErr
					}
					return true, &metav1.List{
						ListMeta: metav1.ListMeta{ResourceVersion: "7"},
						Items: []runtime.RawExtension{{Object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
							Namespace: root.GetNamespace(), Name: root.GetName(), UID: root.GetUID(), ResourceVersion: root.GetResourceVersion(),
						}}}},
					}, nil
				})
				controller.metadata = metadataClient
			}
			refs, err := controller.listReferences(context.Background(), &resourceStream{
				key: discovery.CoverageKey{Namespace: "team", Resource: gvr}, gvk: root.GroupVersionKind(),
			})
			if test.wantErr {
				if !errors.Is(err, metadataErr) || len(refs) != 0 {
					t.Fatalf("failed metadata LIST = %v, %v; want original error and no evidence", refs, err)
				}
			} else if want := []discovery.ObjectRef{objectReference(root, root.GroupVersionKind())}; err != nil || !reflect.DeepEqual(refs, want) {
				t.Fatalf("LIST references = %v, %v; want %v", refs, err, want)
			}
			if (fullCalls == 1) != test.wantFull || fullCalls > 1 || metadataCalls != 1 && !test.noMetadata {
				t.Fatalf("LIST calls: full=%d, metadata=%d; fallback=%t", fullCalls, metadataCalls, test.wantFull)
			}
		})
	}
}
