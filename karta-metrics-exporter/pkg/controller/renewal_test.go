// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/workqueue"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type countedLabels struct{ calls int }

func (c *countedLabels) Resolve(context.Context, labelresolver.Input) ([]labelresolver.ResolvedMetricLabels, error) {
	c.calls++
	return nil, nil
}
func drainQueue(q workqueue.TypedRateLimitingInterface[queueKey]) {
	for q.Len() > 0 {
		key, stopped := q.Get()
		if stopped {
			return
		}
		q.Forget(key)
		q.Done(key)
	}
}
func renewalClock(r *Runtime) *clocktesting.FakeClock {
	clock := clocktesting.NewFakeClock(time.Now())
	r.config.Now = clock.Now
	r.reader.options.Now = clock.Now
	r.reader.watchNow = clock.Now
	r.rootQueue.ShutDown()
	r.rootQueue = workqueue.NewTypedRateLimitingQueueWithConfig(workqueue.NewTypedItemExponentialFailureRateLimiter[queueKey](time.Millisecond, time.Second), workqueue.TypedRateLimitingQueueConfig[queueKey]{Clock: clock})
	return clock
}

var _ = Describe("Source-owned renewal scheduling", func() {
	DescribeTable("uses the earliest relevant publication expiry", func(ctx SpecContext, part string) {
		r, _, publications := testRuntime(nil)
		clock := renewalClock(r)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.rootQueue)
		old := publications.Published(root.GetUID())
		record := *old
		expiry := clock.Now().Add(10 * time.Second)
		if part == "status" {
			record.Workload.HasStatus = true
			record.Status.ExpiresAt = expiry
		} else {
			row, _ := record.Pod(pod.GetUID())
			row.Mapping.ExpiresAt = expiry
			record.PodPages = store.BuildPodPages([]store.PublishedPod{row}, nil)
		}
		publications.Publish(&record)
		r.mu.Lock()
		r.reader.mu.RLock()
		Expect(r.scheduleRenewalLocked(root.GetUID())).To(BeTrue())
		r.reader.mu.RUnlock()
		r.mu.Unlock()
		Expect(r.roots[root.GetUID()].renewalAt.Before(expiry)).To(BeTrue())
	}, Entry("status proof", "status"), Entry("Pod path proof", "pod"))
	DescribeTable("dispatches source recovery and version changes immediately", func(ctx SpecContext, condition string) {
		r, metadata, _ := testRuntime(nil)
		renewalClock(r)
		root := testObject(testRootGVK, "root")
		w := seedWatch(r, testRootGVK, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		if condition == "recovered" {
			r.mu.Lock()
			r.failWatchLocked(w, errors.New("unavailable"))
			r.mu.Unlock()
			drainQueue(r.rootQueue)
		}
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			version := "1"
			if condition == "changed" {
				version = "2"
			}
			return true, metadataList(ownerMetadata("root", version)), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		Expect(r.rootQueue.Len()).To(Equal(1))
		Expect(r.queue.Len()).To(Equal(1))
	}, Entry("watch recovered", "recovered"), Entry("LIST observed a newer version", "changed"))
	DescribeTable("does not lose owner-path work when refresh fails or expiry removes inputs", func(ctx SpecContext, expired bool) {
		r, metadata, publications := testRuntime(nil)
		root, pod, middle := testObject(testRootGVK, "root"), testObject(podGVK, "pod"), testObject(testRootGVK, "middle")
		ownedBy(pod, middle)
		ownedBy(middle, root)
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		key := rememberOwner(r.reader, "middle", "1")
		entry := r.reader.metadata[key]
		entry.object.SetOwnerReferences(middle.GetOwnerReferences())
		r.reader.saveMetadataLocked(key, entry)
		state := seedRoot(r, root, pod)
		state.paths[pod.GetUID()] = []discovery.ObjectRef{refOf(pod), refOf(middle), refOf(root)}
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		if expired {
			entry.expires = time.Now().Add(-time.Second)
			r.reader.saveMetadataLocked(key, entry)
		} else {
			metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
				return true, nil, errors.New("collection unavailable")
			})
		}
		err := r.refreshMetadata(ctx)
		if expired {
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(err).To(HaveOccurred())
		}
		Expect(r.queue.Len()).To(BeNumerically(">", 0))
		Expect(r.rootQueue.Len()).To(BeNumerically(">", 0))
		row, _ := publications.Published(root.GetUID()).Pod(pod.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
	}, Entry("collection error", false), Entry("all owner metadata expired", true))

	DescribeTable("delays healthy unchanged evaluation and avoids duplicate discovery", func(ctx SpecContext, withPod bool) {
		labels := &countedLabels{}
		r, metadata, publications := testRuntime(labels)
		clock := renewalClock(r)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		rw := seedWatch(r, testRootGVK, "jobs", root)
		pw := seedWatch(r, podGVK, "pods")
		if withPod {
			r.acceptEvent(pw, pod, false)
			seedRoot(r, root, pod)
		} else {
			seedRoot(r, root)
		}
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		initialCalls := labels.calls
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		initial := publications.Published(root.GetUID()).Creation.ExpiresAt
		clock.Step(45 * time.Second)
		metadata.PrependReactor("list", "*", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			if action.GetResource().Resource == "pods" {
				if withPod {
					return true, metadataList(ownerMetadata("pod", "1")), nil
				}
				return true, metadataList(), nil
			}
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, rw.key)).To(Succeed())
		Expect(r.verifyWatch(ctx, pw.key)).To(Succeed())
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(r.queue.Len()).To(BeZero())
		Expect(r.rootQueue.Len()).To(BeZero())
		Expect(labels.calls).To(Equal(initialCalls))
		deadline := r.roots[root.GetUID()].renewalAt
		Expect(deadline.After(clock.Now())).To(BeTrue())
		Expect(deadline.Before(initial)).To(BeTrue())
		clock.Step(deadline.Sub(clock.Now()))
		Eventually(r.rootQueue.Len).Should(Equal(1))
		key, stopped := r.rootQueue.Get()
		Expect(stopped).To(BeFalse())
		Expect(r.reconcile(ctx, key.UID)).To(Succeed())
		r.rootQueue.Done(key)
		Expect(labels.calls).To(Equal(initialCalls + 1))
		record := publications.Published(root.GetUID())
		Expect(record.Creation.ExpiresAt.After(initial)).To(BeTrue())
		valid, _ := publications.Capture().Valid(record.Creation)
		Expect(valid).To(BeTrue())
	}, Entry("with a Pod", true), Entry("with no Pods", false))
	It("lets a mutation overtake scheduled renewal while retaining same-root serialization", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		clock := renewalClock(r)
		root := testObject(testRootGVK, "root")
		rw := seedWatch(r, testRootGVK, "jobs", root)
		state := seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		r.mu.Lock()
		r.reader.mu.RLock()
		Expect(r.scheduleRenewalLocked(root.GetUID())).To(BeTrue())
		r.reader.mu.RUnlock()
		r.mu.Unlock()
		scheduled := state.renewalAt
		changed := root.DeepCopy()
		changed.SetResourceVersion("2")
		r.acceptEvent(rw, changed, false)
		clock.Step(20 * time.Millisecond)
		Eventually(r.rootQueue.Len).Should(Equal(1))
		Expect(clock.Now().Before(scheduled)).To(BeTrue())
		key, _ := r.rootQueue.Get()
		r.rootQueue.Add(key)
		r.rootQueue.Add(key)
		Expect(r.rootQueue.Len()).To(BeZero())
		Expect(r.reconcile(ctx, key.UID)).To(Succeed())
		Expect(publications.Published(root.GetUID()).Workload.Ref.ResourceVersion).To(Equal("2"))
		r.rootQueue.Done(key)
		Eventually(r.rootQueue.Len).Should(Equal(1))
	})
	DescribeTable("keeps unsafe or changed observations immediate", func(ctx SpecContext, condition string) {
		r, _, publications := testRuntime(nil)
		clock := renewalClock(r)
		root := testObject(testRootGVK, "root")
		seedWatch(r, testRootGVK, "jobs", root)
		state := seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.rootQueue)
		switch condition {
		case "missing":
			publications.RemovePublication(root.GetUID())
		case "expired":
			clock.Step(r.config.MaxAge + time.Second)
		case "changed":
			state.generation++
		case "dirty":
			state.dirty["pod"] = true
		case "pending":
			state.failResolution(testRootGVK, discovery.ErrPending)
		case "failure":
			state.podErrors["pod"] = errors.New("owner unavailable")
		case "invalid":
			entry := r.reader.objects[keyOf(refOf(root))]
			delete(r.reader.activeKeys, entry.proofKey)
		}
		r.mu.Lock()
		r.reader.mu.RLock()
		Expect(r.scheduleRenewalLocked(root.GetUID())).To(BeFalse())
		r.reader.mu.RUnlock()
		r.mu.Unlock()
		Expect(r.rootQueue.Len()).To(Equal(1))
		Expect(state.renewalAt.IsZero()).To(BeTrue())
	}, Entry("missing publication", "missing"), Entry("expired proof", "expired"), Entry("new generation", "changed"), Entry("dirty Pod", "dirty"), Entry("pending ownership", "pending"), Entry("failed owner", "failure"), Entry("retired proof", "invalid"))
	It("never keeps expired publications valid when delayed work runs late", func(ctx SpecContext) {
		r, _, _ := testRuntime(nil)
		clock := renewalClock(r)
		publications := store.New(store.Options{Now: clock.Now})
		r.deps.Publications, r.reader.publications = publications, publications
		root := testObject(testRootGVK, "root")
		seedWatch(r, testRootGVK, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.rootQueue)
		r.mu.Lock()
		r.reader.mu.RLock()
		Expect(r.scheduleRenewalLocked(root.GetUID())).To(BeTrue())
		r.reader.mu.RUnlock()
		r.mu.Unlock()
		clock.Step(r.config.MaxAge + time.Second)
		valid, _ := publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeFalse())
		Eventually(r.rootQueue.Len).Should(Equal(1))
		key, _ := r.rootQueue.Get()
		Expect(r.reconcile(ctx, key.UID)).To(Succeed())
		r.rootQueue.Done(key)
		valid, _ = publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeFalse())
	})
	It("re-resolves an unchanged direct owner when a metadata ancestor reparents", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		root, other, pod := testObject(testRootGVK, "root"), testObject(testRootGVK, "other"), testObject(podGVK, "pod")
		middle, ancestor := testObject(testRootGVK, "middle"), testObject(testRootGVK, "ancestor")
		ownedBy(pod, middle)
		ownedBy(middle, ancestor)
		ownedBy(ancestor, root)
		seedWatch(r, testRootGVK, "jobs", root, other)
		seedWatch(r, podGVK, "pods", pod)
		for _, object := range []struct {
			name   string
			owners []metav1.OwnerReference
		}{{"middle", middle.GetOwnerReferences()}, {"ancestor", ancestor.GetOwnerReferences()}} {
			key := rememberOwner(r.reader, object.name, "1")
			entry := r.reader.metadata[key]
			entry.object.SetOwnerReferences(object.owners)
			r.reader.saveMetadataLocked(key, entry)
		}
		state := seedRoot(r, root, pod)
		state.paths[pod.GetUID()] = []discovery.ObjectRef{refOf(pod), refOf(middle), refOf(ancestor), refOf(root)}
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		drainQueue(r.queue)
		drainQueue(r.rootQueue)
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			first := ownerMetadata("middle", "1")
			first.SetOwnerReferences(middle.GetOwnerReferences())
			last := ownerMetadata("ancestor", "2")
			ownedBy(ancestor, other)
			last.SetOwnerReferences(ancestor.GetOwnerReferences())
			return true, metadataList(first, last), nil
		})
		Expect(r.refreshMetadata(ctx)).To(Succeed())
		Expect(r.queue.Len()).To(BeNumerically(">", 0))
		Expect(r.rootQueue.Len()).To(BeNumerically(">", 0))
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.podRoots[pod.GetUID()]).To(Equal(types.UID("other")))
		row, _ := publications.Published(root.GetUID()).Pod(pod.GetUID())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
	})
})
