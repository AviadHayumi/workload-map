// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type ownerParityReader struct {
	capturedReader
	result *unstructured.Unstructured
	err    error
	cancel context.CancelFunc
	gets   int
}

func (r *ownerParityReader) Get(context.Context, ObjectRef) (*unstructured.Unstructured, error) {
	r.gets++
	if r.cancel != nil {
		r.cancel()
	}
	return r.result.DeepCopy(), r.err
}

func TestDirectOwnerLookupMalformedValueParity(t *testing.T) {
	for _, field := range [][]string{
		{"apiVersion"}, {"kind"}, {"metadata"},
		{"metadata", "name"}, {"metadata", "namespace"}, {"metadata", "uid"},
		{"metadata", "resourceVersion"}, {"metadata", "ownerReferences"},
	} {
		for _, value := range []struct {
			name   string
			data   any
			absent bool
		}{
			{name: "absent", absent: true}, {name: "null"},
			{name: "number", data: int64(7)},
			{name: "object", data: map[string]any{"unexpected": []any{"value"}}},
			{name: "nil_object", data: map[string]any(nil)},
			{name: "list", data: []any{map[string]any{"controller": "invalid"}}},
		} {
			t.Run(strings.Join(field, "_")+"_"+value.name, func(t *testing.T) {
				for _, alias := range []bool{false, true} {
					root := indexTestObject("root", "team", indexBundleGVK, nil)
					object := root.DeepCopy()
					if alias {
						object.SetAPIVersion(indexBundleGVK.Group + "/v2")
					}
					ref := reference(object)
					if value.absent {
						unstructured.RemoveNestedField(object.Object, field...)
					} else if err := unstructured.SetNestedField(object.Object, value.data, field...); err != nil {
						t.Fatal(err)
					}
					var full ownership.Object
					var fullErr error
					for _, direct := range []bool{false, true} {
						r := &ownerParityReader{result: object, capturedReader: capturedReader{mappings: map[schema.GroupVersionKind]*meta.RESTMapping{ref.GVK: indexTestMapping(ref.GVK, meta.RESTScopeNamespace)}}}
						c := newCollection(r)
						if alias {
							if err := c.remember(root.DeepCopy()); err != nil {
								t.Fatal(err)
							}
						}
						c.metadataOnly = direct
						actual, err := c.lookup(context.Background(), ref)
						if !direct {
							full, fullErr = actual, err
							continue
						}
						if !reflect.DeepEqual(actual, full) || !reflect.DeepEqual(err, fullErr) {
							t.Fatalf("alias=%v: direct lookup = %+v, %#v; full lookup = %+v, %#v", alias, actual, err, full, fullErr)
						}
					}
				}
			})
		}
	}
}

func TestDirectOwnerWalkFailureAndCancellationParity(t *testing.T) {
	readErr := errors.New("reader failed")
	for _, test := range []struct {
		name       string
		mutate     func(*unstructured.Unstructured)
		readErr    error
		cancel     bool
		nilResult  bool
		depth      int
		wantErr    error
		wantCached int
	}{
		{name: "valid", depth: 32, wantCached: 1},
		{name: "cancel_after_valid_get", depth: 32, cancel: true, wantErr: context.Canceled, wantCached: 1},
		{name: "cancel_wrong_uid", depth: 32, cancel: true, mutate: func(o *unstructured.Unstructured) { o.SetUID("replacement") }, wantErr: ownership.ErrUIDMismatch},
		{name: "cancel_malformed_owners", depth: 32, cancel: true, mutate: func(o *unstructured.Unstructured) {
			o.Object["metadata"].(map[string]any)["ownerReferences"] = "invalid"
		}, wantErr: ErrInvalidInput},
		{name: "result_and_error", depth: 32, readErr: readErr, wantErr: readErr},
		{name: "nil_result", depth: 32, nilResult: true, wantErr: ErrInvalidInput},
		{name: "invalid_controller", depth: 32, mutate: func(o *unstructured.Unstructured) {
			owner := indexTestOwner(o)
			owner.APIVersion = ""
			o.SetOwnerReferences([]metav1.OwnerReference{owner})
		}, wantErr: ownership.ErrInvalidReference, wantCached: 1},
		{name: "multiple_controllers", depth: 32, mutate: func(o *unstructured.Unstructured) {
			owner := indexTestOwner(o)
			o.SetOwnerReferences([]metav1.OwnerReference{owner, owner})
		}, wantErr: ownership.ErrMultipleControllers, wantCached: 1},
		{name: "depth", depth: 1, mutate: func(o *unstructured.Unstructured) { o.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(o)}) }, wantErr: ownership.ErrDepthExceeded, wantCached: 1},
		{name: "cycle", depth: 32, mutate: func(o *unstructured.Unstructured) { o.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(o)}) }, wantErr: ownership.ErrCycle, wantCached: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var fullPaths [2][]ownership.Object
			var fullErrors [2]error
			var fullGets int
			for _, direct := range []bool{false, true} {
				root := indexTestObject("root", "", indexBundleGVK, nil)
				pod := indexTestPod("pod", "team", root)
				start := ownership.Object{Ref: podReference(pod), Owners: pod.OwnerReferences, Namespaced: true}
				if test.mutate != nil {
					test.mutate(root)
				}
				ctx, cancel := context.WithCancel(context.Background())
				r := &ownerParityReader{result: root, err: test.readErr, capturedReader: capturedReader{mappings: map[schema.GroupVersionKind]*meta.RESTMapping{indexBundleGVK: indexTestMapping(indexBundleGVK, meta.RESTScopeRoot)}}}
				if test.nilResult {
					r.result = nil
				}
				if test.cancel {
					r.cancel = cancel
				}
				c := newCollection(r)
				c.metadataOnly = direct
				for attempt := 0; attempt < 2; attempt++ {
					queryCtx := ctx
					if attempt == 1 {
						queryCtx = context.Background()
					}
					path, err := ownership.Trace(queryCtx, start, c.lookup, test.depth)
					if attempt == 0 && !errors.Is(err, test.wantErr) {
						t.Fatalf("first walk error = %v, want %v", err, test.wantErr)
					}
					if len(c.lookups) != test.wantCached {
						t.Fatalf("successful lookup cache entries = %d, want %d", len(c.lookups), test.wantCached)
					}
					if !direct {
						fullPaths[attempt], fullErrors[attempt] = path, err
						continue
					}
					if !reflect.DeepEqual(path, fullPaths[attempt]) || !reflect.DeepEqual(err, fullErrors[attempt]) {
						t.Fatalf("attempt %d: direct trace = %+v, %#v; full trace = %+v, %#v", attempt, path, err, fullPaths[attempt], fullErrors[attempt])
					}
				}
				cancel()
				if !direct {
					fullGets = r.gets
				} else if r.gets != fullGets {
					t.Fatalf("direct GETs = %d, full GETs = %d", r.gets, fullGets)
				}
			}
		})
	}
}
