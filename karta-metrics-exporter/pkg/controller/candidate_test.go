// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

var _ = Describe("Root candidate admission", func() {
	It("discovers a generic parent from a selected child with no Pods", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		child, parent := testObject(testRootGVK, "child"), testObject(testRootGVK, "parent")
		ownedBy(child, parent)
		seedWatch(r, testRootGVK, "jobs", child, parent)
		Expect(r.inputQueue.Len()).To(Equal(2))
		Expect(r.rootQueue.Len()).To(BeZero())
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(child)), UID: child.GetUID()})).To(Succeed())
		Expect(r.roots).NotTo(HaveKey(child.GetUID()))
		Expect(r.roots).To(HaveKey(parent.GetUID()))
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(parent)), UID: parent.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, parent.GetUID())).To(Succeed())
		Expect(publications.Published(parent.GetUID())).NotTo(BeNil())
		Expect(publications.Published(parent.GetUID()).PodPages).To(BeEmpty())
	})
	It("starts full coverage for an unselected generic ancestor discovered without Pods", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		ancestorGVK := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Batch"}
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).AddSpecific(ancestorGVK, ancestorGVK.GroupVersion().WithResource("batches"), ancestorGVK.GroupVersion().WithResource("batch"), meta.RESTScopeNamespace)
		child, ancestor := testObject(testRootGVK, "child"), testObject(ancestorGVK, "ancestor")
		ownedBy(child, ancestor)
		seedWatch(r, testRootGVK, "jobs", child)
		metadata.PrependReactor("get", "batches", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: ancestor.GetAPIVersion(), Kind: ancestor.GetKind()}, ObjectMeta: metav1.ObjectMeta{Name: ancestor.GetName(), Namespace: ancestor.GetNamespace(), UID: ancestor.GetUID(), ResourceVersion: ancestor.GetResourceVersion()}}, nil
		})
		resource := ancestorGVK.GroupVersion().WithResource("batches")
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{resource: "BatchList"})
		allowList := make(chan struct{})
		client.PrependReactor("list", "batches", func(clienttesting.Action) (bool, kruntime.Object, error) {
			<-allowList
			return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*ancestor}}, nil
		})
		client.PrependWatchReactor("batches", func(action clienttesting.Action) (bool, watch.Interface, error) {
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			if options.SendInitialEvents != nil && *options.SendInitialEvents {
				return true, nil, apierrors.NewBadRequest("watch-list unavailable")
			}
			return true, watch.NewRaceFreeFake(), nil
		})
		r.reader.clients.Dynamic = client
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(child)), UID: child.GetUID()})).To(Succeed())
		close(allowList)
		r.mu.Lock()
		source := r.watches[watchKey{Resource: resource}]
		r.mu.Unlock()
		Expect(source).NotTo(BeNil())
		DeferCleanup(func() { source.cancel(); r.wg.Wait() })
		Eventually(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return source.ready }).Should(BeTrue())
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(ancestor)), UID: ancestor.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, ancestor.GetUID())).To(Succeed())
		Expect(publications.Published(ancestor.GetUID())).NotTo(BeNil())
		Expect(publications.Published(ancestor.GetUID()).Workload.HasStatus).To(BeFalse())
		Expect(queuedObjects(r)).To(ConsistOf(child.GetUID(), ancestor.GetUID()))
	})
	It("retries a missing parent and suppresses intermediate status-only work after admission", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		child, parent := testObject(testRootGVK, "child"), testObject(testRootGVK, "parent")
		ownedBy(child, parent)
		w := seedWatch(r, testRootGVK, "jobs", child)
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewNotFound(testRootGVK.GroupVersion().WithResource("jobs").GroupResource(), "parent")
		})
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(child)), UID: child.GetUID()})).To(HaveOccurred())
		Expect(r.candidates[child.GetUID()][keyOf(refOf(child))].dirty).To(BeTrue())
		r.acceptEvent(w, parent, false)
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(child)), UID: child.GetUID()})).To(Succeed())
		Expect(r.candidates[child.GetUID()][keyOf(refOf(child))].dirty).To(BeFalse())
		drainQueue(r.rootQueue)
		drainQueue(r.inputQueue)
		next := child.DeepCopy()
		next.SetResourceVersion("2")
		next.Object["status"] = map[string]any{"available": true}
		r.acceptEvent(w, next, false)
		Expect(r.rootQueue.Len()).To(BeZero())
		Expect(r.inputQueue.Len()).To(BeZero())
		Expect(r.reader.objects[keyOf(refOf(child))].object.GetResourceVersion()).To(Equal("2"))
	})
	It("repairs a candidate after an intermediate-only owner change", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		child, middle, a, b := testObject(testRootGVK, "child"), testObject(testRootGVK, "middle"), testObject(testRootGVK, "a"), testObject(testRootGVK, "b")
		ownedBy(child, middle)
		ownedBy(middle, a)
		w := seedWatch(r, testRootGVK, "jobs", child, middle, a, b)
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(child)), UID: child.GetUID()})).To(Succeed())
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(a)), UID: a.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
		Expect(publications.Published(a.GetUID())).NotTo(BeNil())
		drainQueue(r.rootQueue)
		next := middle.DeepCopy()
		next.SetResourceVersion("2")
		ownedBy(next, b)
		r.acceptEvent(w, next, false)
		Expect(r.candidates[child.GetUID()][keyOf(refOf(child))].dirty).To(BeTrue())
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(child)), UID: child.GetUID()})).To(Succeed())
		candidate := r.candidates[child.GetUID()][keyOf(refOf(child))]
		Expect(candidate.path[len(candidate.path)-1].UID).To(Equal(b.GetUID()))
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(b)), UID: b.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, b.GetUID())).To(Succeed())
		Expect(publications.Published(b.GetUID())).NotTo(BeNil())
		Expect(queuedObjects(r)).To(ConsistOf(child.GetUID(), middle.GetUID(), a.GetUID(), b.GetUID()))
	})
	It("releases retired candidate paths while preserving the migrated UID publication", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		oldObject, liveObject := testObject(testRootGVK, "root"), testObject(v2, "root")
		old := seedWatch(r, testRootGVK, "jobs", oldObject)
		live := seedWatch(r, v2, "jobs", liveObject)
		state := seedRoot(r, liveObject)
		Expect(r.reconcile(ctx, liveObject.GetUID())).To(Succeed())
		middle := discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "middle", UID: "middle"}
		parent := discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "parent", UID: "parent"}
		oldCandidate := r.candidates[oldObject.GetUID()][keyOf(refOf(oldObject))]
		liveCandidate := r.candidates[liveObject.GetUID()][keyOf(refOf(liveObject))]
		r.setCandidatePathLocked(oldCandidate, []discovery.ObjectRef{refOf(oldObject), middle, parent})
		r.setCandidatePathLocked(liveCandidate, []discovery.ObjectRef{refOf(liveObject), middle, parent})
		r.mu.Lock()
		delete(r.watches, old.key)
		r.reader.mu.Lock()
		r.retireWatchObjectsLocked(old)
		r.reader.mu.Unlock()
		r.mu.Unlock()
		Expect(r.candidates[liveObject.GetUID()]).To(HaveLen(1))
		Expect(r.candidates[liveObject.GetUID()][keyOf(refOf(liveObject))]).To(BeIdenticalTo(liveCandidate))
		Expect(r.pathDependents[keyOf(middle)]).To(HaveLen(1))
		Expect(state.watch).To(Equal(live.key))
		Expect(publications.Published(liveObject.GetUID())).NotTo(BeNil())
		valid, _ := publications.Capture().Valid(publications.Published(liveObject.GetUID()).Creation)
		Expect(valid).To(BeTrue())
	})
	It("keeps dependency registrations distinct across candidate API representations", func() {
		r, _, _ := testRuntime(nil)
		a := discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "input", UID: "input"}
		b := a
		b.GVK.Version = "v2"
		middle := discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "middle", UID: "middle"}
		root := discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "root", UID: "root"}
		ca, cb := &rootCandidate{ref: a}, &rootCandidate{ref: b}
		r.setCandidatePathLocked(ca, []discovery.ObjectRef{a, middle, root})
		r.setCandidatePathLocked(cb, []discovery.ObjectRef{b, middle, root})
		Expect(r.pathDependents[keyOf(middle)]).To(HaveLen(2))
		r.setCandidatePathLocked(ca, nil)
		Expect(r.pathDependents[keyOf(middle)]).To(HaveLen(1))
	})
})

