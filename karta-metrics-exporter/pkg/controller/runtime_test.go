// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type pausedLabels struct {
	once    sync.Once
	entered chan types.UID
	resume  chan struct{}
}

func (p *pausedLabels) Resolve(ctx context.Context, input labelresolver.Input) ([]labelresolver.ResolvedMetricLabels, error) {
	if input.Pod != nil {
		p.once.Do(func() {
			p.entered <- input.Pod.GetUID()
			select {
			case <-p.resume:
			case <-ctx.Done():
			}
		})
	}
	return nil, nil
}

func TestRuntime(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Runtime ordering and evidence")
}

var testRootGVK = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

func testObject(gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{}}
	o.SetGroupVersionKind(gvk)
	o.SetNamespace("ns")
	o.SetName(name)
	o.SetUID(types.UID(name))
	o.SetResourceVersion("1")
	return o
}
func testRuntime(resolver labelresolver.Resolver) (*Runtime, *metadatafake.FakeMetadataClient, store.Interface) {
	scheme := kruntime.NewScheme()
	metadata := metadatafake.NewSimpleMetadataClient(scheme)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{testRootGVK.GroupVersion(), podGVK.GroupVersion()})
	mapper.Add(testRootGVK, meta.RESTScopeNamespace)
	mapper.Add(podGVK, meta.RESTScopeNamespace)
	reader, err := NewReader(Clients{Dynamic: dynamicfake.NewSimpleDynamicClient(scheme), Metadata: metadata, Mapper: mapper}, ReaderOptions{TTL: time.Minute})
	Expect(err).NotTo(HaveOccurred())
	definitions, err := inventory.New(inventory.Config{}, nil)
	Expect(err).NotTo(HaveOccurred())
	ownership, err := discovery.New(discovery.Dependencies{Reader: reader, Inventory: definitions}, discovery.Config{})
	Expect(err).NotTo(HaveOccurred())
	if resolver == nil {
		resolver, err = labelresolver.New(labelresolver.Config{}, labelresolver.Dependencies{})
		Expect(err).NotTo(HaveOccurred())
	}
	publications := store.New(store.Options{})
	runtime, err := New(Config{}, Dependencies{Discovery: ownership, Inventory: definitions, Labels: resolver, Publications: publications}, reader)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { runtime.queue.ShutDown(); runtime.rootQueue.ShutDown(); runtime.controlQueue.ShutDown() })
	return runtime, metadata, publications
}
func seedWatch(r *Runtime, gvk schema.GroupVersionKind, resource string, objects ...*unstructured.Unstructured) *resourceWatch {
	key := watchKey{Resource: gvk.GroupVersion().WithResource(resource)}
	w := &resourceWatch{key: key, gvk: gvk, objects: map[objectKey]struct{}{}, ready: true, discover: true, failureKey: resource, verifiedAt: time.Now()}
	r.watches[key] = w
	for _, o := range objects {
		r.accept(w, o, false)
	}
	return w
}
func seedRoot(r *Runtime, root *unstructured.Unstructured, pods ...*unstructured.Unstructured) *rootState {
	state := &rootState{ref: refOf(root), resolution: discovery.OwnerResolution("resolved"), paths: map[types.UID][]discovery.ObjectRef{}, dirty: map[types.UID]bool{}, podKeys: map[types.UID]string{}, podErrors: map[types.UID]error{}, retired: map[string]bool{}, cached: map[types.UID]cachedPod{}, fullRefresh: true}
	for _, pod := range pods {
		uid := pod.GetUID()
		state.paths[uid] = []discovery.ObjectRef{refOf(pod), refOf(root)}
		state.podKeys[uid] = "pod:" + string(uid) + ":initial"
		r.podRoots[uid] = root.GetUID()
	}
	r.roots[root.GetUID()] = state
	return state
}

