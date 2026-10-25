// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type countedPublicationReads struct {
	store.Writer
	reads map[types.UID]int
}

func (c *countedPublicationReads) Published(uid types.UID) *store.Publication {
	c.reads[uid]++
	return c.Writer.Published(uid)
}

func queuedObjects(r *Runtime) []types.UID {
	var result []types.UID
	for r.queue.Len() > 0 {
		key, _ := r.queue.Get()
		result = append(result, key.UID)
		r.queue.Done(key)
	}
	return result
}

var _ = Describe("Scoped metadata renewal", func() {
	var (
		r                    *Runtime
		metadataClient       *metadatafake.FakeMetadataClient
		publications         store.Interface
		clock                *clocktesting.FakeClock
		a, b, idle           *unstructured.Unstructured
		first, second, third *unstructured.Unstructured
		owners               map[string]*metav1.PartialObjectMetadata
		listFailure          error
		getFailure           error
		partial              bool
	)
	BeforeEach(func(ctx SpecContext) {
		r, metadataClient, _ = testRuntime(nil)
		clock = renewalClock(r)
		publications = store.New(store.Options{Now: clock.Now})
		r.deps.Publications, r.reader.publications = publications, publications
		a, b, idle = testObject(testRootGVK, "a"), testObject(testRootGVK, "b"), testObject(testRootGVK, "idle")
		b.SetNamespace("other")
		first, second, third = testObject(podGVK, "first"), testObject(podGVK, "second"), testObject(podGVK, "third")
		third.SetNamespace("other")
		middleA, middleB := testObject(testRootGVK, "middle-a"), testObject(testRootGVK, "middle-b")
		middleB.SetNamespace("other")
		ownedBy(middleA, a)
		ownedBy(middleB, b)
		ownedBy(first, middleA)
		ownedBy(second, middleA)
		ownedBy(third, middleB)
		seedWatch(r, testRootGVK, "jobs", a, b, idle)
		seedWatch(r, podGVK, "pods", first, second, third)
		owners = map[string]*metav1.PartialObjectMetadata{}
		for _, object := range []*unstructured.Unstructured{middleA, middleB} {
			key := rememberOwner(r.reader, object.GetName(), "1")
			entry := r.reader.metadata[key]
			r.reader.deleteMetadataLocked(key)
			key.Namespace = object.GetNamespace()
			entry.object.SetNamespace(object.GetNamespace())
			entry.object.SetOwnerReferences(object.GetOwnerReferences())
			entry.expires = clock.Now().Add(time.Minute)
			r.reader.saveMetadataLocked(key, entry)
			owners[object.GetName()] = entry.object.DeepCopy()
		}
		listFailure, getFailure, partial = nil, nil, false
		metadataClient.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			if action.GetNamespace() == "ns" {
				if partial {
					partial = false
					listFailure = errors.New("second page failed")
					page := metadataList(owners["middle-a"].DeepCopy())
					page.Continue = "next"
					return true, page, nil
				}
				if listFailure != nil {
					return true, nil, listFailure
				}
			}
			var items []*metav1.PartialObjectMetadata
			for _, object := range owners {
				if object.Namespace == action.GetNamespace() {
					items = append(items, object.DeepCopy())
				}
			}
			return true, metadataList(items...), nil
		})
		metadataClient.PrependReactor("get", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			if action.GetNamespace() == "ns" && getFailure != nil {
				return true, nil, getFailure
			}
			name := action.(clienttesting.GetAction).GetName()
			if object := owners[name]; object != nil {
				return true, object.DeepCopy(), nil
			}
			return true, nil, apierrors.NewNotFound(testRootGVK.GroupVersion().WithResource("jobs").GroupResource(), name)
		})
		for _, pod := range []*unstructured.Unstructured{first, second, third} {
			Expect(r.resolve(ctx, queueKey{Operation: "object", Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		}
		seedRoot(r, idle)
		for _, root := range []*unstructured.Unstructured{a, b, idle} {
			Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		}
		clock.Step(30 * time.Millisecond)
		Eventually(r.rootQueue.Len).Should(BeNumerically(">=", 2))
		drainQueue(r.rootQueue)
		drainQueue(r.queue)
	})

	It("stagger-renews unchanged roots once without re-resolving shared paths", func(ctx SpecContext) {
		reads := &countedPublicationReads{Writer: publications, reads: map[types.UID]int{}}
		r.deps.Publications = reads
		before := publications.Published(a.GetUID())
		row, _ := before.Pod(first.GetUID())
		clock.Step(10 * time.Second)
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(r.queue.Len()).To(BeZero())
		Expect(r.rootQueue.Len()).To(BeZero())
		Expect(reads.reads).To(Equal(map[types.UID]int{"a": 1, "b": 1}))
		Expect(r.roots[idle.GetUID()].renewalAt.IsZero()).To(BeTrue())
		deadline := r.roots[a.GetUID()].renewalAt
		Expect(deadline.After(clock.Now())).To(BeTrue())
		Expect(deadline.Before(row.Mapping.ExpiresAt)).To(BeTrue())
		clock.Step(deadline.Sub(clock.Now()))
		Eventually(r.rootQueue.Len).Should(BeNumerically(">=", 1))
		Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
		updated, _ := publications.Published(a.GetUID()).Pod(first.GetUID())
		Expect(updated.Mapping.ExpiresAt.After(row.Mapping.ExpiresAt)).To(BeTrue())
		valid, _ := publications.Capture().Valid(updated.Mapping)
		Expect(valid).To(BeTrue())
		Expect(r.queue.Len()).To(BeZero())
	})

	DescribeTable("immediately invalidates only paths with changed or unavailable inputs", func(ctx SpecContext, change, reason string) {
		before := publications.Published(a.GetUID())
		sibling := publications.Published(b.GetUID())
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "middle-a"}
		switch change {
		case "rv":
			owners["middle-a"].ResourceVersion = "2"
		case "reparent":
			owners["middle-a"].ResourceVersion = "2"
			owners["middle-a"].SetOwnerReferences(nil)
		case "replacement":
			owners["middle-a"].UID = "replacement"
		case "missing":
			delete(owners, "middle-a")
		case "failure":
			listFailure = errors.New("collection unavailable")
		case "permission":
			listFailure = apierrors.NewForbidden(testRootGVK.GroupVersion().WithResource("jobs").GroupResource(), "", errors.New("denied"))
			getFailure = listFailure
		case "partial":
			partial = true
		case "expiry":
			entry := r.reader.metadata[key]
			entry.expires = clock.Now()
			r.reader.metadata[key] = entry
		}
		err := r.refreshMetadata(ctx)
		if change == "failure" || change == "partial" || change == "permission" {
			Expect(err).To(HaveOccurred())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(queuedObjects(r)).To(ConsistOf(first.GetUID(), second.GetUID()))
		Expect(r.rootQueue.Len()).To(Equal(1))
		for _, pod := range []*unstructured.Unstructured{first, second} {
			row, _ := before.Pod(pod.GetUID())
			valid, actual := publications.Capture().Valid(row.Mapping)
			Expect(valid).To(BeFalse())
			Expect(actual).To(Equal(reason))
		}
		row, _ := sibling.Pod(third.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
		valid, _ = publications.Capture().Valid(before.Creation)
		Expect(valid).To(BeTrue())
	}, Entry("same-owner RV", "rv", store.ReasonAPIUnavailable),
		Entry("changed owner", "reparent", store.ReasonUnresolvedOwner),
		Entry("replacement UID", "replacement", store.ReasonUnresolvedOwner),
		Entry("confirmed absence", "missing", store.ReasonUnresolvedOwner),
		Entry("failed collection", "failure", store.ReasonAPIUnavailable),
		Entry("denied collection and GET", "permission", store.ReasonPermissionDenied),
		Entry("partial collection", "partial", store.ReasonAPIUnavailable),
		Entry("expired cache", "expiry", store.ReasonAPIUnavailable))

	It("recovers a failed path even when fresh metadata has the old UID and RV", func(ctx SpecContext) {
		listFailure, getFailure = errors.New("unavailable"), errors.New("unavailable")
		Expect(r.refreshMetadata(ctx)).NotTo(Succeed())
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(first)), UID: first.GetUID()})).NotTo(Succeed())
		Expect(r.roots[a.GetUID()].podErrors).To(HaveKey(first.GetUID()))
		listFailure, getFailure = nil, nil
		_, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "middle-a", UID: "middle-a"})
		Expect(err).NotTo(HaveOccurred())
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(queuedObjects(r)).To(ContainElement(first.GetUID()))
		for _, pod := range []*unstructured.Unstructured{first, second} {
			Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		}
		Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
		Expect(r.roots[a.GetUID()].podErrors).To(BeEmpty())
		row, _ := publications.Published(a.GetUID()).Pod(first.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
	})

	It("keeps GET-only collection permissions usable without a discovery wave", func(ctx SpecContext) {
		listFailure = apierrors.NewForbidden(testRootGVK.GroupVersion().WithResource("jobs").GroupResource(), "", errors.New("GET only"))
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(r.queue.Len()).To(BeZero())
		Expect(r.rootQueue.Len()).To(BeZero())
	})

	It("uses the expected path RV even when a newer GET preceded the refresh", func(ctx SpecContext) {
		key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: "middle-a"}
		previous := r.reader.metadata[key]
		owners["middle-a"].ResourceVersion = "2"
		r.reader.mu.Lock()
		r.reader.retireMetadataLocked(previous, store.ReasonAPIUnavailable)
		previous.object = owners["middle-a"].DeepCopy()
		previous.failureKey = "metadata:new-get"
		r.reader.activeKeys[previous.failureKey] = true
		r.reader.saveMetadataLocked(key, previous)
		r.reader.mu.Unlock()
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(queuedObjects(r)).To(ConsistOf(first.GetUID(), second.GetUID()))
	})

	It("does not revive consumers removed before their metadata is pruned", func(ctx SpecContext) {
		for _, pod := range []*unstructured.Unstructured{first, second, third} {
			w := r.watches[watchKey{Resource: podGVK.GroupVersion().WithResource("pods")}]
			r.acceptEvent(w, pod, true)
		}
		clock.Step(30 * time.Millisecond)
		Eventually(r.rootQueue.Len).Should(Equal(2))
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.pathDependents).To(BeEmpty())
		Expect(r.queue.Len()).To(BeZero())
		Expect(r.rootQueue.Len()).To(BeZero())
	})
	It("keeps a newer watched representation authoritative over a blocked metadata LIST", func(ctx SpecContext) {
		middle := testObject(testRootGVK, "middle-a")
		ownedBy(middle, a)
		w := r.watches[r.roots[a.GetUID()].watch]
		r.acceptEvent(w, middle, false)
		entered, resume := make(chan struct{}), make(chan struct{})
		blocked := false
		metadataClient.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			if action.GetNamespace() != "ns" {
				return false, nil, nil
			}
			if !blocked {
				blocked = true
				close(entered)
				<-resume
			}
			return true, metadataList(owners["middle-a"].DeepCopy()), nil
		})
		done := make(chan error, 1)
		go func() { done <- r.refreshMetadata(ctx) }()
		Eventually(entered).Should(BeClosed())
		changed := middle.DeepCopy()
		changed.SetResourceVersion("2")
		r.acceptEvent(w, changed, false)
		close(resume)
		Eventually(done).Should(Receive(Succeed()))
		Expect(queuedObjects(r)).To(ContainElements(first.GetUID(), second.GetUID()))
		for _, pod := range []*unstructured.Unstructured{first, second} {
			Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		}
		Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
		clock.Step(30 * time.Millisecond)
		Eventually(r.rootQueue.Len).Should(BeNumerically(">=", 1))
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		// The old metadata still exists. It must neither override the full RV2
		// observation nor trigger another traversal of the now-current path.
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(r.queue.Len()).To(BeZero())
		Expect(r.rootQueue.Len()).To(BeZero())
		row, _ := publications.Published(a.GetUID()).Pod(first.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
		r.reader.mu.Lock()
		r.reader.removeMetadataLocked(keyOf(refOf(middle)), store.ReasonAPIUnavailable)
		r.reader.mu.Unlock()
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(r.queue.Len()).To(BeZero())
		Expect(r.rootQueue.Len()).To(BeZero())
	})

	It("visits retained dependencies after the final metadata entry disappears", func(ctx SpecContext) {
		for key := range r.reader.metadata {
			r.reader.removeMetadataLocked(key, store.ReasonAPIUnavailable)
		}
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.reader.failures).To(BeEmpty())
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(queuedObjects(r)).To(ConsistOf(first.GetUID(), second.GetUID(), third.GetUID()))
		Expect(r.rootQueue.Len()).To(Equal(2))
	})

	DescribeTable("keeps deeper dependencies scoped across permission failures and rerooting", func(ctx SpecContext, reroot bool) {
		ancestor := testObject(testRootGVK, "ancestor")
		ownedBy(ancestor, a)
		key := rememberOwner(r.reader, "ancestor", "1")
		entry := r.reader.metadata[key]
		entry.object.SetOwnerReferences(ancestor.GetOwnerReferences())
		owners["ancestor"] = entry.object.DeepCopy()
		middle := testObject(testRootGVK, "middle-a")
		ownedBy(middle, ancestor)
		owners["middle-a"].ResourceVersion = "2"
		owners["middle-a"].SetOwnerReferences(middle.GetOwnerReferences())
		_, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "middle-a", UID: "middle-a", ResourceVersion: "2"})
		Expect(err).NotTo(HaveOccurred())
		for _, pod := range []*unstructured.Unstructured{first, second} {
			Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		}
		Expect(r.pathDependents[key][first.GetUID()].position).To(Equal(2))
		Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
		clock.Step(30 * time.Millisecond)
		Eventually(r.rootQueue.Len).Should(BeNumerically(">=", 1))
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		if reroot {
			ownedBy(middle, idle)
			owners["middle-a"].ResourceVersion = "3"
			owners["middle-a"].SetOwnerReferences(middle.GetOwnerReferences())
			_, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "middle-a", UID: "middle-a", ResourceVersion: "3"})
			Expect(err).NotTo(HaveOccurred())
			for _, pod := range []*unstructured.Unstructured{first, second} {
				Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
			}
			Expect(r.pathDependents).NotTo(HaveKey(key))
			Expect(r.podRoots[first.GetUID()]).To(Equal(idle.GetUID()))
			Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
			Expect(r.reconcile(ctx, idle.GetUID())).To(Succeed())
			clock.Step(30 * time.Millisecond)
			Eventually(r.rootQueue.Len).Should(BeNumerically(">=", 2))
			drainQueue(r.queue)
			drainQueue(r.rootQueue)
			Expect(r.refreshMetadata(ctx)).To(Succeed())
			Expect(r.reader.metadata).NotTo(HaveKey(key))
			Expect(r.queue.Len()).To(BeZero())
			row, found := publications.Published(idle.GetUID()).Pod(first.GetUID())
			Expect(found).To(BeTrue())
			valid, _ := publications.Capture().Valid(row.Mapping)
			Expect(valid).To(BeTrue())
		} else {
			denied := apierrors.NewForbidden(testRootGVK.GroupVersion().WithResource("jobs").GroupResource(), "ancestor", errors.New("denied"))
			listFailure = denied
			metadataClient.PrependReactor("get", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
				if action.(clienttesting.GetAction).GetName() == "ancestor" {
					return true, nil, denied
				}
				return false, nil, nil
			})
			Expect(r.refreshMetadata(ctx)).NotTo(Succeed())
			Expect(queuedObjects(r)).To(ConsistOf(first.GetUID(), second.GetUID()))
			row, _ := publications.Published(a.GetUID()).Pod(first.GetUID())
			valid, reason := publications.Capture().Valid(row.Mapping)
			Expect(valid).To(BeFalse())
			Expect(reason).To(Equal(store.ReasonPermissionDenied))
		}
	}, Entry("ancestor-only denied GET", false), Entry("new root removes old ancestor edges", true))

})
