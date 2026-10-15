// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/ownership"
)

type ownerReferenceTransferReader struct{ *collectionCopyReader }

type ownerReferenceCaptureReader struct {
	t       *testing.T
	capture *Capture
}

func (r *ownerReferenceTransferReader) ListPodsForTransfer(ctx context.Context, namespace string) (*corev1.PodList, error) {
	list, err := r.ListPods(ctx, namespace)
	r.listed = nil
	return list, err
}

func (r *ownerReferenceCaptureReader) Capture(context.Context, ObjectRef) (*Capture, error) {
	return r.capture, nil
}

func (r *ownerReferenceCaptureReader) Mapping(context.Context, schema.GroupVersionKind) (*meta.RESTMapping, error) {
	r.t.Fatal("capture validation called direct Mapping")
	return nil, ErrInvalidInput
}

func (r *ownerReferenceCaptureReader) Get(context.Context, ObjectRef) (*unstructured.Unstructured, error) {
	r.t.Fatal("capture validation called direct Get")
	return nil, ErrInvalidInput
}

func (r *ownerReferenceCaptureReader) ListPods(context.Context, string) (*corev1.PodList, error) {
	r.t.Fatal("capture validation called direct ListPods")
	return nil, ErrInvalidInput
}

func invalidOwnerReferenceValues() map[string]any {
	owner := map[string]any{"apiVersion": "index.test/v1", "kind": "Bundle", "name": "a", "uid": "a", "controller": true}
	values := map[string]any{
		"string_list":          "invalid",
		"object_list":          map[string]any{},
		"null_entry":           []any{nil},
		"nil_map_entry":        []any{map[string]any(nil)},
		"string_entry":         []any{"invalid"},
		"valid_then_bad_entry": []any{owner, "invalid"},
	}
	for _, field := range []string{"apiVersion", "kind", "name", "uid", "controller", "blockOwnerDeletion"} {
		invalid := runtime.DeepCopyJSON(owner)
		invalid[field] = int64(1)
		values[field+"_type"] = []any{invalid}
	}
	invalid := runtime.DeepCopyJSON(owner)
	invalid["controller"] = "true"
	values["controller_string"] = []any{invalid}
	return values
}

func ownerReferenceReader(t *testing.T, dataset Dataset) *collectionCopyReader {
	t.Helper()
	reader := &collectionCopyReader{capturedReader: capturedReader{
		objects: make(map[objectKey]*unstructured.Unstructured), mappings: dataset.Mappings,
	}, pods: dataset.Pods}
	objects := append([]*unstructured.Unstructured(nil), dataset.Objects...)
	for _, pod := range dataset.Pods {
		object, err := podObject(pod)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	for _, object := range objects {
		ref := reference(object)
		reader.objects[objectKey{ref.GVK, ref.Namespace, ref.Name}] = object
	}
	return reader
}

func TestOwnershipObjectPreservesNullableOwnerReferences(t *testing.T) {
	for name, value := range map[string]any{
		"absent":      nil,
		"null":        nil,
		"nil_list":    []any(nil),
		"empty_list":  []any{},
		"empty_owner": []any{map[string]any{}},
		"null_fields": []any{map[string]any{"apiVersion": nil, "kind": nil, "name": nil, "uid": nil, "controller": nil, "blockOwnerDeletion": nil}},
		"false":       []any{map[string]any{"controller": false, "blockOwnerDeletion": false}},
		"true":        []any{map[string]any{"apiVersion": "index.test/v1", "kind": "Bundle", "name": "a", "uid": "a", "controller": true, "blockOwnerDeletion": true}},
		"extension":   []any{map[string]any{"controller": nil, "extension": map[string]any{"nested": []any{"retained"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			object := indexTestObject("child", "team", indexWorkerGVK, nil)
			if name != "absent" {
				object.Object["metadata"].(map[string]any)["ownerReferences"] = value
			}
			before := object.DeepCopy()
			got, err := ownershipObject(object, indexTestMapping(indexWorkerGVK, meta.RESTScopeNamespace))
			if err != nil {
				t.Fatal(err)
			}
			var metadata metav1.ObjectMeta
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object["metadata"].(map[string]any), &metadata); err != nil {
				t.Fatal(err)
			}
			if !sameOwnerReferences(got.Owners, metadata.OwnerReferences) || !reflect.DeepEqual(object, before) {
				t.Fatalf("ownershipObject() owners = %+v, want Kubernetes conversion %+v and unchanged input", got.Owners, metadata.OwnerReferences)
			}
		})
	}
}

func TestMalformedOwnerReferencesRejectPublicAcquisition(t *testing.T) {
	for name, value := range invalidOwnerReferenceValues() {
		for _, route := range []string{"pod", "root", "collect", "transfer_pod", "transfer_root", "transfer_collect", "capture_pod", "capture_root"} {
			t.Run(name+"/"+route, func(t *testing.T) {
				dataset := indexTestDataset()
				child := dataset.Objects[2]
				child.Object["metadata"].(map[string]any)["ownerReferences"] = value
				input := ownerReferenceReader(t, dataset)
				var reader Reader = input
				seed := podReference(dataset.Pods[0])
				switch route {
				case "transfer_pod", "transfer_root", "transfer_collect":
					reader = &ownerReferenceTransferReader{input}
				case "capture_pod", "capture_root":
					if route == "capture_root" {
						seed = reference(dataset.Objects[0])
					}
					reader = &ownerReferenceCaptureReader{t: t, capture: &Capture{
						Seed: seed, Objects: []*unstructured.Unstructured{dataset.Objects[0], child},
						Pods: dataset.Pods[:1], Mappings: dataset.Mappings,
						Observation: Observation{Source: "indexed", Namespace: "team"},
					}}
				}
				if route == "collect" || route == "transfer_collect" {
					got, err := Collect(context.Background(), reader, "team")
					if !errors.Is(err, ErrInvalidInput) || !reflect.DeepEqual(got, Dataset{}) {
						t.Fatalf("Collect() = %+v, %v, want empty Dataset and ErrInvalidInput", got, err)
					}
					return
				}
				resolver, err := New(reader, Options{})
				if err != nil {
					t.Fatal(err)
				}
				var got *Workload
				if route == "root" || route == "transfer_root" || route == "capture_root" {
					got, err = resolver.Workload(context.Background(), reference(dataset.Objects[0]))
				} else {
					got, err = resolver.WorkloadForPod(context.Background(), seed)
				}
				if !errors.Is(err, ErrInvalidInput) || got != nil {
					t.Fatalf("acquisition = %v, %v, want nil and ErrInvalidInput", got, err)
				}
			})
		}
	}
}

func TestMalformedOwnerReferencesRejectIndexChangesAtomically(t *testing.T) {
	for name, value := range invalidOwnerReferenceValues() {
		for _, operation := range []string{"replace", "apply"} {
			t.Run(name+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				dataset := indexTestDataset()
				index := NewIndex()
				generation, err := index.Replace(ctx, dataset)
				if err != nil {
					t.Fatal(err)
				}
				before := indexTestCapture(t, index, podReference(dataset.Pods[0]), "pod-a")
				dataset.Objects[0].SetLabels(map[string]string{"change": "must-not-publish"})
				dataset.Objects[2].Object["metadata"].(map[string]any)["ownerReferences"] = value
				if operation == "replace" {
					_, err = index.Replace(ctx, dataset)
				} else {
					_, err = index.Apply(ctx, generation, Changes{Upserts: dataset.Objects})
				}
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("index change error = %v, want ErrInvalidInput", err)
				}
				after := indexTestCapture(t, index, podReference(dataset.Pods[0]), "pod-a")
				if after.Observation.Generation != generation {
					t.Fatalf("rejected change generation = %d, want %d", after.Observation.Generation, generation)
				}
				after.Observation = before.Observation
				if !reflect.DeepEqual(after, before) {
					t.Fatal("rejected change altered the previously published capture")
				}
			})
		}
	}
}

