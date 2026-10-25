// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

func conflictingMetadata() *metav1.PartialObjectMetadata {
	root, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
	ownedBy(root, parent)
	object := ownerMetadata("root", "2")
	object.SetOwnerReferences(root.GetOwnerReferences())
	return object
}

var _ = Describe("Ownership proof retirement", func() {
	DescribeTable("does not downgrade newer verified objects with buffered source callbacks", func(ctx SpecContext, operation string, replacement bool) {
		r, metadata, _ := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		root := testObject(v2, "root")
		w := seedWatch(r, v2, "jobs", root)
		source := watch.NewRaceFreeFake()
		stream := r.monitorWatch(ctx, w, source)
		DeferCleanup(stream.Stop)
		switch operation {
		case "add":
			source.Add(root.DeepCopy())
		case "update":
			source.Modify(root.DeepCopy())
		case "delete":
			source.Delete(root.DeepCopy())
		}
		var event watch.Event
		Eventually(stream.ResultChan()).Should(Receive(&event))
		old := event.Object.(*sourceObject).DeepCopyObject().(*sourceObject)
		Expect(old.Object).To(Equal(root.Object))
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		r.reader.mu.Lock()
		r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: time.Now().Add(time.Minute), failureKey: "contradiction"})
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		r.reader.mu.Unlock()
		current := root.DeepCopy()
		current.SetResourceVersion("2")
		if replacement {
			current.SetUID("replacement")
		}
		r.acceptEvent(w, current, false)
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			object := ownerMetadata("root", "2")
			object.UID = current.GetUID()
			return true, metadataList(object), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		r.acceptEvent(w, old, operation == "delete")
		observed, found := r.reader.Cached(refOf(current))
		Expect(found).To(BeTrue())
		Expect(observed.GetUID()).To(Equal(current.GetUID()))
		_, err := r.reader.evidence(refOf(current))
		Expect(err).NotTo(HaveOccurred())
	}, Entry("Add", "add", false), Entry("Update", "update", false), Entry("Delete", "delete", false), Entry("replacement UID Add", "add", true), Entry("replacement UID Delete", "delete", true))
	It("keeps old callbacks ineligible after their bounded fence expires", func(ctx SpecContext) {
		r, _, _ := testRuntime(nil)
		now := time.Now()
		r.config.Now = func() time.Time { return now }
		r.reader.watchNow = r.config.Now
		v2 := testRootGVK
		v2.Version = "v2"
		root := testObject(v2, "root")
		w := seedWatch(r, v2, "jobs")
		dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{v2.GroupVersion().WithResource("jobs"): "JobList"})
		r.reader.clients.Dynamic = dynamic
		dynamic.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*root}}, nil
		})
		result, err := r.listWatch(ctx, w, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		r.reader.mu.Lock()
		r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: now.Add(time.Minute), failureKey: "contradiction"})
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		r.reader.mu.Unlock()
		now = now.Add(r.config.MaxAge + time.Second)
		r.reader.expireMetadata()
		Expect(r.reader.watchReads[watchReadScope{gvk: v2}].invalidated).To(BeEmpty())
		r.acceptEvent(w, result.(*metav1.List).Items[0].Object, false)
		_, err = r.reader.evidence(refOf(root))
		Expect(err).To(MatchError(discovery.ErrPending))
	})
	It("ignores callbacks from a retired watcher after its scope is recreated", func(ctx SpecContext) {
		r, _, _ := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		old := seedWatch(r, testRootGVK, "jobs", root)
		source := watch.NewRaceFreeFake()
		stream := r.monitorWatch(ctx, old, source)
		DeferCleanup(stream.Stop)
		source.Add(root.DeepCopy())
		var event watch.Event
		Eventually(stream.ResultChan()).Should(Receive(&event))
		r.mu.Lock()
		r.reader.mu.Lock()
		delete(r.watches, old.key)
		r.retireWatchObjectsLocked(old)
		r.reader.mu.Unlock()
		r.mu.Unlock()
		current := root.DeepCopy()
		current.SetUID("replacement")
		seedWatch(r, testRootGVK, "jobs", current)
		r.acceptEvent(old, event.Object, false)
		_, found := r.reader.Cached(refOf(current))
		Expect(found).To(BeTrue())
		_, err := r.reader.evidence(refOf(current))
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("keeps the alias fence between a returned LIST and its delayed Add callback", func(ctx SpecContext, before string) {
		r, metadata, publications := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		root, pod := testObject(v2, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		w := seedWatch(r, v2, "jobs")
		seedWatch(r, podGVK, "pods", pod)
		dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{v2.GroupVersion().WithResource("jobs"): "JobList"})
		r.reader.clients.Dynamic = dynamic
		dynamic.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*root}}, nil
		})
		result, err := r.listWatch(ctx, w, metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		r.reader.mu.Lock()
		r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: time.Now().Add(time.Minute), failureKey: "contradiction"})
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		r.reader.mu.Unlock()
		if before != "none" {
			metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
				if before == "absent" {
					return true, metadataList(), nil
				}
				return true, metadataList(ownerMetadata("root", "1")), nil
			})
			Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		}
		list := result.(*metav1.List)
		r.acceptEvent(w, list.Items[0].Object, false)
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(MatchError(discovery.ErrStale))
		Expect(publications.Capture().Records).To(BeEmpty())
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		valid, _ := publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeTrue())
		scope := watchReadScope{gvk: v2, namespace: w.key.Namespace}
		Expect(r.reader.watchReads[scope].invalidated).To(HaveLen(1))
		r.mu.Lock()
		r.reader.mu.Lock()
		delete(r.watches, w.key)
		r.retireWatchObjectsLocked(w)
		r.reader.mu.Unlock()
		r.mu.Unlock()
		Expect(r.reader.watchReads).NotTo(HaveKey(scope))
	}, Entry("before any verification", "none"), Entry("after fresh matching verification", "matching"), Entry("after fresh absence verification", "absent"))

	DescribeTable("keeps independent published roots valid", func(ctx SpecContext, mode string) {
		r, metadata, publications := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		root := testObject(v2, "root")
		seedWatch(r, v2, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		target := testRootGVK
		object := conflictingMetadata()
		switch mode {
		case "ignored":
			r.reader.ignoredOwners = []schema.GroupVersionKind{testRootGVK}
		case "unrelated":
			object.Name = "other"
			object.UID = "other"
		case "same-version":
			target = v2
		}
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, object, nil })
		_, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: target, Namespace: "ns", Name: object.Name, UID: object.UID})
		Expect(err).NotTo(HaveOccurred())
		valid, _ := publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeTrue())
	}, Entry("when the owner kind is ignored", "ignored"), Entry("when metadata names another object", "unrelated"), Entry("when a full object takes precedence at the same GVK", "same-version"))
	It("fences a pending-only prime when a conflicting representation retires", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		root, pod := testObject(v2, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, podGVK, "pods", pod)
		entered, resume := make(chan struct{}), make(chan struct{})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			metadata.Unlock()
			close(entered)
			<-resume
			metadata.Lock()
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		done := make(chan error, 1)
		go func() { done <- r.primeMetadata(ctx) }()
		Eventually(entered).Should(BeClosed())
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		r.reader.mu.Lock()
		r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: time.Now().Add(time.Minute), failureKey: "contradiction"})
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		r.reader.mu.Unlock()
		close(resume)
		Eventually(done).Should(Receive(Succeed()))
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.reader.priming).To(BeEmpty())
		Expect(r.reader.versions).NotTo(HaveKey(testRootGVK.GroupKind()))
	})
	DescribeTable("rejects informer LIST results predating alias retirement", func(ctx SpecContext, cached bool) {
		r, _, _ := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{v2.GroupVersion().WithResource("jobs"): "JobList"})
		r.reader.clients.Dynamic = dynamic
		root := testObject(v2, "root")
		w := seedWatch(r, v2, "jobs")
		if cached {
			r.acceptEvent(w, root, false)
		}
		entered, resume := make(chan struct{}), make(chan struct{})
		dynamic.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			dynamic.Unlock()
			close(entered)
			<-resume
			dynamic.Lock()
			return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*root}}, nil
		})
		done := make(chan error, 1)
		go func() { _, err := r.listWatch(ctx, w, metav1.ListOptions{}); done <- err }()
		Eventually(entered).Should(BeClosed())
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		r.reader.mu.Lock()
		r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: time.Now().Add(time.Minute), failureKey: "contradiction"})
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		r.reader.mu.Unlock()
		close(resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(r.reader.watchReads).To(HaveLen(1))
		if cached {
			r.acceptEvent(w, root.DeepCopy(), false)
			_, err := r.reader.evidence(refOf(root))
			Expect(err).To(MatchError(discovery.ErrPending))
		}
	}, Entry("with an existing alias and same-RV callback", true), Entry("with no cached alias yet", false))

	It("invalidates a published alias immediately when GET accepts a parent", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		root, pod := testObject(v2, "root"), testObject(podGVK, "pod")
		seedWatch(r, v2, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		record := publications.Published(root.GetUID())
		valid, _ := publications.Capture().Valid(record.Creation)
		Expect(valid).To(BeTrue())
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, conflictingMetadata(), nil })
		_, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "root", UID: "root"})
		Expect(err).NotTo(HaveOccurred())
		capture := publications.Capture()
		valid, _ = capture.Valid(record.Creation)
		Expect(valid).To(BeFalse())
		row, found := record.Pod(pod.GetUID())
		Expect(found).To(BeTrue())
		valid, _ = capture.Valid(row.Mapping)
		Expect(valid).To(BeFalse())
	})
	DescribeTable("cannot renew aliases from a LIST started before contradiction retirement", func(ctx SpecContext, metadataOnly bool) {
		r, metadata, _ := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		alias := testObject(v2, "root")
		w := seedWatch(r, v2, "jobs", alias)
		var old *resourceWatch
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		if metadataOnly {
			r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: time.Now().Add(time.Minute), failureKey: "contradiction"})
			r.reader.activeKeys["contradiction"] = true
		} else {
			root, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
			ownedBy(root, parent)
			old = seedWatch(r, testRootGVK, "jobs", root)
		}
		entered, resume := make(chan struct{}), make(chan struct{})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			metadata.Unlock()
			close(entered)
			<-resume
			metadata.Lock()
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		done := make(chan error, 1)
		go func() { done <- r.verifyWatch(ctx, w.key) }()
		Eventually(entered).Should(BeClosed())
		r.mu.Lock()
		r.reader.mu.Lock()
		if metadataOnly {
			r.reader.removeMetadataLocked(key, "unresolved_owner")
		} else {
			delete(r.watches, old.key)
			r.retireWatchObjectsLocked(old)
		}
		r.reader.mu.Unlock()
		r.mu.Unlock()
		close(resume)
		Eventually(done).Should(Receive(Succeed()))
		_, err := r.reader.evidence(refOf(alias))
		Expect(err).To(MatchError(discovery.ErrPending))
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		_, err = r.reader.evidence(refOf(alias))
		Expect(err).NotTo(HaveOccurred())
	}, Entry("watch source with no root state", false), Entry("metadata source with no root state", true))
	It("rejects a paused publication even after contradictory metadata is pruned", func(ctx SpecContext) {
		paused := &pausedLabels{entered: make(chan types.UID, 1), resume: make(chan struct{})}
		r, _, publications := testRuntime(paused)
		v2 := testRootGVK
		v2.Version = "v2"
		root, pod := testObject(v2, "root"), testObject(podGVK, "pod")
		seedWatch(r, v2, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		seedRoot(r, root, pod)
		done := make(chan error, 1)
		go func() { done <- r.reconcile(ctx, root.GetUID()) }()
		Eventually(paused.entered).Should(Receive())
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		r.reader.mu.Lock()
		r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: time.Now().Add(time.Minute), failureKey: "contradiction"})
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		r.reader.mu.Unlock()
		close(paused.resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(publications.Published(root.GetUID())).To(BeNil())
	})
	It("fences a pending-only GET when a conflicting representation retires", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		entered, resume := make(chan struct{}), make(chan struct{})
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			metadata.Unlock()
			close(entered)
			<-resume
			metadata.Lock()
			return true, ownerMetadata("root", "1"), nil
		})
		done := make(chan error, 1)
		go func() {
			_, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: v2, Namespace: "ns", Name: "root", UID: "root"})
			done <- err
		}()
		Eventually(entered).Should(BeClosed())
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "root"}
		r.reader.mu.Lock()
		r.reader.saveMetadataLocked(key, metadataEntry{object: conflictingMetadata(), expires: time.Now().Add(time.Minute), failureKey: "contradiction"})
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		r.reader.mu.Unlock()
		close(resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.reader.versions).To(BeEmpty())
	})
})
