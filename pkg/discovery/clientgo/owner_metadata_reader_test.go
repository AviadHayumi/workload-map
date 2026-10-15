// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package clientgo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type ownerMetadataClient struct {
	metadata.Interface
	metadata.Getter
	object                       *metav1.PartialObjectMetadata
	err                          error
	cancel                       context.CancelFunc
	gvr                          schema.GroupVersionResource
	namespace, name              string
	calls, resources, namespaces int
}

func (c *ownerMetadataClient) Resource(gvr schema.GroupVersionResource) metadata.Getter {
	c.resources++
	c.gvr = gvr
	return c
}
func (c *ownerMetadataClient) Namespace(ns string) metadata.ResourceInterface {
	c.namespaces++
	c.namespace = ns
	return c
}
func (c *ownerMetadataClient) Get(_ context.Context, name string, _ metav1.GetOptions, _ ...string) (*metav1.PartialObjectMetadata, error) {
	c.calls++
	c.name = name
	if c.cancel != nil {
		c.cancel()
	}
	return c.object, c.err
}
func ownerMetadataObject() *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"}, ObjectMeta: metav1.ObjectMeta{
		Name: "workload", Namespace: "team", UID: "original", ResourceVersion: "17", Labels: map[string]string{"ignored": "label"}, Annotations: map[string]string{"ignored": "annotation"}, Finalizers: []string{"ignored"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "parent", UID: "parent", Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(false)}, {APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other"}},
	}}
}
func ownerMetadataRef() discovery.ObjectRef {
	return discovery.ObjectRef{GVK: deploymentGVK, Namespace: "team", Name: "workload", UID: "original"}
}

func TestNewReaderWithOwnerMetadataDependencies(t *testing.T) {
	var nilDynamic *dynamic.DynamicClient
	var nilMetadata *ownerMetadataClient
	var nilMapper *testMapper
	for _, tt := range []struct {
		name   string
		d      dynamic.Interface
		m      metadata.Interface
		mapper meta.RESTMapper
	}{
		{"nil dynamic", nil, &ownerMetadataClient{}, &testMapper{}}, {"typed nil dynamic", nilDynamic, &ownerMetadataClient{}, &testMapper{}},
		{"nil metadata", &dynamic.DynamicClient{}, nil, &testMapper{}}, {"typed nil metadata", &dynamic.DynamicClient{}, nilMetadata, &testMapper{}},
		{"nil mapper", &dynamic.DynamicClient{}, &ownerMetadataClient{}, nil}, {"typed nil mapper", &dynamic.DynamicClient{}, &ownerMetadataClient{}, nilMapper},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewReaderWithOwnerMetadata(tt.d, tt.m, tt.mapper)
			if got != nil || !errors.Is(err, discovery.ErrNoClient) {
				t.Fatalf("constructor = %v, %v", got, err)
			}
		})
	}
	m := &ownerMetadataClient{}
	mapper := &testMapper{}
	r, err := NewReaderWithOwnerMetadata(&dynamic.DynamicClient{}, m, mapper)
	if err != nil || m.resources != 0 || mapper.calls != 0 {
		t.Fatalf("constructor did I/O: %v", err)
	}
	if _, ok := r.(discovery.PodTransferReader); !ok {
		t.Fatal("wrapper lost PodTransferReader")
	}
	ordinary, err := NewReader(&dynamic.DynamicClient{}, mapper)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ordinary.(discovery.OwnerMetadataReader); ok {
		t.Fatal("default reader advertises opt-in capability")
	}
}

func TestOwnerMetadataProjectionAndIsolation(t *testing.T) {
	source := ownerMetadataObject()
	// Envelope and every owner entry must reach SDK validation without normalization.
	source.TypeMeta = metav1.TypeMeta{APIVersion: "wrong/v1", Kind: "Wrong"}
	source.OwnerReferences = append(source.OwnerReferences, metav1.OwnerReference{})
	client := &ownerMetadataClient{object: source}
	r, err := NewReaderWithOwnerMetadata(&dynamic.DynamicClient{}, client, &testMapper{mapping: mappingFixture()})
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.GetOwnerMetadata(t.Context(), ownerMetadataRef())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.GetOwnerMetadata(t.Context(), ownerMetadataRef())
	if err != nil {
		t.Fatal(err)
	}
	expected := &metav1.PartialObjectMetadata{TypeMeta: source.TypeMeta, ObjectMeta: metav1.ObjectMeta{Name: source.Name, Namespace: source.Namespace, UID: source.UID, ResourceVersion: source.ResourceVersion, OwnerReferences: source.DeepCopy().OwnerReferences}}
	if !reflect.DeepEqual(first, expected) {
		t.Fatalf("projection = %#v, want %#v", first, expected)
	}
	source.Name = "changed"
	source.OwnerReferences[0].Name = "changed"
	*source.OwnerReferences[0].Controller = false
	*source.OwnerReferences[0].BlockOwnerDeletion = true
	if !reflect.DeepEqual(first, expected) || !reflect.DeepEqual(second, expected) {
		t.Fatal("client mutation changed results")
	}
	first.OwnerReferences[0].UID = "mutated"
	*first.OwnerReferences[0].Controller = false
	*first.OwnerReferences[0].BlockOwnerDeletion = true
	if !reflect.DeepEqual(second, expected) || source.OwnerReferences[0].UID != "parent" {
		t.Fatal("result mutation escaped")
	}
	if client.gvr != mappingFixture().Resource || client.namespace != "team" || client.name != "workload" || client.calls != 2 {
		t.Fatalf("request = %+v", client)
	}
}

