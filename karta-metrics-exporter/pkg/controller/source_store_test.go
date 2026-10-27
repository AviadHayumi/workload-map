// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
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

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

func replacement(r *Runtime, w *resourceWatch, objects ...*unstructured.Unstructured) (*reflectorStore, []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reader.mu.RLock()
	defer r.reader.mu.RUnlock()
	w.replacement = &sourceRead{sequence: r.reader.sequence, generation: w.generation, failureEpoch: w.failureEpoch, started: r.config.Now()}
	values := make([]any, 0, len(objects))
	for _, object := range objects {
		values = append(values, &sourceObject{Unstructured: object, sequence: r.reader.sequence, failureEpoch: w.failureEpoch, expiresAt: r.config.Now().Add(r.config.MaxAge)})
	}
	return &reflectorStore{runtime: r, source: w}, values
}

var _ = Describe("Direct accepted-object source store", func() {
	It("preserves a fresh replacement's absence fence when an old LIST starts after cancellation", func(ctx SpecContext) {
		r, _, _ := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		w := seedWatch(r, testRootGVK, "jobs", root)
		adapter, values := replacement(r, w)
		fresh := w.replacement
		scope := watchReadScope{gvk: w.gvk, namespace: w.key.Namespace}
		read := r.reader.watchReads[scope]
		r.reader.sequence++
		key := keyOf(refOf(root))
		read.invalidated[key] = ownershipInvalidation{sequence: r.reader.sequence}
		sequence := r.sequence
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{w.key.Resource: "JobList"})
		r.reader.clients.Dynamic = client
		old, cancel := context.WithCancel(ctx)
		cancel()
		_, err := r.listWatch(old, w, metav1.ListOptions{})
		Expect(err).To(MatchError(context.Canceled))
		Expect(w.replacement).To(BeIdenticalTo(fresh))
		Expect(r.sequence).To(Equal(sequence))
		Expect(r.reader.watchReads[scope]).To(BeIdenticalTo(read))
		Expect(read.readers).To(BeZero())
		Expect(read.invalidated[key].sequence).To(Equal(r.reader.sequence))
		Expect(client.Actions()).To(BeEmpty())
		Expect(adapter.Replace(values, "2")).To(MatchError(discovery.ErrStale))
		Expect(r.reader.objects[key].object.GetUID()).To(Equal(root.GetUID()))
	})
	It("replaces a complete set and removes missing UIDs without reviving predecessor deletes", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		a, b := testObject(testRootGVK, "a"), testObject(testRootGVK, "b")
		w := seedWatch(r, testRootGVK, "jobs", a, b)
		seedRoot(r, a)
		seedRoot(r, b)
		Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
		next := b.DeepCopy()
		next.SetUID("replacement")
		next.SetResourceVersion("2")
		adapter, values := replacement(r, w, next)
		Expect(adapter.Replace(values, "2")).To(Succeed())
		Expect(w.objects).To(HaveLen(1))
		Expect(publications.Published(a.GetUID())).To(BeNil())
		Expect(r.reader.objects[keyOf(refOf(b))].object.GetUID()).To(Equal(next.GetUID()))
		Expect(adapter.Delete(&sourceObject{Unstructured: b, sequence: r.reader.sequence, failureEpoch: w.failureEpoch, expiresAt: time.Now().Add(time.Minute)})).To(Succeed())
		Expect(r.reader.objects[keyOf(refOf(b))].object.GetUID()).To(Equal(next.GetUID()))
		adapter, values = replacement(r, w)
		Expect(adapter.Replace(values, "3")).To(Succeed())
		Expect(w.objects).To(BeEmpty())
		Expect(w.ready).To(BeTrue())
	})
	DescribeTable("rejects an entire superseded replacement", func(ctx SpecContext, changed string) {
		r, _, _ := testRuntime(nil)
		a, b := testObject(testRootGVK, "a"), testObject(testRootGVK, "b")
		w := seedWatch(r, testRootGVK, "jobs", a, b)
		next := a.DeepCopy()
		next.SetResourceVersion("2")
		adapter, values := replacement(r, w, next)
		switch changed {
		case "malformed":
			values = append(values, "invalid")
		case "expired":
			w.replacement.started = time.Now().Add(-2 * r.config.MaxAge)
		case "failure":
			r.mu.Lock()
			r.failWatchLocked(w, errors.New("source failed"))
			r.mu.Unlock()
		case "missing alias":
			r.reader.mu.Lock()
			r.reader.sequence++
			r.reader.watchReads[watchReadScope{gvk: w.gvk, namespace: w.key.Namespace}].invalidated[keyOf(refOf(b))] = ownershipInvalidation{sequence: r.reader.sequence}
			r.reader.mu.Unlock()
		}
		Expect(adapter.Replace(values, "2")).To(MatchError(discovery.ErrStale))
		Expect(r.reader.objects[keyOf(refOf(a))].object.GetResourceVersion()).To(Equal("1"))
		Expect(r.reader.objects[keyOf(refOf(b))].object.GetUID()).To(Equal(b.GetUID()))
		Expect(w.relist).To(BeTrue())
		Expect(r.verifyWatch(ctx, w.key)).To(MatchError(discovery.ErrPending))
	}, Entry("malformed item", "malformed"), Entry("read-start expiry", "expired"), Entry("failure epoch", "failure"), Entry("fence for an absent item", "missing alias"))
	It("makes callbacks from retired sources harmless", func() {
		r, _, _ := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		old := seedWatch(r, testRootGVK, "jobs", root)
		adapter, values := replacement(r, old)
		current := seedWatch(r, testRootGVK, "jobs", root.DeepCopy())
		Expect(adapter.Replace(values, "2")).To(Succeed())
		Expect(adapter.Delete(&sourceObject{Unstructured: root})).To(Succeed())
		Expect(r.watches[old.key]).To(BeIdenticalTo(current))
		Expect(r.reader.objects).To(HaveLen(1))
		Expect(old.relist).To(BeFalse())
		fresh := old.replacement
		sequence := r.sequence
		_, err := r.listWatch(context.Background(), old, metav1.ListOptions{})
		Expect(err).To(MatchError(discovery.ErrStale))
		Expect(old.replacement).To(BeIdenticalTo(fresh))
		Expect(r.sequence).To(Equal(sequence))
		Expect(r.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).Actions()).To(BeEmpty())
	})
	DescribeTable("requests current state only on the first recovery LIST page", func(ctx SpecContext, recovery bool, continuation, expectedVersion string) {
		r, _, _ := testRuntime(nil)
		resource := testRootGVK.GroupVersion().WithResource("jobs")
		w := &resourceWatch{key: watchKey{Resource: resource, Namespace: "ns"}, gvk: testRootGVK, relist: recovery}
		r.watches[w.key] = w
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{resource: "JobList"})
		r.reader.clients.Dynamic = client
		options := metav1.ListOptions{ResourceVersion: "0", ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan, Continue: continuation, Limit: 50, LabelSelector: "app=job"}
		_, err := r.listWatch(ctx, w, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(client.Actions()).To(HaveLen(1))
		actual := client.Actions()[0].(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
		Expect(actual.ResourceVersion).To(Equal(expectedVersion))
		if recovery && continuation == "" {
			Expect(actual.ResourceVersionMatch).To(BeEmpty())
		} else {
			Expect(actual.ResourceVersionMatch).To(Equal(options.ResourceVersionMatch))
		}
		Expect(actual.Continue).To(Equal(continuation))
		Expect(actual.Limit).To(Equal(options.Limit))
		Expect(actual.LabelSelector).To(Equal(options.LabelSelector))
	}, Entry("startup", false, "", "0"), Entry("recovery first page", true, "", ""), Entry("recovery continuation", true, "next-page", "0"))
	It("recovers the full body after a rejected event with no later object mutation", func(ctx SpecContext) {
		r, _, _ := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		next := root.DeepCopy()
		next.SetResourceVersion("2")
		next.Object["status"] = map[string]any{"complete": true}
		resource := testRootGVK.GroupVersion().WithResource("jobs")
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{resource: "JobList"})
		var reads atomic.Int32
		requests := make(chan metav1.ListOptions, 8)
		entered, resume := make(chan struct{}), make(chan struct{})
		client.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			requests <- action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			object := root
			count := reads.Add(1)
			if count > 1 {
				if count == 2 {
					close(entered)
				}
				<-resume
				object = next
			}
			return true, &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "batch/v1", "kind": "JobList", "metadata": map[string]any{"resourceVersion": object.GetResourceVersion()}}, Items: []unstructured.Unstructured{*object}}, nil
		})
		client.PrependWatchReactor("jobs", func(action clienttesting.Action) (bool, watch.Interface, error) {
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			if options.SendInitialEvents != nil && *options.SendInitialEvents {
				return true, nil, apierrors.NewBadRequest("watch-list unavailable")
			}
			return true, watch.NewRaceFreeFake(), nil
		})
		r.reader.clients.Dynamic = client
		key, err := r.ensureWatch(ctx, testRootGVK, "ns", true)
		Expect(err).NotTo(HaveOccurred())
		w := r.watches[key]
		DeferCleanup(func() { w.cancel(); r.wg.Wait() })
		Eventually(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return w.ready }).Should(BeTrue())
		adapter := &reflectorStore{runtime: r, source: w}
		Expect(adapter.Update(&sourceObject{Unstructured: next, expiresAt: time.Now().Add(-time.Second)})).To(MatchError(discovery.ErrStale))
		Expect(r.maintainSource(ctx, sourceKey{Operation: restartSource, Watch: key})).To(Succeed())
		Eventually(entered).Should(BeClosed())
		Expect(r.verifyWatch(ctx, key)).To(MatchError(discovery.ErrPending))
		close(resume)
		Eventually(func() string {
			r.reader.mu.RLock()
			defer r.reader.mu.RUnlock()
			return r.reader.objects[keyOf(refOf(root))].object.GetResourceVersion()
		}).Should(Equal("2"))
		r.reader.mu.RLock()
		body := r.reader.objects[keyOf(refOf(root))].object.DeepCopy()
		r.reader.mu.RUnlock()
		Expect(body.Object["status"]).To(Equal(next.Object["status"]))
		Eventually(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return !w.relist && w.err == nil }).Should(BeTrue())
		var initial, recovery metav1.ListOptions
		Expect(requests).To(Receive(&initial))
		Expect(initial.ResourceVersion).To(Equal("0"))
		Expect(requests).To(Receive(&recovery))
		Expect(recovery.ResourceVersion).To(BeEmpty())
		Expect(recovery.ResourceVersionMatch).To(BeEmpty())
	})
})
