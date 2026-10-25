// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var _ = Describe("Watch recovery scheduling", func() {
	DescribeTable("preserves only recoverable watch negotiation proof", func(ctx SpecContext, initial bool, cause error, failed bool) {
		r, _, publications := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		w := seedWatch(r, testRootGVK, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.rootQueue)
		drainQueue(r.controlQueue)
		client := dynamicfake.NewSimpleDynamicClient(kruntime.NewScheme())
		client.PrependWatchReactor("jobs", func(clienttesting.Action) (bool, watch.Interface, error) { return true, nil, cause })
		r.reader.clients.Dynamic = client
		_, err := r.watchResource(ctx, w, metav1.ListOptions{SendInitialEvents: &initial})
		Expect(err).To(MatchError(cause))
		view := publications.Capture()
		valid, _ := view.Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(Equal(!failed))
		if failed {
			Expect(w.err).To(MatchError(cause))
			Expect(r.rootQueue.Len()).To(Equal(1))
		} else {
			Expect(w.err).NotTo(HaveOccurred())
			Expect(r.rootQueue.Len()).To(BeZero())
			Expect(r.controlQueue.Len()).To(Equal(1))
		}
	}, Entry("unsupported optional WatchList", true, apierrors.NewBadRequest("unsupported"), false),
		Entry("ordinary invalid watch request", false, apierrors.NewBadRequest("invalid"), true),
		Entry("optional watch denied", true, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "", errors.New("denied")), true),
		Entry("optional watch transport failure", true, errors.New("connection failed"), true),
		Entry("expired HTTP watch cursor", false, apierrors.NewResourceExpired("expired"), false))
	It("limits a real Pod source failure to roots containing its Pods", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		a, b, empty := testObject(testRootGVK, "a"), testObject(testRootGVK, "b"), testObject(testRootGVK, "empty")
		a.SetNamespace("team-a")
		b.SetNamespace("team-b")
		pa, pb := testObject(podGVK, "pa"), testObject(podGVK, "pb")
		pa.SetNamespace("team-a")
		pb.SetNamespace("team-b")
		seedWatch(r, testRootGVK, "jobs", a, b, empty)
		addWatch := func(pod *unstructured.Unstructured) *resourceWatch {
			key := watchKey{Resource: podGVK.GroupVersion().WithResource("pods"), Namespace: pod.GetNamespace()}
			w := &resourceWatch{key: key, gvk: podGVK, objects: map[objectKey]struct{}{}, ready: true, discover: true, failureKey: "watch:pods/" + pod.GetNamespace(), verifiedAt: time.Now()}
			r.watches[key] = w
			r.reader.activeKeys[w.failureKey] = true
			r.acceptEvent(w, pod, false)
			return w
		}
		failed := addWatch(pa)
		addWatch(pb)
		seedRoot(r, a, pa)
		seedRoot(r, b, pb)
		seedRoot(r, empty)
		for _, root := range []*unstructured.Unstructured{a, b, empty} {
			Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		}
		drainQueue(r.rootQueue)
		r.mu.Lock()
		r.failWatchLocked(failed, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied")))
		r.mu.Unlock()
		Expect(r.rootQueue.Len()).To(Equal(1))
		key, _ := r.rootQueue.Get()
		Expect(key.UID).To(Equal(a.GetUID()))
		r.rootQueue.Done(key)
		view := publications.Capture()
		for _, record := range view.Records {
			valid, _ := view.Valid(record.Creation)
			Expect(valid).To(BeTrue())
		}
		row, _ := publications.Published(a.GetUID()).Pod(pa.GetUID())
		valid, _ := view.Valid(row.Mapping)
		Expect(valid).To(BeFalse())
		row, _ = publications.Published(b.GetUID()).Pod(pb.GetUID())
		valid, _ = view.Valid(row.Mapping)
		Expect(valid).To(BeTrue())
	})
	It("does not enqueue independent roots when an expired empty Pod watch falls back from WatchList", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		r.ctx = ctx
		podResource := podGVK.GroupVersion().WithResource("pods")
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{podResource: "PodList"})
		client.PrependReactor("list", "pods", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "PodList", "metadata": map[string]any{"resourceVersion": "10"}}}, nil
		})
		streams := make(chan *watch.RaceFreeFakeWatcher, 4)
		var optionalAttempts atomic.Int32
		client.PrependWatchReactor("pods", func(action clienttesting.Action) (bool, watch.Interface, error) {
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			if options.SendInitialEvents != nil && *options.SendInitialEvents {
				optionalAttempts.Add(1)
				return true, nil, apierrors.NewBadRequest("watch list unavailable")
			}
			stream := watch.NewRaceFreeFake()
			streams <- stream
			return true, stream, nil
		})
		r.reader.clients.Dynamic = client
		key, err := r.ensureWatch(ctx, podGVK, "", true)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { r.watches[key].cancel(); r.wg.Wait() })
		var stream *watch.RaceFreeFakeWatcher
		Eventually(streams).Should(Receive(&stream))
		Eventually(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.watches[key].ready }).Should(BeTrue())
		roots := make([]*unstructured.Unstructured, 100)
		for i := range roots {
			roots[i] = testObject(testRootGVK, fmt.Sprintf("root-%d", i))
		}
		seedWatch(r, testRootGVK, "jobs", roots...)
		for _, root := range roots {
			seedRoot(r, root)
			Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		}
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		drainQueue(r.controlQueue)
		r.mu.Lock()
		epoch, proof := r.watches[key].failureEpoch, r.watches[key].failureKey
		r.mu.Unlock()
		stream.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410})
		stream.Stop()
		Eventually(streams, 10*time.Second).Should(Receive(&stream))
		Expect(optionalAttempts.Load()).To(BeNumerically(">=", 2))
		r.mu.Lock()
		Expect(r.watches[key].failureEpoch).To(Equal(epoch))
		Expect(r.watches[key].failureKey).To(Equal(proof))
		r.mu.Unlock()
		Expect(r.rootQueue.Len()).To(BeZero())
		Expect(r.queue.Len()).To(BeZero())
		view := publications.Capture()
		for _, record := range view.Records {
			valid, _ := view.Valid(record.Creation)
			Expect(valid).To(BeTrue())
		}
	})
})
