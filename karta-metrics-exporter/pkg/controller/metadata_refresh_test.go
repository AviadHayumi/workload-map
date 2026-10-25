// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
)

func ownerMetadata(name, version string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name), ResourceVersion: version}}
}
func rememberOwner(r *Reader, name, version string) objectKey {
	key := objectKey{GVK: testRootGVK, Namespace: "ns", Name: name}
	r.sequence++
	proof := fmt.Sprintf("metadata:%s:%d", name, r.sequence)
	r.saveMetadataLocked(key, metadataEntry{object: ownerMetadata(name, version), expires: time.Now().Add(time.Minute), failureKey: proof, sequence: r.sequence})
	r.observed[key] = r.sequence
	r.activeKeys[proof] = true
	return key
}
func metadataList(items ...*metav1.PartialObjectMetadata) *metav1.List {
	result := &metav1.List{ListMeta: metav1.ListMeta{ResourceVersion: "snapshot"}}
	for _, item := range items {
		result.Items = append(result.Items, kruntime.RawExtension{Object: item})
	}
	return result
}

var _ = Describe("Batched owner verification", func() {
	It("refreshes1000 retained owners with two pages and no GETs", func(ctx SpecContext) {
		runtime, metadata, _ := testRuntime(nil)
		owners := make([]*metav1.PartialObjectMetadata, 1000)
		for i := range owners {
			name := fmt.Sprintf("owner-%04d", i)
			rememberOwner(runtime.reader, name, "1")
			owners[i] = ownerMetadata(name, "1")
		}
		pages := 0
		metadata.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			if pages == 1 {
				page := metadataList(owners[:500]...)
				page.Continue = "next"
				return true, page, nil
			}
			return true, metadataList(owners[500:]...), nil
		})
		Expect(runtime.reader.refreshMetadata(ctx)).To(Succeed())
		Expect(pages).To(Equal(2))
		Expect(runtime.reader.metadata).To(HaveLen(1000))
		for _, action := range metadata.Actions() {
			Expect(action.GetVerb()).NotTo(Equal("get"))
		}
	})
	It("does not let an older blocked GET overwrite a newer LIST result", func(ctx SpecContext) {
		runtime, metadata, _ := testRuntime(nil)
		key := rememberOwner(runtime.reader, "owner", "1")
		entry := runtime.reader.metadata[key]
		entry.expires = time.Now().Add(-time.Second)
		runtime.reader.metadata[key] = entry
		entered := make(chan struct{})
		resume := make(chan struct{})
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			// Fake clients serialize reactors; release that test-only guard to model concurrent API requests.
			metadata.Unlock()
			close(entered)
			<-resume
			metadata.Lock()
			return true, ownerMetadata("owner", "1"), nil
		})
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, metadataList(ownerMetadata("owner", "2")), nil
		})
		completed := make(chan *metav1.PartialObjectMetadata, 1)
		failures := make(chan error, 1)
		go func() {
			object, err := runtime.reader.GetMetadata(ctx, discovery.ObjectRef{GVK: testRootGVK, Namespace: "ns", Name: "owner", UID: "owner"})
			completed <- object
			failures <- err
		}()
		Eventually(entered).Should(BeClosed())
		Expect(runtime.reader.refreshMetadata(ctx)).To(Succeed())
		close(resume)
		var observed *metav1.PartialObjectMetadata
		Eventually(completed).Should(Receive(&observed))
		Eventually(failures).Should(Receive(Succeed()))
		Expect(observed.ResourceVersion).To(Equal("2"))
		Expect(runtime.reader.metadata[key].object.ResourceVersion).To(Equal("2"))
	})
	It("falls back to GET when collection LIST is forbidden", func(ctx SpecContext) {
		runtime, metadata, _ := testRuntime(nil)
		key := rememberOwner(runtime.reader, "owner", "1")
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, nil, apierrors.NewForbidden(testRootGVK.GroupVersion().WithResource("jobs").GroupResource(), "", errors.New("get only"))
		})
		metadata.PrependReactor("get", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) {
			return true, ownerMetadata("owner", "2"), nil
		})
		Expect(runtime.reader.refreshMetadata(ctx)).To(Succeed())
		Expect(runtime.reader.metadata[key].object.ResourceVersion).To(Equal("2"))
		Expect(runtime.reader.failureHealth()).To(BeEmpty())
	})
	It("never certifies a partial LIST or treats its missing page as object deletion", func(ctx SpecContext) {
		runtime, metadata, _ := testRuntime(nil)
		key := rememberOwner(runtime.reader, "owner", "1")
		oldKey := runtime.reader.metadata[key].failureKey
		pages := 0
		metadata.PrependReactor("list", "jobs", func(action clienttesting.Action) (bool, kruntime.Object, error) {
			pages++
			if pages == 1 {
				page := metadataList(ownerMetadata("owner", "2"))
				page.Continue = "next"
				return true, page, nil
			}
			return true, nil, errors.New("page failed")
		})
		Expect(runtime.reader.refreshMetadata(ctx)).NotTo(Succeed())
		Expect(runtime.reader.activeKeys[oldKey]).To(BeFalse())
		Expect(runtime.reader.metadata).NotTo(HaveKey(key))
		Expect(runtime.reader.failureHealth()).NotTo(BeEmpty())
	})
	It("rejects a full build when LIST proves its root version stale before informer delivery", func(ctx SpecContext) {
		paused := &pausedLabels{entered: make(chan types.UID, 1), resume: make(chan struct{})}
		runtime, metadata, publications := testRuntime(paused)
		root := testObject(testRootGVK, "root")
		pod := testObject(podGVK, "pod")
		stream := seedWatch(runtime, testRootGVK, "jobs", root)
		seedWatch(runtime, podGVK, "pods", pod)
		seedRoot(runtime, root, pod)
		completed := make(chan error, 1)
		go func() { completed <- runtime.reconcile(ctx, root.GetUID()) }()
		Eventually(paused.entered).Should(Receive())
		changed := ownerMetadata("root", "2")
		metadata.PrependReactor("list", "jobs", func(clienttesting.Action) (bool, kruntime.Object, error) { return true, metadataList(changed), nil })
		Expect(runtime.verifyWatch(ctx, stream.key)).To(Succeed())
		close(paused.resume)
		Eventually(completed).Should(Receive(MatchError(discovery.ErrStale)))
		Expect(publications.Published(root.GetUID())).To(BeNil())
	})
})
