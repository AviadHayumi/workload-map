// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		r.accept(w, changed, false)
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(w.key.Resource.GroupResource(), "parent", errors.New("denied"))
		})
		Expect(r.resolve(ctx, queueKey{Object: keyOf(refOf(changed)), UID: changed.GetUID()})).To(HaveOccurred())
		Expect(state.resolutionError).To(HaveOccurred())
		close(paused.resume)
		Eventually(finished).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(state.resolutionError).To(HaveOccurred())
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
		Expect(state.cached).To(BeEmpty())
		Expect(state.labelFailures).To(HaveKey(pod.GetUID()))
		r.accept(pw, pod, true)
		state.fullRefresh = true
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(state.labelFailures).To(BeEmpty())
		Expect(publications.Published(root.GetUID()).FailureReasons).NotTo(ContainElement(store.ReasonEvaluationFailed))
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Published(root.GetUID()).FailureReasons).NotTo(ContainElement(store.ReasonEvaluationFailed))
	})
})
