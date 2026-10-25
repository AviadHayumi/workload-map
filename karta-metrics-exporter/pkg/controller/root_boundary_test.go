// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

var _ = Describe("Accepted root boundaries", func() {
	It("rejects an ownerless watched alias while metadata proves a parent and recovers after correction", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		root, parent := testObject(v2, "root"), testObject(testRootGVK, "parent")
		pod := testObject(podGVK, "pod")
		ownedBy(pod, root)
		seedWatch(r, v2, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		key := rememberOwner(r.reader, "root", "2")
		entry := r.reader.metadata[key]
		ownedBy(parent, parent)
		entry.object.SetOwnerReferences(parent.GetOwnerReferences())
		r.reader.saveMetadataLocked(key, entry)
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(MatchError(discovery.ErrStale))
		Expect(r.roots).To(BeEmpty())
		Expect(publications.Capture().Records).To(BeEmpty())
		entry.object = entry.object.DeepCopy()
		entry.object.SetOwnerReferences(nil)
		entry.object.SetResourceVersion("3")
		r.reader.saveMetadataLocked(key, entry)
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Capture().Records).To(HaveLen(1))
	})
	DescribeTable("withdraws alias proof when contradictory metadata is retired", func(ctx SpecContext, expire bool) {
		r, metadata, _ := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		root, parent := testObject(v2, "root"), testObject(testRootGVK, "parent")
		w := seedWatch(r, v2, "jobs", root)
		key := rememberOwner(r.reader, "root", "2")
		entry := r.reader.metadata[key]
		ownedBy(parent, parent)
		entry.object.SetOwnerReferences(parent.GetOwnerReferences())
		if expire {
			entry.expires = time.Now().Add(-time.Second)
		}
		r.reader.saveMetadataLocked(key, entry)
		Expect(r.rootBoundaryLocked(refOf(root))).To(BeFalse())
		if expire {
			r.reader.expireMetadata()
		} else {
			r.reader.mu.Lock()
			r.reader.removeMetadataLocked(key, "unresolved_owner")
			r.reader.mu.Unlock()
		}
		Expect(r.reader.versions[testRootGVK.GroupKind()]).To(Equal(map[schema.GroupVersionKind]int{v2: 1}))
		_, err := r.reader.evidence(refOf(root))
		Expect(err).To(MatchError(discovery.ErrPending))
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		_, err = r.reader.evidence(refOf(root))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.rootBoundaryLocked(refOf(root))).To(BeTrue())
	}, Entry("on expiry", true), Entry("on last-demand pruning", false))
	It("prefers the accepted full representation over older metadata at the same version", func() {
		r, _, _ := testRuntime(nil)
		root, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
		root.SetResourceVersion("3")
		seedWatch(r, testRootGVK, "jobs", root)
		key := rememberOwner(r.reader, "root", "2")
		entry := r.reader.metadata[key]
		ownedBy(parent, parent)
		entry.object.SetOwnerReferences(parent.GetOwnerReferences())
		r.reader.saveMetadataLocked(key, entry)
		Expect(r.rootBoundaryLocked(refOf(root))).To(BeTrue())
		r.reader.removeMetadataLocked(key, "unresolved_owner")
		_, err := r.reader.evidence(refOf(root))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.reader.versions[testRootGVK.GroupKind()]).To(Equal(map[schema.GroupVersionKind]int{testRootGVK: 1}))
	})
	It("retires stale metadata aliases with a contradictory watch even without a root publication", func() {
		r, _, _ := testRuntime(nil)
		root, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
		ownedBy(root, parent)
		w := seedWatch(r, testRootGVK, "jobs", root)
		v2 := testRootGVK
		v2.Version = "v2"
		key := objectKey{GVK: v2, Namespace: "ns", Name: "root"}
		r.reader.saveMetadataLocked(key, metadataEntry{object: ownerMetadata("root", "1"), expires: time.Now().Add(time.Minute), failureKey: "alias"})
		r.reader.activeKeys["alias"] = true
		r.mu.Lock()
		delete(r.watches, w.key)
		r.reader.mu.Lock()
		r.retireWatchObjectsLocked(w)
		r.reader.mu.Unlock()
		r.mu.Unlock()
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.reader.versions).To(BeEmpty())
		Expect(r.reader.activeKeys).NotTo(HaveKey("alias"))
	})

	It("preserves ignored-owner semantics but rejects malformed or multiple controllers", func() {
		r, _, _ := testRuntime(nil)
		root, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
		ownedBy(root, parent)
		Expect(r.terminalOwnerBoundary(root)).To(BeFalse())
		r.config.IgnoredOwnerKinds = []schema.GroupVersionKind{testRootGVK}
		Expect(r.terminalOwnerBoundary(root)).To(BeTrue())
		refs := root.GetOwnerReferences()
		refs[0].UID = ""
		root.SetOwnerReferences(refs)
		Expect(r.terminalOwnerBoundary(root)).To(BeFalse())
		ownedBy(root, parent)
		refs = root.GetOwnerReferences()
		root.SetOwnerReferences(append(refs, refs[0]))
		Expect(r.terminalOwnerBoundary(root)).To(BeFalse())
	})
	It("keeps the version index until the last namespace watch is removed", func() {
		r, _, _ := testRuntime(nil)
		root, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
		ownedBy(root, parent)
		first := seedWatch(r, testRootGVK, "jobs")
		second := *first
		second.key.Namespace = "ns"
		second.objects = map[objectKey]struct{}{}
		r.watches[second.key] = &second
		r.trackWatchVersion(testRootGVK, 1)
		r.accept(&second, root, false)
		r.mu.Lock()
		delete(r.watches, first.key)
		r.reader.mu.Lock()
		r.retireWatchObjectsLocked(first)
		Expect(r.rootBoundaryLocked(refOf(root))).To(BeFalse())
		r.reader.mu.Unlock()
		r.mu.Unlock()
		Expect(r.reader.versions[testRootGVK.GroupKind()]).To(HaveLen(1))
		r.mu.Lock()
		delete(r.watches, second.key)
		r.reader.mu.Lock()
		r.retireWatchObjectsLocked(&second)
		r.reader.mu.Unlock()
		r.mu.Unlock()
		Expect(r.reader.versions).To(BeEmpty())
	})
	It("ends watch verification when the complete paginated snapshot exceeds its proof lifetime", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		now := time.Unix(100, 0)
		r.config.Now = func() time.Time { return now }
		r.config.MaxAge = time.Minute
		root := testObject(testRootGVK, "root")
		w := seedWatch(r, testRootGVK, "jobs", root)
		w.verifiedAt = now
		started := now
		pages := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			page := metadataList(ownerMetadata("root", "1"))
			if pages == 1 {
				now = now.Add(time.Minute)
				page.Continue = "next"
			}
			return true, page, nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(MatchError(discovery.ErrStale))
		Expect(pages).To(Equal(1))
		Expect(w.verifiedAt).To(Equal(started))
		Expect(w.err).NotTo(HaveOccurred())
	})
	It("requires fresh alias proof when a contradictory representation is retired", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		root, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
		ownedBy(root, parent)
		alias := root.DeepCopy()
		alias.SetOwnerReferences(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		alias.SetGroupVersionKind(v2)
		old := seedWatch(r, testRootGVK, "jobs", root)
		next := seedWatch(r, v2, "jobs", alias)
		r.mu.Lock()
		delete(r.watches, old.key)
		r.reader.mu.Lock()
		r.retireWatchObjectsLocked(old)
		r.reader.mu.Unlock()
		r.mu.Unlock()
		_, err := r.reader.evidence(refOf(alias))
		Expect(err).To(MatchError(discovery.ErrPending))
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, next.key)).To(Succeed())
		_, err = r.reader.evidence(refOf(alias))
		Expect(err).NotTo(HaveOccurred())
	})
})
