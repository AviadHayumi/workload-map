// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package integration

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

var (
	kartasGVR   = v1alpha1.GroupVersion.WithResource("kartas")
	crdsGVR     = apiextensionsv1.SchemeGroupVersion.WithResource("customresourcedefinitions")
	jobGVK      = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	statefulGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}
	withCatalog = inventory.Options{
		Catalog: inventory.SourceOptions{Enabled: true, Exclude: []string{"core-pod-v1"}},
		Cluster: inventory.SourceOptions{Enabled: true},
	}
)

// clusterKarta copies the catalog definition for gvk under a new name.
func clusterKarta(name string, gvk schema.GroupVersionKind) *unstructured.Unstructured {
	GinkgoHelper()
	k, err := catalog.Get(gvk)
	Expect(err).NotTo(HaveOccurred())
	k.ObjectMeta = metav1.ObjectMeta{Name: name}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(k)
	Expect(err).NotTo(HaveOccurred())
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Karta"))
	return u
}

func create(objects ...*unstructured.Unstructured) {
	GinkgoHelper()
	for _, u := range objects {
		_, err := adminClient.Resource(kartasGVR).Create(testCtx, u, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
	}
}

// start runs a new inventory reading the cluster as config until the spec ends.
func start(config *rest.Config, options inventory.Options) *inventory.Inventory {
	GinkgoHelper()
	client, err := dynamic.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())
	inv, err := inventory.New(client, options)
	Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithCancel(testCtx)
	done := make(chan error, 1)
	go func() { done <- inv.Run(ctx) }()
	DeferCleanup(func() {
		cancel()
		Eventually(done, eventuallyTimeout, eventuallyInterval).Should(Receive(BeNil()))
	})
	return inv
}

func expectOrigin(inv *inventory.Inventory, gvk schema.GroupVersionKind, want func(inventory.Origin) bool) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		sel, err := inv.Lookup(gvk)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(want(sel.Origin)).To(BeTrue(), "origin %+v", sel.Origin)
	}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
}

var _ = Describe("Karta definition inventory", func() {
	AfterEach(func() {
		Expect(adminClient.Resource(kartasGVR).DeleteCollection(testCtx, metav1.DeleteOptions{}, metav1.ListOptions{})).To(Succeed())
		Eventually(func(g Gomega) {
			list, err := adminClient.Resource(kartasGVR).List(testCtx, metav1.ListOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(list.Items).To(BeEmpty())
		}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
	})

	It("serves a cluster Karta with the server's identity, follows changes, and serves the catalog after a delete", func() {
		create(clusterKarta("cluster-job", jobGVK))
		created, err := adminClient.Resource(kartasGVR).Get(testCtx, "cluster-job", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())

		inv := start(adminConfig, withCatalog)

		expectOrigin(inv, jobGVK, func(o inventory.Origin) bool {
			return o == inventory.Origin{Source: inventory.SourceCluster, Name: "cluster-job", UID: created.GetUID(), ResourceVersion: created.GetResourceVersion()}
		})

		created.SetLabels(map[string]string{"example.com/owner": "platform"})
		updated, err := adminClient.Resource(kartasGVR).Update(testCtx, created, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())
		expectOrigin(inv, jobGVK, func(o inventory.Origin) bool { return o.ResourceVersion == updated.GetResourceVersion() })

		Expect(adminClient.Resource(kartasGVR).Delete(testCtx, "cluster-job", metav1.DeleteOptions{})).To(Succeed())
		expectOrigin(inv, jobGVK, func(o inventory.Origin) bool {
			return o == inventory.Origin{Source: inventory.SourceCatalog, Name: "batch-job-v1"}
		})
	})

	It("breaks a tie between Kartas created in the same second by name", func() {
		create(clusterKarta("sts-b", statefulGVK), clusterKarta("sts-a", statefulGVK))
		a, err := adminClient.Resource(kartasGVR).Get(testCtx, "sts-a", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		b, err := adminClient.Resource(kartasGVR).Get(testCtx, "sts-b", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		aCreated, bCreated := a.GetCreationTimestamp(), b.GetCreationTimestamp()
		want := "sts-a"
		if bCreated.Before(&aCreated) {
			want = "sts-b"
		}

		inv := start(adminConfig, withCatalog)

		expectOrigin(inv, statefulGVK, func(o inventory.Origin) bool { return o.Name == want })
	})

	DescribeTable("classifies a LIST the server refuses, and never serves the catalog",
		func(config func() *rest.Config, want error) {
			inv := start(config(), withCatalog)

			Eventually(inv.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(want))
			_, err := inv.Lookup(jobGVK)
			Expect(err).To(MatchError(want))
		},
		Entry("a user without Karta access is forbidden", func() *rest.Config {
			GinkgoHelper()
			user, err := testEnv.AddUser(envtest.User{Name: "no-karta-access"}, nil)
			Expect(err).NotTo(HaveOccurred())
			return user.Config()
		}, inventory.ErrPermissionDenied),
		Entry("an unknown bearer token is unauthorized", func() *rest.Config {
			return &rest.Config{Host: adminConfig.Host, BearerToken: "not-a-real-token", TLSClientConfig: rest.TLSClientConfig{CAData: adminConfig.CAData}}
		}, inventory.ErrPermissionDenied),
	)

	It("is unavailable while the Karta CRD is missing and recovers once it is reinstalled", func() {
		create(clusterKarta("cluster-job", jobGVK))
		inv := start(adminConfig, withCatalog)
		expectOrigin(inv, jobGVK, func(o inventory.Origin) bool { return o.Source == inventory.SourceCluster })

		Expect(adminClient.Resource(crdsGVR).Delete(testCtx, "kartas.run.ai", metav1.DeleteOptions{})).To(Succeed())
		DeferCleanup(func() {
			_, err := envtest.InstallCRDs(adminConfig, envtest.CRDInstallOptions{Paths: crdPaths, ErrorIfPathMissing: true})
			Expect(err).NotTo(HaveOccurred())
		})

		Eventually(inv.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(inventory.ErrUnavailable))
		_, err := inv.Lookup(jobGVK)
		Expect(err).To(MatchError(inventory.ErrUnavailable))

		_, err = envtest.InstallCRDs(adminConfig, envtest.CRDInstallOptions{Paths: crdPaths, ErrorIfPathMissing: true})
		Expect(err).NotTo(HaveOccurred())
		Eventually(inv.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		expectOrigin(inv, jobGVK, func(o inventory.Origin) bool {
			return o == inventory.Origin{Source: inventory.SourceCatalog, Name: "batch-job-v1"}
		})
	})
})