var _ = Describe("WatchList accepted initialization", func() {
	DescribeTable("accepts initialization only at the complete initial bookmark", func(ctx SpecContext, empty bool) {
		r, _, _ := testRuntime(nil)
		resource := testRootGVK.GroupVersion().WithResource("jobs")
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{resource: "JobList"})
		streams := make(chan *watch.RaceFreeFakeWatcher, 1)
		client.PrependWatchReactor("jobs", func(action clienttesting.Action) (bool, watch.Interface, error) {
			options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
			if options.SendInitialEvents == nil || !*options.SendInitialEvents {
				return true, nil, apierrors.NewBadRequest("expected watch-list")
			}
			stream := watch.NewRaceFreeFake()
			streams <- stream
			return true, stream, nil
		})
		r.reader.clients.Dynamic = client
		key, err := r.ensureWatch(ctx, testRootGVK, "", true)
		Expect(err).NotTo(HaveOccurred())
		w := r.watches[key]
		DeferCleanup(func() { w.cancel(); r.wg.Wait() })
		var stream *watch.RaceFreeFakeWatcher
		Eventually(streams).Should(Receive(&stream))
		if !empty {
			stream.Add(testObject(testRootGVK, "root"))
		}
		Consistently(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return w.ready }, 20*time.Millisecond).Should(BeFalse())
		bookmark := &unstructured.Unstructured{}
		bookmark.SetResourceVersion("10")
		bookmark.SetAnnotations(map[string]string{metav1.InitialEventsAnnotationKey: "true"})
		stream.Action(watch.Bookmark, bookmark)
		Eventually(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return w.ready }).Should(BeTrue())
		r.reader.mu.RLock()
		count := len(r.reader.objects)
		r.reader.mu.RUnlock()
		if empty {
			Expect(count).To(BeZero())
		} else {
			Expect(count).To(Equal(1))
		}
	}, Entry("with objects", false), Entry("empty snapshot", true))
})
