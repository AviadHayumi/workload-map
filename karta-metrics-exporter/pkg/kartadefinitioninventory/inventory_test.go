// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

// The reflector backs off for up to a few seconds between attempts.
const (
	eventuallyTimeout  = 10 * time.Second
	eventuallyInterval = 200 * time.Millisecond
)

var (
	kartasGVR        = v1alpha1.GroupVersion.WithResource("kartas")
	kartasGR         = kartasGVR.GroupResource()
	withCatalog      = Options{Catalog: SourceOptions{Enabled: true, Exclude: []string{"core-pod-v1"}}, Cluster: SourceOptions{Enabled: true}}
	catalogJobOrigin = Origin{Source: SourceCatalog, Name: "batch-job-v1"}
)

func unstructuredKarta(k *v1alpha1.Karta) *unstructured.Unstructured {
	GinkgoHelper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(k)
	Expect(err).NotTo(HaveOccurred())
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Karta"))
	return u
}

func newFakeClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kartasGVR: "KartaList"}, objects...)
}

// start runs the inventory until the spec ends and checks that Run returns cleanly.
func start(inventory *Inventory) context.CancelFunc {
	GinkgoHelper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- inventory.Run(ctx) }()
	DeferCleanup(func() {
		cancel()
		Eventually(done, eventuallyTimeout, eventuallyInterval).Should(Receive(BeNil()))
	})
	return cancel
}

func lookupOrigin(inventory *Inventory, gvk schema.GroupVersionKind) Origin {
	GinkgoHelper()
	sel, err := inventory.Lookup(gvk)
	Expect(err).NotTo(HaveOccurred())
	return sel.Origin
}

