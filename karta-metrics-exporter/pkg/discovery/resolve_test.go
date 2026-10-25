// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	inventory "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/kartadefinitioninventory"
)

type reader struct {
	objects map[string]*metav1.PartialObjectMetadata
	cached  map[string]bool
	err     error
	reads   []string
}

func (r *reader) Cached(ref ObjectRef) (*unstructured.Unstructured, bool) {
	if !r.cached[ref.Name] {
		return nil, false
	}
	m := r.objects[ref.Name]
	u := &unstructured.Unstructured{}
	u.SetName(m.Name)
	u.SetNamespace(m.Namespace)
	u.SetUID(m.UID)
	u.SetResourceVersion(m.ResourceVersion)
	u.SetOwnerReferences(m.OwnerReferences)
	return u, true
}
func (r *reader) GetMetadata(_ context.Context, ref ObjectRef) (*metav1.PartialObjectMetadata, error) {
	r.reads = append(r.reads, ref.Name)
	if r.err != nil {
		return nil, r.err
	}
	if m := r.objects[ref.Name]; m != nil {
		return m.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "objects"}, ref.Name)
}

type definitions struct {
	err   error
	calls []schema.GroupVersionKind
}

func (i *definitions) Lookup(g schema.GroupVersionKind) (inventory.Selection, error) {
	i.calls = append(i.calls, g)
	return inventory.Selection{}, i.err
}
func TestDiscovery(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Discovery") }

var _ = Describe("root resolution", func() {
	var r *reader
	var defs *definitions
	var d Discoverer
	var ref ObjectRef
	pod := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	job := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	root := schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "JobSet"}
	BeforeEach(func() {
		yes := true
		r = &reader{objects: map[string]*metav1.PartialObjectMetadata{}, cached: map[string]bool{"pod": true}}
		for _, name := range []string{"pod", "job", "root"} {
			r.objects[name] = &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name), ResourceVersion: "1"}}
		}
		r.objects["pod"].OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "job", UID: "job", Controller: &yes}}
		r.objects["job"].OwnerReferences = []metav1.OwnerReference{{APIVersion: "example.org/v1", Kind: "JobSet", Name: "root", UID: "root", Controller: &yes}}
		defs = &definitions{}
		var err error
		d, err = New(Dependencies{Reader: r, Inventory: defs}, Config{})
		Expect(err).NotTo(HaveOccurred())
		ref = ObjectRef{GVK: pod, Namespace: "ns", Name: "pod", UID: "pod"}
	})
	It("resolves an unknown root without promoting a mapped intermediate", func() {
		result, err := d.Get(context.Background(), ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Workload.Root.GVK).To(Equal(root))
		Expect(result.Workload.Path).To(HaveLen(3))
		Expect(result.MissingCoverage).To(Equal([]WatchRequest{{GVK: root, Namespace: "ns"}}))
		Expect(defs.calls).To(Equal([]schema.GroupVersionKind{root}))
		Expect(r.reads).To(Equal([]string{"job", "root"}))
	})
	It("stops only at an explicit ignored owner", func() {
		d, _ = New(Dependencies{Reader: r, Inventory: defs}, Config{IgnoredTopOwners: []schema.GroupVersionKind{root}})
		result, err := d.Get(context.Background(), ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Workload.Root.GVK).To(Equal(job))
		Expect(result.Workload.Resolution).To(Equal(IgnoredOwner))
	})
	It("does not fabricate a root for missing owners", func() {
		delete(r.objects, "root")
		result, err := d.Get(context.Background(), ref)
		Expect(err).To(MatchError(ContainSubstring(ErrUnresolvedOwner.Error())))
		Expect(result.MissingCoverage).To(BeEmpty())
		Expect(result.Workload).To(BeNil())
	})
	It("rejects replacement UIDs", func() {
		r.objects["job"].UID = "replacement"
		_, err := d.Get(context.Background(), ref)
		Expect(err).To(MatchError(ContainSubstring(ErrStale.Error())))
	})
	It("rejects cycles", func() {
		r.objects["root"].OwnerReferences = r.objects["pod"].OwnerReferences
		_, err := d.Get(context.Background(), ref)
		Expect(err).To(MatchError(ContainSubstring("ownership cycle")))
	})
	It("retains root coverage while inventory is pending", func() {
		defs.err = inventory.ErrPending
		result, err := d.Get(context.Background(), ref)
		Expect(err).To(MatchError(ContainSubstring(ErrPending.Error())))
		Expect(result.MissingCoverage).To(HaveLen(1))
	})
	It("classifies permission and transient errors", func() {
		for _, test := range []struct {
			err  error
			want error
		}{{apierrors.NewForbidden(schema.GroupResource{}, "job", fmt.Errorf("denied")), ErrPermissionDenied}, {fmt.Errorf("transport"), ErrUnavailable}} {
			r.err = test.err
			_, err := d.Get(context.Background(), ref)
			Expect(err).To(MatchError(ContainSubstring(test.want.Error())))
		}
	})
	It("normalizes cluster-scoped owner namespaces", func() {
		r.objects["root"].Namespace = ""
		result, err := d.Get(context.Background(), ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Workload.Root.Namespace).To(BeEmpty())
		Expect(result.MissingCoverage).To(Equal([]WatchRequest{{GVK: root}}))
	})
	It("requests no watch for an already cached root", func() {
		r.cached["root"] = true
		result, err := d.Get(context.Background(), ref)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.MissingCoverage).To(BeEmpty())
	})
})
