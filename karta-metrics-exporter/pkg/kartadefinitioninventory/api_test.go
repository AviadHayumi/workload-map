//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

type apiSource struct{ client client.WithWatch }

func (s apiSource) List(ctx context.Context, options metav1.ListOptions) (*v1alpha1.KartaList, error) {
	list := &v1alpha1.KartaList{}
	err := s.client.List(ctx, list, &client.ListOptions{Raw: &options})
	return list, err
}
func (s apiSource) Watch(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	return s.client.Watch(ctx, &v1alpha1.KartaList{}, &client.ListOptions{Raw: &options})
}

var _ = Describe("Karta API source", func() {
	It("tracks actual LIST/WATCH creation updates and deletion", func() {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Fail("KUBEBUILDER_ASSETS is required for integration tests")
		}
		environment := &envtest.Environment{CRDDirectoryPaths: []string{"../../../charts/karta/crds"}, ErrorIfCRDPathMissing: true}
		cfg, err := environment.Start()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(environment.Stop()).To(Succeed()) })
		scheme := runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		api, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())
		inv, err := New(Config{Cluster: ClusterConfig{Enabled: true}}, apiSource{api})
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- inv.Run(ctx) }()
		DeferCleanup(func() { cancel(); Eventually(done).Should(Receive()) })
		gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
		Eventually(func() error { _, err := inv.Lookup(gvk); return err }, 10*time.Second).Should(Succeed())
		karta, err := catalog.Get(gvk)
		Expect(err).NotTo(HaveOccurred())
		karta.Name = "test-deployment"
		Expect(api.Create(ctx, karta)).To(Succeed())
		Eventually(func() string { v, _ := inv.Lookup(gvk); return v.Origin.Name }).Should(Equal(karta.Name))
		first, _ := inv.Lookup(gvk)
		Expect(first.Origin.UID).To(Equal(karta.UID))
		Expect(first.Origin.ResourceVersion).NotTo(BeEmpty())
		karta.Labels = map[string]string{"revision": "updated"}
		Expect(api.Update(ctx, karta)).To(Succeed())
		Eventually(func() string {
			v, _ := inv.Lookup(gvk)
			if v.Definition == nil {
				return ""
			}
			return v.Definition.Labels["revision"]
		}).Should(Equal("updated"))
		Expect(api.Delete(ctx, karta)).To(Succeed())
		Eventually(func() *v1alpha1.Karta { v, _ := inv.Lookup(gvk); return v.Definition }).Should(BeNil())
	})
})
