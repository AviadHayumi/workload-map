// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

type testInventory struct {
	selection inventory.Selection
	err       error
}

func (i *testInventory) Lookup(gvk schema.GroupVersionKind) (inventory.Selection, error) {
	if gvk.GroupKind() != i.selection.GVK.GroupKind() {
		return inventory.Selection{}, nil
	}
	return i.selection, i.err
}
func (i *testInventory) Selections() []inventory.Selection {
	return []inventory.Selection{i.selection}
}
func (i *testInventory) ReplaceCluster([]*v1alpha1.Karta) {}

type failingPodLabel struct{}

func (failingPodLabel) ForWorkload(*unstructured.Unstructured) labelresolver.ResolveFunc {
	return func(_ context.Context, _ string, pod *unstructured.Unstructured) (labelresolver.Result, error) {
		if pod != nil && pod.GetLabels()["bad"] == "true" {
			return labelresolver.Result{Errors: map[string]error{"team": errors.New("bad label")}}, nil
		}
		return labelresolver.Result{}, nil
	}
}
func ownedBy(object, owner *unstructured.Unstructured) {
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(), Name: owner.GetName(), UID: owner.GetUID(), Controller: ptr.To(true)}})
}
func jobInventory() *testInventory {
	definition, err := catalog.Get(testRootGVK)
	Expect(err).NotTo(HaveOccurred())
	return &testInventory{selection: inventory.Selection{Definition: definition, GVK: testRootGVK, Origin: inventory.Origin{Source: inventory.SourceCatalog, Name: definition.Name}}}
}