var _ = Describe("Inventory", func() {
	It("rejects a missing client when the cluster source is enabled", func() {
		_, err := New(nil, DefaultOptions())

		Expect(err).To(MatchError(ContainSubstring("requires a client")))
	})

	It("rejects invalid options", func() {
		_, err := New(newFakeClient(), Options{Catalog: SourceOptions{Include: []string{"nope"}}})

		Expect(err).To(MatchError(ErrInvalidOptions))
	})

	It("serves a catalog-only configuration before Run and after it returns", func() {
		inventory, err := New(nil, Options{Catalog: SourceOptions{Enabled: true}})
		Expect(err).NotTo(HaveOccurred())
		Expect(inventory.Ready()).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))

		start(inventory)()

		Consistently(func(g Gomega) {
			sel, err := inventory.Lookup(jobGVK)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(sel.Origin).To(Equal(catalogJobOrigin))
		}, time.Second, eventuallyInterval).Should(Succeed())
	})

	It("is not ready before Run, and serves nothing from the catalog meanwhile", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())

		Expect(inventory.Ready()).To(MatchError(ErrNotReady))
		_, err = inventory.Lookup(jobGVK)
		Expect(err).To(MatchError(ErrNotReady))
		listed, err := inventory.Selections()
		Expect(err).To(MatchError(ErrNotReady))
		Expect(listed).To(BeNil())
	})

	It("refuses a second Run", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		start(inventory)
		Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())

		Expect(inventory.Run(context.Background())).To(MatchError(ContainSubstring("already started")))
	})

	Context("with a cluster", func() {
		var (
			client    *fake.FakeDynamicClient
			inventory *Inventory
		)

		BeforeEach(func() {
			client = newFakeClient(unstructuredKarta(newKarta("cluster-job", jobGVK, fromCluster("uid-1", "1"))))
			var err error
			inventory, err = New(client, withCatalog)
			Expect(err).NotTo(HaveOccurred())
		})

		It("serves cluster definitions once the initial list is in", func() {
			start(inventory)

			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
			Expect(lookupOrigin(inventory, jobGVK)).To(Equal(Origin{Source: SourceCluster, Name: "cluster-job", UID: "uid-1", ResourceVersion: "1"}))
			Expect(lookupOrigin(inventory, deploymentGVK)).To(Equal(Origin{Source: SourceCatalog, Name: "apps-deployment-v1"}))
		})

		It("follows adds, updates and deletes, and serves the catalog again after a delete", func() {
			start(inventory)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
			resource := client.Resource(kartasGVR)
			ctx := context.Background()

			_, err := resource.Create(ctx, unstructuredKarta(newKarta("cluster-deployment", deploymentGVK, fromCluster("uid-2", "1"))), metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				sel, err := inventory.Lookup(deploymentGVK)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sel.Origin.Name).To(Equal("cluster-deployment"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			_, err = resource.Update(ctx, unstructuredKarta(newKarta("cluster-deployment", deploymentGVK, fromCluster("uid-2", "2"))), metav1.UpdateOptions{})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				sel, err := inventory.Lookup(deploymentGVK)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sel.Origin.ResourceVersion).To(Equal("2"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			Expect(resource.Delete(ctx, "cluster-job", metav1.DeleteOptions{})).To(Succeed())
			Eventually(func(g Gomega) {
				sel, err := inventory.Lookup(jobGVK)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sel.Origin).To(Equal(catalogJobOrigin))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})

		It("reports an invalid cluster winner without falling back", func() {
			start(inventory)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			_, err := client.Resource(kartasGVR).Update(context.Background(),
				unstructuredKarta(newKarta("cluster-job", jobGVK, fromCluster("uid-1", "2"), invalid)), metav1.UpdateOptions{})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() error {
				_, err := inventory.Lookup(jobGVK)
				return err
			}, eventuallyTimeout, eventuallyInterval).Should(MatchError(ErrInvalidDefinition))
			Expect(inventory.Ready()).To(Succeed())
		})

		It("fails closed on a Karta it cannot decode, and recovers once it is gone", func() {
			start(inventory)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
			resource := client.Resource(kartasGVR)
			broken := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "run.ai/v1alpha1", "kind": "Karta",
				"metadata": map[string]any{"name": "broken"},
				"spec":     map[string]any{"structureDefinition": "not-an-object"},
			}}

			_, err := resource.Create(context.Background(), broken, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(ContainSubstring(`decode Karta "broken"`)))
			Expect(inventory.Ready()).To(MatchError(ErrUnavailable))
			_, err = inventory.Lookup(jobGVK)
			Expect(err).To(MatchError(ErrUnavailable))

			Expect(resource.Delete(context.Background(), "broken", metav1.DeleteOptions{})).To(Succeed())
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})

		It("reports the source as stopped after Run returns", func() {
			cancel := start(inventory)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			cancel()

			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(ErrUnavailable))
			Expect(inventory.Ready()).To(MatchError(errStopped))
		})

		It("serves concurrent readers while definitions change", func() {
			start(inventory)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
			resource := client.Resource(kartasGVR)

			var wg sync.WaitGroup
			stop := make(chan struct{})
			for range 8 {
				wg.Go(func() {
					defer GinkgoRecover()
					for {
						select {
						case <-stop:
							return
						default:
						}
						sel, err := inventory.Lookup(jobGVK)
						Expect(err).NotTo(HaveOccurred())
						Expect(sel.Definition).NotTo(BeNil())
						_, err = inventory.Selections()
						Expect(err).NotTo(HaveOccurred())
					}
				})
			}
			for n := range 20 {
				k := unstructuredKarta(newKarta("churn", deploymentGVK))
				if n%2 == 0 {
					_, err := resource.Create(context.Background(), k, metav1.CreateOptions{})
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(resource.Delete(context.Background(), "churn", metav1.DeleteOptions{})).To(Succeed())
				}
			}
			close(stop)
			wg.Wait()
		})
	})

	Context("with a failing source", func() {
		var (
			client    *fake.FakeDynamicClient
			inventory *Inventory
			listErr   atomic.Pointer[error]
		)

		BeforeEach(func() {
			client = newFakeClient(unstructuredKarta(newKarta("cluster-job", jobGVK)))
			listErr.Store(nil)
			client.PrependReactor("list", "kartas", func(ktesting.Action) (bool, runtime.Object, error) {
				if err := listErr.Load(); err != nil {
					return true, nil, *err
				}
				return false, nil, nil
			})
			var err error
			inventory, err = New(client, withCatalog)
			Expect(err).NotTo(HaveOccurred())
		})

		fail := func(err error) { listErr.Store(&err) }

		DescribeTable("classifies a failing LIST and never serves the catalog meanwhile",
			func(cause error, want error) {
				fail(cause)
				start(inventory)

				Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(want))
				_, err := inventory.Lookup(jobGVK)
				Expect(err).To(MatchError(want))
				Expect(err).To(MatchError(cause))
			},
			Entry("forbidden", apierrors.NewForbidden(kartasGR, "", errors.New("no list permission")), ErrPermissionDenied),
			Entry("unauthorized", apierrors.NewUnauthorized("unknown token"), ErrPermissionDenied),
			Entry("a missing Karta CRD, which the API server reports as not found", apierrors.NewNotFound(kartasGR, ""), ErrUnavailable),
			Entry("a server error", apierrors.NewInternalError(errors.New("etcd is down")), ErrUnavailable),
		)

		It("recovers once LIST succeeds again", func() {
			fail(apierrors.NewForbidden(kartasGR, "", errors.New("no list permission")))
			start(inventory)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(ErrPermissionDenied))

			listErr.Store(nil)

			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
			Expect(lookupOrigin(inventory, jobGVK).Source).To(Equal(SourceCluster))
		})

		It("fails when a relist after a sync is denied, and recovers", func() {
			var watches atomic.Pointer[watch.RaceFreeFakeWatcher]
			client.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) {
				w := watch.NewRaceFreeFake()
				watches.Store(w)
				return true, w, nil
			})
			start(inventory)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			fail(apierrors.NewForbidden(kartasGR, "", errors.New("permission revoked")))
			watches.Load().Stop()

			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(ErrPermissionDenied))
			_, err := inventory.Lookup(jobGVK)
			Expect(err).To(MatchError(ErrPermissionDenied))

			listErr.Store(nil)
			Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})
	})
})
