// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type pausedPodDiscovery struct {
	discovery.Discoverer
	entered chan struct{}
	resume  chan struct{}
}

func (p *pausedPodDiscovery) Get(ctx context.Context, ref discovery.ObjectRef) (discovery.Result, error) {
	result, err := p.Discoverer.Get(ctx, ref)
	if ref.GVK == podGVK {
		close(p.entered)
		select {
		case <-p.resume:
		case <-ctx.Done():
			return discovery.Result{}, ctx.Err()
		}
	}
	return result, err
}

var _ = Describe("Superseded dependency paths", func() {
	It("rejects delayed Pod discovery after its former root acquires an unreadable controller", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		root, pod, parent := testObject(testRootGVK, "root"), testObject(podGVK, "pod"), testObject(testRootGVK, "parent")
		ownedBy(pod, root)
		rememberOwner(r.reader, "root", "1")
		w := seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		paused := &pausedPodDiscovery{Discoverer: r.deps.Discovery, entered: make(chan struct{}), resume: make(chan struct{})}
		r.deps.Discovery = paused
		finished := make(chan error, 1)
		go func() { finished <- r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()}) }()
		Eventually(paused.entered).Should(BeClosed())
		changed := root.DeepCopy()
		changed.SetResourceVersion("2")
		ownedBy(changed, parent)
		r.acceptEvent(w, changed, false)
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(w.key.Resource.GroupResource(), "parent", errors.New("denied"))
		})
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(changed)), UID: changed.GetUID()})).To(HaveOccurred())
		Expect(state.resolutionErrors).NotTo(BeEmpty())
		close(paused.resume)
		Eventually(finished).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(state.resolutionErrors).NotTo(BeEmpty())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		publication := publications.Published(root.GetUID())
		valid, _ := publications.Capture().Valid(publication.Creation)
		Expect(valid).To(BeFalse())
		Expect(publication.Workload.OwnerResolution).To(Equal(store.UnknownOwner))
	})
	DescribeTable("does not use metadata instead of a differing accepted full object", func(metadataVersion, requestedVersion string) {
		r, _, _ := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		rememberOwner(r.reader, "root", metadataVersion)
		seedWatch(r, testRootGVK, "jobs", root)
		ref := refOf(root)
		ref.ResourceVersion = requestedVersion
		_, err := r.reader.evidence(ref)
		Expect(err).To(MatchError(discovery.ErrStale))
	}, Entry("older metadata", "0", "0"), Entry("newer metadata waits for the full object", "2", "2"))
	It("removes label failures without a successful cache entry when the Pod disappears", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(failingPodLabel{})
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		pod.SetLabels(map[string]string{"bad": "true"})
		w := seedWatch(r, testRootGVK, "jobs", root)
		pw := seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, root, pod)
		r.mu.Lock()
		r.failWatchLocked(w, errors.New("outage"))
		r.mu.Unlock()
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(state.pods[pod.GetUID()].cached).To(BeNil())
		Expect(state.labelFailures).To(Equal(1))
		Expect(state.pods[pod.GetUID()].labelFailure).To(BeTrue())
		r.acceptEvent(pw, pod, true)
		state.fullRefresh = true
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(state.labelFailures).To(BeZero())
		Expect(publications.Published(root.GetUID()).FailureReasons).NotTo(ContainElement(store.ReasonEvaluationFailed))
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Published(root.GetUID()).FailureReasons).NotTo(ContainElement(store.ReasonEvaluationFailed))
	})
	DescribeTable("cannot clear unresolved ownership through an unchanged served-version alias", func(ctx SpecContext, mode int) {
		r, metadata, publications := testRuntime(nil)
		canonical, alias := testObject(testRootGVK, "root"), testObject(testRootGVK, "root")
		aliasGVK := testRootGVK
		aliasGVK.Version = "v2"
		alias.SetGroupVersionKind(aliasGVK)
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(aliasGVK, meta.RESTScopeNamespace)
		pod, parent := testObject(podGVK, "pod"), testObject(testRootGVK, "parent")
		ownedBy(pod, alias)
		cw := seedWatch(r, testRootGVK, "jobs", canonical)
		aw := seedWatch(r, aliasGVK, "jobs", alias)
		seedWatch(r, podGVK, "pods", pod)
		root := canonical
		if mode == 1 {
			root = alias
		}
		state := seedRoot(r, root, pod)
		state.watch = cw.key
		if mode > 0 {
			definitions := jobInventory()
			if mode == 2 {
				definitions.selection.GVK = aliasGVK
				state.watch = aw.key
			}
			r.deps.Inventory = definitions
		}
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		paused := &pausedPodDiscovery{Discoverer: r.deps.Discovery, entered: make(chan struct{}), resume: make(chan struct{})}
		r.deps.Discovery = paused
		done := make(chan error, 1)
		go func() { done <- r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()}) }()
		Eventually(paused.entered).Should(BeClosed())
		changed := canonical.DeepCopy()
		changed.SetResourceVersion("2")
		ownedBy(changed, parent)
		r.acceptEvent(cw, changed, false)
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(cw.key.Resource.GroupResource(), "parent", errors.New("denied"))
		})
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(changed)), UID: changed.GetUID()})).To(HaveOccurred())
		close(paused.resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		record := publications.Published(root.GetUID())
		valid, _ := publications.Capture().Valid(record.Creation)
		Expect(valid).To(BeFalse())
	}, Entry("generic canonical version", 0), Entry("selected version differs from original root", 1), Entry("selected watch moved to the alias", 2))

	It("transfers a retired representation failure and requires a fresh replacement read", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		canonical, alias := testObject(testRootGVK, "root"), testObject(testRootGVK, "root")
		v2 := testRootGVK
		v2.Version = "v2"
		alias.SetGroupVersionKind(v2)
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		pod := testObject(podGVK, "pod")
		ownedBy(pod, alias)
		old := seedWatch(r, testRootGVK, "jobs", canonical)
		replacement := seedWatch(r, v2, "jobs", alias)
		seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, canonical, pod)
		state.watch = replacement.key
		definitions := jobInventory()
		definitions.selection.GVK = v2
		r.deps.Inventory = definitions
		Expect(r.reconcile(ctx, canonical.GetUID())).To(Succeed())
		state.failResolution(testRootGVK, discovery.ErrUnresolvedOwner)
		entered, resume := make(chan struct{}), make(chan struct{})
		first := true
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			if first {
				first = false
				close(entered)
				<-resume
			}
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		done := make(chan error, 1)
		go func() { done <- r.verifyWatch(ctx, replacement.key) }()
		Eventually(entered).Should(BeClosed())
		r.mu.Lock()
		delete(r.watches, old.key)
		r.reader.mu.Lock()
		r.retireWatchObjectsLocked(old)
		r.reader.mu.Unlock()
		r.mu.Unlock()
		Expect(state.resolutionErrors).NotTo(HaveKey(testRootGVK))
		Expect(state.resolutionErrors).To(HaveKey(v2))
		close(resume)
		Eventually(done).Should(Receive(Succeed()))
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(MatchError(discovery.ErrPending))
		Expect(r.reconcile(ctx, canonical.GetUID())).To(Succeed())
		valid, _ := publications.Capture().Valid(publications.Published(canonical.GetUID()).Creation)
		Expect(valid).To(BeFalse())
		Expect(r.verifyWatch(ctx, replacement.key)).To(Succeed())
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(state.resolutionErrors).To(BeEmpty())
		Expect(r.reconcile(ctx, canonical.GetUID())).To(Succeed())
		valid, _ = publications.Capture().Valid(publications.Published(canonical.GetUID()).Creation)
		Expect(valid).To(BeTrue())
	})

	DescribeTable("does not invent a workload for never-resolved ownership and recovers after correction", func(ctx SpecContext, cycle bool) {
		r, _, publications := testRuntime(nil)
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		if cycle {
			ownedBy(pod, pod)
		} else {
			ownedBy(pod, root)
			refs := pod.GetOwnerReferences()
			refs = append(refs, refs[0])
			pod.SetOwnerReferences(refs)
		}
		seedWatch(r, testRootGVK, "jobs", root)
		pw := seedWatch(r, podGVK, "pods", pod)
		err := r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})
		Expect(err).To(MatchError(ContainSubstring(discovery.ErrUnresolvedOwner.Error())))
		Expect(r.roots).To(BeEmpty())
		Expect(publications.Capture().Records).To(BeEmpty())
		Expect(r.reader.failureHealth()).To(BeEmpty(), "a structural error has no invented resource-read failure")
		corrected := pod.DeepCopy()
		corrected.SetResourceVersion("2")
		ownedBy(corrected, root)
		r.acceptEvent(pw, corrected, false)
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(corrected)), UID: corrected.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		record := publications.Published(root.GetUID())
		Expect(record).NotTo(BeNil())
		row, found := record.Pod(pod.GetUID())
		Expect(found).To(BeTrue())
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeTrue())
	}, Entry("multiple controller references", false), Entry("owner cycle", true))

	DescribeTable("cannot recreate a former root through an alias after successful reparenting", func(ctx SpecContext, selected, retire bool) {
		r, _, publications := testRuntime(nil)
		canonical, alias, parent := testObject(testRootGVK, "root"), testObject(testRootGVK, "root"), testObject(testRootGVK, "parent")
		if retire {
			parentGVK := testRootGVK
			parentGVK.Kind = "Parent"
			parent.SetGroupVersionKind(parentGVK)
			r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(parentGVK, meta.RESTScopeNamespace)
			seedWatch(r, parentGVK, "parents", parent)
		}
		v2 := testRootGVK
		v2.Version = "v2"
		alias.SetGroupVersionKind(v2)
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		pod := testObject(podGVK, "pod")
		ownedBy(pod, alias)
		cw := seedWatch(r, testRootGVK, "jobs", canonical)
		if !retire {
			r.acceptEvent(cw, parent, false)
		}
		aw := seedWatch(r, v2, "jobs", alias)
		seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, canonical, pod)
		state.watch = cw.key
		if selected {
			r.deps.Inventory = jobInventory()
		}
		Expect(r.reconcile(ctx, canonical.GetUID())).To(Succeed())
		paused := &pausedPodDiscovery{Discoverer: r.deps.Discovery, entered: make(chan struct{}), resume: make(chan struct{})}
		r.deps.Discovery = paused
		done := make(chan error, 1)
		go func() { done <- r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()}) }()
		Eventually(paused.entered).Should(BeClosed())
		changed := canonical.DeepCopy()
		changed.SetResourceVersion("2")
		ownedBy(changed, parent)
		r.acceptEvent(cw, changed, false)
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(changed)), UID: changed.GetUID()})).To(Succeed())
		Expect(r.roots).NotTo(HaveKey(canonical.GetUID()))
		if retire {
			r.mu.Lock()
			delete(r.watches, cw.key)
			r.reader.mu.Lock()
			r.retireWatchObjectsLocked(cw)
			r.reader.mu.Unlock()
			r.mu.Unlock()
		}
		close(paused.resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		r.deps.Discovery = paused.Discoverer
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(MatchError(discovery.ErrStale))
		Expect(r.roots).NotTo(HaveKey(canonical.GetUID()))
		Expect(publications.Published(canonical.GetUID())).To(BeNil())
		updatedAlias := alias.DeepCopy()
		updatedAlias.SetResourceVersion("2")
		ownedBy(updatedAlias, parent)
		r.acceptEvent(aw, updatedAlias, false)
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, parent.GetUID())).To(Succeed())
		record := publications.Published(parent.GetUID())
		Expect(record).NotTo(BeNil())
		valid, _ := publications.Capture().Valid(record.Creation)
		Expect(valid).To(BeTrue())
		_, found := record.Pod(pod.GetUID())
		Expect(found).To(BeTrue())
		Expect(publications.Published(canonical.GetUID())).To(BeNil())
	}, Entry("generic identity", false, false), Entry("selected Karta representation", true, false), Entry("generic identity after contradictory watch retirement", false, true), Entry("selected Karta after contradictory watch retirement", true, true))

})