var _ = Describe("Accepted failures and compatible retained evidence", func() {
	DescribeTable("rejects a LIST started before a newer watch failure", func(ctx SpecContext, informer bool) {
		r, metadata, publications := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		w := seedWatch(r, testRootGVK, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		entered, resume := make(chan struct{}), make(chan struct{})
		first := true
		reactor := func(clienttesting.Action) (bool, kruntime.Object, error) {
			if first {
				first = false
				close(entered)
				<-resume
			}
			if informer {
				return true, &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "batch/v1", "kind": "JobList"}, Items: []unstructured.Unstructured{*root}}, nil
			}
			return true, metadataList(ownerMetadata("root", "1")), nil
		}
		if informer {
			client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{w.key.Resource: "JobList"})
			r.reader.clients.Dynamic = client
			client.PrependReactor("list", "jobs", reactor)
		} else {
			metadata.PrependReactor("list", "jobs", reactor)
		}
		run := func() error {
			if informer {
				return r.replaceWatch(ctx, w)
			}
			return r.verifyWatch(ctx, w.key)
		}
		done := make(chan error, 1)
		go func() { done <- run() }()
		Eventually(entered).Should(BeClosed())
		cause := apierrors.NewForbidden(w.key.Resource.GroupResource(), "", errors.New("denied"))
		r.mu.Lock()
		r.failWatchLocked(w, cause)
		r.mu.Unlock()
		close(resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(w.err).To(Equal(cause))
		valid, reason := publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(store.ReasonPermissionDenied))
		Expect(run()).To(Succeed())
		Expect(w.err).NotTo(HaveOccurred())
	}, Entry("verification", false), Entry("informer relist", true))

	It("does not republish a former root while its new controller is unreadable", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		root, pod, parent := testObject(testRootGVK, "root"), testObject(podGVK, "pod"), testObject(testRootGVK, "parent")
		ownedBy(pod, root)
		w := seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		changed := root.DeepCopy()
		changed.SetResourceVersion("2")
		ownedBy(changed, parent)
		r.acceptEvent(w, changed, false)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		valid, _ := publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeFalse(), "owner changes are pending before GET completes")
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(w.key.Resource.GroupResource(), "parent", errors.New("denied"))
		})
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(changed)), UID: root.GetUID()})).To(HaveOccurred())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		prior := publications.Published(root.GetUID())
		valid, _ = publications.Capture().Valid(prior.Creation)
		Expect(valid).To(BeFalse())
		Expect(prior.Workload.OwnerResolution).To(Equal(store.UnknownOwner))
		r.acceptEvent(w, parent, false)
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(changed)), UID: root.GetUID()})).To(Succeed())
		Expect(publications.Published(root.GetUID())).To(BeNil())
		Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		Expect(r.reconcile(ctx, parent.GetUID())).To(Succeed())
		valid, _ = publications.Capture().Valid(publications.Published(parent.GetUID()).Creation)
		Expect(valid).To(BeTrue())
	})

	It("retains label failures from untouched Pods until that Pod recovers or disappears", func(ctx SpecContext) {
		r, _, publications := testRuntime(failingPodLabel{})
		root, bad, good := testObject(testRootGVK, "root"), testObject(podGVK, "bad"), testObject(podGVK, "good")
		bad.SetLabels(map[string]string{"bad": "true"})
		seedWatch(r, testRootGVK, "jobs", root)
		w := seedWatch(r, podGVK, "pods", bad, good)
		seedRoot(r, root, bad, good)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		updated := good.DeepCopy()
		updated.SetResourceVersion("2")
		r.acceptEvent(w, updated, false)
		r.setPodPathLocked(r.roots[root.GetUID()], good.GetUID(), []discovery.ObjectRef{refOf(updated), refOf(root)})
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Published(root.GetUID()).FailureReasons).To(ContainElement(store.ReasonEvaluationFailed))
		r.acceptEvent(w, bad, true)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Published(root.GetUID()).FailureReasons).NotTo(ContainElement(store.ReasonEvaluationFailed))
	})

	It("does not cache unknown grouping from a root outage as a successful mapping", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		r.deps.Inventory = jobInventory()
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		root.Object["spec"] = map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"job-name": "root"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "example.test/image:v1"}}}}}
		pod.SetLabels(map[string]string{"job-name": "root"})
		w := seedWatch(r, testRootGVK, "jobs", root)
		pw := seedWatch(r, podGVK, "pods", pod)
		seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		before, _ := publications.Published(root.GetUID()).Pod(pod.GetUID())
		Expect(before.Record.Component).NotTo(Equal("<unknown>"))
		r.mu.Lock()
		r.failWatchLocked(w, fmt.Errorf("unavailable"))
		r.mu.Unlock()
		changed := pod.DeepCopy()
		changed.SetResourceVersion("2")
		r.acceptEvent(pw, changed, false)
		r.setPodPathLocked(r.roots[root.GetUID()], pod.GetUID(), []discovery.ObjectRef{refOf(changed), refOf(root)})
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("root", "1")), nil
		})
		Expect(r.verifyWatch(ctx, w.key)).To(Succeed())
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		after, _ := publications.Published(root.GetUID()).Pod(pod.GetUID())
		Expect(after.Record.Component).To(Equal(before.Record.Component))
		valid, _ := publications.Capture().Valid(after.Mapping)
		Expect(valid).To(BeTrue())
	})

	It("withdraws incompatible status and grouping when a different definition wins", func(ctx SpecContext) {
		r, _, publications := testRuntime(nil)
		definitions := jobInventory()
		r.deps.Inventory = definitions
		root, pod := testObject(testRootGVK, "root"), testObject(podGVK, "pod")
		ownedBy(pod, root)
		pod.SetLabels(map[string]string{"job-name": "root"})
		seedWatch(r, testRootGVK, "jobs", root)
		seedWatch(r, podGVK, "pods", pod)
		state := seedRoot(r, root, pod)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		Expect(publications.Published(root.GetUID()).Workload.HasStatus).To(BeTrue())
		definitions.selection.Origin = inventory.Origin{Source: inventory.SourceCluster, Name: "replacement", UID: "new-definition", ResourceVersion: "2"}
		definitions.err = inventory.ErrInvalidDefinition
		state.generation++
		state.fullRefresh = true
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		record := publications.Published(root.GetUID())
		Expect(record.Workload.HasStatus).To(BeFalse())
		row, _ := record.Pod(pod.GetUID())
		Expect(row.Record.Component).To(Equal("<unknown>"))
		valid, _ := publications.Capture().Valid(row.Mapping)
		Expect(valid).To(BeFalse())
	})

	It("prunes an existing intermediate after its last Pod disappears and fences an older GET", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		pod, owner := testObject(podGVK, "pod"), testObject(testRootGVK, "owner")
		ownedBy(pod, owner)
		other := pod.DeepCopy()
		other.SetName("other")
		other.SetUID("other")
		w := seedWatch(r, podGVK, "pods", pod, other)
		key := rememberOwner(r.reader, "owner", "1")
		r.pruneMetadata()
		Expect(r.reader.metadata).To(HaveKey(key))
		r.acceptEvent(w, other, true)
		r.pruneMetadata()
		Expect(r.reader.metadata).To(HaveKey(key), "a shared owner remains live until the last descendant exits")
		entry := r.reader.metadata[key]
		entry.expires = time.Now().Add(-time.Second)
		r.reader.metadata[key] = entry
		entered, resume := make(chan struct{}), make(chan struct{})
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			close(entered)
			<-resume
			return true, ownerMetadata("owner", "1"), nil
		})
		done := make(chan error, 1)
		go func() { _, err := r.reader.GetMetadata(ctx, refOf(owner)); done <- err }()
		Eventually(entered).Should(BeClosed())
		r.acceptEvent(w, pod, true)
		r.pruneMetadata()
		close(resume)
		Eventually(done).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.reader.metadataReads).To(BeEmpty())
		metadata.ClearActions()
		Expect(r.reader.refreshMetadata(ctx)).To(Succeed())
		Expect(metadata.Actions()).To(BeEmpty())
	})

	It("keeps accepted proof when shutdown cancels verification or metadata reads", func(ctx SpecContext) {
		r, metadata, publications := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		w := seedWatch(r, testRootGVK, "jobs", root)
		seedRoot(r, root)
		Expect(r.reconcile(ctx, root.GetUID())).To(Succeed())
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, nil, context.Canceled })
		Expect(r.verifyWatch(canceled, w.key)).To(MatchError(context.Canceled))
		Expect(w.err).NotTo(HaveOccurred())
		key := rememberOwner(r.reader, "owner", "1")
		proof := r.reader.metadata[key].failureKey
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, nil, context.Canceled })
		_, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "owner", UID: types.UID("different")})
		Expect(err).To(MatchError(context.Canceled))
		Expect(r.reader.refreshMetadata(canceled)).To(MatchError(context.Canceled))
		Expect(r.reader.activeKeys[proof]).To(BeTrue())
		Expect(r.reader.failures).To(BeEmpty())
		valid, _ := publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeTrue())
	})
	DescribeTable("preserves each generic Pod's verified root version and applies version scope", func(ctx SpecContext, onlyV2, selectedDefinition bool) {
		r, _, publications := testRuntime(nil)
		v2 := testRootGVK
		v2.Version = "v2"
		if selectedDefinition {
			r.deps.Inventory = jobInventory()
		}
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(v2, meta.RESTScopeNamespace)
		first := testObject(testRootGVK, "root")
		second := first.DeepCopy()
		second.SetGroupVersionKind(v2)
		one, two := testObject(podGVK, "one"), testObject(podGVK, "two")
		ownedBy(one, first)
		ownedBy(two, second)
		seedWatch(r, testRootGVK, "jobs", first)
		seedWatch(r, v2, "jobs", second)
		seedWatch(r, podGVK, "pods", one, two)
		if onlyV2 {
			r.config.ResourceRules = []ResourceRule{{APIGroups: []string{"batch"}, APIVersions: []string{"v2"}, Resources: []string{"jobs"}}}
		}
		for _, pod := range []*unstructured.Unstructured{one, two, one, two} {
			Expect(r.resolveInput(ctx, inputKey{Object: keyOf(refOf(pod)), UID: pod.GetUID()})).To(Succeed())
		}
		Expect(r.reconcile(ctx, first.GetUID())).To(Succeed())
		record := publications.Published(first.GetUID())
		Expect(record).NotTo(BeNil())
		firstRow, firstFound := record.Pod(one.GetUID())
		secondRow, secondFound := record.Pod(two.GetUID())
		Expect(secondFound).To(BeTrue())
		expectedSecond := v2
		if selectedDefinition {
			expectedSecond = testRootGVK
		}
		Expect(secondRow.Record.WorkloadGVK).To(Equal(expectedSecond))
		if onlyV2 {
			Expect(firstFound).To(BeFalse())
			Expect(record.Workload.Ref.GVK).To(Equal(v2))
		} else {
			Expect(firstFound).To(BeTrue())
			Expect(firstRow.Record.WorkloadGVK).To(Equal(testRootGVK))
			Expect(record.Workload.Ref.GVK).To(Equal(testRootGVK))
		}
	}, Entry("both API versions", false, false), Entry("v2-only resource rule", true, false), Entry("selected Karta supplies one version", false, true))

})
