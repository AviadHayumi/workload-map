// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

var _ = Describe("WaitLoaded", func() {
	It("waits for a delayed initial list and releases every waiter after applying it", func() {
		client := newFakeClient(unstructuredKarta(newKarta("cluster-job", jobGVK)))
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		client.PrependReactor("list", "kartas", func(ktesting.Action) (bool, runtime.Object, error) {
			close(entered)
			<-release
			return false, nil, nil
		})
		inventory, err := New(client, withCatalog)
		Expect(err).NotTo(HaveOccurred())
		start(inventory)
		DeferCleanup(unblock)
		Eventually(entered, eventuallyTimeout).Should(BeClosed())
		ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
		defer cancel()
		results := make(chan error, 8)
		for range cap(results) {
			go func() { results <- inventory.WaitLoaded(ctx) }()
		}
		Consistently(results, 100*time.Millisecond).ShouldNot(Receive())
		Expect(inventory.Ready()).To(MatchError(ErrNotReady))

		unblock()

		for range cap(results) {
			Eventually(results, eventuallyTimeout).Should(Receive(BeNil()))
		}
		Expect(lookupOrigin(inventory, jobGVK).Name).To(Equal("cluster-job"))
	})

	It("loads an empty list and serves the catalog", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		start(inventory)
		ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
		defer cancel()

		Expect(inventory.WaitLoaded(ctx)).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))
	})

	It("cancels one waiter without canceling another or starting Run", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		canceled := make(chan error, 1)
		go func() { canceled <- inventory.WaitLoaded(ctx) }()
		otherCtx, otherCancel := context.WithTimeout(context.Background(), eventuallyTimeout)
		defer otherCancel()
		other := make(chan error, 1)
		go func() { other <- inventory.WaitLoaded(otherCtx) }()

		cancel()

		Eventually(canceled, eventuallyTimeout).Should(Receive(MatchError(context.Canceled)))
		Expect(inventory.Ready()).To(MatchError(ErrNotReady))
		Expect(other).NotTo(Receive())
		start(inventory)
		Eventually(other, eventuallyTimeout).Should(Receive(BeNil()))
	})

	It("returns immediately for catalog-only inventories before Run", func() {
		inventory, err := New(nil, Options{Catalog: SourceOptions{Enabled: true}})
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
		defer cancel()

		Expect(inventory.WaitLoaded(ctx)).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))
	})

	It("releases waiters when Run stops before the first list succeeds", func() {
		client := newFakeClient()
		client.PrependReactor("list", "kartas", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("not yet available")
		})
		inventory, err := New(client, withCatalog)
		Expect(err).NotTo(HaveOccurred())
		cancelRun := start(inventory)
		Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(MatchError(ErrUnavailable))
		ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- inventory.WaitLoaded(ctx) }()

		cancelRun()

		Eventually(result, eventuallyTimeout).Should(Receive(MatchError(errStopped)))
		Expect(inventory.WaitLoaded(ctx)).To(MatchError(errStopped))
	})

	It("remembers loading after a later source failure and shutdown", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		cancelRun := start(inventory)
		ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
		defer cancel()
		Expect(inventory.WaitLoaded(ctx)).To(Succeed())
		inventory.mu.Lock()
		inventory.fail(apierrors.NewServiceUnavailable("disconnected"))
		inventory.mu.Unlock()

		Expect(inventory.Ready()).To(MatchError(ErrUnavailable))
		Expect(inventory.WaitLoaded(ctx)).To(Succeed())
		cancelRun()
		Eventually(inventory.Ready, eventuallyTimeout).Should(MatchError(errStopped))
		Expect(inventory.WaitLoaded(ctx)).To(Succeed())
	})

	It("marks a complete list loaded even when a definition cannot be decoded", func() {
		broken := unstructuredKarta(newKarta("broken", jobGVK))
		broken.Object["spec"] = "not-an-object"
		inventory, err := New(newFakeClient(broken), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		start(inventory)
		ctx, cancel := context.WithTimeout(context.Background(), eventuallyTimeout)
		defer cancel()

		Expect(inventory.WaitLoaded(ctx)).To(Succeed())
		Expect(inventory.Ready()).To(MatchError(ErrUnavailable))
	})
})
