// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

func TestOwnerMetadataValidatesEveryTypedOwner(t *testing.T) {
	for _, test := range []struct {
		name, owners string
		valid        bool
	}{
		{name: "null_list", owners: "null", valid: true},
		{name: "empty_list", owners: "[]", valid: true},
		{name: "null_entry", owners: "[null]"},
		{name: "empty_entry", owners: "[{}]"},
		{name: "incomplete_noncontroller", owners: `[{"name":"observer","controller":false}]`},
		{name: "null_identity", owners: `[{"apiVersion":"v1","kind":"ConfigMap","name":"observer","uid":null}]`},
		{name: "empty_identity", owners: `[{"apiVersion":"v1","kind":"","name":"observer","uid":"observer"}]`},
		{name: "missing_version", owners: `[{"apiVersion":"group/","kind":"ConfigMap","name":"observer","uid":"observer"}]`},
		{name: "malformed_version", owners: `[{"apiVersion":"group/v1/extra","kind":"ConfigMap","name":"observer","uid":"observer"}]`},
		{name: "valid_then_null", owners: `[{"apiVersion":"v1","kind":"ConfigMap","name":"observer","uid":"observer"},null]`},
		{name: "noncontroller", owners: `[{"apiVersion":"v1","kind":"ConfigMap","name":"observer","uid":"observer"}]`, valid: true},
		{name: "nullable_bools", owners: `[{"apiVersion":"v1","kind":"ConfigMap","name":"observer","uid":"observer","controller":null,"blockOwnerDeletion":null}]`, valid: true},
		{name: "false_bools", owners: `[{"apiVersion":"v1","kind":"ConfigMap","name":"observer","uid":"observer","controller":false,"blockOwnerDeletion":false}]`, valid: true},
		{name: "core_alias", owners: `[{"apiVersion":"/v1","kind":"ConfigMap","name":"observer","uid":"observer","controller":true,"blockOwnerDeletion":true}]`, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := indexTestObject("root", "team", indexBundleGVK, nil)
			ref := reference(root)
			base := newOwnerRecordReader(root)
			reader := newOwnerMetadataTestReader(base, root)
			metadata := reader.metadata[objectKey{ref.GVK, ref.Namespace, ref.Name}]
			if err := json.Unmarshal([]byte(`{"metadata":{"ownerReferences":`+test.owners+`}}`), metadata); err != nil {
				t.Fatal(err)
			}
			c := newCollection(reader)
			c.metadataOnly = true
			actual, err := c.lookup(context.Background(), ref)
			if test.valid {
				if err != nil || !reflect.DeepEqual(actual.Owners, metadata.OwnerReferences) {
					t.Fatalf("valid owners = %+v, %v", actual, err)
				}
			} else if !errors.Is(err, ErrInvalidInput) || len(c.directOwners) != 0 || len(c.lookups) != 0 {
				t.Fatalf("incomplete owner became an observation: %+v, %v", actual, err)
			}
			if len(base.gets) != 0 || reader.calls[ref] != 1 {
				t.Fatal("typed validation fell back or refetched")
			}
		})
	}
}

func TestOwnerMetadataIncompleteNonControllerIsStricterThanRaw(t *testing.T) {
	root := indexTestObject("root", "team", indexBundleGVK, nil)
	root.SetOwnerReferences([]metav1.OwnerReference{{}})
	base := newOwnerRecordReader(root)
	raw := newCollection(base)
	raw.metadataOnly = true
	if _, err := raw.lookup(context.Background(), reference(root)); err != nil {
		t.Fatalf("raw non-controller policy changed: %v", err)
	}
	typed := newCollection(newOwnerMetadataTestReader(base, root))
	typed.metadataOnly = true
	if _, err := typed.lookup(context.Background(), reference(root)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("typed incomplete non-controller = %v", err)
	}
}

