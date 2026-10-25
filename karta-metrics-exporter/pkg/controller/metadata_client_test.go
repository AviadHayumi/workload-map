// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"encoding/json"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

var _ = Describe("Metadata negotiation fallback", func() {
	DescribeTable("uses a full GET only after explicit metadata negotiation rejection", func(ctx SpecContext, code int) {
		runtime, metadata, _ := testRuntime(nil)
		resource := testRootGVK.GroupVersion().WithResource("jobs")
		rejected := apierrors.NewGenericServerResponse(code, "get", resource.GroupResource(), "root", "metadata unsupported", 0, false)
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, nil, rejected })
		dynamic := runtime.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient)
		full := testObject(testRootGVK, "root")
		full.Object["spec"] = map[string]any{"discard": "large body"}
		dynamic.PrependReactor("get", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			Expect(action.GetNamespace()).To(Equal("ns"))
			Expect(action.(clienttesting.GetAction).GetName()).To(Equal("root"))
			return true, full, nil
		})
		result, err := runtime.reader.fetchMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "root", UID: "root"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.UID).To(Equal(full.GetUID()))
		Expect(result.ResourceVersion).To(Equal("1"))
		encoded, err := json.Marshal(result)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).NotTo(ContainSubstring("large body"))
		Expect(dynamic.Actions()).To(HaveLen(1))
	}, Entry("HTTP406", 406), Entry("HTTP415", 415))
	DescribeTable("preserves pagination and namespace in full LIST fallback", func(ctx SpecContext, code int) {
		runtime, metadata, _ := testRuntime(nil)
		resource := testRootGVK.GroupVersion().WithResource("jobs")
		dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{resource: "JobList"})
		runtime.reader.clients.Dynamic = dynamic
		rememberOwner(runtime.reader, "first", "1")
		rememberOwner(runtime.reader, "second", "1")
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewGenericServerResponse(code, "list", resource.GroupResource(), "", "metadata unsupported", 0, false)
		})
		pages := 0
		dynamic.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			Expect(action.GetNamespace()).To(Equal("ns"))
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			Expect(options.Limit).To(Equal(int64(500)))
			pages++
			result := &unstructured.UnstructuredList{}
			result.SetResourceVersion("snapshot")
			if pages == 1 {
				Expect(options.Continue).To(BeEmpty())
				result.Items = []unstructured.Unstructured{*testObject(testRootGVK, "first")}
				result.SetContinue("next")
			} else {
				Expect(options.Continue).To(Equal("next"))
				result.Items = []unstructured.Unstructured{*testObject(testRootGVK, "second")}
			}
			return true, result, nil
		})
		Expect(runtime.reader.refreshMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(2))
		Expect(runtime.reader.metadata).To(HaveLen(2))
	}, Entry("HTTP406", 406), Entry("HTTP415", 415))
	DescribeTable("keeps ordinary GET and LIST errors without negotiation fallback", func(ctx SpecContext, code int) {
		runtime, metadata, _ := testRuntime(nil)
		resource := testRootGVK.GroupVersion().WithResource("jobs")
		rejected := apierrors.NewGenericServerResponse(code, "read", resource.GroupResource(), "owner", fmt.Sprintf("status%d", code), 0, false)
		metadata.PrependReactor("*", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, nil, rejected })
		_, getErr := runtime.reader.getMetadata(ctx, resource, "ns", "owner")
		_, listErr := runtime.reader.listMetadata(ctx, resource, "ns", metav1.ListOptions{Limit: 500})
		Expect(errors.Is(getErr, rejected)).To(BeTrue())
		Expect(errors.Is(listErr, rejected)).To(BeTrue())
		Expect(runtime.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).Actions()).To(BeEmpty())
	}, Entry("permission denied", 403), Entry("not found", 404), Entry("server failure", 500))
})
