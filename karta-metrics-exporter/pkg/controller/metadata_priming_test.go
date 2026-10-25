// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

type changingMapper struct {
	meta.RESTMapper
	mu  sync.RWMutex
	err error
}

func (m *changingMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	return m.RESTMapper.RESTMapping(gk, versions...)
}

var _ = Describe("Demand-aware cold owner priming", func() {
	It("serves1000 distinct cold owners with two LIST pages and no GETs", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		pods := make([]*unstructured.Unstructured, 1000)
		owners := make([]*metav1.PartialObjectMetadata, 1000)
		for i := range pods {
			name := fmt.Sprintf("owner-%04d", i)
			owner := testObject(testRootGVK, name)
			pods[i] = testObject(podGVK, fmt.Sprintf("pod-%04d", i))
			ownedBy(pods[i], owner)
			owners[i] = ownerMetadata(name, "1")
		}
		seedWatch(r, podGVK, "pods", pods...)
		pages := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			if pages == 1 {
				page := metadataList(owners[:500]...)
				page.Continue = "next"
				return true, page, nil
			}
			return true, metadataList(owners[500:]...), nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(2))
		Expect(r.reader.metadata).To(HaveLen(1000))
		for _, owner := range owners {
			object, err := r.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: owner.Name, UID: owner.UID})
			Expect(err).NotTo(HaveOccurred())
			Expect(object.UID).To(Equal(owner.UID))
		}
		for _, action := range metadata.Actions() {
			Expect(action.GetVerb()).To(Equal("list"))
		}
		Expect(r.reader.priming).To(BeEmpty())
		Expect(r.watches).To(HaveLen(1), "priming never starts an intermediate watch")
	})
	It("retains only requested shared metadata while discarding unrelated collection members", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		owner := testObject(testRootGVK, "owner")
		a, b := testObject(podGVK, "a"), testObject(podGVK, "b")
		ownedBy(a, owner)
		ownedBy(b, owner)
		seedWatch(r, podGVK, "pods", a, b)
		pages := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			items := make([]*metav1.PartialObjectMetadata, 500)
			for i := range items {
				items[i] = ownerMetadata(fmt.Sprintf("unrelated-%d-%d", pages, i), "1")
			}
			if pages == 20 {
				items[499] = ownerMetadata("owner", "1")
			}
			page := metadataList(items...)
			if pages < 20 {
				page.Continue = fmt.Sprint(pages)
			}
			return true, page, nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(20))
		Expect(r.reader.metadata).To(HaveLen(1))
		Expect(r.reader.priming).To(BeEmpty())
	})
	It("discovers deeper requested chains without retaining unrelated ancestors early", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		child, parent := testObject(testRootGVK, "child"), testObject(testRootGVK, "parent")
		ownedBy(child, parent)
		pod := testObject(podGVK, "pod")
		ownedBy(pod, child)
		seedWatch(r, podGVK, "pods", pod)
		childMeta := ownerMetadata("child", "1")
		childMeta.OwnerReferences = child.GetOwnerReferences()
		pages := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			return true, metadataList(childMeta, ownerMetadata("parent", "1")), nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(2))
		Expect(r.reader.metadata).To(HaveLen(2))
	})
	It("discards all staging after a partial LIST failure", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		owner, pod := testObject(testRootGVK, "owner"), testObject(podGVK, "pod")
		ownedBy(pod, owner)
		seedWatch(r, podGVK, "pods", pod)
		pages := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			if pages == 1 {
				page := metadataList(ownerMetadata("owner", "1"))
				page.Continue = "next"
				return true, page, nil
			}
			return true, nil, errors.New("page unavailable")
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.reader.priming).To(BeEmpty())
		Expect(r.reader.failures).To(BeEmpty())
	})
	It("keeps GET-only permission working without retrying the denied collection per owner", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		owner, pod := testObject(testRootGVK, "owner"), testObject(podGVK, "pod")
		ownedBy(pod, owner)
		seedWatch(r, podGVK, "pods", pod)
		lists := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			lists++
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "", errors.New("get only"))
		})
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, ownerMetadata("owner", "1"), nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(lists).To(Equal(1))
		_, err := r.reader.GetMetadata(ctx, refOf(owner))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.reader.failures).To(BeEmpty())
	})
	DescribeTable("cannot overwrite a newer accepted input", func(ctx SpecContext, operation string) {
		r, metadata, _ := testRuntime(nil)
		mapper := &changingMapper{RESTMapper: r.reader.clients.Mapper}
		r.reader.clients.Mapper = mapper
		owner, pod := testObject(testRootGVK, "owner"), testObject(podGVK, "pod")
		ownedBy(pod, owner)
		pw := seedWatch(r, podGVK, "pods", pod)
		var nw *resourceWatch
		if operation == "namespace" {
			r.config.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"selected": "yes"}}
			r.namespaceSelector = labels.SelectorFromSet(labels.Set{"selected": "yes"})
			ns := testObject(namespaceGVK, "ns")
			ns.SetNamespace("")
			ns.SetLabels(map[string]string{"selected": "yes"})
			nw = seedWatch(r, namespaceGVK, "namespaces", ns)
		}
		entered, resume := make(chan struct{}), make(chan struct{})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			metadata.Unlock()
			close(entered)
			<-resume
			metadata.Lock()
			return true, metadataList(ownerMetadata("owner", "1")), nil
		})
		done := make(chan error, 1)
		go func() { done <- r.primeMetadata(ctx) }()
		Eventually(entered).Should(BeClosed())
		switch operation {
		case "get", "failed-get":
			metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
				if operation == "failed-get" {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "jobs"}, "owner", errors.New("denied"))
				}
				return true, ownerMetadata("owner", "2"), nil
			})
			_, err := r.reader.GetMetadata(ctx, refOf(owner))
			if operation == "get" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred())
			}
		case "mapping-failure":
			mapper.mu.Lock()
			mapper.err = errors.New("mapping unavailable")
			mapper.mu.Unlock()
			_, err := r.reader.GetMetadata(ctx, refOf(owner))
			Expect(err).To(HaveOccurred())
		case "cancel-get":
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, err := r.reader.GetMetadata(canceled, refOf(owner))
			Expect(err).To(MatchError(context.Canceled))
		case "watch-failure":
			w := seedWatch(r, testRootGVK, "jobs")
			r.mu.Lock()
			r.failWatchLocked(w, errors.New("watch failed"))
			r.mu.Unlock()
		case "watch":
			w := seedWatch(r, testRootGVK, "jobs")
			changed := owner.DeepCopy()
			changed.SetResourceVersion("2")
			r.acceptEvent(w, changed, false)
		case "uid":
			changed := pod.DeepCopy()
			changed.SetResourceVersion("2")
			refs := changed.GetOwnerReferences()
			refs[0].UID = "replacement"
			changed.SetOwnerReferences(refs)
			r.acceptEvent(pw, changed, false)
		case "prune":
			r.acceptEvent(pw, pod, true)
			r.pruneMetadata()
		case "namespace":
			ns := testObject(namespaceGVK, "ns")
			ns.SetNamespace("")
			ns.SetResourceVersion("2")
			r.acceptEvent(nw, ns, false)
		}
		close(resume)
		Eventually(done).Should(Receive(Succeed()))
		Expect(r.reader.priming).To(BeEmpty())
		switch operation {
		case "cancel-get":
			Expect(r.reader.metadata).To(HaveLen(1))
		case "get":
			Expect(r.reader.metadata[keyOf(refOf(owner))].object.ResourceVersion).To(Equal("2"))
		default:
			Expect(r.reader.metadata).To(BeEmpty())
		}
	}, Entry("GET success", "get"), Entry("GET failure", "failed-get"), Entry("newer mapping failure", "mapping-failure"), Entry("watched update", "watch"), Entry("newer watch failure", "watch-failure"), Entry("one GET caller cancellation", "cancel-get"), Entry("owner UID replacement", "uid"), Entry("pruned last descendant", "prune"), Entry("namespace withdrawal", "namespace"))
	It("normalizes cluster scope and skips ignored or malformed controller references", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		clusterGVK := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "ClusterOwner"}
		r.reader.clients.Mapper.(*meta.DefaultRESTMapper).Add(clusterGVK, meta.RESTScopeRoot)
		owner := testObject(clusterGVK, "cluster")
		owner.SetNamespace("")
		pod := testObject(podGVK, "pod")
		ownedBy(pod, owner)
		ignored := testObject(podGVK, "ignored")
		ownedBy(ignored, testObject(testRootGVK, "ignored"))
		r.config.IgnoredOwnerKinds = []schema.GroupVersionKind{testRootGVK}
		malformed := pod.DeepCopy()
		malformed.SetName("malformed")
		malformed.SetUID(types.UID("malformed"))
		refs := malformed.GetOwnerReferences()
		refs = append(refs, refs[0])
		malformed.SetOwnerReferences(refs)
		seedWatch(r, podGVK, "pods", pod, ignored, malformed)
		metadata.PrependReactor("list", "clusterowners", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			Expect(action.GetNamespace()).To(BeEmpty())
			object := ownerMetadata("cluster", "1")
			object.Namespace = ""
			return true, metadataList(object), nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(r.reader.metadata).To(HaveLen(1))
		Expect(metadata.Actions()).To(HaveLen(1))
		Expect(r.reader.metadata).To(HaveKey(objectKey{GVK: clusterGVK, Name: "cluster"}))
	})
	It("does not let an older cold GET overwrite a primed snapshot", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		owner, pod := testObject(testRootGVK, "owner"), testObject(podGVK, "pod")
		ownedBy(pod, owner)
		seedWatch(r, podGVK, "pods", pod)
		entered, resume := make(chan struct{}), make(chan struct{})
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			metadata.Unlock()
			close(entered)
			<-resume
			metadata.Lock()
			return true, ownerMetadata("owner", "1"), nil
		})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("owner", "2")), nil
		})
		done := make(chan *metav1.PartialObjectMetadata, 1)
		failures := make(chan error, 1)
		go func() { object, err := r.reader.GetMetadata(ctx, refOf(owner)); done <- object; failures <- err }()
		Eventually(entered).Should(BeClosed())
		Expect(r.primeMetadata(ctx)).To(Succeed())
		close(resume)
		var observed *metav1.PartialObjectMetadata
		Eventually(done).Should(Receive(&observed))
		Eventually(failures).Should(Receive(Succeed()))
		Expect(observed.ResourceVersion).To(Equal("2"))
	})
	It("stops pagination when ordinary GETs have satisfied every requested key", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		owner, pod := testObject(testRootGVK, "owner"), testObject(podGVK, "pod")
		ownedBy(pod, owner)
		seedWatch(r, podGVK, "pods", pod)
		pages := 0
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, ownerMetadata("owner", "2"), nil
		})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			metadata.Unlock()
			_, err := r.reader.GetMetadata(ctx, refOf(owner))
			metadata.Lock()
			Expect(err).NotTo(HaveOccurred())
			page := metadataList(ownerMetadata("owner", "1"))
			page.Continue = "next"
			return true, page, nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(1))
		Expect(r.reader.metadata[keyOf(refOf(owner))].object.ResourceVersion).To(Equal("2"))
	})
	DescribeTable("uses only the existing explicit metadata representation fallback", func(ctx SpecContext, code int) {
		r, metadata, _ := testRuntime(nil)
		owner, pod := testObject(testRootGVK, "owner"), testObject(podGVK, "pod")
		ownedBy(pod, owner)
		seedWatch(r, podGVK, "pods", pod)
		gvr := testRootGVK.GroupVersion().WithResource("jobs")
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "JobList"})
		r.reader.clients.Dynamic = client
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Code: int32(code), Reason: metav1.StatusReasonUnknown}}
		})
		client.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			full := owner.DeepCopy()
			full.Object["spec"] = map[string]any{"large": "discard"}
			return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*full}}, nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(r.reader.metadata).To(HaveLen(1))
		Expect(client.Actions()).To(HaveLen(1))
		Expect(r.reader.metadata[keyOf(refOf(owner))].object.Name).To(Equal("owner"))
	}, Entry("not acceptable", 406), Entry("unsupported media type", 415))

	It("stops a partial page chain once its read-start proof lifetime is spent", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		now := time.Unix(100, 0)
		r.reader.options.Now = func() time.Time { return now }
		r.reader.options.TTL = time.Minute
		owner, pod := testObject(testRootGVK, "owner"), testObject(podGVK, "pod")
		ownedBy(pod, owner)
		seedWatch(r, podGVK, "pods", pod)
		pages := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			page := metadataList(ownerMetadata("owner", "1"))
			if pages == 1 {
				now = now.Add(time.Minute)
				page.Continue = "next"
			}
			return true, page, nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(1))
		Expect(r.reader.metadata).To(BeEmpty())
		Expect(r.reader.priming).To(BeEmpty())
		Expect(r.reader.failures).To(BeEmpty())
	})
	It("uses one proof-lifetime budget across newly discovered chain layers", func(ctx SpecContext) {
		r, metadata, _ := testRuntime(nil)
		now := time.Unix(100, 0)
		r.reader.options.Now = func() time.Time { return now }
		r.reader.options.TTL = 100 * time.Second
		child, parent, grand := testObject(testRootGVK, "child"), testObject(testRootGVK, "parent"), testObject(testRootGVK, "grand")
		ownedBy(child, parent)
		ownedBy(parent, grand)
		pod := testObject(podGVK, "pod")
		ownedBy(pod, child)
		seedWatch(r, podGVK, "pods", pod)
		cm, pm := ownerMetadata("child", "1"), ownerMetadata("parent", "1")
		cm.OwnerReferences = child.GetOwnerReferences()
		pm.OwnerReferences = parent.GetOwnerReferences()
		pages := 0
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			now = now.Add(60 * time.Second)
			return true, metadataList(cm, pm, ownerMetadata("grand", "1")), nil
		})
		Expect(r.primeMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(2))
		Expect(r.reader.metadata).To(HaveLen(1))
		Expect(r.reader.metadata).To(HaveKey(keyOf(refOf(child))))
		Expect(r.reader.priming).To(BeEmpty())
		Expect(r.reader.failures).To(BeEmpty())
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, pm, nil })
		_, err := r.reader.GetMetadata(ctx, refOf(parent))
		Expect(err).NotTo(HaveOccurred(), "ordinary GETs remain usable after the priming budget ends")
	})

})