func TestOwnerMetadataWrapperDelegatesFullReadsAndPodTransfer(t *testing.T) {
	f := newPodTransferFixture(t, 4, 2, false)
	base := f.reader.(*reader)
	m := &ownerMetadataClient{}
	r, err := NewReaderWithOwnerMetadata(base.client, m, base.mapper)
	if err != nil {
		t.Fatal(err)
	}
	object, err := r.Get(t.Context(), f.root)
	if err != nil || !reflect.DeepEqual(object, f.rootObject) {
		t.Fatalf("full root = %v, %v", object, err)
	}
	pods, err := r.ListPods(t.Context(), "team")
	if err != nil || !reflect.DeepEqual(pods.Items, f.pods) {
		t.Fatalf("full Pods = %v, %v", pods, err)
	}
	transferred, err := r.(discovery.PodTransferReader).ListPodsForTransfer(t.Context(), "team")
	if err != nil || !reflect.DeepEqual(transferred.Items, f.pods) {
		t.Fatalf("transferred Pods = %v, %v", transferred, err)
	}
	transferred.Items[0].Spec.Containers[0].Args[0] = "changed"
	if !reflect.DeepEqual(pods.Items, f.pods) || m.resources != 0 || f.gets != 1 || f.lists != 4 {
		t.Fatal("delegation changed ownership, metadata dispatch, or request counts")
	}
}

func TestOwnerMetadataValidationAndErrorOrdering(t *testing.T) {
	sentinel := errors.New("client failure")
	for _, tt := range []struct {
		name        string
		change      func(*discovery.ObjectRef, *metav1.PartialObjectMetadata, *testMapper, *ownerMetadataClient)
		cancelStage string
		want        error
		calls       int
	}{
		{name: "missing name", change: func(r *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, _ *testMapper, _ *ownerMetadataClient) {
			r.Name = ""
		}, want: discovery.ErrInvalidInput},
		{name: "missing UID", change: func(r *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, _ *testMapper, _ *ownerMetadataClient) {
			r.UID = ""
		}, want: discovery.ErrInvalidInput},
		{name: "namespace required", change: func(r *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, _ *testMapper, _ *ownerMetadataClient) {
			r.Namespace = ""
		}, want: ownership.ErrScope},
		{name: "served alias", change: func(_ *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, m *testMapper, _ *ownerMetadataClient) {
			m.mapping.GroupVersionKind.Version = "v2"
		}, want: discovery.ErrInvalidInput},
		{name: "resource alias", change: func(_ *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, m *testMapper, _ *ownerMetadataClient) {
			m.mapping.Resource.Version = "v2"
		}, want: discovery.ErrInvalidInput},
		{name: "mapping error", change: func(_ *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, m *testMapper, _ *ownerMetadataClient) {
			m.err = sentinel
		}, want: sentinel},
		{name: "wrong name before UID", change: func(_ *discovery.ObjectRef, o *metav1.PartialObjectMetadata, _ *testMapper, _ *ownerMetadataClient) {
			o.Name = "wrong"
			o.UID = "wrong"
		}, want: discovery.ErrStaleInput, calls: 1},
		{name: "wrong namespace", change: func(_ *discovery.ObjectRef, o *metav1.PartialObjectMetadata, _ *testMapper, _ *ownerMetadataClient) {
			o.Namespace = "wrong"
		}, want: discovery.ErrStaleInput, calls: 1},
		{name: "wrong UID", change: func(_ *discovery.ObjectRef, o *metav1.PartialObjectMetadata, _ *testMapper, _ *ownerMetadataClient) {
			o.UID = "replacement"
		}, want: ownership.ErrUIDMismatch, calls: 1},
		{name: "nil response", change: func(_ *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, _ *testMapper, c *ownerMetadataClient) {
			c.object = nil
		}, want: discovery.ErrInvalidInput, calls: 1},
		{name: "object and error", change: func(_ *discovery.ObjectRef, o *metav1.PartialObjectMetadata, _ *testMapper, c *ownerMetadataClient) {
			o.Name = "wrong"
			c.err = sentinel
		}, want: sentinel, calls: 1},
		{name: "canceled before mapping", cancelStage: "before", want: context.Canceled},
		{name: "canceled in mapping", cancelStage: "mapping", want: context.Canceled},
		{name: "canceled after response", cancelStage: "response", want: context.Canceled, calls: 1},
		{name: "client error before cancellation", cancelStage: "response", change: func(_ *discovery.ObjectRef, _ *metav1.PartialObjectMetadata, _ *testMapper, c *ownerMetadataClient) {
			c.err = sentinel
		}, want: sentinel, calls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ref := ownerMetadataRef()
			o := ownerMetadataObject()
			m := &testMapper{mapping: mappingFixture()}
			c := &ownerMetadataClient{object: o}
			switch tt.cancelStage {
			case "before":
				cancel()
			case "mapping":
				m.cancel = cancel
			case "response":
				c.cancel = cancel
			}
			if tt.change != nil {
				tt.change(&ref, o, m, c)
			}
			r, err := NewReaderWithOwnerMetadata(&dynamic.DynamicClient{}, c, m)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.GetOwnerMetadata(ctx, ref)
			if got != nil || !errors.Is(err, tt.want) || c.calls != tt.calls {
				t.Fatalf("GetOwnerMetadata = %v, %v; calls=%d", got, err, c.calls)
			}
			if errors.Is(err, ownership.ErrUIDMismatch) {
				var identity *ownership.Error
				if !errors.As(err, &identity) || identity.Expected != ref || identity.Actual != (discovery.ObjectRef{GVK: ref.GVK, Namespace: ref.Namespace, Name: ref.Name, UID: "replacement"}) {
					t.Fatalf("identity error = %#v", identity)
				}
			}
		})
	}
}
