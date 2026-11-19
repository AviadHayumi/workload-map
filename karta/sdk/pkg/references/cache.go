// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package references

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type cachingReader struct {
	reader  ResourceReader
	mu      sync.Mutex
	objects map[lookupKey]*unstructured.Unstructured
	lists   map[listKey][]unstructured.Unstructured
}
type lookupKey struct {
	gvk             schema.GroupVersionKind
	namespace, name string
}
type listKey struct {
	gvk                 schema.GroupVersionKind
	namespace, selector string
}

// NewCachingReader shares successful calls within one observation. Create a new
// wrapper for the next observation; errors and missing lookups are not cached.
func NewCachingReader(reader ResourceReader) ResourceReader {
	if reader == nil {
		return nil
	}
	return &cachingReader{reader: reader, objects: map[lookupKey]*unstructured.Unstructured{}, lists: map[listKey][]unstructured.Unstructured{}}
}
func (r *cachingReader) Get(ctx context.Context, gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := lookupKey{gvk, namespace, name}
	if object, ok := r.objects[key]; ok {
		return object.DeepCopy(), nil
	}
	object, err := r.reader.Get(ctx, gvk, namespace, name)
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, nil
	}
	r.objects[key] = object.DeepCopy()
	return object.DeepCopy(), nil
}
func (r *cachingReader) List(ctx context.Context, gvk schema.GroupVersionKind, query ListQuery) ([]unstructured.Unstructured, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selector := ""
	if query.Selector != nil {
		selector = query.Selector.String()
	}
	key := listKey{gvk, query.Namespace, selector}
	if objects, ok := r.lists[key]; ok {
		return NewListValue(objects).List, nil
	}
	objects, err := r.reader.List(ctx, gvk, query)
	if err != nil {
		return nil, err
	}
	r.lists[key] = NewListValue(objects).List
	return NewListValue(objects).List, nil
}
