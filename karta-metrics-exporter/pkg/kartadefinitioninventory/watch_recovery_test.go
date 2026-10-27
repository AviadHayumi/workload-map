// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

// truncatedDecoder optionally sends a bookmark, then ends with a decode error.
// StreamWatcher closes without an Error event for io.ErrUnexpectedEOF.
type truncatedDecoder struct {
	bookmark bool
	broken   chan struct{}
	once     sync.Once
}

func (d *truncatedDecoder) Decode() (watch.EventType, runtime.Object, error) {
	if d.bookmark {
		d.bookmark = false
		bookmark := &unstructured.Unstructured{}
		bookmark.SetResourceVersion("2")
		return watch.Bookmark, bookmark, nil
	}
	<-d.broken
	return "", nil, io.ErrUnexpectedEOF
}

func (d *truncatedDecoder) Close() { d.once.Do(func() { close(d.broken) }) }

type streamErrorReporter struct{}

func (streamErrorReporter) AsObject(error) runtime.Object {
	return &metav1.Status{Status: "Failure", Code: 500}
}

var _ = Describe("Inventory watch recovery", func() {
	It("does not let a stopped old watch closing invalidate a fresh list", func() {
		client := newFakeClient()
		server := watch.NewRaceFreeFake()
		client.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) {
			return true, server, nil
		})
		inventory, err := New(client, withCatalog)
		Expect(err).NotTo(HaveOccurred())
		store := observedStore{Store: cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc), inventory: inventory}
		inventory.update(func() { inventory.store = store })
		Expect(store.Replace(nil, "1")).To(Succeed())
		w, err := inventory.listWatch(client.Resource(kartasGVR)).WatchFuncWithContext(context.Background(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.Stop)

		w.Stop()
		Expect(store.Replace([]any{unstructuredKarta(newKarta("fresh", jobGVK))}, "2")).To(Succeed())
		server.Stop()

		Eventually(w.ResultChan(), eventuallyTimeout).Should(BeClosed())
		Expect(inventory.Ready()).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK).Name).To(Equal("fresh"))
	})

	It("preserves the permission error when the failed watch subsequently closes", func() {
		client := newFakeClient()
		server := watch.NewRaceFreeFake()
		client.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) {
			return true, server, nil
		})
		inventory, err := New(client, withCatalog)
		Expect(err).NotTo(HaveOccurred())
		store := observedStore{Store: cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc), inventory: inventory}
		inventory.update(func() { inventory.store = store })
		Expect(store.Replace(nil, "1")).To(Succeed())
		w, err := inventory.listWatch(client.Resource(kartasGVR)).WatchFuncWithContext(context.Background(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.Stop)
		denied := apierrors.NewForbidden(kartasGR, "", errors.New("permission revoked"))
		server.Error(&denied.ErrStatus)
		Eventually(w.ResultChan(), eventuallyTimeout).Should(Receive())
		Expect(inventory.Ready()).To(MatchError(ErrPermissionDenied))

		server.Stop()

		Eventually(w.ResultChan(), eventuallyTimeout).Should(BeClosed())
		Expect(inventory.Ready()).To(MatchError(ErrPermissionDenied))
		_, err = inventory.Lookup(jobGVK)
		Expect(err).To(MatchError(ErrPermissionDenied))
		Expect(err).To(MatchError(ContainSubstring("permission revoked")))
	})

	DescribeTable("fails closed when a watch ends without an Error event", func(truncated bool) {
		client := newFakeClient()
		var server watch.Interface = watch.NewRaceFreeFake()
		end := server.Stop
		if truncated {
			decoder := &truncatedDecoder{broken: make(chan struct{})}
			server = watch.NewStreamWatcher(decoder, streamErrorReporter{})
			end = decoder.Close
		}
		DeferCleanup(end)
		client.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) {
			return true, server, nil
		})
		inventory, err := New(client, withCatalog)
		Expect(err).NotTo(HaveOccurred())
		store := observedStore{Store: cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc), inventory: inventory}
		inventory.update(func() { inventory.store = store })
		Expect(store.Replace(nil, "1")).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))
		w, err := inventory.listWatch(client.Resource(kartasGVR)).WatchFuncWithContext(context.Background(), metav1.ListOptions{ResourceVersion: "1"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.Stop)

		end()

		Eventually(w.ResultChan(), eventuallyTimeout).Should(BeClosed())
		Expect(inventory.Ready()).To(MatchError(ErrUnavailable))
		_, err = inventory.Lookup(jobGVK)
		Expect(err).To(MatchError(ErrUnavailable))
	},
		Entry("unexpected EOF during decoding", true),
		Entry("clean closure of an idle watch", false),
	)

	It("requires a full relist after a truncated stream before serving catalog absence again", func() {
		client := newFakeClient()
		decoder := &truncatedDecoder{bookmark: true, broken: make(chan struct{})}
		var lists atomic.Int32
		arrived := unstructuredKarta(newKarta("arrived-while-disconnected", jobGVK))
		relisting, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		client.PrependReactor("list", "kartas", func(ktesting.Action) (bool, runtime.Object, error) {
			if lists.Add(1) == 1 {
				return true, &unstructured.UnstructuredList{}, nil
			}
			close(relisting)
			<-release
			return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*arrived}}, nil
		})
		var watches atomic.Int32
		client.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) {
			if watches.Add(1) == 1 {
				return true, watch.NewStreamWatcher(decoder, streamErrorReporter{}), nil
			}
			return true, watch.NewRaceFreeFake(), nil
		})
		inventory, err := New(client, withCatalog)
		Expect(err).NotTo(HaveOccurred())
		start(inventory)
		DeferCleanup(unblock)
		DeferCleanup(decoder.Close)
		Eventually(inventory.Ready, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		Eventually(watches.Load, eventuallyTimeout).Should(Equal(int32(1)))
		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))

		decoder.Close()

		Eventually(relisting, eventuallyTimeout).Should(BeClosed())
		Expect(inventory.Ready()).To(MatchError(ErrUnavailable))
		_, err = inventory.Lookup(jobGVK)
		Expect(err).To(MatchError(ErrUnavailable))
		Expect(watches.Load()).To(Equal(int32(1)), "a resumed watch cannot prove the current absence of cluster definitions")
		unblock()
		Eventually(func(g Gomega) {
			sel, err := inventory.Lookup(jobGVK)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(sel.Origin.Name).To(Equal("arrived-while-disconnected"))
		}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
	})
})