var _ = Describe("Publication concurrency", func() {
	It("commits unaffected rows during Pod churn and replaces only the dirty mapping", func(ctx SpecContext) {
		paused := &pausedLabels{entered: make(chan types.UID, 1), resume: make(chan struct{})}
		runtime, _, publications := testRuntime(paused)
		root := testObject(testRootGVK, "root")
		first := testObject(podGVK, "first")
		second := testObject(podGVK, "second")
		seedWatch(runtime, testRootGVK, "jobs", root)
		podsWatch := seedWatch(runtime, podGVK, "pods", first, second)
		state := seedRoot(runtime, root, first, second)
		completed := make(chan error, 1)
		go func() { completed <- runtime.reconcile(ctx, root.GetUID()) }()
		var changed types.UID
		Eventually(paused.entered).Should(Receive(&changed))
		original := first
		if second.GetUID() == changed {
			original = second
		}
		updated := original.DeepCopy()
		updated.SetResourceVersion("2")
		updated.SetLabels(map[string]string{"team": "changed"})
		runtime.accept(podsWatch, updated, false)
		Expect(state.generation).To(BeZero(), "a Pod change must not discard the whole root build")
		close(paused.resume)
		Eventually(completed).Should(Receive(Succeed()))
		snapshot := publications.Capture()
		record := publications.Published(root.GetUID())
		Expect(record).NotTo(BeNil())
		valid, _ := snapshot.Valid(record.Creation)
		Expect(valid).To(BeTrue())
		for _, page := range record.PodPages {
			for _, row := range page.Rows {
				valid, _ := snapshot.Valid(row.Mapping)
				Expect(valid).To(Equal(row.Record.UID != changed))
			}
		}
		runtime.mu.Lock()
		state.paths[changed] = []discovery.ObjectRef{refOf(updated), refOf(root)}
		runtime.dirtyPod(state, changed)
		runtime.mu.Unlock()
		Expect(runtime.reconcile(ctx, root.GetUID())).To(Succeed())
		record = publications.Published(root.GetUID())
		snapshot = publications.Capture()
		for _, page := range record.PodPages {
			for _, row := range page.Rows {
				valid, _ := snapshot.Valid(row.Mapping)
				Expect(valid).To(BeTrue())
			}
		}
		Expect(state.retired).To(BeEmpty())
	})
	It("does not reject a verification snapshot because another object changed during LIST", func(ctx SpecContext) {
		runtime, metadata, _ := testRuntime(nil)
		quiet := testObject(testRootGVK, "quiet")
		busy := testObject(testRootGVK, "busy")
		stream := seedWatch(runtime, testRootGVK, "jobs", quiet, busy)
		entered := make(chan struct{})
		resume := make(chan struct{})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			close(entered)
			<-resume
			return true, &metav1.List{ListMeta: metav1.ListMeta{ResourceVersion: "9"}, Items: []kruntime.RawExtension{{Object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{UID: quiet.GetUID(), ResourceVersion: "1"}}}, {Object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{UID: busy.GetUID(), ResourceVersion: "1"}}}}}, nil
		})
		completed := make(chan error, 1)
		go func() { completed <- runtime.verifyWatch(ctx, stream.key) }()
		Eventually(entered).Should(BeClosed())
		changed := busy.DeepCopy()
		changed.SetResourceVersion("2")
		runtime.accept(stream, changed, false)
		close(resume)
		Eventually(completed).Should(Receive(Succeed()))
		proof, err := runtime.reader.evidence(refOf(quiet))
		Expect(err).NotTo(HaveOccurred())
		Expect(proof.ExpiresAt).To(BeTemporally(">", time.Now()))
		latest, found := runtime.reader.Cached(refOf(changed))
		Expect(found).To(BeTrue())
		Expect(latest.GetResourceVersion()).To(Equal("2"))
	})
	It("keeps evidence available while a metadata refresh is in flight", func(ctx SpecContext) {
		runtime, metadata, _ := testRuntime(nil)
		now := time.Now()
		runtime.reader.options.Now = func() time.Time { return now }
		runtime.reader.options.TTL = time.Minute
		ref := discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "middle", UID: "middle", ResourceVersion: "1"}
		runtime.reader.metadata[keyOf(ref)] = metadataEntry{object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "middle", UID: "middle", ResourceVersion: "1"}}, expires: now.Add(20 * time.Second)}
		entered := make(chan struct{})
		resume := make(chan struct{})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			close(entered)
			<-resume
			return true, &metav1.List{ListMeta: metav1.ListMeta{ResourceVersion: "9"}, Items: []kruntime.RawExtension{{Object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "middle", UID: "middle", ResourceVersion: "1"}}}}}, nil
		})
		completed := make(chan error, 1)
		go func() { completed <- runtime.reader.refreshMetadata(ctx) }()
		Eventually(entered).Should(BeClosed())
		runtime.reader.expireMetadata()
		_, err := runtime.reader.evidence(ref)
		Expect(err).NotTo(HaveOccurred())
		close(resume)
		Eventually(completed).Should(Receive(Succeed()))
	})
	It("batches the initial publication instead of inserting each Pod separately", func(ctx SpecContext) {
		runtime, _, publications := testRuntime(nil)
		root := testObject(testRootGVK, "root")
		seedWatch(runtime, testRootGVK, "jobs", root)
		pods := make([]*unstructured.Unstructured, 1000)
		for i := range pods {
			pods[i] = testObject(podGVK, fmt.Sprintf("pod-%04d", i))
		}
		seedWatch(runtime, podGVK, "pods", pods...)
		seedRoot(runtime, root, pods...)
		Expect(runtime.reconcile(ctx, root.GetUID())).To(Succeed())
		record := publications.Published(root.GetUID())
		Expect(record.PodPages).To(HaveLen(4))
		total := 0
		for _, page := range record.PodPages {
			total += len(page.Rows)
		}
		Expect(total).To(Equal(1000))
	})
	It("keeps supplemental root failures local while required-kind failures block readiness", func() {
		runtime, _, _ := testRuntime(nil)
		runtime.started = true
		seedWatch(runtime, podGVK, "pods")
		failed := seedWatch(runtime, testRootGVK, "jobs")
		failed.err = fmt.Errorf("root unavailable")
		Expect(runtime.Ready()).To(BeTrue())
		runtime.config.RequiredKinds = []schema.GroupVersionKind{testRootGVK}
		Expect(runtime.Ready()).To(BeFalse())
		runtime.config.RequiredKinds = nil
		delete(runtime.watches, failed.key)
		runtime.watchFailures[discovery.WatchRequest{GVK: testRootGVK}] = fmt.Errorf("kind absent")
		Expect(runtime.Ready()).To(BeTrue())
		runtime.config.RequiredKinds = []schema.GroupVersionKind{testRootGVK}
		Expect(runtime.Ready()).To(BeFalse())
	})

})
