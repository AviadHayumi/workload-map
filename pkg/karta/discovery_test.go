// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package karta_test

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

type failingReader struct {
	err error
}

func (r *failingReader) Mapping(context.Context, schema.GroupVersionKind) (*meta.RESTMapping, error) {
	return nil, r.err
}

func (r *failingReader) Get(context.Context, discovery.ObjectRef) (*unstructured.Unstructured, error) {
	return nil, r.err
}

func (r *failingReader) ListPods(context.Context, string) (*corev1.PodList, error) {
	return nil, r.err
}

func TestDiscovererWithoutReader(t *testing.T) {
	for _, reader := range []discovery.Reader{nil, (*failingReader)(nil), (*discovery.Index)(nil)} {
		discoverer, err := karta.NewDiscoverer(reader, discovery.Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, acquire := range []func(context.Context, discovery.ObjectRef) (*discovery.Workload, error){
			discoverer.Workload, discoverer.WorkloadForPod,
		} {
			if _, err := acquire(t.Context(), discovery.ObjectRef{}); !errors.Is(err, discovery.ErrNoClient) {
				t.Errorf("acquire with reader %T = %v, want ErrNoClient", reader, err)
			}
		}
	}
}

func TestDiscovererPreservesErrors(t *testing.T) {
	if _, err := karta.NewDiscoverer(nil, discovery.Options{MaxDepth: -1}); !errors.Is(err, discovery.ErrInvalidInput) {
		t.Fatalf("NewDiscoverer() = %v, want ErrInvalidInput", err)
	}
	readErr := errors.New("reader unavailable")
	discoverer, err := karta.NewDiscoverer(&failingReader{err: readErr}, discovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ref := discovery.ObjectRef{
		GVK:       schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
		Namespace: "example", Name: "training", UID: "pod-uid",
	}
	for _, acquire := range []func(context.Context, discovery.ObjectRef) (*discovery.Workload, error){
		discoverer.Workload, discoverer.WorkloadForPod,
	} {
		if _, err := acquire(t.Context(), ref); !errors.Is(err, readErr) {
			t.Errorf("acquire = %v, want reader error", err)
		}
	}
}