func TestOwnerMetadataErrorsNeverFallBack(t *testing.T) {
	readError := errors.New("metadata read failed")
	for _, test := range []struct {
		name      string
		mutate    func(*metav1.PartialObjectMetadata)
		err       error
		want      error
		nilResult bool
	}{
		{name: "empty_envelope", mutate: func(m *metav1.PartialObjectMetadata) { m.TypeMeta = metav1.TypeMeta{} }},
		{name: "optional_resource_version", mutate: func(m *metav1.PartialObjectMetadata) { m.ResourceVersion = "" }},
		{name: "nil", nilResult: true, want: ErrInvalidInput},
		{name: "object_and_error", err: readError, want: readError},
		{name: "error_before_envelope", err: readError, mutate: func(m *metav1.PartialObjectMetadata) { m.Kind = "wrong" }, want: readError},
		{name: "source_gvk_is_not_envelope", mutate: func(m *metav1.PartialObjectMetadata) {
			m.APIVersion, m.Kind = indexBundleGVK.GroupVersion().String(), indexBundleGVK.Kind
		}, want: ErrInvalidInput},
		{name: "missing_envelope_version", mutate: func(m *metav1.PartialObjectMetadata) { m.APIVersion = "" }, want: ErrInvalidInput},
		{name: "missing_envelope_kind", mutate: func(m *metav1.PartialObjectMetadata) { m.Kind = "" }, want: ErrInvalidInput},
		{name: "other_envelope_version", mutate: func(m *metav1.PartialObjectMetadata) { m.APIVersion = "meta.k8s.io/v1beta1" }, want: ErrInvalidInput},
		{name: "name", mutate: func(m *metav1.PartialObjectMetadata) { m.Name = "other" }, want: ErrStaleInput},
		{name: "namespace", mutate: func(m *metav1.PartialObjectMetadata) { m.Namespace = "other" }, want: ErrStaleInput},
		{name: "malformed_owner", mutate: func(m *metav1.PartialObjectMetadata) { m.OwnerReferences = []metav1.OwnerReference{{}} }, want: ErrInvalidInput},
	} {
		for _, canceled := range []bool{false, true} {
			t.Run(test.name+map[bool]string{false: "", true: "/canceled_after_read"}[canceled], func(t *testing.T) {
				root := indexTestObject("root", "team", indexBundleGVK, nil)
				ref := reference(root)
				base := newOwnerRecordReader(root)
				reader := newOwnerMetadataTestReader(base, root)
				key := objectKey{ref.GVK, ref.Namespace, ref.Name}
				if test.mutate != nil {
					test.mutate(reader.metadata[key])
				}
				if test.nilResult {
					delete(reader.metadata, key)
				}
				reader.err = test.err
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if canceled {
					reader.onGet = func(ObjectRef) { cancel() }
				}
				c := newCollection(reader)
				c.metadataOnly = true
				for attempt := 0; attempt < 2; attempt++ {
					_, err := c.lookup(ctx, ref)
					if !errors.Is(err, test.want) {
						t.Fatalf("lookup = %v, want %v", err, test.want)
					}
					if test.want != nil && (len(c.directOwners) != 0 || len(c.lookups) != 0) {
						t.Fatal("failed metadata entered cache")
					}
					ctx = context.Background()
				}
				wantCalls := 1
				if test.want != nil {
					wantCalls = 2
				}
				if len(base.gets) != 0 || reader.calls[ref] != wantCalls {
					t.Fatal("metadata error fell back or was cached")
				}
			})
		}
	}
}

func TestOwnerMetadataPreservesWellFormedTraversalErrors(t *testing.T) {
	for _, failure := range []string{"multiple_controllers", "cycle", "depth", "scope", "missing"} {
		t.Run(failure, func(t *testing.T) {
			root := indexTestObject("root", "team", indexBundleGVK, nil)
			child := indexTestObject("child", "team", indexWorkerGVK, root)
			pod := indexTestPod("pod", "team", child)
			depth := 32
			var readError error
			switch failure {
			case "multiple_controllers":
				child.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(root), indexTestOwner(root)})
			case "cycle":
				child.SetOwnerReferences([]metav1.OwnerReference{indexTestOwner(child)})
			case "depth":
				depth = 1
			case "scope":
				child.SetNamespace("")
			case "missing":
				readError = apierrors.NewNotFound(schema.GroupResource{Group: indexWorkerGVK.Group, Resource: "workers"}, child.GetName())
			}
			var expected error
			for _, typed := range []bool{false, true} {
				base := newOwnerRecordReader(root, child)
				base.readError = readError
				var reader Reader = base
				if typed {
					metadata := newOwnerMetadataTestReader(base, root, child)
					metadata.err = readError
					reader = metadata
				}
				c := newCollection(reader)
				c.metadataOnly = true
				_, err := ownership.Trace(context.Background(), ownership.Object{Ref: podReference(pod), Owners: pod.OwnerReferences, Namespaced: true}, c.lookup, depth)
				if err == nil {
					t.Fatal("invalid traversal succeeded")
				}
				if !typed {
					expected = err
				} else if !reflect.DeepEqual(err, expected) {
					t.Fatalf("metadata error = %#v, raw = %#v", err, expected)
				}
			}
		})
	}
}
