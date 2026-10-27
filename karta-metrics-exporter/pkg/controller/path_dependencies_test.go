// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"

	"k8s.io/apimachinery/pkg/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

var _ = Describe("Watched intermediate dependencies", func() {
	It("rejects a publication evaluated before its watched intermediate changes", func(ctx SpecContext) {
		labels := &pausedLabels{entered: make(chan types.UID, 1), resume: make(chan struct{})}
		r, _, publications := testRuntime(labels)
		a, b, middle := testObject(testRootGVK, "a"), testObject(testRootGVK, "b"), testObject(testRootGVK, "middle")
		pod := testObject(podGVK, "pod")
		ownedBy(middle, a)
		ownedBy(pod, middle)
		w := seedWatch(r, testRootGVK, "jobs", a, b, middle)
		seedWatch(r, podGVK, "pods", pod)
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		done := make(chan error, 1)
		go func() { done <- r.reconcile(ctx, a.GetUID()) }()
		Eventually(labels.entered).Should(Receive(Equal(pod.GetUID())))
		changed := middle.DeepCopy()
		changed.SetResourceVersion("2")
		ownedBy(changed, b)
		r.acceptEvent(w, changed, false)
		close(labels.resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(publications.Published(a.GetUID())).To(BeNil())
	})
	DescribeTable("invalidates only dependent mappings before recovery work", func(ctx SpecContext, change string) {
		r, metadata, publications := testRuntime(nil)
		a, b, middle := testObject(testRootGVK, "a"), testObject(testRootGVK, "b"), testObject(testRootGVK, "middle")
		pod, sibling := testObject(podGVK, "pod"), testObject(podGVK, "sibling")
		ownedBy(middle, a)
		ownedBy(pod, middle)
		ownedBy(sibling, a)
		w := seedWatch(r, testRootGVK, "jobs", a, b, middle)
		seedWatch(r, podGVK, "pods", pod, sibling)
		for _, ref := range []discovery.ObjectRef{refOf(pod), refOf(sibling)} {
			Expect(r.resolveInput(ctx, inputKey{Object: keyOf(ref), UID: ref.UID})).To(Succeed())
		}
		Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.pathDependents).To(HaveLen(1))
		drainQueue(r.inputQueue)
		before := publications.Published(a.GetUID())
		row, _ := before.Pod(pod.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
		changed := middle.DeepCopy()
		changed.SetResourceVersion("2")
		ownedBy(changed, b)
		switch change {
		case "event":
			r.acceptEvent(w, changed, false)
		case "delete":
			r.acceptEvent(w, middle, true)
		case "list":
			metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
				return true, metadataList(ownerMetadata("a", "1"), ownerMetadata("b", "1"), ownerMetadata("middle", "2")), nil
			})
			Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		case "failure":
			r.mu.Lock()
			r.failWatchLocked(w, errors.New("unavailable"))
			r.mu.Unlock()
		}
		valid, _ = publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
		if change != "failure" {
			siblingRow, _ := before.Pod(sibling.GetUID())
			valid, _ = publications.Capture().Valid(siblingRow.Mapping)
			Expect(valid).To(BeTrue())
			valid, _ = publications.Capture().Valid(before.Creation)
			Expect(valid).To(BeTrue())
		}
		queued := map[string]bool{}
		for r.inputQueue.Len() > 0 {
			key, _ := r.inputQueue.Get()
			queued[string(key.UID)] = true
			r.inputQueue.Done(key)
		}
		Expect(queued).To(HaveKey("pod"))
		Expect(queued).NotTo(HaveKey("sibling"))
		if change == "event" {
			Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
			Expect(r.reconcile(ctx, a.GetUID())).To(Succeed())
			Expect(r.reconcile(ctx, b.GetUID())).To(Succeed())
			_, found := publications.Published(a.GetUID()).Pod(pod.GetUID())
			Expect(found).To(BeFalse())
			recovered, found := publications.Published(b.GetUID()).Pod(pod.GetUID())
			Expect(found).To(BeTrue())
			valid, _ = publications.Capture().Valid(recovered.Mapping)
			Expect(valid).To(BeTrue())
			r.mu.Lock()
			r.removeRoot(b.GetUID())
			r.mu.Unlock()
			Expect(r.pathDependents).To(BeEmpty())
		}
	}, Entry("owner watch event", "event"), Entry("owner deletion", "delete"), Entry("LIST before event", "list"), Entry("failed owner source", "failure"))
})
