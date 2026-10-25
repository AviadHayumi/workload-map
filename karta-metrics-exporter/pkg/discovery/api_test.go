//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type apiReader struct{ client client.Client }

func (r apiReader) Cached(ObjectRef) (*unstructured.Unstructured, bool) { return nil, false }
func (r apiReader) GetMetadata(ctx context.Context, ref ObjectRef) (*metav1.PartialObjectMetadata, error) {
	m := &metav1.PartialObjectMetadata{}
	m.SetGroupVersionKind(ref.GVK)
	err := r.client.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, m)
	return m, err
}

var _ = Describe("API ownership resolution", func() {
	It("reads real metadata and keeps a deleted owner unresolved", func() {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Fail("KUBEBUILDER_ASSETS is required for integration tests")
		}
		environment := &envtest.Environment{}
		cfg, err := environment.Start()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(environment.Stop()).To(Succeed()) })
		api, err := client.New(cfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())
		ctx := context.Background()
		namespace := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "discovery-test"}}}
		Expect(api.Create(ctx, namespace)).To(Succeed())
		root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "root", "namespace": namespace.GetName()}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "test"}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "test"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "test", "image": "example.test/test:v1"}}}}}}}
		Expect(api.Create(ctx, root)).To(Succeed())
		pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "pod", "namespace": namespace.GetName()}, "spec": map[string]any{"containers": []any{map[string]any{"name": "test", "image": "example.test/test:v1"}}}}}
		yes := true
		pod.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: root.GetName(), UID: root.GetUID(), Controller: &yes}})
		Expect(api.Create(ctx, pod)).To(Succeed())
		d, err := New(Dependencies{Reader: apiReader{api}, Inventory: &definitions{}}, Config{})
		Expect(err).NotTo(HaveOccurred())
		ref := ObjectRef{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, Namespace: pod.GetNamespace(), Name: pod.GetName(), UID: pod.GetUID()}
		result, err := d.Get(ctx, ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Workload.Root.UID).To(Equal(root.GetUID()))
		Expect(result.MissingCoverage).To(Equal([]WatchRequest{{GVK: root.GroupVersionKind(), Namespace: namespace.GetName()}}))
		Expect(api.Delete(ctx, root)).To(Succeed())
		result, err = d.Get(ctx, ref)
		Expect(err).To(MatchError(ContainSubstring(ErrUnresolvedOwner.Error())))
		Expect(result.Workload).To(BeNil())
		Expect(result.MissingCoverage).To(BeEmpty())
	})
})