func TestCollectionRejectsMalformedNormalizedAliases(t *testing.T) {
	for name, value := range invalidOwnerReferenceValues() {
		for _, source := range []string{"object", "typed_pod"} {
			t.Run(name+"/"+source, func(t *testing.T) {
				gvk := indexBundleGVK
				if source == "typed_pod" {
					gvk = podGVK
				}
				invalid := indexTestObject("root", "team", gvk, nil)
				invalid.Object["metadata"].(map[string]any)["ownerReferences"] = value
				prior := invalid.DeepCopy()
				prior.SetOwnerReferences(invalid.GetOwnerReferences())
				collection := newCollection(nil)
				if source == "object" {
					if err := collection.remember(prior); err != nil {
						t.Fatal(err)
					}
					gvk.Version = "v2"
					invalid.SetGroupVersionKind(gvk)
				} else {
					pod, err := objectPod(prior)
					if err != nil {
						t.Fatal(err)
					}
					collection.podUIDs[pod.UID] = pod
				}
				if err := collection.remember(invalid); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("remember malformed normalized alias error = %v, want ErrInvalidInput", err)
				}
				if collection.identities[prior.GetUID()] == invalid || collection.objects[objectKey{gvk, "team", "root"}] == invalid {
					t.Fatal("malformed observation entered the collection cache")
				}
			})
		}
	}
}

func TestRawOwnerReferenceValidationPreservesControllerPolicy(t *testing.T) {
	for _, policy := range []string{"absent", "null", "false", "missing_uid", "multiple"} {
		t.Run(policy, func(t *testing.T) {
			dataset := indexTestDataset()
			child := dataset.Objects[2]
			owner := child.Object["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)
			wantRoot := reference(child)
			var wantError error
			switch policy {
			case "absent":
				delete(owner, "controller")
			case "null":
				owner["controller"] = nil
			case "false":
				owner["controller"] = false
			case "missing_uid":
				owner["uid"] = nil
				wantError = ownership.ErrInvalidReference
			case "multiple":
				child.Object["metadata"].(map[string]any)["ownerReferences"] = []any{owner, runtime.DeepCopyJSON(owner)}
				wantError = ownership.ErrMultipleControllers
			}
			resolver, err := New(ownerReferenceReader(t, dataset), Options{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolver.WorkloadForPod(context.Background(), podReference(dataset.Pods[0]))
			if wantError != nil {
				if got != nil || !errors.Is(err, wantError) {
					t.Fatalf("controller policy = %v, %v, want nil and %v", got, err, wantError)
				}
				return
			}
			if err != nil || got.Root() != wantRoot || len(got.Pods()) != 1 {
				t.Fatalf("controller policy = %v, %v, want child root with one Pod", got, err)
			}
		})
	}
}
